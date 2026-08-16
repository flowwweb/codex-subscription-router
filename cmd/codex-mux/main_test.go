package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInteractiveAppServerDetection(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{args: []string{"-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled"}, want: true},
		{args: []string{"app-server", "daemon", "version"}, want: false},
		{args: []string{"app-server", "generate-ts", "--out", "/tmp/schema"}, want: false},
		{args: []string{"exec", "hello"}, want: false},
	}
	for _, test := range tests {
		if got := isInteractiveAppServer(test.args); got != test.want {
			t.Fatalf("isInteractiveAppServer(%q)=%v, want %v", test.args, got, test.want)
		}
	}
}

func TestValidateControlToken(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got, err := validateControlToken("\n" + valid + "\t"); err != nil || got != valid {
		t.Fatalf("validateControlToken(valid) = %q, %v", got, err)
	}
	for _, invalid := range []string{"short", valid + "00", valid[:63] + "z"} {
		if _, err := validateControlToken(invalid); err == nil {
			t.Fatalf("validateControlToken(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestResolveRealExecutableUsesConfiguredPath(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "codex.exe")
	if err := os.WriteFile(configured, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_MUX_REAL_CODEX", configured)
	got, err := resolveRealExecutable()
	if err != nil {
		t.Fatalf("resolveRealExecutable() error = %v", err)
	}
	if got != configured {
		t.Fatalf("resolveRealExecutable() = %q, want %q", got, configured)
	}
}

func TestResolveRealExecutableRejectsMissingConfiguredPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-codex.exe")
	t.Setenv("CODEX_MUX_REAL_CODEX", missing)
	if _, err := resolveRealExecutable(); err == nil {
		t.Fatal("resolveRealExecutable() unexpectedly accepted a missing configured path")
	}
}

func TestWindowsUsesExeSiblingName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows sibling naming is platform-specific")
	}
	// The Windows install uses CODEX_MUX_REAL_CODEX explicitly, while this
	// assertion protects future bundled-wrapper builds from regressing to the
	// extensionless Unix name.
	if filepath.Ext("codex.real.exe") != ".exe" {
		t.Fatal("Windows real Codex sibling must retain the .exe extension")
	}
}
