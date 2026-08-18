package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/control"
	"github.com/b-nnett/codex-subscription-router/internal/mux"
	"github.com/b-nnett/codex-subscription-router/internal/protocol"
	"github.com/b-nnett/codex-subscription-router/internal/state"
)

func TestTaskClientFakeBackend(t *testing.T) {
	if os.Getenv("CODEX_MUX_TASK_FAKE_BACKEND") != "1" {
		return
	}
	connected := false
	loginCounter := 0
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		message, err := protocol.Parse(scanner.Bytes())
		if err != nil {
			continue
		}
		switch message.Method {
		case "initialize":
			writeTaskFakeMessage(protocol.Success(message.ID, json.RawMessage(`{"server":"task-fake"}`)))
		case "account/read":
			account := json.RawMessage("null")
			if connected {
				account = json.RawMessage(`{"type":"chatgpt","email":"test@example.com","planType":"plus"}`)
			}
			result, _ := json.Marshal(map[string]any{"account": account})
			writeTaskFakeMessage(protocol.Success(message.ID, result))
		case "account/login/start":
			loginCounter++
			var input struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message.Params, &input) != nil || input.Type != "chatgpt" {
				writeTaskFakeMessage(protocol.Failure(message.ID, -32602, "browser sign-in required"))
				continue
			}
			verificationURL := "https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback"
			if os.Getenv("CODEX_MUX_TASK_FAKE_UNTRUSTED") == "1" {
				verificationURL = "https://example.test/device"
			}
			result, _ := json.Marshal(map[string]string{"loginId": fmt.Sprintf("task-fake-login-%d", loginCounter), "authUrl": verificationURL})
			writeTaskFakeMessage(protocol.Success(message.ID, result))
		case "account/login/cancel":
			writeTaskFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
		case "account/logout":
			connected = false
			writeTaskFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
		default:
			if len(message.ID) > 0 {
				writeTaskFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
			}
		}
	}
	os.Exit(0)
}

func writeTaskFakeMessage(message protocol.Message) {
	encoded, err := protocol.Encode(message)
	if err == nil {
		_, _ = fmt.Fprintln(os.Stdout, string(encoded))
	}
}

