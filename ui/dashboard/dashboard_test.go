package dashboard

import (
	"strings"
	"testing"
)

func TestStaticDashboardContracts(t *testing.T) {
	html := string(Index)
	css := string(CSS)
	js := string(JS)
	for _, text := range []string{
		"Add account",
		"Settings",
		"Connect with ChatGPT",
		"About &amp; diagnostics",
		"official Codex Windows app does not currently route through this standalone router",
		`aria-live="polite"`,
		"Use this account",
		"migration-preview",
		"technical-build",
		"Codex Router",
		"flowwweb-icon.png",
		"settings-dialog",
		"login-dialog",
	} {
		if !strings.Contains(html, text) {
			t.Errorf("index is missing %q", text)
		}
	}
	if _, contentType, ok := Asset("flowwweb-icon.png"); !ok || contentType != "image/png" {
		t.Fatalf("Flowwweb icon asset is unavailable: ok=%v type=%q", ok, contentType)
	}
	for _, removed := range []string{"Subscription Router", "Your ChatGPT subscriptions", "subscriptions connected", "flowwweb-mark.svg"} {
		if strings.Contains(html+js, removed) {
			t.Errorf("dashboard retained non-essential copy %q", removed)
		}
	}
	for _, contract := range []string{"min-width: 0", "min-height: 44px", "min(calc(100% - 1.5rem), 46rem)", ".dialog:focus { outline: none; }", "prefers-reduced-motion", "forced-colors: active", ":focus-visible"} {
		if !strings.Contains(css, contract) {
			t.Errorf("CSS is missing %q", contract)
		}
	}
	for _, contract := range []string{"usage-reset", "aria-labelledby", "aria-describedby", "diagnostics[open]", "account-menu-panel"} {
		if !strings.Contains(css+js, contract) {
			t.Errorf("accessible dashboard styling is missing %q", contract)
		}
	}
	for _, contract := range []string{"history.replaceState", "credentials: 'same-origin'", "X-Codex-Mux-CSRF", "new EventSource('/v1/events')", "Router is offline", "Waiting for confirmation", "Account connected", "sourcePaused: true", "Sign-in cancelled", "account.controller", "Review this import", "Open ChatGPT", "Repair", "const needsRepair = Boolean(account.error)", "login.hidden = account.connected && !needsRepair", "trustedVerificationURL", "login?.userCode", "login?.verificationUrl", "notice.focus()", "Ready to route", "No usage available", "hasCapacity", "% left", "showModal()"} {
		if !strings.Contains(js, contract) {
			t.Errorf("JavaScript is missing %q", contract)
		}
	}
	for _, forbidden := range []string{"login?.deviceCode", "login?.device_code"} {
		if strings.Contains(js, forbidden) {
			t.Errorf("JavaScript exposes provider polling credential %q", forbidden)
		}
	}
}
