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

	"github.com/b-nnett/codex-subscription-router/internal/backend"
	"github.com/b-nnett/codex-subscription-router/internal/protocol"
	"github.com/b-nnett/codex-subscription-router/internal/state"
)

func TestMuxFakeBackendProcess(t *testing.T) {
	if os.Getenv("CODEX_MUX_FAKE_BACKEND") != "1" {
		return
	}
	logPath := os.Getenv("CODEX_MUX_FAKE_LOG")
	appendFakeLog(logPath, fmt.Sprintf("start:%d", os.Getpid()))
	connected := os.Getenv("CODEX_MUX_FAKE_PRECONNECTED") == "1"
	loginCounter := 0
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		message, err := protocol.Parse(scanner.Bytes())
		if err != nil {
			continue
		}
		if message.Method == "" && len(message.ID) > 0 {
			appendFakeLog(logPath, "server-reply")
			continue
		}
		switch message.Method {
		case "initialize":
			appendFakeLog(logPath, "initialize")
			if os.Getenv("CODEX_MUX_FAKE_DELAY_INITIALIZE") == "1" {
				time.Sleep(1500 * time.Millisecond)
			}
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{"server":"fake"}`)))
			if os.Getenv("CODEX_MUX_FAKE_CRASH_AFTER_INIT") == "1" {
				time.Sleep(50 * time.Millisecond)
				os.Exit(24)
			}
		case "initialized":
			appendFakeLog(logPath, "initialized")
		case "account/read":
			appendFakeLog(logPath, "account-read")
			if os.Getenv("CODEX_MUX_FAKE_DELAY_ACCOUNT_READ") == "1" {
				time.Sleep(1500 * time.Millisecond)
			}
			account := json.RawMessage("null")
			if connected {
				account = json.RawMessage(`{"type":"chatgpt","email":"test@example.com","planType":"plus"}`)
			}
			result, _ := json.Marshal(map[string]any{"account": account})
			writeFakeMessage(protocol.Success(message.ID, result))
		case "account/rateLimits/read":
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{"rateLimits":{}}`)))
		case "account/login/start":
			appendFakeLog(logPath, "login-start")
			if os.Getenv("CODEX_MUX_FAKE_LOGIN_START_FAIL") == "1" {
				writeFakeMessage(protocol.Failure(message.ID, -32000, "login start failed"))
				continue
			}
			loginCounter++
			loginID := fmt.Sprintf("fake-login-%d", loginCounter)
			if os.Getenv("CODEX_MUX_FAKE_REUSE_LOGIN_ID") == "1" {
				loginID = "fake-login-reused"
			}
			result := map[string]string{"loginId": loginID, "authUrl": "https://example.test/device", "userCode": "ABCD-EFGH"}
			if os.Getenv("CODEX_MUX_FAKE_MISSING_LOGIN_ID") == "1" {
				delete(result, "loginId")
			}
			encodedResult, _ := json.Marshal(result)
			writeFakeMessage(protocol.Success(message.ID, encodedResult))
			if os.Getenv("CODEX_MUX_FAKE_LOGIN_COMPLETE") == "1" {
				succeeded := os.Getenv("CODEX_MUX_FAKE_LOGIN_FAIL") != "1"
				if succeeded && os.Getenv("CODEX_MUX_FAKE_LOGIN_UNCONFIRMED") != "1" {
					connected = true
				}
				completed, _ := json.Marshal(map[string]any{"loginId": loginID, "success": succeeded})
				writeFakeMessage(protocol.Message{Method: "account/login/completed", Params: completed})
				if os.Getenv("CODEX_MUX_FAKE_DUPLICATE_LOGIN_COMPLETE") == "1" {
					writeFakeMessage(protocol.Message{Method: "account/login/completed", Params: completed})
				}
			}
		case "account/login/cancel":
			var cancelInput struct {
				LoginID string `json:"loginId"`
			}
			_ = json.Unmarshal(message.Params, &cancelInput)
			appendFakeLog(logPath, "cancel-login:"+cancelInput.LoginID)
			if os.Getenv("CODEX_MUX_FAKE_DELAY_CANCEL") == "1" {
				time.Sleep(1500 * time.Millisecond)
			}
			if os.Getenv("CODEX_MUX_FAKE_COMPLETE_DURING_CANCEL") == "1" {
				connected = true
				completed, _ := json.Marshal(map[string]any{"loginId": cancelInput.LoginID, "success": true})
				writeFakeMessage(protocol.Message{Method: "account/login/completed", Params: completed})
				time.Sleep(100 * time.Millisecond)
			}
			if os.Getenv("CODEX_MUX_FAKE_CANCEL_FAIL") == "1" {
				writeFakeMessage(protocol.Failure(message.ID, -32000, "cancel failed"))
				continue
			}
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
			if os.Getenv("CODEX_MUX_FAKE_LATE_LOGIN_COMPLETE") == "1" {
				connected = true
				completed, _ := json.Marshal(map[string]any{"loginId": cancelInput.LoginID, "success": true})
				writeFakeMessage(protocol.Message{Method: "account/login/completed", Params: completed})
			}
		case "account/logout":
			appendFakeLog(logPath, "logout")
			if os.Getenv("CODEX_MUX_FAKE_LOGOUT_FAIL") == "1" {
				writeFakeMessage(protocol.Failure(message.ID, -32000, "logout failed"))
				continue
			}
			connected = false
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
		case "test/crash":
			appendFakeLog(logPath, "crash")
			os.Exit(23)
		case "test/hang":
			appendFakeLog(logPath, "hang")
		case "test/set-connected":
			connected = true
			writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
		default:
			if len(message.ID) > 0 {
				writeFakeMessage(protocol.Success(message.ID, json.RawMessage(`{}`)))
			}
		}
	}
	if os.Getenv("CODEX_MUX_FAKE_DELAY_EXIT") == "1" {
		time.Sleep(1500 * time.Millisecond)
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
	events, unsubscribe := multiplexer.SubscribeEvents()
	defer unsubscribe()
	multiplexer.now = func() time.Time { return time.Unix(second.ExpiresAt+1, 0) }
	expired, err := multiplexer.LoginStatus(second.ID)
	if err != nil || expired.State != LoginExpired {
		t.Fatalf("expired login = %#v, %v", expired, err)
	}
	select {
	case event := <-events:
		attempt, ok := event.Data.(LoginAttempt)
		if event.Type != "account-login" || event.AccountID != "primary" || !ok || attempt.ID != second.ID || attempt.State != LoginExpired {
			t.Fatalf("expired login event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("expired login event was not published")
	}
}

func TestEnabledLoginExpiresAutonomouslyAndPublishesTerminalEvent(t *testing.T) {
	multiplexer, store, logPath := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_LATE_LOGIN_COMPLETE=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "enabled-expiry")
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := multiplexer.SubscribeEvents()
	defer unsubscribe()
	multiplexer.loginMu.Lock()
	short := multiplexer.loginAttempts[attempt.ID]
	short.ExpiresAt = time.Now().Unix()
	multiplexer.loginAttempts[attempt.ID] = short
	multiplexer.loginMu.Unlock()
	multiplexer.scheduleLoginExpiry(attempt.ID, short.ExpiresAt)
	expiryDeadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			expired, ok := event.Data.(LoginAttempt)
			if event.Type == "account-login" && event.AccountID == "primary" && ok && expired.ID == attempt.ID {
				if expired.State != LoginExpired {
					t.Fatalf("autonomous expiry state = %#v", event)
				}
				goto expiryObserved
			}
		case <-expiryDeadline:
			t.Fatal("enabled login did not expire autonomously")
		}
	}

