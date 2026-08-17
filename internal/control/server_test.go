package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/mux"
)

func TestPresentLoginOnlyReturnsUserCredentialAndTrustedOpenAIURL(t *testing.T) {
	presentation := presentLogin(json.RawMessage(`{"userCode":"ABCD-EFGH","deviceCode":"private-polling-secret","verificationUrl":"https://auth.openai.com/codex/device"}`))
	if presentation.UserCode != "ABCD-EFGH" || presentation.VerificationURL != "https://auth.openai.com/codex/device" {
		t.Fatalf("unexpected login presentation: %#v", presentation)
	}
	encoded, err := json.Marshal(presentation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-polling-secret") || strings.Contains(string(encoded), "deviceCode") {
		t.Fatalf("public login response exposed provider polling credentials: %s", encoded)
	}

	untrusted := presentLogin(json.RawMessage(`{"user_code":"WXYZ-1234","verification_uri":"https://example.com/steal"}`))
	if untrusted.UserCode != "WXYZ-1234" || untrusted.VerificationURL != "" {
		t.Fatalf("untrusted login destination was not removed: %#v", untrusted)
	}

	event := presentEvent(mux.Event{Type: "account-login", Data: mux.LoginAttempt{ID: "login-1", Result: json.RawMessage(`{"deviceCode":"private-polling-secret"}`)}})
	eventJSON, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(eventJSON), "private-polling-secret") || strings.Contains(string(eventJSON), "result") {
		t.Fatalf("login event exposed provider result: %s", eventJSON)
	}
}

const testHost = "127.0.0.1:48123"

func request(server *Server, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+testHost+target, strings.NewReader(body))
	req.Host = testHost
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	return response
}

func bootstrap(t *testing.T, server *Server) (*http.Cookie, string, string) {
	t.Helper()
	bootstrapURL, err := server.IssueBootstrap("http://" + testHost + "/")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(bootstrapURL)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	nonce := values.Get("bootstrap")
	req := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/v1/session/bootstrap", strings.NewReader(`{"nonce":"`+nonce+`"}`))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+testHost)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookie: %#v", cookies)
	}
	return cookies[0], payload.CSRF, nonce
}

func TestBootstrapIsOneUseAndSessionSurvivesRefresh(t *testing.T) {
	server := New(testHost, strings.Repeat("a", 64), nil, false)
	cookie, csrf, nonce := bootstrap(t, server)
	if csrf == "" {
		t.Fatal("empty CSRF token")
	}

	replay := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/v1/session/bootstrap", strings.NewReader(`{"nonce":"`+nonce+`"}`))
	replay.Host = testHost
	replay.Header.Set("Content-Type", "application/json")
	replayResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", replayResponse.Code)
	}

	refresh := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/v1/session", nil)
	refresh.Host = testHost
	refresh.AddCookie(cookie)
	refreshResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(refreshResponse, refresh)
	if refreshResponse.Code != http.StatusOK || !strings.Contains(refreshResponse.Body.String(), csrf) {
		t.Fatalf("session refresh status = %d, body = %s", refreshResponse.Code, refreshResponse.Body.String())
	}
}

func TestBootstrapExpires(t *testing.T) {
	now := time.Unix(100, 0)
	server := NewWithOptions(testHost, "token", nil, false, Options{
		BootstrapTTL: time.Second,
		Now:          func() time.Time { return now },
	})
	bootstrapURL, err := server.IssueBootstrap("http://" + testHost + "/")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bootstrapURL)
	values, _ := url.ParseQuery(parsed.Fragment)
	now = now.Add(2 * time.Second)
	response := request(server, http.MethodPost, "/v1/session/bootstrap", `{"nonce":"`+values.Get("bootstrap")+`"}`)
	if response.Code != http.StatusBadRequest { // missing JSON content type is rejected before nonce handling
		t.Fatalf("content-type status = %d", response.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/v1/session/bootstrap", strings.NewReader(`{"nonce":"`+values.Get("bootstrap")+`"}`))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/json")
	expired := httptest.NewRecorder()
	server.Handler().ServeHTTP(expired, req)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired bootstrap status = %d", expired.Code)
	}
}

