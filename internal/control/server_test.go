package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/mux"
	"github.com/b-nnett/codex-subscription-router/internal/state"
)

const testBrowserLoginURL = "https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback"

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

	browser := presentLogin(json.RawMessage(`{"type":"chatgpt","loginId":"login-1","authUrl":"` + testBrowserLoginURL + `"}`))
	if browser.UserCode != "" || !TrustedOpenAIBrowserLoginURL(browser.VerificationURL) {
		t.Fatalf("browser login URL was not presented safely: %#v", browser)
	}

	event := presentEvent(mux.Event{Type: "account-login", Data: mux.LoginAttempt{ID: "login-1", Result: json.RawMessage(`{"deviceCode":"private-polling-secret"}`)}})
	eventJSON, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(eventJSON), "private-polling-secret") || strings.Contains(string(eventJSON), "result") {
		t.Fatalf("login event exposed provider result: %s", eventJSON)
	}
	providerError := `unauthorized C:\\Users\\person\\.codex\\auth.json token=secret\nnext-line`
	presentedAttempt, err := json.Marshal(presentLoginAttempt(mux.LoginAttempt{ID: "login-2", State: mux.LoginFailed, Error: providerError}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(presentedAttempt), "person") || strings.Contains(string(presentedAttempt), "secret") || strings.Contains(string(presentedAttempt), "next-line") || !strings.Contains(string(presentedAttempt), "OpenAI account connection failed") {
		t.Fatalf("public attempt did not sanitize provider error: %s", presentedAttempt)
	}
	presentedAccount, err := json.Marshal(presentAccountSnapshot(mux.AccountSnapshot{ID: "primary", Error: providerError}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(presentedAccount), "person") || strings.Contains(string(presentedAccount), "secret") || strings.Contains(string(presentedAccount), "next-line") || !strings.Contains(string(presentedAccount), "Account is unavailable") {
		t.Fatalf("public account did not sanitize provider error: %s", presentedAccount)
	}
}

func TestTrustedOpenAIVerificationURLRejectsLookalikesAndUnsafePorts(t *testing.T) {
	for _, trusted := range []string{
		"https://chatgpt.com/device", "https://auth.openai.com/codex/device", "https://login.auth.openai.com:443/device",
	} {
		if !TrustedOpenAIVerificationURL(trusted) {
			t.Fatalf("trusted verification URL rejected: %s", trusted)
		}
	}
	for _, untrusted := range []string{
		"http://auth.openai.com/device", "https://auth.openai.com.evil.test/device", "https://user@chatgpt.com/device",
		"https://chatgpt.com:444/device", "https://chatgpt.com/device\nmalicious", strings.Repeat("x", 2049),
	} {
		if TrustedOpenAIVerificationURL(untrusted) {
			t.Fatalf("unsafe verification URL accepted: %q", untrusted)
		}
	}
}

func TestTrustedOpenAIBrowserLoginURLRequiresOneLoopbackCallback(t *testing.T) {
	for _, trusted := range []string{
		testBrowserLoginURL,
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Fauth%2Fcallback",
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2F%5B%3A%3A1%5D%3A49152%2Fauth%2Fcallback",
	} {
		if !TrustedOpenAIBrowserLoginURL(trusted) {
			t.Fatalf("trusted browser login URL rejected: %s", trusted)
		}
	}
	for _, untrusted := range []string{
		"https://auth.openai.com/oauth/authorize",
		"https://auth.openai.com/not-authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback",
		"https://chatgpt.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback",
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=https%3A%2F%2Fattacker.example%2Fcallback",
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fwrong",
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&redirect_uri=http%3A%2F%2Flocalhost%3A2455%2Fauth%2Fcallback",
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%2Fauth%2Fcallback",
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2F%2561uth%2Fcallback",
		"https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&bad=%ZZ&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback",
		testBrowserLoginURL + "#fragment",
	} {
		if TrustedOpenAIBrowserLoginURL(untrusted) {
			t.Fatalf("unsafe browser login URL accepted: %s", untrusted)
		}
	}
}

func TestTaskActionsRequireExactTokenAndRuntimeInstance(t *testing.T) {
	token := strings.Repeat("a", 64)
	server := New(testHost, token, nil, false)
	server.SetRuntimeIdentity("runtime-1")
	for name, test := range map[string]struct {
		token, instance string
		want            bool
	}{
		"exact":           {token, "runtime-1", true},
		"missing token":   {"", "runtime-1", false},
		"wrong token":     {strings.Repeat("b", 64), "runtime-1", false},
		"missing runtime": {token, "", false},
		"wrong runtime":   {token, "runtime-2", false},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/v1/task-actions/status", nil)
			request.Header.Set("X-Codex-Mux-Token", test.token)
			request.Header.Set("X-Codex-Mux-Instance", test.instance)
			if got := server.taskAuthorized(request); got != test.want {
				t.Fatalf("taskAuthorized() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestTaskPresentationsDoNotExposeBackendErrors(t *testing.T) {
	secret := "device_code=private-secret\nC:\\Users\\person\\.codex"
	accountJSON, err := json.Marshal(taskAccountFromSnapshot(mux.AccountSnapshot{
		ID: "account-1", Label: "Primary", Error: secret,
	}))
	if err != nil {
		t.Fatal(err)
	}
	attemptJSON, err := json.Marshal(presentTaskLoginAttempt(mux.LoginAttempt{
		ID: "attempt-1", State: mux.LoginFailed, Error: secret,
	}))
	if err != nil {
		t.Fatal(err)
	}
	combined := string(accountJSON) + string(attemptJSON)
	if strings.Contains(combined, "private-secret") || strings.Contains(combined, `C:\Users`) || strings.Contains(combined, "device_code") {
		t.Fatalf("task presentation exposed backend details: %s", combined)
	}
	if len(combined) > 512 {
		t.Fatalf("task presentation is unexpectedly large: %d", len(combined))
	}
}

func TestLoginCleanupRemainsPendingInPublicPresentations(t *testing.T) {
	attempt := mux.LoginAttempt{ID: "attempt-1", State: mux.LoginFailed, Error: "private failure", Cancelling: true}
	public := presentLoginAttempt(attempt)
	task := presentTaskLoginAttempt(attempt)
	if public.State != mux.LoginPending || public.Error != "" || task.State != mux.LoginPending || task.Error != "" {
		t.Fatalf("cleanup was exposed as retryable terminal state: public=%#v task=%#v", public, task)
	}
}

func TestAuthenticatedAccountMutationsDoNotExposeBackendErrors(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"), filepath.Join(root, "primary"))
	if err != nil {
		t.Fatal(err)
	}
	multiplexer, err := mux.New(mux.Options{
		RealExecutable: filepath.Join(root, "private-backend-secret.exe"),
		Store:          store,
		Output:         io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	server := New(testHost, "token", multiplexer, false)
	cookie, csrf, _ := bootstrap(t, server)

	requestMutation := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://"+testHost+target, strings.NewReader(body))
		req.Host = testHost
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://"+testHost)
		req.Header.Set("X-Codex-Mux-CSRF", csrf)
		req.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}

	for name, test := range map[string]struct {
		response *httptest.ResponseRecorder
		expected string
	}{
		"add":         {requestMutation(http.MethodPost, "/v1/accounts", `{"label":"Work","idempotencyKey":"redaction-add"}`), "Account could not be added"},
		"update":      {requestMutation(http.MethodPatch, "/v1/accounts/primary", `{"enabled":true}`), "Account could not be updated"},
		"disconnect":  {requestMutation(http.MethodPost, "/v1/accounts/primary/logout", `{}`), "Account could not be disconnected"},
		"remove":      {requestMutation(http.MethodDelete, "/v1/accounts/primary", `{}`), "Account could not be removed"},
		"import":      {requestMutation(http.MethodPost, "/v1/accounts/primary/import/codex-lb", `{"export":{},"sourcePaused":true}`), "Account import failed"},
		"list resets": {requestMutation(http.MethodGet, "/v1/accounts/primary/rate-limit-resets", ``), "Usage resets are unavailable"},
		"apply reset": {requestMutation(http.MethodPost, "/v1/accounts/primary/rate-limit-resets/consume", `{"redeemRequestId":"redaction-reset"}`), "Usage reset could not be applied"},
	} {
		body := test.response.Body.String()
		if test.response.Code < 400 || strings.Contains(body, "private-backend-secret") || strings.Contains(body, root) || !strings.Contains(body, test.expected) {
			t.Fatalf("%s response exposed backend details: status=%d body=%s", name, test.response.Code, body)
		}
	}
}

func TestAuthenticatedProfileAndThreadErrorsDoNotExposeBackendDetails(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"), filepath.Join(root, "private-auth-path"))
	if err != nil {
		t.Fatal(err)
	}
	multiplexer, err := mux.New(mux.Options{RealExecutable: filepath.Join(root, "backend.exe"), Store: store, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer multiplexer.Close()
	server := New(testHost, "token", multiplexer, false)
	cookie, _, _ := bootstrap(t, server)

	for name, target := range map[string]string{
		"profile": "/v1/profile/combined?accountId=private-secret",
		"thread":  "/v1/thread-account?threadId=private-secret",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+testHost+target, nil)
		req.Host = testHost
		req.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		body := response.Body.String()
		if response.Code < 400 || strings.Contains(body, "private-secret") || strings.Contains(body, root) || strings.Contains(body, "auth-path") {
			t.Fatalf("%s response exposed backend details: status=%d body=%s", name, response.Code, body)
		}
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