func TestStrictTaskClientAgainstRealControlHandlers(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"), filepath.Join(root, "primary"))
	if err != nil {
		t.Fatal(err)
	}
	environment := append([]string(nil), os.Environ()...)
	environment = append(environment, "CODEX_MUX_TASK_FAKE_BACKEND=1")
	multiplexer, err := mux.New(mux.Options{
		RealExecutable: os.Args[0], RealArgs: []string{"-test.run=TestTaskClientFakeBackend", "--"},
		Environment: environment, Store: store, Output: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	server := control.New("127.0.0.1:1", token, multiplexer, false)
	server.SetRuntimeIdentity("integration-instance")
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	parsed, _ := url.Parse(httpServer.URL)
	if err := server.RegisterRuntime(parsed.Host); err != nil {
		t.Fatal(err)
	}
	client := &taskClient{
		baseURL: httpServer.URL, token: token, instance: "integration-instance",
		http: &http.Client{Timeout: 10 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var status taskStatusResponse
	if err := client.request(ctx, http.MethodGet, "/v1/task-actions/status", nil, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Accounts) != 1 || status.Accounts[0].Connected {
		t.Fatalf("unexpected real-handler status: %#v", status)
	}
	var started connectResponse
	if err := client.request(ctx, http.MethodPost, "/v1/task-actions/connect-account", map[string]any{
		"idempotencyKey": "integration-connect", "newAccount": false,
	}, &started); err != nil {
		t.Fatal(err)
	}
	if started.Login.UserCode != "" || !control.TrustedOpenAIBrowserLoginURL(started.Login.VerificationURL) {
		t.Fatalf("unexpected real-handler challenge: %#v", started)
	}
	var cancelled attemptResponse
	if err := client.request(ctx, http.MethodPost, "/v1/task-actions/login-attempts/"+started.Attempt.ID+"/cancel", map[string]any{}, &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Attempt.State != "cancelled" {
		t.Fatalf("real-handler cancellation was not terminal: %#v", cancelled)
	}
}

func TestUntrustedChallengeIsCancelledBeforeRetry(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"), filepath.Join(root, "primary"))
	if err != nil {
		t.Fatal(err)
	}
	environment := append([]string(nil), os.Environ()...)
	environment = append(environment, "CODEX_MUX_TASK_FAKE_BACKEND=1", "CODEX_MUX_TASK_FAKE_UNTRUSTED=1")
	multiplexer, err := mux.New(mux.Options{
		RealExecutable: os.Args[0], RealArgs: []string{"-test.run=TestTaskClientFakeBackend", "--"},
		Environment: environment, Store: store, Output: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	events, unsubscribe := multiplexer.SubscribeEvents()
	defer unsubscribe()

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	server := control.New("127.0.0.1:1", token, multiplexer, false)
	server.SetRuntimeIdentity("untrusted-instance")
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	parsed, _ := url.Parse(httpServer.URL)
	if err := server.RegisterRuntime(parsed.Host); err != nil {
		t.Fatal(err)
	}
	client := &taskClient{baseURL: httpServer.URL, token: token, instance: "untrusted-instance", http: httpServer.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var ignored connectResponse
	err = client.request(ctx, http.MethodPost, "/v1/task-actions/connect-account", map[string]any{
		"idempotencyKey": "untrusted-connect", "newAccount": false,
	}, &ignored)
	if err == nil || !strings.Contains(err.Error(), "trusted sign-in URL") {
		t.Fatalf("untrusted challenge was not rejected: %v", err)
	}
	oldAttemptID := ""
	deadline := time.After(3 * time.Second)
	for oldAttemptID == "" {
		select {
		case event := <-events:
			if event.Type == "account-login" {
				if attempt, ok := event.Data.(mux.LoginAttempt); ok && attempt.ID != "" {
					oldAttemptID = attempt.ID
				}
			}
		case <-deadline:
			t.Fatal("login attempt event was not published")
		}
	}
	oldStatus, err := multiplexer.LoginStatus(oldAttemptID)
	if err != nil || oldStatus.State != mux.LoginCancelled {
		t.Fatalf("untrusted attempt remained active: %#v %v", oldStatus, err)
	}
	dashboardRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/v1/accounts/primary/login", strings.NewReader(`{"mode":"chatgpt","idempotencyKey":"dashboard-untrusted"}`))
	if err != nil {
		t.Fatal(err)
	}
	dashboardRequest.Header.Set("Content-Type", "application/json")
	dashboardRequest.Header.Set("X-Codex-Mux-Token", token)
	dashboardResponse, err := httpServer.Client().Do(dashboardRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer dashboardResponse.Body.Close()
	if dashboardResponse.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(dashboardResponse.Body)
		t.Fatalf("dashboard accepted untrusted callback: status=%d body=%s", dashboardResponse.StatusCode, body)
	}
	dashboardAttemptID := ""
	dashboardCancelled := false
	dashboardDeadline := time.After(3 * time.Second)
	for !dashboardCancelled {
		select {
		case event := <-events:
			attempt, ok := event.Data.(mux.LoginAttempt)
			if !ok || attempt.ID == oldAttemptID {
				continue
			}
			dashboardAttemptID = attempt.ID
			dashboardCancelled = attempt.State == mux.LoginCancelled
		case <-dashboardDeadline:
			t.Fatal("dashboard untrusted login was not cancelled")
		}
	}
	dashboardStatus, err := multiplexer.LoginStatus(dashboardAttemptID)
	if err != nil || dashboardStatus.State != mux.LoginCancelled {
		t.Fatalf("dashboard untrusted attempt remained active: %#v %v", dashboardStatus, err)
	}
	retry, err := multiplexer.StartLoginAttempt(ctx, "primary", "chatgpt", "untrusted-retry")
	if err != nil || retry.ID == oldAttemptID || retry.State != mux.LoginPending {
		t.Fatalf("retry reused untrusted attempt: %#v %v", retry, err)
	}
}

func TestDecodeStrictRejectsUnknownAndTrailingData(t *testing.T) {
	var status taskStatusResponse
	if err := decodeStrict([]byte(`{"routing":{"active":false,"connectedAccounts":0},"accounts":[]}`), &status); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"routing":{"active":false,"connectedAccounts":0,"extra":true},"accounts":[]}`,
		`{"routing":{"active":false,"connectedAccounts":0},"accounts":[]} {}`,
	} {
		if err := decodeStrict([]byte(payload), &status); err == nil {
			t.Fatalf("unsafe response accepted: %s", payload)
		}
	}
}

func withTaskCommandServer(t *testing.T, handler http.HandlerFunc) (*bytes.Buffer, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	output := &bytes.Buffer{}
	previousFactory, previousOpener, previousOutput := taskClientFactory, verificationURLOpener, taskCommandOutput
	taskClientFactory = func(context.Context) (*taskClient, error) {
		return &taskClient{baseURL: server.URL, token: "token", instance: "instance", http: server.Client()}, nil
	}
	verificationURLOpener = func(string) error { return nil }
	taskCommandOutput = output
	return output, func() {
		taskClientFactory, verificationURLOpener, taskCommandOutput = previousFactory, previousOpener, previousOutput
		server.Close()
	}
}

func assertTaskHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("X-Codex-Mux-Token") != "token" || request.Header.Get("X-Codex-Mux-Instance") != "instance" {
		t.Fatalf("protected task headers missing: %#v", request.Header)
	}
}

func TestStatusCommandUsesProtectedMachineContract(t *testing.T) {
	output, cleanup := withTaskCommandServer(t, func(response http.ResponseWriter, request *http.Request) {
		assertTaskHeaders(t, request)
		if request.Method != http.MethodGet || request.URL.Path != "/v1/task-actions/status" {
			t.Fatalf("unexpected status request: %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"routing":{"active":false,"connectedAccounts":0},"accounts":[{"id":"primary","label":"Primary","enabled":false,"primary":true,"connected":false,"error":"Needs sign-in"}]}`))
	})
	defer cleanup()
	if err := runAccountStatus([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"label":"Primary"`) || !strings.Contains(output.String(), `"active":false`) {
		t.Fatalf("unexpected status output: %s", output.String())
	}
}

func TestConnectCommandsPreserveIntentAndEmitSafeEvents(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		wantNew   bool
		openFails bool
	}{
		{name: "repair", args: []string{"--json", "--wait=false", "--open=true"}, openFails: true},
		{name: "another", args: []string{"--json", "--wait=false", "--open=false", "--new-account"}, wantNew: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, cleanup := withTaskCommandServer(t, func(response http.ResponseWriter, request *http.Request) {
				assertTaskHeaders(t, request)
				if request.Method != http.MethodPost || request.URL.Path != "/v1/task-actions/connect-account" {
					t.Fatalf("unexpected connect request: %s %s", request.Method, request.URL.Path)
				}
				var input struct {
					NewAccount bool `json:"newAccount"`
				}
				if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if input.NewAccount != test.wantNew {
					t.Fatalf("newAccount = %v, want %v", input.NewAccount, test.wantNew)
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{"account":{"id":"primary","label":"Primary","enabled":false,"primary":true,"connected":false,"error":"Needs sign-in"},"attempt":{"id":"attempt-1","state":"pending","expiresAt":9999999999},"login":{"verificationUrl":"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback"}}`))
			})
			defer cleanup()
			if test.openFails {
				verificationURLOpener = func(string) error { return errors.New("private executable path") }
			}
			if err := runConnectAccount(test.args); err != nil {
				t.Fatal(err)
			}
			got := output.String()
			if !strings.Contains(got, `"event":"sign_in_required"`) || !strings.Contains(got, `"accountId":"primary"`) || !strings.Contains(got, `"attemptId":"attempt-1"`) || strings.Contains(got, "private executable path") {
				t.Fatalf("unsafe or incomplete connect output: %s", got)
			}
			if test.openFails && !strings.Contains(got, `"event":"browser_open_failed"`) {
				t.Fatalf("browser failure event missing: %s", got)
			}
		})
	}
}

