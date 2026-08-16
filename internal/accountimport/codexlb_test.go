package accountimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canonicalFixture = `{
  "auth_mode": "chatgpt",
  "OPENAI_API_KEY": null,
  "tokens": {
    "id_token": "id-secret",
    "access_token": "access-secret",
    "refresh_token": "refresh-secret",
    "account_id": "account-123"
  },
  "last_refresh": "2026-08-17T00:00:00.000000Z"
}`

func TestImportCodexLBExportRequiresPausedSource(t *testing.T) {
	_, err := ImportCodexLBExport(t.TempDir(), []byte(canonicalFixture), Options{})
	if err == nil || !strings.Contains(err.Error(), "must be paused") {
		t.Fatalf("expected source ownership error, got %v", err)
	}
}

func TestImportCodexLBEnvelopeBacksUpDestinationWithoutReturningSecrets(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "auth.json")
	if err := os.WriteFile(target, []byte("old credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	var canonical json.RawMessage = []byte(canonicalFixture)
	envelope, err := json.Marshal(map[string]any{"codex_auth_json": &canonical})
	if err != nil {
		t.Fatal(err)
	}

	result, err := ImportCodexLBExport(home, envelope, Options{SourcePaused: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "account-123" || result.BackupPath == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if strings.Contains(result.BackupPath, "secret") {
		t.Fatal("result exposed token material")
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "old credential" {
		t.Fatalf("unexpected backup: %q", backup)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"id-secret", "access-secret", "refresh-secret", "account-123"} {
		if !strings.Contains(string(installed), expected) {
			t.Fatalf("installed auth is missing %q", expected)
		}
	}
}

func TestImportCodexLBExportRejectsIncompleteCredential(t *testing.T) {
	_, err := ImportCodexLBExport(t.TempDir(), []byte(`{"auth_mode":"chatgpt","tokens":{}}`), Options{SourcePaused: true})
	if err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("expected validation error, got %v", err)
	}
}
