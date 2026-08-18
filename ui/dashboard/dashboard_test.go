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
		"Connecting to OpenAI",
		"About &amp; diagnostics",
		"Open the installed FLOW app",
		`aria-live="polite"`,
		"migration-preview",
		"technical-build",
		"FLOW",
		"Codex Router",
		"flowwweb-icon.png",
		"flow-wordmark.png",
		"settings-dialog",
		"login-dialog",
		"stats-dialog",
		"stats-content",
		"open-stats",
		"Cancel sign-in",
		"toast-region",
		"settings-toast-region",
		"icon-sprite",
		"account-sort",
		"show-spark-usage",
		"hide-account-emails",
		"account-plan",
		"skeleton-list",
		"skeleton-row",
		"button-icon",
		"remove-account-dialog",
		"account-menu-trigger",
		"account-details-trigger",
		"activity-strip",
		"refresh-account",
		"toggle-account",
		"state-icon",
		"action-icon",
		"dialog-actions",
	} {
		if !strings.Contains(html, text) {
			t.Errorf("index is missing %q", text)
		}
	}
	if _, contentType, ok := Asset("flowwweb-icon.png"); !ok || contentType != "image/png" {
		t.Fatalf("Flowwweb icon asset is unavailable: ok=%v type=%q", ok, contentType)
	}
	if _, contentType, ok := Asset("flow-wordmark.png"); !ok || contentType != "image/png" {
		t.Fatalf("FLOW wordmark asset is unavailable: ok=%v type=%q", ok, contentType)
	}
	for _, removed := range []string{"Subscription Router", "Your ChatGPT subscriptions", "subscriptions connected", "Connected account", "Primary account", "flowwweb-mark.svg"} {
		if strings.Contains(html+js, removed) {
			t.Errorf("dashboard retained non-essential copy %q", removed)
		}
	}
	for _, contract := range []string{"min-width: 0", "min-height: 44px", "min(calc(100% - 1.5rem), 46rem)", ".brand-mark", ".brand-lockup", ".brand-wordmark", ".icon-sprite", ".icon {", ".sort-control", ".setting-toggle", ".usage-row.spark", ".activity-dot", ".account-row.is-clickable", ".stats-summary", ".stats-bars", "@keyframes activity-pulse", ".dialog:focus { outline: none; }", ".toast.success", ".toast.error", ".dialog-toast-region", ".login-opening", ".skeleton-line", "@keyframes shimmer", "@keyframes account-row-in", "@keyframes action-spin", ".account-menu-panel", ".danger-button", "prefers-reduced-motion", "forced-colors: active", ":focus-visible"} {
		if !strings.Contains(css, contract) {
			t.Errorf("CSS is missing %q", contract)
		}
	}
	for _, contract := range []string{"usage-reset", "reset-credits", "aria-labelledby", "aria-describedby", "diagnostics[open]", "account-menu-panel"} {
		if !strings.Contains(css+js, contract) {
			t.Errorf("accessible dashboard styling is missing %q", contract)
		}
	}
	for _, contract := range []string{"history.replaceState", "connectAttempt", "credentials: 'same-origin'", "X-Codex-Mux-CSRF", "new EventSource('/v1/events')", "Router is offline", "Router unavailable", "Usage data could not be displayed", "Waiting for approval", "Finishing connection", "Account connected", "sourcePaused: true", "Sign-in cancelled", "account.controller", "Review this import", "Opening OpenAI", "Repair", "const needsRepair = Boolean(account.error)", "isPlaceholderAccount", "accountDisplayName", "accountIdentity", "readPreference", "writePreference", "orderedAccounts", "showSparkUsage", "hideAccountEmails", "showDataError", "loadActivity", "openStats", "renderStatsUnavailable", "combinedProfile", "/v1/profile/combined", "item?.windowDurationMins", "state.addInFlight", "[data-state-icon]", "statusNode.querySelector('.state-label')", "login.hidden = account.connected && !needsRepair", "codexMuxTrustedBrowserLoginURL", "getAll('redirect_uri')", "'/oauth/authorize'", "'/auth/callback'", "response_type", "code_challenge_method", "login?.verificationUrl", "window.open('', state.loginWindowName", "flow-openai-connect-", "popup.location.replace(uri)", "state.loginWindow.close()", "toastRegion.replaceChildren()", "finishLogin", "showLoginFinishing", "current.finished", "state.pending.size > 0", "Finish the current sign-in first.", "payload?.type === 'account-login'", "Ready to route", "Waiting for reset", "Routing paused", "Routing is ready.", "No usage available", "hasCapacity", "% left", "showModal()", "refreshAccount", "Usage refreshed.", "removeRequest", "closeRemoveDialog", "confirmRemoveAccount", "event.currentTarget.parentElement"} {
		if !strings.Contains(js, contract) {
			t.Errorf("JavaScript is missing %q", contract)
		}
	}
	for _, contract := range []string{"error.status = response.status", "error?.status === 401", "Dashboard access expired. Open FLOW again.", "Sign-in could not be cancelled. Try again.", "A new account could not be added. Try again."} {
		if !strings.Contains(js, contract) {
			t.Errorf("dashboard session recovery is missing %q", contract)
		}
	}
	if strings.Contains(string(Index)+string(JS), "Account added.") {
		t.Error("dashboard claims an account is added before OpenAI sign-in succeeds")
	}
	if strings.Contains(js, "style=") || strings.Contains(js, ".style.") {
		t.Error("dashboard JavaScript uses an inline style that violates the dashboard CSP")
	}
	if strings.Contains(js, "item.window") {
		t.Error("dashboard renderer still dereferences a non-existent item.window wrapper")
	}
	if strings.Contains(html, `role="menu"`) || strings.Contains(html, `role="menuitem"`) {
		t.Error("account disclosure uses menu roles without keyboard menu behavior")
	}
	if strings.Contains(js, "toast.focus") {
		t.Error("dashboard JavaScript moves focus into transient toasts")
	}
	for _, forbidden := range []string{"window.confirm", "Account ${state.accounts.length + 1}", "Imported account ${index + 1}", "Subscription ${connected.length + 1}"} {
		if strings.Contains(js, forbidden) {
			t.Errorf("dashboard retained generated account naming or browser confirmation %q", forbidden)
		}
	}
	for _, forbidden := range []string{"login?.deviceCode", "login?.device_code"} {
		if strings.Contains(js, forbidden) {
			t.Errorf("JavaScript exposes provider polling credential %q", forbidden)
		}
	}
}
