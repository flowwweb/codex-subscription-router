package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/control"
	muxruntime "github.com/b-nnett/codex-subscription-router/internal/runtime"
)

const taskResponseLimit = 64 * 1024

var (
	taskClientFactory               = newTaskClient
	verificationURLOpener           = openTrustedURL
	taskCommandOutput     io.Writer = os.Stdout
)

type taskClient struct {
	baseURL  string
	token    string
	instance string
	http     *http.Client
}

type taskAccountView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Enabled   bool   `json:"enabled"`
	Primary   bool   `json:"primary"`
	Connected bool   `json:"connected"`
	Email     string `json:"email,omitempty"`
	Plan      string `json:"plan,omitempty"`
	Error     string `json:"error,omitempty"`
}

type taskStatusResponse struct {
	Routing struct {
		Active            bool `json:"active"`
		ConnectedAccounts int  `json:"connectedAccounts"`
	} `json:"routing"`
	Accounts []taskAccountView `json:"accounts"`
}

type taskAttempt struct {
	ID        string `json:"id"`
	AccountID string `json:"accountId"`
	Mode      string `json:"mode"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	StartedAt int64  `json:"startedAt"`
	UpdatedAt int64  `json:"updatedAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

type connectResponse struct {
	Account taskAccountView `json:"account"`
	Attempt taskAttempt     `json:"attempt"`
	Login   struct {
		UserCode        string `json:"userCode"`
		VerificationURL string `json:"verificationUrl"`
	} `json:"login"`
}

type attemptResponse struct {
	Attempt taskAttempt `json:"attempt"`
}

type reportedError struct{ err error }

func (e *reportedError) Error() string { return e.err.Error() }

func reported(err error) error { return &reportedError{err: err} }

func newTaskClient(ctx context.Context) (*taskClient, error) {
	root, err := runtimeRoot()
	if err != nil {
		return nil, err
	}
	receipt, err := muxruntime.ReadReceipt(root)
	if err != nil {
		return nil, errors.New("Codex Router is not installed or running. Re-run the installer, then try again")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = muxruntime.Probe(probeCtx, receipt)
	cancel()
	if err != nil {
		return nil, errors.New("Codex Router is not ready. Restart or repair the installation, then try again")
	}
	token, err := loadExistingToken(root)
	if err != nil {
		return nil, errors.New("Codex Router credentials are unavailable. Repair the installation, then try again")
	}
	return &taskClient{
		baseURL: "http://" + receipt.ControlAddress, token: token, instance: receipt.Instance,
		http: &http.Client{Timeout: 35 * time.Second},
	}, nil
}

func (c *taskClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("X-Codex-Mux-Token", c.token)
	request.Header.Set("X-Codex-Mux-Instance", c.instance)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("Codex Router stopped responding. Restart it, then try again")
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, taskResponseLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read router response: %w", err)
	}
	if len(data) > taskResponseLimit {
		return errors.New("router response exceeded the safe size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error    string   `json:"error"`
			Accounts []string `json:"accounts,omitempty"`
		}
		if decodeStrict(data, &failure) == nil && failure.Error != "" {
			if len(failure.Accounts) > 0 {
				return fmt.Errorf("%s: %s", safeText(failure.Error), strings.Join(failure.Accounts, ", "))
			}
			return errors.New(safeText(failure.Error))
		}
		return fmt.Errorf("router returned HTTP %d", response.StatusCode)
	}
	if output != nil {
		if err := decodeStrict(data, output); err != nil {
			return fmt.Errorf("validate router response: %w", err)
		}
	}
	return nil
}

func decodeStrict(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("router response contains trailing data")
	}
	return nil
}

func safeText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}

func runAccountStatus(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "write machine-readable status")
	flags.SetOutput(io.Discard)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !*jsonOutput {
		return errors.New("usage: codex-mux status --json")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := taskClientFactory(ctx)
	if err != nil {
		return err
	}
	var status taskStatusResponse
	if err := client.request(ctx, http.MethodGet, "/v1/task-actions/status", nil, &status); err != nil {
		return err
	}
	return json.NewEncoder(taskCommandOutput).Encode(status)
}