func TestConnectAmbiguityReturnsOnlyCandidateLabels(t *testing.T) {
	output, cleanup := withTaskCommandServer(t, func(response http.ResponseWriter, request *http.Request) {
		assertTaskHeaders(t, request)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusConflict)
		_, _ = response.Write([]byte(`{"error":"choose an account","accounts":["Personal","Work"]}`))
	})
	defer cleanup()
	err := runConnectAccount([]string{"--json", "--wait=false", "--open=false"})
	if err == nil || !strings.Contains(err.Error(), "Personal, Work") || output.Len() != 0 {
		t.Fatalf("unexpected ambiguity result: err=%v output=%s", err, output.String())
	}
}

func TestCancellationFailureIsNeverReportedAsCancelled(t *testing.T) {
	output, cleanup := withTaskCommandServer(t, func(response http.ResponseWriter, request *http.Request) {
		assertTaskHeaders(t, request)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadGateway)
		_, _ = response.Write([]byte(`{"error":"Connection cancellation could not be confirmed"}`))
	})
	defer cleanup()
	client, err := taskClientFactory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = cancelTaskConnection(client, "attempt-1", "Primary", json.NewEncoder(output))
	if err == nil || !strings.Contains(output.String(), `"event":"cancellation_unconfirmed"`) || strings.Contains(output.String(), `"event":"cancelled"`) {
		t.Fatalf("cancellation failure was misreported: err=%v output=%s", err, output.String())
	}
}