expiryObserved:
	account, ok := store.Account("primary")
	if !ok || !account.Enabled || !multiplexer.hasChild("primary") {
		t.Fatalf("enabled login expiry disrupted routing: account=%#v child=%v", account, multiplexer.hasChild("primary"))
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		logData, _ := os.ReadFile(logPath)
		if strings.Contains(string(logData), "cancel-login") && strings.Contains(string(logData), "logout") {
			snapshot, snapshotErr := multiplexer.accountSnapshot(context.Background(), "primary")
			if snapshotErr == nil && !snapshot.Connected {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	logData, _ := os.ReadFile(logPath)
	t.Fatalf("late OAuth completion remained routable; log=%s", logData)
}

func TestDuplicateProviderSuccessPublishesOnceAndDoesNotLogout(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, true)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_DUPLICATE_LOGIN_COMPLETE=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	events, unsubscribe := multiplexer.SubscribeEvents()
	defer unsubscribe()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "duplicate-success")
	if err != nil {
		t.Fatal(err)
	}
	terminalCount := 0
	deadline := time.After(3 * time.Second)
	settle := time.NewTimer(time.Hour)
	defer settle.Stop()
	for {
		select {
		case event := <-events:
			current, ok := event.Data.(LoginAttempt)
			if ok && current.ID == attempt.ID && current.State == LoginSucceeded {
				terminalCount++
				settle.Reset(250 * time.Millisecond)
			}
		case <-settle.C:
			if terminalCount != 1 {
				t.Fatalf("duplicate success published %d terminal events", terminalCount)
			}
			logData, _ := os.ReadFile(logPath)
			if strings.Contains(string(logData), "logout") {
				t.Fatalf("duplicate success logged out a valid account: %s", logData)
			}
			return
		case <-deadline:
			t.Fatalf("success event missing or duplicated: count=%d", terminalCount)
		}
	}
}

