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
		"Connect subscription",
		"Technical details",
		"official Codex Windows app does not currently route through it",
		`aria-live="polite"`,
		"Included in routing",
		"migration-preview",
		"technical-build",
	} {
		if !strings.Contains(html, text) {
			t.Errorf("index is missing %q", text)
		}
	}
	for _, contract := range []string{"min-width: 0", "min-height: 44px", "prefers-reduced-motion", "forced-colors: active", ":focus-visible"} {
		if !strings.Contains(css, contract) {
			t.Errorf("CSS is missing %q", contract)
		}
	}
	for _, contract := range []string{"usage-reset", "aria-labelledby", "aria-describedby", "summary::after", "details[open] > summary::after"} {
		if !strings.Contains(css+js, contract) {
			t.Errorf("accessible dashboard styling is missing %q", contract)
		}
	}
	for _, contract := range []string{"history.replaceState", "credentials: 'same-origin'", "X-Codex-Mux-CSRF", "new EventSource('/v1/events')", "Router is offline", "Waiting for ChatGPT", "Subscription connected", "sourcePaused: true", "Sign-in cancelled", "account.controller", "Review this one-time migration", "backupAvailable", "Open ChatGPT verification", "restoreFocus", "login?.authUrl", "notice.focus()"} {
		if !strings.Contains(js, contract) {
			t.Errorf("JavaScript is missing %q", contract)
		}
	}
}