func TestSafeTextRemovesControlsAndBoundsOutput(t *testing.T) {
	got := safeText("provider\nsecret\x00" + strings.Repeat("x", 300))
	if strings.ContainsAny(got, "\n\x00") || len(got) > 240 {
		t.Fatalf("unsafe text was not sanitized: %q", got)
	}
}

func TestJSONCommandsAndOpaqueAttemptValidation(t *testing.T) {
	if !wantsJSONError([]string{"status", "--json"}) || !wantsJSONError([]string{"connect-account", "--json=true"}) {
		t.Fatal("JSON command failure detection rejected a supported command")
	}
	if wantsJSONError([]string{"daemon", "--json"}) || wantsJSONError([]string{"connect-account", "--json=false"}) {
		t.Fatal("JSON command failure detection accepted an unsupported command")
	}
	for _, valid := range []string{"abc123", "login.dead-beef:1"} {
		if !safeOpaqueID(valid) {
			t.Fatalf("valid opaque ID rejected: %q", valid)
		}
	}
	for _, invalid := range []string{"", "../attempt", "attempt/child", "attempt\nsecret", strings.Repeat("a", 129)} {
		if safeOpaqueID(invalid) {
			t.Fatalf("unsafe opaque ID accepted: %q", invalid)
		}
	}
}

func TestJSONCommandFailuresAreStructuredAndBounded(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	reportCommandError(errors.New("unauthorized\n"+strings.Repeat("secret", 100)), []string{"connect-account", "--json"}, stdout, stderr)
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), `"event":"error"`) || strings.Contains(stdout.String(), "\nsecret") || stdout.Len() > 400 {
		t.Fatalf("unsafe JSON command failure: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	reportCommandError(reported(errors.New("already emitted")), []string{"connect-account", "--json"}, stdout, stderr)
	if stdout.Len() != 0 {
		t.Fatalf("reported failure was duplicated: %s", stdout.String())
	}
}