func TestCancelFailureIsTerminalAndPublishedOnce(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_CANCEL_FAIL=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "cancel-failure")
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := multiplexer.SubscribeEvents()
	defer unsubscribe()
	failed, cancelErr := multiplexer.CancelLogin(context.Background(), attempt.ID)
	if cancelErr == nil || failed.State != LoginFailed {
		t.Fatalf("cancel failure = %#v, %v", failed, cancelErr)
	}
	select {
	case event := <-events:
		terminal, ok := event.Data.(LoginAttempt)
		if !ok || terminal.ID != attempt.ID || terminal.State != LoginFailed {
			t.Fatalf("cancel failure event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel failure terminal event missing")
	}
	select {
	case duplicate := <-events:
		t.Fatalf("cancel failure published duplicate event: %#v", duplicate)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestCancelFailureCleansUpDisabledLoginChild(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_CANCEL_FAIL=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	disabled := false
	if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "cancel-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := multiplexer.CancelLogin(context.Background(), attempt.ID); err == nil {
		t.Fatal("cancel failure was not returned")
	}
	if multiplexer.hasChild("primary") {
		t.Fatal("disabled temporary login child survived cancel failure")
	}
}

func TestLoginUsesLivePreexistingConnectionState(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_PRECONNECTED=1", "CODEX_MUX_FAKE_CANCEL_FAIL=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "live-preconnected")
	if err != nil || !attempt.WasConnected {
		t.Fatalf("live connection state was not captured: %#v %v", attempt, err)
	}
	_, _ = multiplexer.CancelLogin(context.Background(), attempt.ID)
	logData, _ := os.ReadFile(logPath)
	if strings.Contains(string(logData), "logout") {
		t.Fatalf("stale persistence logged out a valid session: %s", logData)
	}
}

func TestLoginSuccessRequiresConnectedAccountReadAndHidesProviderID(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, true)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_LOGIN_UNCONFIRMED=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "unconfirmed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(attempt.Result), "loginId") || strings.Contains(string(attempt.Result), "fake-login") {
		t.Fatalf("provider login identifier escaped in attempt result: %s", attempt.Result)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := multiplexer.LoginStatus(attempt.ID)
		if statusErr == nil && status.State != LoginPending {
			if status.State != LoginFailed {
				t.Fatalf("unconfirmed account read produced success: %#v", status)
			}
			output := multiplexer.output.(*bytes.Buffer).String()
			if strings.Contains(output, "fake-login") || strings.Contains(output, "loginId") {
				t.Fatalf("provider login identifier reached mux output: %s", output)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("unconfirmed completion did not terminate")
}

func TestMissingAndReusedProviderLoginIDsFailClosed(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		multiplexer, _, logPath := newLifecycleMux(t, false)
		multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_MISSING_LOGIN_ID=1")
		if err := multiplexer.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer multiplexer.Close()
		attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "missing-provider-id")
		if err == nil || attempt.State != LoginFailed {
			t.Fatalf("missing loginId = %#v, %v", attempt, err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			logData, _ := os.ReadFile(logPath)
			if strings.Count(string(logData), "start:") >= 2 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("untracked provider flow was not terminated and restarted")
	})
	t.Run("reused", func(t *testing.T) {
		multiplexer, _, _ := newLifecycleMux(t, false)
		multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_REUSE_LOGIN_ID=1")
		if err := multiplexer.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer multiplexer.Close()
		first, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "reused-first")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := multiplexer.CancelLogin(context.Background(), first.ID); err != nil {
			t.Fatal(err)
		}
		second, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgptDeviceCode", "reused-second")
		if err == nil || second.State != LoginFailed {
			t.Fatalf("reused loginId = %#v, %v", second, err)
		}
	})
}

