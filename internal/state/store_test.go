package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddAccountIdempotentPersistsAcrossReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	primary := filepath.Join(t.TempDir(), "primary")
	store, err := Open(root, primary)
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := store.AddAccountIdempotent("Work", "request-1")
	if err != nil || !created {
		t.Fatalf("first add = %#v, %v, %v", first, created, err)
	}
	reopened, err := Open(root, primary)
	if err != nil {
		t.Fatal(err)
	}
	second, created, err := reopened.AddAccountIdempotent("Different label", "request-1")
	if err != nil || created {
		t.Fatalf("replayed add = %#v, %v, %v", second, created, err)
	}
	if second.ID != first.ID || second.Label != "Work" {
		t.Fatalf("replay created or changed account: first=%#v second=%#v", first, second)
	}
}

func TestRemoveAccountArchivesHomeAndClearsReferences(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root, filepath.Join(t.TempDir(), "primary"))
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := store.AddAccountIdempotent("Work", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(account.CodexHome, "auth.json")
	if err := os.WriteFile(marker, []byte("test credential marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetThreadOwner("thread-1", account.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAccount(account.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Account(account.ID); ok {
		t.Fatal("removed account remains in state")
	}
	if _, ok := store.ThreadOwner("thread-1"); ok {
		t.Fatal("removed account retained thread ownership")
	}
	archives, err := filepath.Glob(filepath.Join(root, "removed-accounts", account.ID+"-*", "auth.json"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("recoverable account archive = %v, %v", archives, err)
	}
	replacement, created, err := store.AddAccountIdempotent("Replacement", "request-1")
	if err != nil || !created || replacement.ID == account.ID {
		t.Fatalf("create key was not released: %#v, %v, %v", replacement, created, err)
	}
}

func TestRemoveAccountRejectsController(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "primary"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAccount("primary"); err == nil {
		t.Fatal("controller removal unexpectedly succeeded")
	}
}

func TestOpenAcceptsLegacyVersionOneWithoutCreateKeys(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	primary := filepath.Join(t.TempDir(), "primary")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"version": 1,
		"accounts": []Account{{
			ID: "primary", Label: "Primary", CodexHome: primary,
			Enabled: true, Controller: true, CreatedAt: 1,
		}},
		"threadOwner": map[string]string{},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(root, primary)
	if err != nil {
		t.Fatal(err)
	}
	if account, ok := store.Account("primary"); !ok || account.Label != "Primary" {
		t.Fatalf("legacy account not loaded: %#v, %v", account, ok)
	}
}

func TestStoreBootstrapsPrimaryAndPersistsThreadAffinity(t *testing.T) {
	root := t.TempDir()
	primaryHome := filepath.Join(root, "primary")
	store, err := Open(filepath.Join(root, "mux"), primaryHome)
	if err != nil {
		t.Fatal(err)
	}
	accounts := store.Accounts()
	if len(accounts) != 1 || accounts[0].ID != "primary" || !accounts[0].Controller {
		t.Fatalf("unexpected bootstrap accounts: %#v", accounts)
	}
	added, err := store.AddAccount("Work")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(added.CodexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantConfig := "cli_auth_credentials_store = \"file\"\nmcp_oauth_credentials_store = \"file\"\n"
	if string(config) != wantConfig {
		t.Fatalf("unexpected isolated config: %q", config)
	}
	if err := store.SetThreadOwner("thread-1", added.ID); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(filepath.Join(root, "mux"), primaryHome)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := reopened.ThreadOwner("thread-1")
	if !ok || owner != added.ID {
		t.Fatalf("thread affinity was not persisted: owner=%q ok=%v", owner, ok)
	}
}

func TestAccountConfigInheritsManagedMCPAndPreservesLocalProjects(t *testing.T) {
	root := t.TempDir()
	primaryHome := filepath.Join(root, "primary")
	if err := os.MkdirAll(primaryHome, 0o700); err != nil {
		t.Fatal(err)
	}
	primaryConfig := `model = "gpt-test"

[mcp_servers.node_repl]
command = "/Applications/Codex Subscription Router.app/node_repl"

[mcp_servers.node_repl.env]
SKY_CUA_SERVICE_PATH = "/Applications/Codex Subscription Router Computer Use.app"

[projects."/primary-only"]
trust_level = "trusted"
`
	if err := os.WriteFile(filepath.Join(primaryHome, "config.toml"), []byte(primaryConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	muxRoot := filepath.Join(root, "mux")
	store, err := Open(muxRoot, primaryHome)
	if err != nil {
		t.Fatal(err)
	}
	added, err := store.AddAccount("Work")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(added.CodexHome, "config.toml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, expected := range []string{
		`cli_auth_credentials_store = "file"`,
		`mcp_oauth_credentials_store = "file"`,
		`model = "gpt-test"`,
		`[mcp_servers.node_repl]`,
		`SKY_CUA_SERVICE_PATH = "/Applications/Codex Subscription Router Computer Use.app"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("account config is missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "/primary-only") {
		t.Fatalf("primary project trust leaked into account config:\n%s", text)
	}

	text += `
[projects."/account-project"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	primaryConfig = strings.ReplaceAll(primaryConfig, "gpt-test", "gpt-updated")
	if err := os.WriteFile(filepath.Join(primaryHome, "config.toml"), []byte(primaryConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(muxRoot, primaryHome); err != nil {
		t.Fatal(err)
	}
	config, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text = string(config)
	if !strings.Contains(text, `model = "gpt-updated"`) {
		t.Fatalf("managed config was not refreshed:\n%s", text)
	}
	if !strings.Contains(text, `[projects."/account-project"]`) {
		t.Fatalf("account project trust was not preserved:\n%s", text)
	}
}

func TestSyncManagedConfigPropagatesPluginsWithoutRestart(t *testing.T) {
	root := t.TempDir()
	primaryHome := filepath.Join(root, "primary")
	if err := os.MkdirAll(primaryHome, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(primaryHome, "config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"before\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(root, "mux"), primaryHome)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.AddAccount("Work")
	if err != nil {
		t.Fatal(err)
	}
	updated := "model = \"after\"\n\n[plugins.\"browser@openai-bundled\"]\nenabled = true\n"
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncManagedConfig(); err != nil {
		t.Fatal(err)
	}
	isolated, err := os.ReadFile(filepath.Join(account.CodexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(isolated), `[plugins."browser@openai-bundled"]`) {
		t.Fatalf("plugin config did not propagate:\n%s", isolated)
	}
}

func TestUpdateAccountPreservesController(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, filepath.Join(root, "primary"))
	if err != nil {
		t.Fatal(err)
	}
	label := "Personal"
	enabled := false
	account, err := store.UpdateAccount("primary", &label, &enabled)
	if err != nil {
		t.Fatal(err)
	}
	if account.Label != label || account.Enabled || !account.Controller {
		t.Fatalf("unexpected updated account: %#v", account)
	}
}

func TestSetAccountIdentityPersistsNonSecretMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	primary := filepath.Join(t.TempDir(), "primary")
	store, err := Open(root, primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAccountIdentity("primary", "person@example.com", "plus"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, primary)
	if err != nil {
		t.Fatal(err)
	}
	account, ok := reopened.Account("primary")
	if !ok || account.LastKnownEmail != "person@example.com" || account.LastKnownPlanType != "plus" {
		t.Fatalf("identity metadata was not persisted: %#v, %v", account, ok)
	}
}