func TestRejectsUnexpectedHostOriginAndPreflight(t *testing.T) {
	server := New(testHost, "token", nil, false)

	wrongHost := httptest.NewRequest(http.MethodGet, "http://attacker.test/v1/health", nil)
	wrongHost.Host = "attacker.test"
	wrongHostResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongHostResponse, wrongHost)
	if wrongHostResponse.Code != http.StatusMisdirectedRequest {
		t.Fatalf("wrong Host status = %d", wrongHostResponse.Code)
	}

	wrongOrigin := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/v1/health", nil)
	wrongOrigin.Host = testHost
	wrongOrigin.Header.Set("Origin", "http://attacker.test")
	wrongOriginResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongOriginResponse, wrongOrigin)
	if wrongOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong Origin status = %d", wrongOriginResponse.Code)
	}

	preflight := httptest.NewRequest(http.MethodOptions, "http://"+testHost+"/v1/accounts", nil)
	preflight.Host = testHost
	preflight.Header.Set("Origin", "http://"+testHost)
	preflightResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusForbidden {
		t.Fatalf("same-origin preflight status = %d", preflightResponse.Code)
	}
}

func TestDurableTokenIsNeverAcceptedFromQuery(t *testing.T) {
	token := strings.Repeat("b", 64)
	server := New(testHost, token, nil, false)
	response := request(server, http.MethodGet, "/v1/accounts?token="+token, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("query token status = %d", response.Code)
	}
}

func TestSessionMutationRequiresCSRF(t *testing.T) {
	server := New(testHost, "token", nil, false)
	cookie, csrf, _ := bootstrap(t, server)
	req := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/v1/accounts", strings.NewReader(`{"label":"Work"}`))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing CSRF status = %d", response.Code)
	}
	req.Header.Set("X-Codex-Mux-CSRF", "wrong")
	if server.csrfAuthorized(req) {
		t.Fatal("wrong CSRF token was accepted")
	}
	req.Header.Set("X-Codex-Mux-CSRF", csrf)
	if !server.csrfAuthorized(req) {
		t.Fatal("valid CSRF token was rejected")
	}
}

func TestSessionAuthorizesSSEWithoutURLCredential(t *testing.T) {
	server := New(testHost, "token", nil, false)
	cookie, _, _ := bootstrap(t, server)
	req := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/v1/events", nil)
	req.Host = testHost
	req.AddCookie(cookie)
	if !server.authorized(req) {
		t.Fatal("same-origin session did not authorize SSE")
	}
	if req.URL.RawQuery != "" {
		t.Fatalf("SSE request unexpectedly contains a query credential: %q", req.URL.RawQuery)
	}
}

func TestDashboardUsesStrictSecurityHeaders(t *testing.T) {
	server := New(testHost, "token", nil, false)
	response := request(server, http.MethodGet, "/", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Codex Router") {
		t.Fatalf("dashboard status = %d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("missing strict CSP: %q", response.Header().Get("Content-Security-Policy"))
	}
}

func TestAuthenticatedHealthIncludesTechnicalDetailsWithoutLeakingThem(t *testing.T) {
	server := New(testHost, "token", nil, false)
	server.SetTechnicalDetails(TechnicalDetails{Build: "build-123", StateRoot: `C:\private\state`, PrimaryCodexHome: `C:\private\primary`})
	public := request(server, http.MethodGet, "/v1/health", "")
	if strings.Contains(public.Body.String(), "private") {
		t.Fatal("unauthenticated health leaked local support paths")
	}
	cookie, _, _ := bootstrap(t, server)
	req := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/v1/health", nil)
	req.Host = testHost
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "build-123") || !strings.Contains(response.Body.String(), "primary") {
		t.Fatalf("authenticated health omitted technical details: %s", response.Body.String())
	}
}