func TestInvalidProviderIDCleanupBlocksConcurrentRetry(t *testing.T) {
	for _, reused := range []bool{false, true} {
		name := "missing"
		if reused {
			name = "reused"
		}
		t.Run(name, func(t *testing.T) {
			multiplexer, _, logPath := newLifecycleMux(t, false)
			multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_DELAY_ACCOUNT_READ=1", "CODEX_MUX_FAKE_DELAY_EXIT=1")
			if reused {
				multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_REUSE_LOGIN_ID=1")
			} else {
				multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_MISSING_LOGIN_ID=1")
			}
			if err := multiplexer.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer multiplexer.Close()
			providerStarts := 0
			if reused {
				seed, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "invalid-id-seed")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := multiplexer.CancelLogin(context.Background(), seed.ID); err != nil {
					t.Fatal(err)
				}
				providerStarts = 1
			}
			initialLog, _ := os.ReadFile(logPath)
			initialChildStarts := strings.Count(string(initialLog), "start:")
			type result struct {
				attempt LoginAttempt
				err     error
			}
			results := make(chan result, 2)
			for _, key := range []string{"invalid-id-a", "invalid-id-b"} {
				go func(key string) {
					attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", key)
					results <- result{attempt: attempt, err: err}
				}(key)
				time.Sleep(100 * time.Millisecond)
			}
			first, second := <-results, <-results
			if first.attempt.ID == "" || first.attempt.ID != second.attempt.ID {
				t.Fatalf("concurrent retry escaped invalid-ID cleanup: %#v %#v", first, second)
			}
			logData, _ := os.ReadFile(logPath)
			if strings.Count(string(logData), "login-start") != providerStarts+1 {
				t.Fatalf("invalid-ID cleanup admitted another provider login: %s", logData)
			}
			deadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) {
				logData, _ = os.ReadFile(logPath)
				if strings.Count(string(logData), "start:") >= initialChildStarts+1 {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("confirmed teardown did not restart enabled account: %s", logData)
		})
	}
}

func TestInvalidProviderIDSynchronousTeardownRestartsWithoutDeadlock(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_MISSING_LOGIN_ID=1")
	multiplexer.watchChildDelay = time.Second
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	done := make(chan error, 1)
	go func() {
		_, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "sync-invalid-id")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing provider login ID unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("synchronous teardown deadlocked while restarting the enabled account")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logData, _ := os.ReadFile(logPath)
		if strings.Count(string(logData), "start:") >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("synchronous teardown did not restart the enabled account")
}

