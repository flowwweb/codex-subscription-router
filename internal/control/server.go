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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/mux"
	"github.com/b-nnett/codex-subscription-router/ui/dashboard"
)

type publicLoginAttempt struct {
	ID        string         `json:"id"`
	AccountID string         `json:"accountId"`
	Mode      string         `json:"mode"`
	State     mux.LoginState `json:"state"`
	Error     string         `json:"error,omitempty"`
	StartedAt int64          `json:"startedAt"`
	UpdatedAt int64          `json:"updatedAt"`
	ExpiresAt int64          `json:"expiresAt"`
}

type loginPresentation struct {
	UserCode        string `json:"userCode,omitempty"`
	VerificationURL string `json:"verificationUrl,omitempty"`
}

type taskAccount struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Enabled    bool   `json:"enabled"`
	Controller bool   `json:"primary"`
	Connected  bool   `json:"connected"`
	Email      string `json:"email,omitempty"`
	PlanLabel  string `json:"plan,omitempty"`
	Error      string `json:"error,omitempty"`
}

type taskLoginAttempt struct {
	ID        string         `json:"id"`
	State     mux.LoginState `json:"state"`
	Error     string         `json:"error,omitempty"`
	ExpiresAt int64          `json:"expiresAt"`
}

func presentTaskLoginAttempt(attempt mux.LoginAttempt) taskLoginAttempt {
	presented := taskLoginAttempt{ID: attempt.ID, State: attempt.State, ExpiresAt: attempt.ExpiresAt}
	if attempt.Cancelling && attempt.State != mux.LoginPending {
		presented.State = mux.LoginPending
		return presented
	}
	if attempt.Error != "" {
		presented.Error = "OpenAI account connection failed"
	}
	return presented
}

func presentLoginAttempt(attempt mux.LoginAttempt) publicLoginAttempt {
	presented := publicLoginAttempt{
		ID: attempt.ID, AccountID: attempt.AccountID, Mode: attempt.Mode, State: attempt.State,
		StartedAt: attempt.StartedAt, UpdatedAt: attempt.UpdatedAt, ExpiresAt: attempt.ExpiresAt,
	}
	if attempt.Cancelling && attempt.State != mux.LoginPending {
		presented.State = mux.LoginPending
		return presented
	}
	if attempt.Error != "" {
		presented.Error = "OpenAI account connection failed"
	}
	return presented
}

func presentAccountSnapshot(account mux.AccountSnapshot) mux.AccountSnapshot {
	if account.Error != "" {
		account.Error = "Account is unavailable"
	}
	return account
}

func presentAccountSnapshots(accounts []mux.AccountSnapshot) []mux.AccountSnapshot {
	presented := make([]mux.AccountSnapshot, len(accounts))
	for index, account := range accounts {
		presented[index] = presentAccountSnapshot(account)
	}
	return presented
}

func presentEvent(event mux.Event) mux.Event {
	if event.Type != "account-login" {
		return event
	}
	switch attempt := event.Data.(type) {
	case mux.LoginAttempt:
		event.Data = presentLoginAttempt(attempt)
	case *mux.LoginAttempt:
		if attempt != nil {
			event.Data = presentLoginAttempt(*attempt)
		}
	}
	return event
}

