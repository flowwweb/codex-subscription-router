package mux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/protocol"
	"github.com/b-nnett/codex-subscription-router/internal/state"
)

func TestMuxFakeBackendProcess(t *testing.T) {
	if os.Getenv("CODEX_MUX_FAKE_BACKEND") != "1" {
		return
	}
	logPath := os.Getenv("CODEX_MUX_FAKE_LOG")
	appendFakeLog(logPath, fmt.Sprintf("start:%d", os.Getpid()))
	connected := false
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		message, err := protocol.Parse(scanner.Bytes())
		if err != nil {
			continue
		}
		switch message.Method {
		case "initialize":
			appendFakeLog(logPath, "initialize")
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{"server":"fake"}`)))
			if os.Getenv("CODEX_MUX_FAKE_CRASH_AFTER_INIT") == "1" {
				time.Sleep(50 * time.Millisecond)
				os.Exit(24)
			}
		case "initialized":
			appendFakeLog(logPath, "initialized")
		case "account/read":
			account := json.RawMessage("null")
			if connected {
				account = json.RawMessage(`{"type":"chatgpt","email":"test@example.com","planType":"plus"}`)
			}
			result, _ := json.Marshal(map[string]any{"account": account})
			writeFakeMessage(protocol.Success(message.ID, result))
		case "account/rateLimits/read":
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{"rateLimits":{}}`)))
		case "account/login/start":
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{"authUrl":"https://example.test/device","userCode":"ABCD-EFGH"}`)))
			if os.Getenv("CODEX_MUX_FAKE_LOGIN_COMPLETE") == "1" {
				connected = true
				writeFakeMessage(protocol.Message{Method: "account/login/completed", Params: json.RawMessage(`{}`)})
			}
		case "account/logout":
			connected = false
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
		case "test/crash":
			appendFakeLog(logPath, "crash")
			os.Exit(23)
		default:
			if len(message.ID) > 0 {
				writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
			}
		}
	}
	os.Exit(0)
}

func TestCloseStopsManagedConfigLoop(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, false)
	primaryHome := store.Accounts()[0].CodexHome
	account, err := store.AddAccount("Work")
	if err != nil {
		t.Fatal(err)
	}
	multiplexer.startBackgroundLoops(context.Background())
	multiplexer.Close()

	updated := "model = \"must-not-sync-after-close\"\n"
	if err := os.WriteFile(filepath.Join(primaryHome, "config.toml"), []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	isolated, err := os.ReadFile(filepath.Join(account.CodexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(isolated), "must-not-sync-after-close") {
		t.Fatal("managed config loop continued after Close returned")
	}
	if err := multiplexer.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Start after Close error = %v, want closed", err)
	}
}

func TestStartSkipsDisabledAccounts(t *testing.T) {
	multiplexer, store, logPath := newLifecycleMux(t, false)
	disabled := false
	if _, err := store.UpdateAccount("primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	if err := multiplexer.Start(context.Background()); err == nil {
		t.Fatal("start unexpectedly succeeded without an enabled account")
	}
	if data, err := os.ReadFile(logPath); err == nil && len(data) != 0 {
		t.Fatalf("disabled account started a backend: %s", data)
	}
}

func TestDisableStopsAndEnableRestartsAccount(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	first, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("primary backend did not start")
	}
	disabled := false
	snapshot, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Enabled || multiplexer.hasChild("primary") {
		t.Fatal("disabled account retained a backend")
	}
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("disabled backend did not stop")
	}
	enabled := true
	snapshot, err = multiplexer.UpdateAccount(context.Background(), "primary", nil, &enabled)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := multiplexer.child("primary")
	if !ok || second == first || !snapshot.Enabled {
		t.Fatal("enable did not create a fresh live backend")
	}
}

func TestCrashedChildRestartsAndReplaysInitialization(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	multiplexer.initializeParams = json.RawMessage(`{"clientInfo":{"name":"test"}}`)
	multiplexer.initialized = true
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	first, _ := multiplexer.child("primary")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, err := first.Request(ctx, "test/crash", nil)
	cancel()
	if err == nil {
		t.Fatal("crashing backend unexpectedly returned success")
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if current, ok := multiplexer.child("primary"); ok && current != first {
			logData, _ := os.ReadFile(logPath)
			if strings.Count(string(logData), "initialize") >= 2 && strings.Count(string(logData), "initialized") >= 2 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	logData, _ := os.ReadFile(logPath)
	t.Fatalf("backend did not restart with initialization replay:\n%s", logData)
}

func TestChildRestartIsBounded(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_CRASH_AFTER_INIT=1")
	multiplexer.initializeParams = json.RawMessage(`{"clientInfo":{"name":"test"}}`)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logData, _ := os.ReadFile(logPath)
		starts := strings.Count(string(logData), "start:")
		if starts >= maxChildRestartAttempts+1 {
			time.Sleep(750 * time.Millisecond)
			logData, _ = os.ReadFile(logPath)
			starts = strings.Count(string(logData), "start:")
			if starts > maxChildRestartAttempts+1 {
				t.Fatalf("restart exceeded bound: starts=%d\n%s", starts, logData)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	logData, _ := os.ReadFile(logPath)
	t.Fatalf("restart bound was not exercised:\n%s", logData)
}

func TestFailedAddIsDisabledAndRetryReusesAccount(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	realExecutable := multiplexer.realExecutable
	multiplexer.realExecutable = filepath.Join(t.TempDir(), "missing-backend.exe")
	_, err := multiplexer.AddAccountIdempotent(context.Background(), "Work", "request-1")
	var provisionErr *AccountProvisionError
	if !errors.As(err, &provisionErr) {
		t.Fatalf("failed add error = %v, want AccountProvisionError", err)
	}
	failed, ok := store.Account(provisionErr.AccountID)
	if !ok || failed.Enabled {
		t.Fatalf("failed account was not retained disabled: %#v, %v", failed, ok)
	}
	multiplexer.realExecutable = realExecutable
	snapshot, err := multiplexer.AddAccountIdempotent(context.Background(), "Ignored", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != failed.ID || !snapshot.Enabled || !multiplexer.hasChild(failed.ID) {
		t.Fatalf("retry did not recover the same account: %#v", snapshot)
	}
}

func TestLoginAttemptsAreIdempotentAndTerminal(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	first, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "login-1")
	if err != nil || first.State != LoginPending {
		t.Fatalf("start login = %#v, %v", first, err)
	}
	replayed, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "login-1")
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed login = %#v, %v", replayed, err)
	}
	cancelled, err := multiplexer.CancelLogin(context.Background(), first.ID)
	if err != nil || cancelled.State != LoginCancelled {
		t.Fatalf("cancel login = %#v, %v", cancelled, err)
	}

	second, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "login-2")
	if err != nil {
		t.Fatal(err)
	}
	multiplexer.now = func() time.Time { return time.Unix(second.ExpiresAt+1, 0) }
	expired, err := multiplexer.LoginStatus(second.ID)
	if err != nil || expired.State != LoginExpired {
		t.Fatalf("expired login = %#v, %v", expired, err)
	}
}

func TestConcurrentLoginStartReusesOneAttempt(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	results := make(chan LoginAttempt, 2)
	errors := make(chan error, 2)
	for index := range 2 {
		key := fmt.Sprintf("concurrent-login-%d", index)
		go func() {
			attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", key)
			results <- attempt
			errors <- err
		}()
	}
	first, second := <-results, <-results
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("concurrent login created duplicate attempts: %#v %#v", first, second)
	}
}

func TestDisabledAccountCanSignInWithoutResumingRouting(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	disabled := false
	if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "disabled-login")
	if err != nil || attempt.State != LoginPending || !multiplexer.hasChild("primary") {
		t.Fatalf("disabled login start = %#v, %v, child=%v", attempt, err, multiplexer.hasChild("primary"))
	}
	cancelled, err := multiplexer.CancelLogin(context.Background(), attempt.ID)
	if err != nil || cancelled.State != LoginCancelled {
		t.Fatalf("disabled login cancel = %#v, %v", cancelled, err)
	}
	account, ok := store.Account("primary")
	if !ok || account.Enabled || multiplexer.hasChild("primary") {
		t.Fatalf("login repair resumed disabled routing: account=%#v child=%v", account, multiplexer.hasChild("primary"))
	}
}

func TestAbandonedDisabledLoginExpiresAndStopsTemporaryChild(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	disabled := false
	if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "abandoned-login")
	if err != nil {
		t.Fatal(err)
	}
	multiplexer.loginMu.Lock()
	short := multiplexer.loginAttempts[attempt.ID]
	short.ExpiresAt = time.Now().Add(75 * time.Millisecond).Unix()
	multiplexer.loginAttempts[attempt.ID] = short
	multiplexer.loginMu.Unlock()
	multiplexer.scheduleDisabledLoginExpiry(attempt.ID, short.ExpiresAt)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !multiplexer.hasChild("primary") {
			status, statusErr := multiplexer.LoginStatus(attempt.ID)
			if statusErr != nil || status.State != LoginExpired {
				t.Fatalf("autonomous cleanup left wrong terminal state: %#v %v", status, statusErr)
			}
			account, ok := store.Account("primary")
			if !ok || account.Enabled {
				t.Fatalf("expiry resumed routing: %#v", account)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("abandoned disabled login was not cleaned up: child=%v", multiplexer.hasChild("primary"))
}

func TestDisabledLoginCompletionStopsChildAndKeepsRoutingPaused(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, true)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	disabled := false
	if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "disabled-complete")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := multiplexer.LoginStatus(attempt.ID)
		if statusErr == nil && status.State == LoginSucceeded && !multiplexer.hasChild("primary") {
			account, ok := store.Account("primary")
			if !ok || account.Enabled || !account.LastKnownConnected {
				t.Fatalf("completion resumed routing: %#v", account)
			}
			snapshots := multiplexer.Accounts(context.Background())
			if len(snapshots) != 1 || !snapshots[0].Connected || snapshots[0].Enabled {
				t.Fatalf("paused connected status is not truthful: %#v", snapshots)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("completed disabled login was not cleaned up: child=%v", multiplexer.hasChild("primary"))
}

func TestLoginCompletesOnlyAfterConnectedAccountRead(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, true)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "login-complete")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := multiplexer.LoginStatus(attempt.ID)
		if statusErr == nil && status.State == LoginSucceeded {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, _ := multiplexer.LoginStatus(attempt.ID)
	t.Fatalf("login never reached confirmed success: %#v", status)
}

func newLifecycleMux(t *testing.T, completeLogin bool) (*Multiplexer, *state.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"), filepath.Join(root, "primary"))
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "backend.log")
	environment := append([]string(nil), os.Environ()...)
	environment = append(environment,
		"CODEX_MUX_FAKE_BACKEND=1",
		"CODEX_MUX_FAKE_LOG="+logPath,
		fmt.Sprintf("CODEX_MUX_FAKE_LOGIN_COMPLETE=%d", boolInt(completeLogin)),
	)
	multiplexer, err := New(Options{
		RealExecutable: os.Args[0],
		RealArgs:       []string{"-test.run=TestMuxFakeBackendProcess", "--"},
		Environment:    environment,
		Store:          store,
		Output:         &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return multiplexer, store, logPath
}

func (m *Multiplexer) hasChild(accountID string) bool {
	_, ok := m.child(accountID)
	return ok
}

func appendFakeLog(path, line string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(file, line)
	_ = file.Close()
}

func writeFakeMessage(message protocol.Message) {
	encoded, err := protocol.Encode(message)
	if err == nil {
		_, _ = fmt.Fprintln(os.Stdout, string(encoded))
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