func TestUnknownOrMalformedCompletionDoesNotLogoutLiveAccount(t *testing.T) {
	for name, params := range map[string]json.RawMessage{
		"unknown":   json.RawMessage(`{"loginId":"unknown","success":true}`),
		"malformed": json.RawMessage(`{"success":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			multiplexer, _, logPath := newLifecycleMux(t, false)
			multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_PRECONNECTED=1")
			if err := multiplexer.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer multiplexer.Close()
			child, _ := multiplexer.child("primary")
			multiplexer.handleLoginCompleted("primary", child.Generation(), params)
			logData, _ := os.ReadFile(logPath)
			if strings.Contains(string(logData), "logout") {
				t.Fatalf("unknown completion logged out a live account: %s", logData)
			}
			snapshot, err := multiplexer.accountSnapshot(context.Background(), "primary")
			if err != nil || !snapshot.Connected {
				t.Fatalf("unknown completion disrupted live account: %#v %v", snapshot, err)
			}
		})
	}
}

func TestCancellationWinsConcurrentProviderSuccess(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_COMPLETE_DURING_CANCEL=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "cancel-race")
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := multiplexer.SubscribeEvents()
	defer unsubscribe()
	result, err := multiplexer.CancelLogin(context.Background(), attempt.ID)
	if err != nil || result.State != LoginCancelled {
		t.Fatalf("cancel did not win concurrent success: %#v %v", result, err)
	}
	terminalCount := 0
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case event := <-events:
			current, ok := event.Data.(LoginAttempt)
			if ok && current.ID == attempt.ID && current.State != LoginPending {
				if current.Cancelling {
					t.Fatalf("terminal event published before cancellation cleanup: %#v", current)
				}
				terminalCount++
			}
		case <-deadline:
			if terminalCount != 1 {
				t.Fatalf("cancel race published %d terminal events", terminalCount)
			}
			return
		}
	}
}

func TestRollbackFailurePublishesFinalTerminalStateOnce(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		name := "cancel"
		if expiry {
			name = "expiry"
		}
		t.Run(name, func(t *testing.T) {
			multiplexer, _, _ := newLifecycleMux(t, false)
			multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_LOGOUT_FAIL=1")
			if err := multiplexer.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer multiplexer.Close()
			attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "rollback-failure-"+name)
			if err != nil {
				t.Fatal(err)
			}
			events, unsubscribe := multiplexer.SubscribeEvents()
			defer unsubscribe()
			if expiry {
				multiplexer.loginMu.Lock()
				short := multiplexer.loginAttempts[attempt.ID]
				short.ExpiresAt = time.Now().Unix()
				multiplexer.loginAttempts[attempt.ID] = short
				multiplexer.loginMu.Unlock()
				_, _ = multiplexer.LoginStatus(attempt.ID)
			} else {
				_, _ = multiplexer.CancelLogin(context.Background(), attempt.ID)
			}
			terminalCount := 0
			deadline := time.After(5 * time.Second)
			for {
				select {
				case event := <-events:
					terminal, ok := event.Data.(LoginAttempt)
					if !ok || terminal.ID != attempt.ID || terminal.State == LoginPending {
						continue
					}
					terminalCount++
					if terminal.State != LoginFailed || terminal.Cancelling || !strings.Contains(strings.ToLower(terminal.Error), "rolled back") {
						t.Fatalf("published preliminary rollback state: %#v", terminal)
					}
				case <-deadline:
					if terminalCount != 1 {
						t.Fatalf("rollback failure published %d terminal events", terminalCount)
					}
					return
				}
			}
		})
	}
}

func TestLateSuccessAfterCancellationCleanupDoesNotRollbackAgain(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "late-after-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := multiplexer.CancelLogin(context.Background(), attempt.ID); err != nil {
		t.Fatal(err)
	}
	beforeLate, _ := os.ReadFile(logPath)
	logoutCount := strings.Count(string(beforeLate), "logout")
	child, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("primary child unavailable")
	}
	multiplexer.handleLoginCompleted("primary", child.Generation(), json.RawMessage(`{"loginId":"fake-login-1","success":true}`))
	afterLate, _ := os.ReadFile(logPath)
	if strings.Count(string(afterLate), "logout") != logoutCount {
		t.Fatalf("late success repeated cancellation rollback: %s", afterLate)
	}
}

func TestAdmittedCompletionCannotConfirmOrLogoutReplacementChild(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_DELAY_ACCOUNT_READ=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "admitted-old-completion")
	if err != nil {
		t.Fatal(err)
	}
	original, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("original child unavailable")
	}
	baseline, _ := os.ReadFile(logPath)
	baselineReads := strings.Count(string(baseline), "account-read")
	completed := make(chan struct{})
	go func() {
		multiplexer.handleLoginCompleted("primary", original.Generation(), json.RawMessage(`{"loginId":"fake-login-1","success":true}`))
		close(completed)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logData, _ := os.ReadFile(logPath)
		if strings.Count(string(logData), "account-read") > baselineReads {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = original.Close()

	var replacement *backend.Child
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		candidate, exists := multiplexer.child("primary")
		if exists && candidate.Generation() != original.Generation() {
			replacement = candidate
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if replacement == nil {
		t.Fatal("replacement child did not start")
	}
	if _, err := replacement.Request(context.Background(), "test/set-connected", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("old completion handler did not finish")
	}
	logData, _ := os.ReadFile(logPath)
	if strings.Contains(string(logData), "logout") {
		t.Fatalf("old completion logged out replacement child: %s", logData)
	}
	snapshot, err := multiplexer.accountSnapshot(context.Background(), "primary")
	if err != nil || !snapshot.Connected {
		t.Fatalf("replacement child did not remain connected: %#v %v", snapshot, err)
	}
	status, err := multiplexer.LoginStatus(attempt.ID)
	if err != nil || status.State == LoginSucceeded {
		t.Fatalf("old completion confirmed against replacement child: %#v %v", status, err)
	}
}

func TestOldGenerationRollbackFailureTeardownCannotStopReplacement(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	original, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("original child unavailable")
	}
	_ = original.Close()
	var replacement *backend.Child
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		candidate, exists := multiplexer.child("primary")
		if exists && candidate.Generation() != original.Generation() {
			replacement = candidate
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if replacement == nil {
		t.Fatal("replacement child did not start")
	}
	if _, err := replacement.Request(context.Background(), "test/set-connected", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := multiplexer.stopChildGeneration("primary", original.Generation()); err != nil {
		t.Fatal(err)
	}
	current, ok := multiplexer.child("primary")
	if !ok || current.Generation() != replacement.Generation() {
		t.Fatal("old rollback-failure teardown stopped the replacement child")
	}
	snapshot, err := multiplexer.accountSnapshot(context.Background(), "primary")
	if err != nil || !snapshot.Connected {
		t.Fatalf("replacement did not remain connected: %#v %v", snapshot, err)
	}
}

func TestLateCancelledSuccessCannotLogoutNewerSuccessfulRetry(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	first, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "generation-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := multiplexer.CancelLogin(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "generation-b")
	if err != nil {
		t.Fatal(err)
	}
	child, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("primary child unavailable")
	}
	if _, err := child.Request(context.Background(), "test/set-connected", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	child, _ = multiplexer.child("primary")
	multiplexer.handleLoginCompleted("primary", child.Generation(), json.RawMessage(`{"loginId":"fake-login-2","success":true}`))
	status, _ := multiplexer.LoginStatus(second.ID)
	if status.State != LoginSucceeded {
		t.Fatalf("retry did not connect: %#v", status)
	}
	beforeLate, _ := os.ReadFile(logPath)
	logoutCount := strings.Count(string(beforeLate), "logout")
	multiplexer.handleLoginCompleted("primary", child.Generation(), json.RawMessage(`{"loginId":"fake-login-1","success":true}`))
	logData, _ := os.ReadFile(logPath)
	if strings.Count(string(logData), "logout") != logoutCount {
		t.Fatalf("late older success logged out newer retry: %s", logData)
	}
	snapshot, err := multiplexer.accountSnapshot(context.Background(), "primary")
	if err != nil || !snapshot.Connected {
		t.Fatalf("newer retry did not remain connected: %#v %v", snapshot, err)
	}
}

func TestLoginCleanupBlocksReplacementAttempt(t *testing.T) {
	for _, expire := range []bool{false, true} {
		name := "cancel"
		if expire {
			name = "expiry"
		}
		t.Run(name, func(t *testing.T) {
			multiplexer, _, logPath := newLifecycleMux(t, false)
			multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_DELAY_CANCEL=1", "CODEX_MUX_FAKE_CANCEL_FAIL=1")
			if err := multiplexer.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer multiplexer.Close()
			disabled := false
			if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
				t.Fatal(err)
			}
			first, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", name+"-first")
			if err != nil {
				t.Fatal(err)
			}
			if expire {
				multiplexer.loginMu.Lock()
				attempt := multiplexer.loginAttempts[first.ID]
				attempt.ExpiresAt = time.Now().Add(-time.Second).Unix()
				multiplexer.loginAttempts[first.ID] = attempt
				multiplexer.loginMu.Unlock()
				go func() { _, _ = multiplexer.LoginStatus(first.ID) }()
			} else {
				go func() { _, _ = multiplexer.CancelLogin(context.Background(), first.ID) }()
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				logData, _ := os.ReadFile(logPath)
				if strings.Contains(string(logData), "cancel-login:") {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			replacement, _ := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", name+"-replacement")
			if replacement.ID != first.ID {
				t.Fatalf("replacement started during cleanup: first=%s replacement=%s", first.ID, replacement.ID)
			}
			deadline = time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				multiplexer.loginMu.Lock()
				cleaning := multiplexer.loginAttempts[first.ID].Cancelling
				multiplexer.loginMu.Unlock()
				if !cleaning {
					next, nextErr := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", name+"-after-cleanup")
					if nextErr != nil || next.ID == first.ID {
						t.Fatalf("new attempt did not start after cleanup: %#v %v", next, nextErr)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("login cleanup barrier did not clear")
		})
	}
}

func TestProviderLoginIDScopeResetsAfterEnabledChildRestart(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	first, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "before-restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := multiplexer.CancelLogin(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	child, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("primary child unavailable")
	}
	_, _ = child.Request(context.Background(), "test/crash", json.RawMessage(`{}`))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logData, _ := os.ReadFile(logPath)
		if strings.Count(string(logData), "start:") >= 2 && multiplexer.hasChild("primary") {
			second, secondErr := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "after-restart")
			if secondErr != nil || second.State != LoginPending || second.ID == first.ID {
				t.Fatalf("provider ID was not scoped to restarted child: %#v %v", second, secondErr)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("enabled child did not restart")
}

func TestPendingLoginFailsWithOwningChildAndRejectsStaleEvents(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	first, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "child-a")
	if err != nil {
		t.Fatal(err)
	}
	childA, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("first child unavailable")
	}
	generationA := childA.Generation()
	_, _ = childA.Request(context.Background(), "test/crash", json.RawMessage(`{}`))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := multiplexer.LoginStatus(first.ID)
		childB, childReady := multiplexer.child("primary")
		if statusErr == nil && status.State == LoginFailed && childReady && childB.Generation() != generationA {
			second, secondErr := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "child-b")
			if secondErr != nil || second.ID == first.ID || second.State != LoginPending {
				t.Fatalf("dead login was reused after restart: %#v %v", second, secondErr)
			}
			if _, requestErr := childB.Request(context.Background(), "test/set-connected", json.RawMessage(`{}`)); requestErr != nil {
				t.Fatal(requestErr)
			}
			multiplexer.handleLoginCompleted("primary", childB.Generation(), json.RawMessage(`{"loginId":"fake-login-1","success":true}`))
			before, _ := os.ReadFile(logPath)
			staleParams := json.RawMessage(`{"loginId":"fake-login-1","success":true}`)
			staleMessage := protocol.Message{Method: "account/login/completed", Params: staleParams}
			staleRaw, _ := protocol.Encode(staleMessage)
			multiplexer.handleInbound(backend.Inbound{AccountID: "primary", Generation: generationA, Message: staleMessage, Raw: staleRaw})
			after, _ := os.ReadFile(logPath)
			secondStatus, _ := multiplexer.LoginStatus(second.ID)
			if secondStatus.State != LoginSucceeded || strings.Count(string(after), "logout") != strings.Count(string(before), "logout") {
				t.Fatalf("stale child event affected replacement: status=%#v log=%s", secondStatus, after)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, _ := multiplexer.LoginStatus(first.ID)
	t.Fatalf("pending login survived owning child exit: %#v", status)
}

func TestChildExitFailsExactRoutesAndRejectsLateServerReply(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	childA, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("first child unavailable")
	}
	generationA := childA.Generation()
	requestID := protocol.StringID("client-route")
	if err := multiplexer.forward("primary", protocol.Request("test/hang", requestID, nil)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logData, _ := os.ReadFile(logPath)
		if strings.Contains(string(logData), "hang") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, _ = childA.Request(context.Background(), "test/crash", json.RawMessage(`{}`))
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		childB, ready := multiplexer.child("primary")
		if ready && childB.Generation() != generationA {
			output := multiplexer.output.(*bytes.Buffer).String()
			if !strings.Contains(output, "subscription backend stopped before responding") {
				t.Fatalf("dead external route was not failed: %s", output)
			}
			lateID := protocol.StringID("late-server-route")
			key := protocol.RequestIDKey(lateID)
			multiplexer.serverMu.Lock()
			multiplexer.serverRoutes[key] = serverRequestRoute{accountID: "primary", generation: generationA, original: protocol.StringID("original")}
			multiplexer.serverMu.Unlock()
			before, _ := os.ReadFile(logPath)
			multiplexer.handleServerRequestResponse(protocol.Success(lateID, json.RawMessage(`{}`)))
			after, _ := os.ReadFile(logPath)
			if strings.Count(string(after), "server-reply") != strings.Count(string(before), "server-reply") {
				t.Fatalf("late server reply was sent to replacement child: %s", after)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("replacement child did not start")
}

func TestStoppingChildIsNotSelectable(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	child, ok := multiplexer.child("primary")
	if !ok {
		t.Fatal("primary child unavailable")
	}
	multiplexer.childrenMu.Lock()
	multiplexer.stopping["primary"] = child
	multiplexer.childrenMu.Unlock()
	defer func() {
		multiplexer.childrenMu.Lock()
		delete(multiplexer.stopping, "primary")
		multiplexer.childrenMu.Unlock()
	}()
	if _, ok := multiplexer.child("primary"); ok {
		t.Fatal("stopping child remained directly selectable")
	}
	if entries := multiplexer.childEntries(); len(entries) != 0 {
		t.Fatalf("stopping child remained routable: %#v", entries)
	}
	account, _ := store.Account("primary")
	if _, err := multiplexer.startChildInternal(context.Background(), account, false); err == nil || !strings.Contains(err.Error(), "still stopping") {
		t.Fatalf("replacement started before teardown confirmation: %v", err)
	}
}

func TestInFlightChildStartCannotEscapeDisableOrStop(t *testing.T) {
	for _, action := range []string{"disable", "stop"} {
		t.Run(action, func(t *testing.T) {
			multiplexer, store, logPath := newLifecycleMux(t, false)
			multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_DELAY_INITIALIZE=1")
			defer multiplexer.Close()
			multiplexer.initializeParams = json.RawMessage(`{}`)
			account, _ := store.Account("primary")
			started := make(chan error, 1)
			go func() {
				_, err := multiplexer.startChild(context.Background(), account)
				started <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				logData, _ := os.ReadFile(logPath)
				if strings.Contains(string(logData), "initialize") {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if action == "disable" {
				disabled := false
				if _, err := store.UpdateAccount("primary", nil, &disabled); err != nil {
					t.Fatal(err)
				}
				if err := <-started; err == nil || !strings.Contains(err.Error(), "changed while") {
					t.Fatalf("disabled in-flight start was accepted: %v", err)
				}
			} else {
				stopped := make(chan error, 1)
				go func() { stopped <- multiplexer.stopChild("primary") }()
				if err := <-started; err != nil {
					t.Fatal(err)
				}
				if err := <-stopped; err != nil {
					t.Fatal(err)
				}
			}
			if multiplexer.hasChild("primary") {
				t.Fatalf("%s left an in-flight child selectable", action)
			}
		})
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

func TestLoginStartupOwnershipBlocksRetryUntilExactCleanup(t *testing.T) {
	multiplexer, _, logPath := newLifecycleMux(t, false)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_DELAY_ACCOUNT_READ=1", "CODEX_MUX_FAKE_LOGIN_START_FAIL=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	disabled := false
	if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	type startResult struct {
		attempt LoginAttempt
		err     error
	}
	results := make(chan startResult, 2)
	for _, key := range []string{"startup-owner-a", "startup-owner-b"} {
		go func(key string) {
			attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", key)
			results <- startResult{attempt: attempt, err: err}
		}(key)
		time.Sleep(100 * time.Millisecond)
	}
	first, second := <-results, <-results
	if first.attempt.ID == "" || first.attempt.ID != second.attempt.ID {
		t.Fatalf("concurrent startup escaped ownership: %#v %#v", first, second)
	}
	logData, _ := os.ReadFile(logPath)
	if strings.Count(string(logData), "login-start") != 1 {
		t.Fatalf("concurrent startup reached provider more than once: %s", logData)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := multiplexer.LoginStatus(first.attempt.ID)
		if statusErr == nil && !status.Cancelling && !multiplexer.hasChild("primary") {
			multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_LOGIN_START_FAIL=0", "CODEX_MUX_FAKE_DELAY_ACCOUNT_READ=0")
			next, nextErr := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "startup-owner-after")
			if nextErr != nil || next.ID == first.attempt.ID || !multiplexer.hasChild("primary") {
				t.Fatalf("retry did not advance safely after startup cleanup: %#v %v", next, nextErr)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("startup ownership cleanup did not release")
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
	multiplexer.scheduleLoginExpiry(attempt.ID, short.ExpiresAt)
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
	deadline := time.Now().Add(10 * time.Second)
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

func TestDisabledFailedLoginStopsExactTemporaryChild(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, true)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_LOGIN_FAIL=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	disabled := false
	if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "disabled-failed-completion")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := multiplexer.LoginStatus(attempt.ID)
		if statusErr == nil && status.State == LoginFailed && !multiplexer.hasChild("primary") {
			account, ok := store.Account("primary")
			if !ok || account.Enabled {
				t.Fatalf("failed login resumed routing: %#v", account)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("failed disabled login left its temporary child running: child=%v", multiplexer.hasChild("primary"))
}

func TestTerminalLoginCleanupBlocksConcurrentRetry(t *testing.T) {
	multiplexer, _, _ := newLifecycleMux(t, true)
	multiplexer.environment = append(multiplexer.environment, "CODEX_MUX_FAKE_LOGIN_FAIL=1")
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	disabled := false
	if _, err := multiplexer.UpdateAccount(context.Background(), "primary", nil, &disabled); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := multiplexer.SubscribeEvents()
	defer unsubscribe()
	attempt, err := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "cleanup-barrier-first")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			terminal, ok := event.Data.(LoginAttempt)
			if event.Type != "account-login" || !ok || terminal.ID != attempt.ID || terminal.State == LoginPending {
				continue
			}
			status, statusErr := multiplexer.LoginStatus(attempt.ID)
			if statusErr != nil || status.Cancelling {
				t.Fatalf("terminal event published before cleanup finished: %#v %v", status, statusErr)
			}
			retry, retryErr := multiplexer.StartLoginAttempt(context.Background(), "primary", "chatgpt", "cleanup-barrier-retry")
			if retryErr != nil || retry.ID == attempt.ID {
				t.Fatalf("retry did not advance after terminal event: %#v %v", retry, retryErr)
			}
			return
		case <-deadline:
			t.Fatal("terminal login event was not published")
		}
	}
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
