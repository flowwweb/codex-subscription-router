package mux

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedCodexLBVerificationRestoresCredentialAndBackend(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, false)
	if err := multiplexer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	account, ok := store.Account("primary")
	if !ok {
		t.Fatal("primary account missing")
	}
	target := filepath.Join(account.CodexHome, "auth.json")
	if err := os.WriteFile(target, []byte("original-auth"), 0o600); err != nil {
		t.Fatal(err)
	}
	exported := json.RawMessage(`{"auth_mode":"chatgpt","tokens":{"id_token":"id","access_token":"access","refresh_token":"refresh","account_id":"source-account"},"last_refresh":"2026-08-17T00:00:00Z"}`)
	if _, err := multiplexer.ImportCodexLBExport(context.Background(), "primary", exported, true); err == nil {
		t.Fatal("disconnected imported credential unexpectedly verified")
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "original-auth" {
		t.Fatalf("credential rollback restored %q", restored)
	}
	if _, live := multiplexer.child("primary"); !live {
		t.Fatal("prior backend was not restarted after migration rollback")
	}
}

func TestCodexLBImportRejectsCredentialAlreadyOwnedByAnotherAccount(t *testing.T) {
	multiplexer, store, _ := newLifecycleMux(t, false)
	defer multiplexer.Close()
	primary, ok := store.Account("primary")
	if !ok {
		t.Fatal("primary account missing")
	}
	destination, err := store.AddAccount("second")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary.CodexHome, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"id_token":"id","access_token":"access","refresh_token":"refresh","account_id":"source-account"},"last_refresh":"2026-08-17T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exported := json.RawMessage(`{"auth_mode":"chatgpt","tokens":{"id_token":"id","access_token":"access","refresh_token":"refresh","account_id":"source-account"},"last_refresh":"2026-08-17T00:00:00Z"}`)
	if _, err := multiplexer.ImportCodexLBExport(context.Background(), destination.ID, exported, true); err == nil || !strings.Contains(err.Error(), "already connected") {
		t.Fatalf("expected duplicate import rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination.CodexHome, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("duplicate import mutated destination auth: %v", err)
	}
}