func presentLogin(result json.RawMessage) loginPresentation {
	var provider struct {
		UserCode                string `json:"userCode"`
		UserCodeSnake           string `json:"user_code"`
		VerificationURL         string `json:"verificationUrl"`
		VerificationURLSnake    string `json:"verification_url"`
		VerificationURI         string `json:"verificationUri"`
		VerificationURISnake    string `json:"verification_uri"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		VerificationCompleteRaw string `json:"verification_uri_complete"`
		AuthURL                 string `json:"authUrl"`
		AuthURLSnake            string `json:"auth_url"`
	}
	if json.Unmarshal(result, &provider) != nil {
		return loginPresentation{}
	}
	code := provider.UserCode
	if code == "" {
		code = provider.UserCodeSnake
	}
	verificationURL := firstNonEmpty(
		provider.VerificationURIComplete, provider.VerificationCompleteRaw,
		provider.VerificationURL, provider.VerificationURLSnake,
		provider.VerificationURI, provider.VerificationURISnake,
		provider.AuthURL, provider.AuthURLSnake,
	)
	if !TrustedOpenAIVerificationURL(verificationURL) {
		verificationURL = ""
	}
	if len(code) > 64 || strings.IndexFunc(code, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		code = ""
	}
	return loginPresentation{UserCode: code, VerificationURL: verificationURL}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func TrustedOpenAIVerificationURL(value string) bool {
	if len(value) == 0 || len(value) > 2048 || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return false
	}
	destination, err := url.ParseRequestURI(value)
	if err != nil || destination.Scheme != "https" || destination.User != nil {
		return false
	}
	if destination.Port() != "" && destination.Port() != "443" {
		return false
	}
	hostname := strings.ToLower(destination.Hostname())
	return hostname == "chatgpt.com" || strings.HasSuffix(hostname, ".chatgpt.com") ||
		hostname == "auth.openai.com" || strings.HasSuffix(hostname, ".auth.openai.com")
}

func TrustedOpenAIBrowserLoginURL(value string) bool {
	if !TrustedOpenAIVerificationURL(value) {
		return false
	}
	destination, err := url.ParseRequestURI(value)
	if err != nil {
		return false
	}
	if strings.ToLower(destination.Hostname()) != "auth.openai.com" || destination.EscapedPath() != "/oauth/authorize" || destination.Fragment != "" {
		return false
	}
	query, err := url.ParseQuery(destination.RawQuery)
	if err != nil {
		return false
	}
	redirects := query["redirect_uri"]
	if len(redirects) != 1 {
		return false
	}
	for key, expected := range map[string]string{"response_type": "code", "code_challenge_method": "S256"} {
		if values := query[key]; len(values) != 1 || values[0] != expected {
			return false
		}
	}
	for _, key := range []string{"state", "code_challenge"} {
		if values := query[key]; len(values) != 1 || values[0] == "" {
			return false
		}
	}
	callback, err := url.ParseRequestURI(redirects[0])
	if err != nil || callback.Scheme != "http" || callback.User != nil || callback.Port() == "" || callback.EscapedPath() != "/auth/callback" || callback.RawQuery != "" || callback.Fragment != "" {
		return false
	}
	hostname := strings.ToLower(callback.Hostname())
	if hostname != "localhost" && hostname != "127.0.0.1" && hostname != "::1" {
		return false
	}
	port, err := strconv.Atoi(callback.Port())
	return err == nil && port > 0 && port <= 65535
}

func trustedLoginPresentation(mode string, login loginPresentation) bool {
	if mode == "chatgpt" {
		return login.UserCode == "" && TrustedOpenAIBrowserLoginURL(login.VerificationURL)
	}
	return mode == "chatgptDeviceCode" && login.UserCode != "" && TrustedOpenAIVerificationURL(login.VerificationURL)
}

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
	instance     string
	connectMu    sync.Mutex
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
	router.HandleFunc("/v1/task-actions/status", server.taskStatus)
	router.HandleFunc("/v1/task-actions/connect-account", server.taskConnectAccount)
	router.HandleFunc("/v1/task-actions/login-attempts/", server.taskLoginAttempt)
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

func (s *Server) SetRuntimeIdentity(instance string) {
	s.mu.Lock()
	s.instance = instance
	s.mu.Unlock()
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
		writeJSON(response, http.StatusBadGateway, map[string]any{"error": "Account profile is unavailable"})
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
		writeJSON(response, http.StatusNotFound, map[string]any{"error": "Thread account is unavailable"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"account": presentAccountSnapshot(account)})
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
		writeJSON(response, http.StatusOK, map[string]any{"accounts": presentAccountSnapshots(s.mux.Accounts(ctx))})
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
			writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "Account could not be added"})
			return
		}
		writeJSON(response, http.StatusCreated, map[string]any{"account": presentAccountSnapshot(account)})
	default:
		methodNotAllowed(response)
	}
}

func taskAccountFromSnapshot(account mux.AccountSnapshot) taskAccount {
	publicError := ""
	if account.Error != "" {
		publicError = "Account unavailable"
	} else if !account.Connected {
		publicError = "Needs sign-in"
	}
	return taskAccount{
		ID: account.ID, Label: account.Label, Enabled: account.Enabled, Controller: account.Controller,
		Connected: account.Connected, Email: account.Email, PlanLabel: account.PlanLabel, Error: publicError,
	}
}

func (s *Server) taskStatus(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	if !s.taskAuthorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	snapshots := s.mux.Accounts(ctx)
	accounts := make([]taskAccount, 0, len(snapshots))
	connected := 0
	for _, account := range snapshots {
		accounts = append(accounts, taskAccountFromSnapshot(account))
		if account.Enabled && account.Connected {
			connected++
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"routing":  map[string]any{"active": connected > 0, "connectedAccounts": connected},
		"accounts": accounts,
	})
}

func (s *Server) taskConnectAccount(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response)
		return
	}
	if !s.taskAuthorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var input struct {
		AccountID      string `json:"accountId"`
		NewAccount     bool   `json:"newAccount"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": "Invalid connection request"})
		return
	}
	if input.IdempotencyKey == "" || (input.AccountID != "" && input.NewAccount) {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": "choose an account or request a new account"})
		return
	}

	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	snapshots := s.mux.Accounts(ctx)
	var selected mux.AccountSnapshot
	if input.AccountID != "" {
		for _, account := range snapshots {
			if account.ID == input.AccountID {
				selected = account
				break
			}
		}
		if selected.ID == "" {
			writeJSON(response, http.StatusNotFound, map[string]any{"error": "account not found"})
			return
		}
	} else if input.NewAccount {
		var err error
		selected, err = s.mux.AddAccountIdempotent(ctx, "", "account-"+input.IdempotencyKey)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]any{"error": "Could not prepare a router account"})
			return
		}
	} else {
		candidates := make([]mux.AccountSnapshot, 0)
		for _, account := range snapshots {
			if !account.Connected || account.Error != "" {
				candidates = append(candidates, account)
			}
		}
		switch len(candidates) {
		case 0:
			var err error
			selected, err = s.mux.AddAccountIdempotent(ctx, "", "account-"+input.IdempotencyKey)
			if err != nil {
				writeJSON(response, http.StatusBadGateway, map[string]any{"error": "Could not prepare a router account"})
				return
			}
		case 1:
			selected = candidates[0]
		default:
			labels := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				labels = append(labels, candidate.Label)
			}
			writeJSON(response, http.StatusConflict, map[string]any{"error": "choose an account", "accounts": labels})
			return
		}
	}
	attempt, err := s.mux.StartLoginAttempt(ctx, selected.ID, "chatgpt", "login-"+input.IdempotencyKey)
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]any{"error": "OpenAI did not start account verification"})
		return
	}
	login := presentLogin(attempt.Result)
	if !trustedLoginPresentation("chatgpt", login) {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = s.mux.CancelLogin(cancelCtx, attempt.ID)
		cancel()
		writeJSON(response, http.StatusBadGateway, map[string]any{"error": "OpenAI did not return a trusted sign-in URL"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"account": taskAccountFromSnapshot(selected), "attempt": presentTaskLoginAttempt(attempt), "login": login,
	})
}