func runConnectAccount(args []string) error {
	flags := flag.NewFlagSet("connect-account", flag.ContinueOnError)
	wait := flags.Bool("wait", true, "wait for OpenAI verification")
	open := flags.Bool("open", true, "open the trusted verification URL")
	jsonOutput := flags.Bool("json", false, "write machine-readable events")
	newAccount := flags.Bool("new-account", false, "create and connect another account")
	accountID := flags.String("account-id", "", "repair a specific account")
	flags.SetOutput(io.Discard)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !*jsonOutput || (*newAccount && *accountID != "") {
		return errors.New("usage: codex-mux connect-account --json [--wait=true] [--open=true] [--new-account] [--account-id ID]")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client, err := taskClientFactory(ctx)
	if err != nil {
		return err
	}
	key, err := clientIdempotencyKey()
	if err != nil {
		return err
	}
	var started connectResponse
	input := map[string]any{"idempotencyKey": key, "newAccount": *newAccount}
	if *accountID != "" {
		input["accountId"] = *accountID
	}
	if err := client.request(ctx, http.MethodPost, "/v1/task-actions/connect-account", input, &started); err != nil {
		return err
	}
	if !control.TrustedOpenAIVerificationURL(started.Login.VerificationURL) || started.Login.UserCode == "" {
		return errors.New("router returned an untrusted OpenAI verification challenge")
	}
	if !safeOpaqueID(started.Attempt.ID) {
		return errors.New("router returned an invalid connection attempt")
	}
	encoder := json.NewEncoder(taskCommandOutput)
	if err := encoder.Encode(map[string]any{
		"event": "verification_required", "account": started.Account.Label,
		"userCode": started.Login.UserCode, "verificationUrl": started.Login.VerificationURL,
		"expiresAt": started.Attempt.ExpiresAt,
	}); err != nil {
		return err
	}
	if *open {
		if err := verificationURLOpener(started.Login.VerificationURL); err != nil {
			_ = encoder.Encode(map[string]any{"event": "browser_open_failed", "message": "The OpenAI page could not be opened automatically."})
		}
	}
	if !*wait {
		return nil
	}

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return cancelTaskConnection(client, started.Attempt.ID, started.Account.Label, encoder)
		case <-ticker.C:
			pollCtx, pollCancel := context.WithTimeout(ctx, 10*time.Second)
			var current attemptResponse
			err := client.request(pollCtx, http.MethodGet, "/v1/task-actions/login-attempts/"+started.Attempt.ID, nil, &current)
			pollCancel()
			if err != nil {
				return err
			}
			switch current.Attempt.State {
			case "pending":
				continue
			case "succeeded":
				return encoder.Encode(map[string]any{"event": "connected", "account": started.Account.Label})
			case "expired":
				_ = encoder.Encode(map[string]any{"event": "expired", "message": "The OpenAI verification code expired. Run connect account again."})
				return reported(errors.New("OpenAI verification expired"))
			case "cancelled":
				return encoder.Encode(map[string]any{"event": "cancelled"})
			default:
				_ = encoder.Encode(map[string]any{"event": "failed", "message": safeText(current.Attempt.Error)})
				return reported(errors.New("OpenAI account connection failed"))
			}
		}
	}
}

func cancelTaskConnection(client *taskClient, attemptID, accountLabel string, encoder *json.Encoder) error {
	cancelCtx, cancelRequest := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRequest()
	var cancelled attemptResponse
	if err := client.request(cancelCtx, http.MethodPost, "/v1/task-actions/login-attempts/"+attemptID+"/cancel", map[string]any{}, &cancelled); err != nil {
		_ = encoder.Encode(map[string]any{"event": "cancellation_unconfirmed", "message": "Connection cancellation could not be confirmed. Check router status before trying again."})
		return reported(errors.New("connection cancellation was not confirmed"))
	}
	switch cancelled.Attempt.State {
	case "cancelled":
		return encoder.Encode(map[string]any{"event": "cancelled"})
	case "succeeded":
		return encoder.Encode(map[string]any{"event": "connected", "account": accountLabel})
	default:
		_ = encoder.Encode(map[string]any{"event": "cancellation_unconfirmed", "message": "Connection cancellation could not be confirmed. Check router status before trying again."})
		return reported(errors.New("connection cancellation was not confirmed"))
	}
}

func safeOpaqueID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func clientIdempotencyKey() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("create connection request: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func openTrustedURL(value string) error {
	if !control.TrustedOpenAIVerificationURL(value) {
		return errors.New("refused an untrusted verification URL")
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer.exe", value)
	case "darwin":
		command = exec.Command("open", value)
	default:
		command = exec.Command("xdg-open", value)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open the OpenAI verification page: %w", err)
	}
	return command.Process.Release()
}
