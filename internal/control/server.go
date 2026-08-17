package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/mux"
	"github.com/b-nnett/codex-subscription-router/ui/dashboard"
)

const sessionCookieName = "codex_mux_session"

type Options struct {
	BootstrapTTL time.Duration
	SessionTTL   time.Duration
	Now          func() time.Time
}

type TechnicalDetails struct {
	Build            string `json:"build"`
	StateRoot        string `json:"stateRoot"`
	PrimaryCodexHome string `json:"primaryCodexHome"`
}

type browserSession struct {
	csrf      string
	expiresAt time.Time
}

type Server struct {
	token   string
	mux     *mux.Multiplexer
	uiTests bool
	http    *http.Server

	mu           sync.RWMutex
	hosts        map[string]struct{}
	bootstraps   map[string]time.Time
	sessions     map[string]browserSession
	bootstrapTTL time.Duration
	sessionTTL   time.Duration
	now          func() time.Time
	technical    TechnicalDetails
}

func New(address, token string, multiplexer *mux.Multiplexer, uiTests bool) *Server {
	return NewWithOptions(address, token, multiplexer, uiTests, Options{})
}

func NewWithOptions(address, token string, multiplexer *mux.Multiplexer, uiTests bool, options Options) *Server {
	if options.BootstrapTTL <= 0 {
		options.BootstrapTTL = 2 * time.Minute
	}
	if options.SessionTTL <= 0 {
		options.SessionTTL = 12 * time.Hour
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	server := &Server{
		token: token, mux: multiplexer, uiTests: uiTests,
		hosts: make(map[string]struct{}), bootstraps: make(map[string]time.Time),
		sessions: make(map[string]browserSession), bootstrapTTL: options.BootstrapTTL,
		sessionTTL: options.SessionTTL, now: options.Now,
	}
	_ = server.RegisterRuntime(address)
	router := http.NewServeMux()
	router.HandleFunc("/", server.dashboard)
	router.HandleFunc("/assets/", server.dashboardAsset)
	router.HandleFunc("/v1/session", server.browserSession)
	router.HandleFunc("/v1/session/bootstrap", server.bootstrapSession)
	router.HandleFunc("/v1/health", server.health)
	router.HandleFunc("/v1/accounts", server.accounts)
	router.HandleFunc("/v1/accounts/", server.accountAction)
	router.HandleFunc("/v1/login-attempts/", server.loginAttemptAction)
	router.HandleFunc("/v1/thread-account", server.threadAccount)
	router.HandleFunc("/v1/profile/combined", server.combinedProfile)
	router.HandleFunc("/v1/events", server.events)
	if uiTests {
		router.HandleFunc("/v1/test/rate-limits", server.rateLimitPreview)
		router.HandleFunc("/v1/test/rate-limit-resets", server.resetCreditsPreview)
	}
	server.http = &http.Server{
		Addr:              address,
		Handler:           server.securityHeaders(router),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	return server
}

// RegisterRuntime allows a daemon to publish the exact loopback listener it owns.
// The server rejects every Host header that has not been registered here.
func (s *Server) RegisterRuntime(address string) error {
	host, err := loopbackHost(address)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.hosts[host] = struct{}{}
	s.mu.Unlock()
	return nil
}

// IssueBootstrap returns a URL whose fragment contains a short-lived, one-use
// nonce. URL fragments are not sent in HTTP requests or referrer headers.
func (s *Server) IssueBootstrap(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return "", errors.New("dashboard URL must be an absolute HTTP loopback URL")
	}
	if err := s.RegisterRuntime(parsed.Host); err != nil {
		return "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.pruneLocked()
	s.bootstraps[nonce] = s.now().Add(s.bootstrapTTL)
	s.mu.Unlock()
	parsed.RawQuery = ""
	parsed.Fragment = "bootstrap=" + url.QueryEscape(nonce)
	return parsed.String(), nil
}

func (s *Server) Handler() http.Handler { return s.http.Handler }

func (s *Server) SetTechnicalDetails(details TechnicalDetails) { s.technical = details }

func (s *Server) combinedProfile(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	accountID := strings.TrimSpace(request.URL.Query().Get("accountId"))
	var profile mux.CombinedProfile
	var err error
	if accountID == "" {
		profile, err = s.mux.CombinedProfile(ctx)
	} else {
		profile, err = s.mux.AccountProfile(ctx, accountID)
	}
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, profile)
}

func (s *Server) resetCreditsPreview(response http.ResponseWriter, request *http.Request) {
	if !s.authorizedMutation(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var preview mux.ResetCreditsPreview
	if err := decodeJSON(request, &preview); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.mux.SetResetCreditsPreview(preview); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) rateLimitPreview(response http.ResponseWriter, request *http.Request) {
	if !s.authorizedMutation(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var preview mux.RateLimitPreview
	if err := decodeJSON(request, &preview); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	if err := s.mux.SetRateLimitPreview(ctx, preview); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) threadAccount(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	threadID := strings.TrimSpace(request.URL.Query().Get("threadId"))
	if threadID == "" {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": "threadId is required"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	account, err := s.mux.ThreadAccount(ctx, threadID)
	if err != nil {
		writeJSON(response, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) Serve(listener net.Listener) error {
	if err := s.RegisterRuntime(listener.Addr().String()); err != nil {
		return err
	}
	return s.http.Serve(listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	payload := map[string]any{"ok": true}
	if s.authorized(request) {
		payload["technical"] = s.technical
	}
	writeJSON(response, http.StatusOK, payload)
}

func (s *Server) accounts(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) || (request.Method != http.MethodGet && !s.csrfAuthorized(request)) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()
		writeJSON(response, http.StatusOK, map[string]any{"accounts": s.mux.Accounts(ctx)})
	case http.MethodPost:
		var input struct {
			Label          string `json:"label"`
			IdempotencyKey string `json:"idempotencyKey"`
		}
		if err := decodeJSON(request, &input); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()
		var account mux.AccountSnapshot
		var err error
		if input.IdempotencyKey == "" {
			account, err = s.mux.AddAccount(ctx, input.Label)
		} else {
			account, err = s.mux.AddAccountIdempotent(ctx, input.Label, input.IdempotencyKey)
		}
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusCreated, map[string]any{"account": account})
	default:
		methodNotAllowed(response)
	}
}

func (s *Server) accountAction(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) || (request.Method != http.MethodGet && !s.csrfAuthorized(request)) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, "/v1/accounts/")
	parts := strings.Split(strings.Trim(remainder, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(response, request)
		return
	}
	accountID := parts[0]
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()

	if len(parts) == 1 && request.Method == http.MethodPatch {
		var input struct {
			Label   *string `json:"label"`
			Enabled *bool   `json:"enabled"`
		}
		if err := decodeJSON(request, &input); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		account, err := s.mux.UpdateAccount(ctx, accountID, input.Label, input.Enabled)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"account": account})
		return
	}
	if len(parts) == 1 && request.Method == http.MethodDelete {
		if err := s.mux.RemoveAccount(accountID); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"removed": true})
		return
	}
	if len(parts) == 3 && parts[1] == "import" && parts[2] == "codex-lb" && request.Method == http.MethodPost {
		var input struct {
			Export       json.RawMessage `json:"export"`
			SourcePaused bool            `json:"sourcePaused"`
		}
		if err := decodeJSON(request, &input); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		result, err := s.mux.ImportCodexLBExport(ctx, accountID, input.Export, input.SourcePaused)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if len(parts) == 2 && parts[1] == "rate-limit-resets" && request.Method == http.MethodGet {
		result, err := s.mux.RateLimitResetCredits(ctx, accountID)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeRawJSON(response, http.StatusOK, result)
		return
	}
	if len(parts) == 3 && parts[1] == "rate-limit-resets" && parts[2] == "consume" && request.Method == http.MethodPost {
		var input struct {
			CreditID        *string `json:"creditId"`
			RedeemRequestID string  `json:"redeemRequestId"`
		}
		if err := decodeJSON(request, &input); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		result, err := s.mux.ConsumeRateLimitResetCredit(ctx, accountID, input.CreditID, input.RedeemRequestID)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeRawJSON(response, http.StatusOK, result)
		return
	}
	if len(parts) != 2 || request.Method != http.MethodPost {
		http.NotFound(response, request)
		return
	}
	switch parts[1] {
	case "login":
		var input struct {
			Mode           string `json:"mode"`
			IdempotencyKey string `json:"idempotencyKey"`
		}
		if err := decodeJSON(request, &input); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		var attempt mux.LoginAttempt
		var err error
		if input.IdempotencyKey == "" {
			var result json.RawMessage
			result, err = s.mux.StartLogin(ctx, accountID, input.Mode)
			attempt = mux.LoginAttempt{AccountID: accountID, Mode: input.Mode, State: mux.LoginPending, Result: result}
		} else {
			attempt, err = s.mux.StartLoginAttempt(ctx, accountID, input.Mode, input.IdempotencyKey)
		}
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		var login any
		if json.Unmarshal(attempt.Result, &login) != nil {
			login = map[string]any{}
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": attempt, "login": login})
	case "logout":
		if err := s.mux.Logout(ctx, accountID); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"ok": true})
	default:
		http.NotFound(response, request)
	}
}