func (s *Server) taskLoginAttempt(response http.ResponseWriter, request *http.Request) {
	if !s.taskAuthorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, "/v1/task-actions/login-attempts/")
	parts := strings.Split(strings.Trim(remainder, "/"), "/")
	if len(parts) == 1 && parts[0] != "" && request.Method == http.MethodGet {
		attempt, err := s.mux.LoginStatus(parts[0])
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]any{"error": "Connection attempt not found"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": presentTaskLoginAttempt(attempt)})
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "cancel" && request.Method == http.MethodPost {
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()
		attempt, err := s.mux.CancelLogin(ctx, parts[0])
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]any{"error": "Connection cancellation could not be confirmed"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": presentTaskLoginAttempt(attempt)})
		return
	}
	methodNotAllowed(response)
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
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "Account could not be updated"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"account": presentAccountSnapshot(account)})
		return
	}
	if len(parts) == 1 && request.Method == http.MethodDelete {
		if err := s.mux.RemoveAccount(accountID); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "Account could not be removed"})
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
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "Account import failed"})
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if len(parts) == 2 && parts[1] == "rate-limit-resets" && request.Method == http.MethodGet {
		result, err := s.mux.RateLimitResetCredits(ctx, accountID)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]any{"error": "Usage resets are unavailable"})
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
			writeJSON(response, http.StatusBadGateway, map[string]any{"error": "Usage reset could not be applied"})
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
		if input.IdempotencyKey == "" {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "idempotencyKey is required"})
			return
		}
		attempt, err := s.mux.StartLoginAttempt(ctx, accountID, input.Mode, input.IdempotencyKey)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "Account sign-in could not start"})
			return
		}
		login := presentLogin(attempt.Result)
		if !trustedLoginPresentation(input.Mode, login) {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = s.mux.CancelLogin(cancelCtx, attempt.ID)
			cancel()
			writeJSON(response, http.StatusBadGateway, map[string]any{"error": "OpenAI did not return a trusted sign-in URL"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": presentLoginAttempt(attempt), "login": login})
	case "logout":
		if err := s.mux.Logout(ctx, accountID); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "Account could not be disconnected"})
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
			writeJSON(response, http.StatusNotFound, map[string]any{"error": "Sign-in attempt was not found"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": presentLoginAttempt(attempt)})
	case len(parts) == 2 && parts[1] == "cancel" && request.Method == http.MethodPost:
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()
		attempt, err := s.mux.CancelLogin(ctx, parts[0])
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": "Sign-in cancellation could not be confirmed"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"attempt": presentLoginAttempt(attempt)})
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
			encoded, _ := json.Marshal(presentEvent(event))
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

func (s *Server) taskAuthorized(request *http.Request) bool {
	provided := request.Header.Get("X-Codex-Mux-Token")
	if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
		return false
	}
	s.mu.RLock()
	instance := s.instance
	s.mu.RUnlock()
	providedInstance := request.Header.Get("X-Codex-Mux-Instance")
	return instance != "" && len(providedInstance) == len(instance) &&
		subtle.ConstantTimeCompare([]byte(providedInstance), []byte(instance)) == 1
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
			response.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Codex-Mux-Token, X-Codex-Mux-Instance")
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