func (s *Server) loginAttemptAction(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) || (request.Method != http.MethodGet && !s.csrfAuthorized(request)) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, "/v1/login-attempts/")
	parts := strings.Split(strings.Trim(remainder, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(response, request)
		return
	}
	switch {
	case len(parts) == 1 && request.Method == http.MethodGet:
		attempt, err := s.mux.LoginStatus(parts[0])
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": attempt})
	case len(parts) == 2 && parts[1] == "cancel" && request.Method == http.MethodPost:
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()
		attempt, err := s.mux.CancelLogin(ctx, parts[0])
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": attempt})
	default:
		methodNotAllowed(response)
	}
}

func (s *Server) events(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	if !s.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "streaming unavailable"})
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	events, unsubscribe := s.mux.SubscribeEvents()
	defer unsubscribe()
	_, _ = fmt.Fprint(response, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			encoded, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(response, "data: %s\n\n", encoded)
			flusher.Flush()
		}
	}
}

func (s *Server) authorized(request *http.Request) bool {
	provided := request.Header.Get("X-Codex-Mux-Token")
	if len(provided) == len(s.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1 {
		return true
	}
	_, ok := s.requestSession(request)
	return ok
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' https:; script-src 'self'; style-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		response.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		if !s.allowedHost(request.Host) {
			writeJSON(response, http.StatusMisdirectedRequest, map[string]any{"error": "unexpected host"})
			return
		}
		origin := request.Header.Get("Origin")
		if origin != "" && origin != "app://-" && !sameOrigin(origin, request.Host) {
			writeJSON(response, http.StatusForbidden, map[string]any{"error": "unexpected origin"})
			return
		}
		if origin == "app://-" {
			response.Header().Set("Access-Control-Allow-Origin", "app://-")
			response.Header().Set("Vary", "Origin")
			response.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Codex-Mux-Token")
			response.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		}
		if request.Method == http.MethodOptions {
			if origin != "app://-" {
				writeJSON(response, http.StatusForbidden, map[string]any{"error": "cross-origin preflight denied"})
				return
			}
			response.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func decodeJSON(request *http.Request, target any) error {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func (s *Server) dashboard(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = response.Write(dashboard.Index)
}

func (s *Server) dashboardAsset(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	name := path.Base(request.URL.Path)
	asset, contentType, ok := dashboard.Asset(name)
	if !ok {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", contentType)
	_, _ = response.Write(asset)
}

func (s *Server) bootstrapSession(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	var input struct {
		Nonce string `json:"nonce"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.pruneLocked()
	expiresAt, ok := s.bootstraps[input.Nonce]
	if ok {
		delete(s.bootstraps, input.Nonce)
	}
	if !ok || !expiresAt.After(s.now()) {
		s.mu.Unlock()
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "bootstrap expired or already used"})
		return
	}
	sessionID, sessionErr := randomToken()
	csrf, csrfErr := randomToken()
	if sessionErr != nil || csrfErr != nil {
		s.mu.Unlock()
		writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "could not create browser session"})
		return
	}
	s.sessions[sessionID] = browserSession{csrf: csrf, expiresAt: s.now().Add(s.sessionTTL)}
	s.mu.Unlock()
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookieName, Value: sessionID, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(s.sessionTTL.Seconds()),
	})
	writeJSON(response, http.StatusOK, map[string]any{"csrfToken": csrf})
}

func (s *Server) browserSession(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	session, ok := s.requestSession(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "browser session unavailable"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"csrfToken": session.csrf})
}

func (s *Server) authorizedMutation(request *http.Request) bool {
	return s.authorized(request) && s.csrfAuthorized(request)
}

func (s *Server) csrfAuthorized(request *http.Request) bool {
	provided := request.Header.Get("X-Codex-Mux-Token")
	if len(provided) == len(s.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1 {
		return true
	}
	session, ok := s.requestSession(request)
	if !ok {
		return false
	}
	csrf := request.Header.Get("X-Codex-Mux-CSRF")
	return len(csrf) == len(session.csrf) && subtle.ConstantTimeCompare([]byte(csrf), []byte(session.csrf)) == 1
}

func (s *Server) requestSession(request *http.Request) (browserSession, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return browserSession{}, false
	}
	s.mu.RLock()
	session, ok := s.sessions[cookie.Value]
	s.mu.RUnlock()
	return session, ok && session.expiresAt.After(s.now())
}

func (s *Server) allowedHost(host string) bool {
	s.mu.RLock()
	_, ok := s.hosts[strings.ToLower(host)]
	s.mu.RUnlock()
	return ok
}

func (s *Server) pruneLocked() {
	now := s.now()
	for nonce, expiry := range s.bootstraps {
		if !expiry.After(now) {
			delete(s.bootstraps, nonce)
		}
	}
	for id, session := range s.sessions {
		if !session.expiresAt.After(now) {
			delete(s.sessions, id)
		}
	}
}

func loopbackHost(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return "", errors.New("runtime address must include a loopback host and port")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("runtime address must use a numeric loopback host")
	}
	return strings.ToLower(net.JoinHostPort(host, port)), nil
}

func sameOrigin(origin, host string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "http" && strings.EqualFold(parsed.Host, host)
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return fmt.Sprintf("%x", bytes), nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeRawJSON(response http.ResponseWriter, status int, value json.RawMessage) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = response.Write(value)
}

func methodNotAllowed(response http.ResponseWriter) {
	writeJSON(response, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}
