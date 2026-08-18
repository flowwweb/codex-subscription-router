package mux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/backend"
	"github.com/b-nnett/codex-subscription-router/internal/state"
)

var errNoSubscriptionCapacity = errors.New("no enabled ChatGPT subscription has capacity")
var errLoginChildChanged = errors.New("login account backend generation changed")

const loginAttemptLifetime = 15 * time.Minute

const maxIdempotencyKeyLength = 128

type LoginState string

const (
	LoginPending   LoginState = "pending"
	LoginSucceeded LoginState = "succeeded"
	LoginFailed    LoginState = "failed"
	LoginExpired   LoginState = "expired"
	LoginCancelled LoginState = "cancelled"
)

type LoginAttempt struct {
	ID                  string          `json:"id"`
	AccountID           string          `json:"accountId"`
	Mode                string          `json:"mode"`
	State               LoginState      `json:"state"`
	Result              json.RawMessage `json:"result,omitempty"`
	Error               string          `json:"error,omitempty"`
	StartedAt           int64           `json:"startedAt"`
	UpdatedAt           int64           `json:"updatedAt"`
	ExpiresAt           int64           `json:"expiresAt"`
	ProviderLoginID     string          `json:"-"`
	ChildGeneration     uint64          `json:"-"`
	Generation          uint64          `json:"-"`
	WasConnected        bool            `json:"-"`
	Cancelling          bool            `json:"-"`
	CleanupHandled      bool            `json:"-"`
	RestartAfterCleanup bool            `json:"-"`
	TerminalPublished   bool            `json:"-"`
}

type AccountProvisionError struct {
	AccountID string
	Err       error
}

func (e *AccountProvisionError) Error() string {
	return fmt.Sprintf("prepare account %q: %v", e.AccountID, e.Err)
}

func (e *AccountProvisionError) Unwrap() error { return e.Err }

const (
	routingFallbackWindow      = 7 * 24 * time.Hour
	routingMinimumWindow       = time.Minute
	routingResetBonusPerCredit = 0.15
	routingResetBonusCreditCap = 3
)

type RateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

type RateLimits struct {
	Primary              *RateLimitWindow `json:"primary"`
	Secondary            *RateLimitWindow `json:"secondary"`
	RateLimitReachedType any              `json:"rateLimitReachedType"`
}

type AccountSnapshot struct {
	ID              string          `json:"id"`
	Label           string          `json:"label"`
	Enabled         bool            `json:"enabled"`
	Controller      bool            `json:"controller"`
	Connected       bool            `json:"connected"`
	Email           string          `json:"email,omitempty"`
	PlanType        string          `json:"planType,omitempty"`
	PlanLabel       string          `json:"planLabel,omitempty"`
	AuthType        string          `json:"authType,omitempty"`
	ProfileImageURL string          `json:"profileImageUrl,omitempty"`
	RateLimits      *RateLimits     `json:"rateLimits,omitempty"`
	ThreadCount     int             `json:"threadCount"`
	Error           string          `json:"error,omitempty"`
	CreatedAt       int64           `json:"createdAt"`
	RawAccount      json.RawMessage `json:"-"`
}

type RouteReason struct {
	WeeklyUsedPercent    *float64 `json:"weeklyUsedPercent"`
	WeeklyResetsAt       *int64   `json:"weeklyResetsAt,omitempty"`
	ShortUsedPercent     *float64 `json:"shortUsedPercent"`
	BankedResetCount     *int     `json:"bankedResetCount,omitempty"`
	ResetCreditExpiresAt *int64   `json:"resetCreditExpiresAt,omitempty"`
	UrgencyScore         *float64 `json:"urgencyScore,omitempty"`
	ThreadCount          int      `json:"threadCount"`
}

func (m *Multiplexer) Accounts(ctx context.Context) []AccountSnapshot {
	return m.accountSnapshots(ctx, true)
}

func (m *Multiplexer) accountSnapshots(ctx context.Context, includeProfile bool) []AccountSnapshot {
	accounts := m.store.Accounts()
	results := make(chan AccountSnapshot, len(accounts))
	for _, account := range accounts {
		go func(account state.Account) {
			snapshot, err := m.accountSnapshotWithProfile(ctx, account.ID, includeProfile)
			if err != nil {
				snapshot = AccountSnapshot{
					ID: account.ID, Label: account.Label, Enabled: account.Enabled,
					Controller: account.Controller, CreatedAt: account.CreatedAt,
					Connected: account.LastKnownConnected, Email: account.LastKnownEmail,
					PlanType: account.LastKnownPlanType, PlanLabel: planLabel(account.LastKnownPlanType),
					Error: err.Error(),
				}
			}
			results <- snapshot
		}(account)
	}
	snapshots := make([]AccountSnapshot, 0, len(accounts))
	for range accounts {
		select {
		case snapshot := <-results:
			snapshots = append(snapshots, snapshot)
		case <-ctx.Done():
			return snapshots
		}
	}
	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].Controller != snapshots[j].Controller {
			return snapshots[i].Controller
		}
		return snapshots[i].CreatedAt < snapshots[j].CreatedAt
	})
	return snapshots
}

func (m *Multiplexer) AddAccount(ctx context.Context, label string) (AccountSnapshot, error) {
	key, err := opaqueID()
	if err != nil {
		return AccountSnapshot{}, err
	}
	return m.AddAccountIdempotent(ctx, label, key)
}

// AddAccountIdempotent durably reuses the account created for key.
func (m *Multiplexer) AddAccountIdempotent(ctx context.Context, label, key string) (AccountSnapshot, error) {
	if err := validateIdempotencyKey(key); err != nil {
		return AccountSnapshot{}, err
	}
	account, created, err := m.store.AddAccountIdempotent(label, key)
	if err != nil {
		return AccountSnapshot{}, err
	}
	if !created {
		if !account.Enabled {
			enabled := true
			return m.UpdateAccount(ctx, account.ID, nil, &enabled)
		}
		if _, err := m.startChild(ctx, account); err != nil {
			return AccountSnapshot{}, &AccountProvisionError{AccountID: account.ID, Err: err}
		}
		return m.accountSnapshot(ctx, account.ID)
	}
	if _, err := m.startChild(ctx, account); err != nil {
		disabled := false
		_, _ = m.store.UpdateAccount(account.ID, nil, &disabled)
		return AccountSnapshot{}, &AccountProvisionError{AccountID: account.ID, Err: err}
	}
	return m.accountSnapshot(ctx, account.ID)
}

func (m *Multiplexer) UpdateAccount(ctx context.Context, id string, label *string, enabled *bool) (AccountSnapshot, error) {
	previous, ok := m.store.Account(id)
	if !ok {
		return AccountSnapshot{}, fmt.Errorf("account %q not found", id)
	}
	updated, err := m.store.UpdateAccount(id, label, enabled)
	if err != nil {
		return AccountSnapshot{}, err
	}
	if enabled != nil && !*enabled {
		if err := m.stopChild(id); err != nil {
			return AccountSnapshot{}, fmt.Errorf("stop disabled account %q: %w", id, err)
		}
	}
	if enabled != nil && *enabled {
		if _, err := m.startChild(ctx, updated); err != nil {
			if !previous.Enabled {
				disabled := false
				_, _ = m.store.UpdateAccount(id, nil, &disabled)
			}
			return AccountSnapshot{}, fmt.Errorf("start enabled account %q: %w", id, err)
		}
	}
	return m.accountSnapshot(ctx, id)
}

func (m *Multiplexer) ThreadAccount(ctx context.Context, threadID string) (AccountSnapshot, error) {
	accountID, ok := m.store.ThreadOwner(threadID)
	if !ok {
		return AccountSnapshot{}, fmt.Errorf("thread %q has no subscription assignment", threadID)
	}
	return m.accountSnapshotWithProfile(ctx, accountID, true)
}

func (m *Multiplexer) StartLogin(ctx context.Context, id, mode string) (json.RawMessage, error) {
	key, err := opaqueID()
	if err != nil {
		return nil, err
	}
	attempt, err := m.StartLoginAttempt(ctx, id, mode, key)
	return attempt.Result, err
}

func (m *Multiplexer) StartLoginAttempt(ctx context.Context, id, mode, key string) (LoginAttempt, error) {
	if mode != "chatgpt" && mode != "chatgptDeviceCode" {
		return LoginAttempt{}, errors.New("login mode must be chatgpt or chatgptDeviceCode")
	}
	if err := validateIdempotencyKey(key); err != nil {
		return LoginAttempt{}, err
	}
	attemptID, err := opaqueID()
	if err != nil {
		return LoginAttempt{}, err
	}
	now := m.now().Unix()
	attempt := LoginAttempt{
		ID: attemptID, AccountID: id, Mode: mode, State: LoginPending,
		StartedAt: now, UpdatedAt: now, ExpiresAt: m.now().Add(loginAttemptLifetime).Unix(),
		Generation: m.loginSequence.Add(1),
	}
	m.loginMu.Lock()
	if attemptID := m.loginKeys[key]; attemptID != "" {
		ready := m.loginReady[attemptID]
		m.loginMu.Unlock()
		return m.waitForLoginStart(ctx, attemptID, ready)
	}
	if attemptID := m.pendingLoginForAccountLocked(id); attemptID != "" {
		m.loginKeys[key] = attemptID
		ready := m.loginReady[attemptID]
		m.loginMu.Unlock()
		return m.waitForLoginStart(ctx, attemptID, ready)
	}
	m.loginKeys[key] = attempt.ID
	m.loginAttempts[attempt.ID] = attempt
	m.loginReady[attempt.ID] = make(chan struct{})
	m.loginMu.Unlock()
	account, ok := m.store.Account(id)
	if !ok {
		attempt = m.failLoginStart(attempt.ID, fmt.Sprintf("account %q not found", id), false)
		return attempt, fmt.Errorf("account %q not found", id)
	}
	child, ok := m.child(id)
	if !ok {
		var err error
		if account.Enabled {
			child, err = m.startChild(ctx, account)
		} else {
			// Authentication is a repair action, not a routing action. A disabled
			// account may temporarily start its backend to sign in without becoming
			// eligible for routing.
			child, err = m.startChildInternal(ctx, account, false)
		}
		if err != nil {
			attempt = m.failLoginStart(attempt.ID, err.Error(), false)
			return attempt, fmt.Errorf("account %q is unavailable: %w", id, err)
		}
	}
	m.loginMu.Lock()
	attempt = m.loginAttempts[attempt.ID]
	attempt.ChildGeneration = child.Generation()
	m.loginAttempts[attempt.ID] = attempt
	m.loginMu.Unlock()
	liveAccount, liveErr := m.accountSnapshot(ctx, id)
	if liveErr != nil {
		attempt = m.failLoginStart(attempt.ID, liveErr.Error(), true)
		if m.stopDisabledLoginChildGeneration(id, child.Generation()) {
			m.finishLoginCleanup(attempt.ID)
		}
		m.loginMu.Lock()
		attempt = m.loginAttemptLocked(attempt.ID)
		m.loginMu.Unlock()
		return attempt, fmt.Errorf("read account %q before login: %w", id, liveErr)
	}
	m.loginMu.Lock()
	attempt = m.loginAttempts[attempt.ID]
	attempt.WasConnected = liveAccount.Connected
	m.loginAttempts[attempt.ID] = attempt
	m.loginMu.Unlock()
	params, _ := json.Marshal(map[string]any{"type": mode})
	response, err := child.Request(ctx, "account/login/start", params)
	if err != nil {
		attempt = m.failLoginStart(attempt.ID, err.Error(), true)
		if m.stopDisabledLoginChildGeneration(id, child.Generation()) {
			m.finishLoginCleanup(attempt.ID)
		}
		m.loginMu.Lock()
		attempt = m.loginAttemptLocked(attempt.ID)
		m.loginMu.Unlock()
		return attempt, err
	}
	m.loginMu.Lock()
	attempt = m.loginAttempts[attempt.ID]
	if attempt.State != LoginPending {
		if ready := m.loginReady[attempt.ID]; ready != nil {
			close(ready)
			delete(m.loginReady, attempt.ID)
		}
		m.loginMu.Unlock()
		return cloneLoginAttempt(attempt), errors.New("account backend stopped during login startup")
	}
	providerLoginID, loginIDErr := loginIDFromResult(response.Result)
	if loginIDErr != nil {
		attempt.State = LoginFailed
		attempt.Error = "OpenAI did not return a valid login identifier"
		attempt.Cancelling = true
		attempt.RestartAfterCleanup = account.Enabled
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[attempt.ID] = attempt
		close(m.loginReady[attempt.ID])
		delete(m.loginReady, attempt.ID)
		m.loginMu.Unlock()
		m.abortUntrackedLogin(account.ID, attempt.ChildGeneration)
		return attempt, loginIDErr
	}
	providerKey := providerLoginKey(id, attempt.ChildGeneration, providerLoginID)
	if existingID := m.loginByProviderID[providerKey]; existingID != "" && existingID != attempt.ID {
		attempt.State = LoginFailed
		attempt.Error = "OpenAI reused a login identifier"
		attempt.Cancelling = true
		attempt.RestartAfterCleanup = account.Enabled
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[attempt.ID] = attempt
		close(m.loginReady[attempt.ID])
		delete(m.loginReady, attempt.ID)
		m.loginMu.Unlock()
		m.abortUntrackedLogin(account.ID, attempt.ChildGeneration)
		return attempt, errors.New("OpenAI reused loginId")
	}
	attempt.Result = publicLoginResult(response.Result)
	attempt.ProviderLoginID = providerLoginID
	attempt.UpdatedAt = m.now().Unix()
	m.loginAttempts[attempt.ID] = attempt
	m.loginByProviderID[providerKey] = attempt.ID
	close(m.loginReady[attempt.ID])
	delete(m.loginReady, attempt.ID)
	m.loginMu.Unlock()
	m.publish(Event{Type: "account-login", AccountID: id, Data: attempt})
	m.scheduleLoginExpiry(attempt.ID, attempt.ExpiresAt)
	return attempt, nil
}

func (m *Multiplexer) abortUntrackedLogin(accountID string, generation uint64) {
	_ = m.stopChildGeneration(accountID, generation)
	m.loginMu.Lock()
	pendingCleanup := false
	for _, attempt := range m.loginAttempts {
		if attempt.AccountID == accountID && attempt.ChildGeneration == generation && attempt.Cancelling {
			pendingCleanup = true
			break
		}
	}
	m.loginMu.Unlock()
	if pendingCleanup && !m.hasChildGeneration(accountID, generation) {
		m.releaseLoginCleanup(accountID, generation)
	}
}

func (m *Multiplexer) failLoginStart(attemptID, message string, cleanup bool) LoginAttempt {
	m.loginMu.Lock()
	attempt := m.loginAttempts[attemptID]
	if attempt.State == LoginPending {
		attempt.State = LoginFailed
		attempt.Error = message
		attempt.Cancelling = cleanup
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[attemptID] = attempt
	}
	if ready := m.loginReady[attemptID]; ready != nil {
		close(ready)
		delete(m.loginReady, attemptID)
	}
	m.loginMu.Unlock()
	if !cleanup {
		attempt = m.publishTerminalLogin(attemptID)
	}
	return attempt
}

func (m *Multiplexer) pendingLoginForAccountLocked(accountID string) string {
	now := m.now().Unix()
	for id, attempt := range m.loginAttempts {
		if attempt.AccountID == accountID && (attempt.Cancelling || (attempt.State == LoginPending && now < attempt.ExpiresAt)) {
			return id
		}
	}
	return ""
}

func (m *Multiplexer) scheduleLoginExpiry(attemptID string, expiresAt int64) {
	delay := time.Until(time.Unix(expiresAt, 0))
	if delay < 0 {
		delay = 0
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = m.LoginStatus(attemptID)
		case <-m.runCtx.Done():
		}
	}()
}

func (m *Multiplexer) publishTerminalLogin(attemptID string) LoginAttempt {
	m.loginMu.Lock()
	attempt, ok := m.loginAttempts[attemptID]
	if !ok || attempt.State == LoginPending || attempt.TerminalPublished {
		m.loginMu.Unlock()
		return cloneLoginAttempt(attempt)
	}
	attempt.TerminalPublished = true
	m.loginAttempts[attemptID] = attempt
	presented := cloneLoginAttempt(attempt)
	m.loginMu.Unlock()
	m.publish(Event{Type: "account-login", AccountID: attempt.AccountID, Data: presented})
	return presented
}

func (m *Multiplexer) LoginStatus(attemptID string) (LoginAttempt, error) {
	m.loginMu.Lock()
	attempt, ok := m.loginAttempts[attemptID]
	if !ok {
		m.loginMu.Unlock()
		return LoginAttempt{}, fmt.Errorf("login attempt %q not found", attemptID)
	}
	expired := false
	if attempt.State == LoginPending && m.now().Unix() >= attempt.ExpiresAt {
		attempt.State = LoginExpired
		attempt.Error = "login expired before the subscription connected"
		attempt.UpdatedAt = m.now().Unix()
		attempt.Cancelling = true
		m.loginAttempts[attemptID] = attempt
		expired = true
	}
	result := cloneLoginAttempt(attempt)
	m.loginMu.Unlock()
	if expired {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cancelErr := m.cancelProviderLogin(cancelCtx, result)
		cancel()
		rollbackErr := m.rollbackLoginConnection(result)
		m.loginMu.Lock()
		attempt = m.loginAttempts[attemptID]
		attempt.CleanupHandled = true
		m.loginAttempts[attemptID] = attempt
		m.loginMu.Unlock()
		if rollbackErr != nil {
			m.loginMu.Lock()
			attempt = m.loginAttempts[attemptID]
			attempt.State = LoginFailed
			attempt.Error = "Expired sign-in could not be rolled back"
			attempt.UpdatedAt = m.now().Unix()
			m.loginAttempts[attemptID] = attempt
			m.loginMu.Unlock()
			stopErr := m.stopChildGeneration(result.AccountID, result.ChildGeneration)
			if stopErr == nil {
				m.finishLoginCleanup(attemptID)
			}
			m.loginMu.Lock()
			result = m.loginAttemptLocked(attemptID)
			m.loginMu.Unlock()
			if stopErr != nil {
				return result, fmt.Errorf("rollback expired login: %w; stop backend: %v", rollbackErr, stopErr)
			}
			return result, fmt.Errorf("rollback expired login: %w", rollbackErr)
		}
		if !m.stopDisabledLoginChild(attempt.AccountID) {
			return result, errors.New("login expired but account backend teardown is still pending")
		}
		m.finishLoginCleanup(attemptID)
		m.loginMu.Lock()
		result = m.loginAttemptLocked(attemptID)
		m.loginMu.Unlock()
		if cancelErr != nil {
			return result, fmt.Errorf("cancel expired login: %w", cancelErr)
		}
	}
	return result, nil
}

func (m *Multiplexer) CancelLogin(ctx context.Context, attemptID string) (LoginAttempt, error) {
	m.loginMu.Lock()
	attempt, ok := m.loginAttempts[attemptID]
	if !ok {
		m.loginMu.Unlock()
		return LoginAttempt{}, fmt.Errorf("login attempt %q not found", attemptID)
	}
	if attempt.State != LoginPending {
		m.loginMu.Unlock()
		return cloneLoginAttempt(attempt), nil
	}
	attempt.Cancelling = true
	m.loginAttempts[attemptID] = attempt
	m.loginMu.Unlock()
	cancelErr := m.cancelProviderLogin(ctx, attempt)
	m.loginMu.Lock()
	attempt = m.loginAttempts[attemptID]
	if attempt.State == LoginPending {
		if cancelErr != nil {
			attempt.State = LoginFailed
			attempt.Error = "connection cancellation could not be confirmed"
		} else {
			attempt.State = LoginCancelled
			attempt.Error = "login cancelled"
		}
		attempt.UpdatedAt = m.now().Unix()
	}
	m.loginAttempts[attemptID] = attempt
	m.loginMu.Unlock()
	rollbackErr := m.rollbackLoginConnection(attempt)
	m.loginMu.Lock()
	attempt = m.loginAttempts[attemptID]
	attempt.CleanupHandled = true
	m.loginAttempts[attemptID] = attempt
	m.loginMu.Unlock()
	if rollbackErr != nil {
		m.loginMu.Lock()
		attempt = m.loginAttempts[attemptID]
		attempt.State = LoginFailed
		attempt.Error = "Connection cancellation could not be rolled back"
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[attemptID] = attempt
		m.loginMu.Unlock()
		stopErr := m.stopChildGeneration(attempt.AccountID, attempt.ChildGeneration)
		if stopErr == nil {
			m.finishLoginCleanup(attemptID)
		}
		m.loginMu.Lock()
		attempt = m.loginAttemptLocked(attemptID)
		m.loginMu.Unlock()
		if stopErr != nil {
			return attempt, fmt.Errorf("rollback cancelled login: %w; stop backend: %v", rollbackErr, stopErr)
		}
		return attempt, fmt.Errorf("rollback cancelled login: %w", rollbackErr)
	}
	if !m.stopDisabledLoginChild(attempt.AccountID) {
		if cancelErr != nil {
			return attempt, fmt.Errorf("cancel login: %w; account backend teardown is still pending", cancelErr)
		}
		return attempt, errors.New("login cancelled but account backend teardown is still pending")
	}
	m.finishLoginCleanup(attemptID)
	m.loginMu.Lock()
	attempt = m.loginAttemptLocked(attemptID)
	m.loginMu.Unlock()
	if cancelErr != nil {
		return attempt, fmt.Errorf("cancel login: %w", cancelErr)
	}
	return attempt, nil
}

func (m *Multiplexer) rollbackLoginConnection(attempt LoginAttempt) error {
	if attempt.WasConnected {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child, ok := m.loginChild(attempt.AccountID, attempt.ChildGeneration)
	if !ok {
		return nil
	}
	if _, err := child.Request(ctx, "account/logout", nil); err != nil {
		return err
	}
	if _, ok := m.loginChild(attempt.AccountID, attempt.ChildGeneration); !ok {
		return nil
	}
	if err := m.store.SetAccountConnected(attempt.AccountID, false); err != nil {
		return err
	}
	snapshot, err := m.accountSnapshotForGeneration(ctx, attempt.AccountID, attempt.ChildGeneration)
	if errors.Is(err, errLoginChildChanged) {
		return nil
	}
	if err != nil {
		return err
	}
	if snapshot.Connected {
		return errors.New("account remained connected after rollback")
	}
	return nil
}

func (m *Multiplexer) cancelProviderLogin(ctx context.Context, attempt LoginAttempt) error {
	if attempt.ProviderLoginID == "" {
		return errors.New("provider login identifier is unavailable")
	}
	child, ok := m.loginChild(attempt.AccountID, attempt.ChildGeneration)
	if !ok {
		return fmt.Errorf("account %q is unavailable", attempt.AccountID)
	}
	params, _ := json.Marshal(map[string]string{"loginId": attempt.ProviderLoginID})
	_, err := child.Request(ctx, "account/login/cancel", params)
	return err
}

func loginIDFromResult(result json.RawMessage) (string, error) {
	var provider struct {
		LoginID string `json:"loginId"`
	}
	if json.Unmarshal(result, &provider) != nil || provider.LoginID == "" || len(provider.LoginID) > 128 {
		return "", errors.New("OpenAI login response omitted loginId")
	}
	for _, r := range provider.LoginID {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("OpenAI login response returned an invalid loginId")
		}
	}
	return provider.LoginID, nil
}

func publicLoginResult(result json.RawMessage) json.RawMessage {
	var presented map[string]any
	if json.Unmarshal(result, &presented) != nil {
		return nil
	}
	delete(presented, "loginId")
	encoded, err := json.Marshal(presented)
	if err != nil {
		return nil
	}
	return encoded
}

func providerLoginKey(accountID string, childGeneration uint64, providerLoginID string) string {
	return fmt.Sprintf("%s\x00%d\x00%s", accountID, childGeneration, providerLoginID)
}

func (m *Multiplexer) RemoveAccount(id string) error {
	account, ok := m.store.Account(id)
	if !ok {
		return fmt.Errorf("account %q not found", id)
	}
	if account.Controller {
		return errors.New("controller account cannot be removed")
	}
	if err := m.stopChild(id); err != nil {
		return err
	}
	if err := m.store.RemoveAccount(id); err != nil {
		if account.Enabled {
			_, _ = m.startChild(context.Background(), account)
		}
		return err
	}
	m.cancelLoginAttemptsForAccount(id)
	return nil
}

func (m *Multiplexer) Logout(ctx context.Context, id string) error {
	child, ok := m.child(id)
	if !ok {
		return fmt.Errorf("account %q is unavailable", id)
	}
	_, err := child.Request(ctx, "account/logout", nil)
	if err == nil {
		err = m.store.SetAccountConnected(id, false)
	}
	return err
}

func (m *Multiplexer) accountSnapshot(ctx context.Context, accountID string) (AccountSnapshot, error) {
	return m.accountSnapshotWithProfile(ctx, accountID, true)
}

func (m *Multiplexer) loginChild(accountID string, generation uint64) (*backend.Child, bool) {
	m.childrenMu.RLock()
	defer m.childrenMu.RUnlock()
	child := m.children[accountID]
	if child == nil || child.Generation() != generation || m.stopping[accountID] == child {
		return nil, false
	}
	return child, true
}

func (m *Multiplexer) accountSnapshotForGeneration(ctx context.Context, accountID string, generation uint64) (AccountSnapshot, error) {
	account, ok := m.store.Account(accountID)
	if !ok {
		return AccountSnapshot{}, fmt.Errorf("account %q not found", accountID)
	}
	child, ok := m.loginChild(accountID, generation)
	if !ok {
		return AccountSnapshot{}, errLoginChildChanged
	}
	return m.accountSnapshotFromChild(ctx, account, child, true, generation)
}

func (m *Multiplexer) accountSnapshotWithProfile(ctx context.Context, accountID string, includeProfile bool) (AccountSnapshot, error) {
	account, ok := m.store.Account(accountID)
	if !ok {
		return AccountSnapshot{}, fmt.Errorf("account %q not found", accountID)
	}
	child, ok := m.child(accountID)
	if !ok {
		if !account.Enabled {
			email := account.LastKnownEmail
			if email == "" {
				email = accountEmailFromAuth(filepath.Join(account.CodexHome, "auth.json"))
				if email != "" {
					_ = m.store.SetAccountIdentity(account.ID, email, account.LastKnownPlanType)
				}
			}
			return AccountSnapshot{
				ID: account.ID, Label: account.Label, Enabled: false, Connected: account.LastKnownConnected,
				Email: email, PlanType: account.LastKnownPlanType, PlanLabel: planLabel(account.LastKnownPlanType),
				Controller: account.Controller, CreatedAt: account.CreatedAt,
				ThreadCount: m.store.ThreadCounts()[account.ID],
			}, nil
		}
		return AccountSnapshot{}, fmt.Errorf("account %q app-server is unavailable", accountID)
	}
	return m.accountSnapshotFromChild(ctx, account, child, includeProfile, 0)
}

func (m *Multiplexer) accountSnapshotFromChild(ctx context.Context, account state.Account, child *backend.Child, includeProfile bool, expectedGeneration uint64) (AccountSnapshot, error) {
	params := json.RawMessage(`{"refreshToken":false}`)
	accountResponse, err := child.Request(ctx, "account/read", params)
	if err != nil {
		return AccountSnapshot{}, err
	}
	if expectedGeneration != 0 {
		if _, ok := m.loginChild(account.ID, expectedGeneration); !ok {
			return AccountSnapshot{}, errLoginChildChanged
		}
	}
	var accountResult struct {
		Account json.RawMessage `json:"account"`
	}
	if err := json.Unmarshal(accountResponse.Result, &accountResult); err != nil {
		return AccountSnapshot{}, fmt.Errorf("decode account response: %w", err)
	}
	snapshot := AccountSnapshot{
		ID: account.ID, Label: account.Label, Enabled: account.Enabled,
		Controller: account.Controller, Connected: string(accountResult.Account) != "null" && len(accountResult.Account) > 0,
		Email: account.LastKnownEmail, PlanType: account.LastKnownPlanType,
		PlanLabel: planLabel(account.LastKnownPlanType),
		CreatedAt: account.CreatedAt, RawAccount: accountResult.Account,
		ThreadCount: m.store.ThreadCounts()[account.ID],
	}
	if err := m.store.SetAccountConnected(account.ID, snapshot.Connected); err != nil {
		return AccountSnapshot{}, err
	}
	if snapshot.Connected {
		var details struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		}
		_ = json.Unmarshal(accountResult.Account, &details)
		snapshot.AuthType = details.Type
		snapshot.Email = details.Email
		snapshot.PlanType = details.PlanType
		snapshot.PlanLabel = planLabel(details.PlanType)
		if details.Email != "" || details.PlanType != "" {
			_ = m.store.SetAccountIdentity(account.ID, details.Email, details.PlanType)
		}
		if includeProfile {
			snapshot.ProfileImageURL = m.profileImageURL(ctx, account)
		}
		if details.Type == "chatgpt" {
			rateResponse, rateErr := child.Request(ctx, "account/rateLimits/read", nil)
			if rateErr == nil {
				var rateResult struct {
					RateLimits RateLimits `json:"rateLimits"`
				}
				if json.Unmarshal(rateResponse.Result, &rateResult) == nil {
					snapshot.RateLimits = &rateResult.RateLimits
				}
			}
		}
	}
	m.applyRateLimitPreview(&snapshot)
	return snapshot, nil
}

func (m *Multiplexer) handleLoginCompleted(accountID string, childGeneration uint64, params json.RawMessage) {
	var completed struct {
		LoginID string `json:"loginId"`
		Success bool   `json:"success"`
	}
	if json.Unmarshal(params, &completed) != nil || completed.LoginID == "" {
		m.rejectUnknownLoginCompletion(accountID, completed.Success)
		return
	}
	m.loginMu.Lock()
	matchedID := m.loginByProviderID[providerLoginKey(accountID, childGeneration, completed.LoginID)]
	var startReady <-chan struct{}
	if matchedID == "" {
		for id, attempt := range m.loginAttempts {
			if attempt.AccountID == accountID && attempt.ChildGeneration == childGeneration && attempt.State == LoginPending && attempt.ProviderLoginID == "" {
				startReady = m.loginReady[id]
				break
			}
		}
	}
	matched, matchedOK := m.loginAttempts[matchedID]
	if matchedID == "" || !matchedOK || matched.AccountID != accountID || matched.ChildGeneration != childGeneration {
		m.loginMu.Unlock()
		if startReady != nil {
			select {
			case <-startReady:
				m.handleLoginCompleted(accountID, childGeneration, params)
			case <-time.After(time.Second):
				m.rejectUnknownLoginCompletion(accountID, completed.Success)
			}
			return
		}
		m.rejectUnknownLoginCompletion(accountID, completed.Success)
		return
	}
	if matched.State != LoginPending {
		newerSucceeded := m.hasNewerSucceededLoginLocked(accountID, matched.Generation)
		cleanupOwned := matched.Cancelling || matched.CleanupHandled
		m.loginMu.Unlock()
		if !cleanupOwned && matched.State != LoginSucceeded && completed.Success && !matched.WasConnected && !newerSucceeded {
			if err := m.rollbackLoginConnection(matched); err != nil {
				_ = m.stopChildGeneration(accountID, matched.ChildGeneration)
			}
		}
		m.publishAccountRefresh(accountID)
		return
	}
	if matched.Cancelling {
		matched.State = LoginCancelled
		matched.Error = "login cancelled"
		matched.UpdatedAt = m.now().Unix()
		m.loginAttempts[matchedID] = matched
		m.loginMu.Unlock()
		m.publishAccountRefresh(accountID)
		return
	}
	if !completed.Success {
		wasCancelling := matched.Cancelling
		if wasCancelling {
			matched.State = LoginCancelled
			matched.Error = "login cancelled"
		} else {
			matched.State = LoginFailed
			matched.Error = "OpenAI account connection failed"
		}
		matched.Cancelling = true
		matched.UpdatedAt = m.now().Unix()
		m.loginAttempts[matchedID] = matched
		m.loginMu.Unlock()
		if m.stopDisabledLoginChildGeneration(accountID, childGeneration) {
			m.finishLoginCleanup(matchedID)
		}
		m.publishAccountRefresh(accountID)
		return
	}
	m.loginMu.Unlock()
	confirmCtx, confirmCancel := context.WithTimeout(context.Background(), 10*time.Second)
	snapshot, confirmErr := m.accountSnapshotForGeneration(confirmCtx, accountID, childGeneration)
	confirmCancel()
	m.loginMu.Lock()
	matched = m.loginAttempts[matchedID]
	if matched.State != LoginPending {
		newerSucceeded := m.hasNewerSucceededLoginLocked(accountID, matched.Generation)
		cleanupOwned := matched.Cancelling || matched.CleanupHandled
		m.loginMu.Unlock()
		if !cleanupOwned && matched.State != LoginSucceeded && !matched.WasConnected && !newerSucceeded {
			if err := m.rollbackLoginConnection(matched); err != nil {
				_ = m.stopChildGeneration(accountID, matched.ChildGeneration)
			}
		}
		m.publishAccountRefresh(accountID)
		return
	}
	if matched.Cancelling {
		matched.State = LoginCancelled
		matched.Error = "login cancelled"
		matched.UpdatedAt = m.now().Unix()
		m.loginAttempts[matchedID] = matched
		m.loginMu.Unlock()
		m.publishAccountRefresh(accountID)
		return
	}
	matched.Cancelling = true
	matched.UpdatedAt = m.now().Unix()
	if confirmErr == nil && snapshot.Connected {
		matched.State = LoginSucceeded
		matched.Error = ""
	} else {
		matched.State = LoginFailed
		matched.Error = "OpenAI account connection could not be confirmed"
	}
	m.loginAttempts[matchedID] = matched
	m.loginMu.Unlock()
	if m.stopDisabledLoginChildGeneration(accountID, childGeneration) {
		m.finishLoginCleanup(matchedID)
	}
	m.publishAccountRefresh(accountID)
}

func (m *Multiplexer) rejectUnknownLoginCompletion(accountID string, success bool) {
	m.publishAccountRefresh(accountID)
}

func (m *Multiplexer) hasNewerSucceededLoginLocked(accountID string, generation uint64) bool {
	for _, attempt := range m.loginAttempts {
		if attempt.AccountID == accountID && attempt.Generation > generation && attempt.State == LoginSucceeded {
			return true
		}
	}
	return false
}

func (m *Multiplexer) stopDisabledLoginChild(accountID string) bool {
	return m.stopDisabledLoginChildGeneration(accountID, 0)
}

func (m *Multiplexer) stopDisabledLoginChildGeneration(accountID string, generation uint64) bool {
	account, ok := m.store.Account(accountID)
	if ok && !account.Enabled {
		var err error
		if generation == 0 {
			err = m.stopChild(accountID)
		} else {
			err = m.stopChildGeneration(accountID, generation)
		}
		if err != nil {
			m.publish(Event{Type: "account-unavailable", AccountID: accountID, Message: "Subscription backend is still stopping"})
			return false
		}
	}
	return true
}

func (m *Multiplexer) finishLoginCleanup(attemptID string) {
	m.loginMu.Lock()
	attempt, ok := m.loginAttempts[attemptID]
	if ok {
		attempt.Cancelling = false
		m.loginAttempts[attemptID] = attempt
	}
	m.loginMu.Unlock()
	if ok {
		m.publishTerminalLogin(attemptID)
	}
}

func (m *Multiplexer) confirmChildExit(accountID string, childGeneration uint64) {
	m.loginMu.Lock()
	for providerID, attemptID := range m.loginByProviderID {
		attempt := m.loginAttempts[attemptID]
		if attempt.AccountID == accountID && attempt.ChildGeneration == childGeneration {
			delete(m.loginByProviderID, providerID)
		}
	}
	terminalIDs := make([]string, 0, 1)
	for id, attempt := range m.loginAttempts {
		if attempt.AccountID != accountID || attempt.ChildGeneration != childGeneration {
			continue
		}
		if attempt.State == LoginPending {
			attempt.State = LoginFailed
			attempt.Error = "Account backend stopped during sign-in"
			attempt.UpdatedAt = m.now().Unix()
			attempt.Cancelling = false
			m.loginAttempts[id] = attempt
			if ready := m.loginReady[id]; ready != nil {
				close(ready)
				delete(m.loginReady, id)
			}
			terminalIDs = append(terminalIDs, id)
		}
	}
	m.loginMu.Unlock()
	for _, id := range terminalIDs {
		m.publishTerminalLogin(id)
	}
	m.failRoutesForChild(accountID, childGeneration)
}

func (m *Multiplexer) releaseLoginCleanup(accountID string, childGeneration uint64) {
	m.loginMu.Lock()
	finished := make([]string, 0, 1)
	restart := false
	for id, attempt := range m.loginAttempts {
		if attempt.AccountID == accountID && attempt.ChildGeneration == childGeneration && attempt.State != LoginPending && attempt.Cancelling {
			attempt.Cancelling = false
			attempt.CleanupHandled = true
			restart = restart || attempt.RestartAfterCleanup
			attempt.RestartAfterCleanup = false
			m.loginAttempts[id] = attempt
			finished = append(finished, id)
		}
	}
	m.loginMu.Unlock()
	if restart {
		if account, ok := m.store.Account(accountID); ok && account.Enabled {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if _, err := m.startChild(ctx, account); err != nil {
				m.publish(Event{Type: "account-unavailable", AccountID: accountID, Message: "Subscription backend could not restart"})
			}
			cancel()
		}
	}
	for _, id := range finished {
		m.publishTerminalLogin(id)
	}
}

func (m *Multiplexer) cancelLoginAttemptsForAccount(accountID string) {
	m.loginMu.Lock()
	terminalIDs := make([]string, 0, 1)
	for id, attempt := range m.loginAttempts {
		if attempt.AccountID != accountID || attempt.State != LoginPending {
			continue
		}
		attempt.State = LoginCancelled
		attempt.Error = "account removed"
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[id] = attempt
		terminalIDs = append(terminalIDs, id)
	}
	m.loginMu.Unlock()
	for _, id := range terminalIDs {
		m.publishTerminalLogin(id)
	}
}

func (m *Multiplexer) loginAttemptLocked(id string) LoginAttempt {
	return cloneLoginAttempt(m.loginAttempts[id])
}

func (m *Multiplexer) waitForLoginStart(ctx context.Context, attemptID string, ready <-chan struct{}) (LoginAttempt, error) {
	if ready != nil {
		select {
		case <-ready:
		case <-ctx.Done():
			m.loginMu.Lock()
			attempt := m.loginAttemptLocked(attemptID)
			m.loginMu.Unlock()
			return attempt, ctx.Err()
		}
	}
	m.loginMu.Lock()
	attempt, ok := m.loginAttempts[attemptID]
	m.loginMu.Unlock()
	if !ok {
		return LoginAttempt{}, fmt.Errorf("login attempt %q not found", attemptID)
	}
	return cloneLoginAttempt(attempt), nil
}

func cloneLoginAttempt(attempt LoginAttempt) LoginAttempt {
	attempt.Result = append(json.RawMessage(nil), attempt.Result...)
	return attempt
}

func opaqueID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validateIdempotencyKey(key string) error {
	if key == "" {
		return errors.New("idempotency key is required")
	}
	if len(key) > maxIdempotencyKeyLength {
		return fmt.Errorf("idempotency key exceeds %d bytes", maxIdempotencyKeyLength)
	}
	return nil
}

func planLabel(planType string) string {
	switch planType {
	case "free":
		return "Free"
	case "go":
		return "Go"
	case "plus":
		return "Plus"
	case "prolite":
		return "Pro 5x"
	case "pro":
		return "Pro 20x"
	case "team":
		return "Team"
	case "self_serve_business_prolite", "self_serve_business_usage_based", "business":
		return "Business"
	case "ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based", "enterprise":
		return "Enterprise"
	case "edu":
		return "Edu"
	default:
		return ""
	}
}

func (m *Multiplexer) chooseAccount(ctx context.Context) (state.Account, RouteReason, error) {
	return m.chooseAccountExcluding(ctx, nil)
}

func (m *Multiplexer) chooseAccountExcluding(ctx context.Context, excluded map[string]struct{}) (state.Account, RouteReason, error) {
	snapshots := m.accountSnapshots(ctx, false)
	type candidate struct {
		account      state.Account
		reason       RouteReason
		weekly       *RateLimitWindow
		weeklyUsed   float64
		shortUsed    float64
		resetCredits resetCreditMetadata
		urgency      float64
	}
	candidates := make([]candidate, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if _, skip := excluded[snapshot.ID]; skip {
			continue
		}
		if !snapshot.Enabled || !snapshot.Connected || snapshot.AuthType != "chatgpt" {
			continue
		}
		account, ok := m.store.Account(snapshot.ID)
		if !ok {
			continue
		}
		weekly, short := longestAndShortestWindow(snapshot.RateLimits)
		if weekly != nil && weekly.UsedPercent >= 100 {
			continue
		}
		weeklyUsed := 1_000.0
		shortUsed := 1_000.0
		reason := RouteReason{ThreadCount: snapshot.ThreadCount}
		if weekly != nil {
			weeklyUsed = weekly.UsedPercent
			reason.WeeklyUsedPercent = &weekly.UsedPercent
			if weekly.ResetsAt != nil {
				value := *weekly.ResetsAt
				reason.WeeklyResetsAt = &value
			}
		}
		if short != nil {
			shortUsed = short.UsedPercent
			reason.ShortUsedPercent = &short.UsedPercent
		}
		candidates = append(candidates, candidate{
			account: account, reason: reason, weekly: weekly,
			weeklyUsed: weeklyUsed, shortUsed: shortUsed,
		})
	}
	if len(candidates) == 0 {
		return state.Account{}, RouteReason{}, errNoSubscriptionCapacity
	}

	type resetResult struct {
		index    int
		metadata resetCreditMetadata
	}
	resetResults := make(chan resetResult, len(candidates))
	for index := range candidates {
		go func(index int, account state.Account) {
			resetResults <- resetResult{
				index: index, metadata: m.routingResetCredits(ctx, account),
			}
		}(index, candidates[index].account)
	}

collectResetCredits:
	for received := 0; received < len(candidates); received++ {
		select {
		case result := <-resetResults:
			candidates[result.index].resetCredits = result.metadata
		case <-ctx.Done():
			break collectResetCredits
		}
	}

	now := m.now()
	for index := range candidates {
		entry := &candidates[index]
		entry.urgency = routeUrgencyScore(now, entry.weekly, entry.resetCredits)
		urgency := entry.urgency
		entry.reason.UrgencyScore = &urgency
		if entry.resetCredits.Known {
			count := entry.resetCredits.AvailableCount
			entry.reason.BankedResetCount = &count
		}
		if entry.resetCredits.EarliestExpiry != nil {
			expiresAt := *entry.resetCredits.EarliestExpiry
			entry.reason.ResetCreditExpiresAt = &expiresAt
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if math.Abs(left.urgency-right.urgency) > 0.000001 {
			return left.urgency > right.urgency
		}
		if math.Abs(left.shortUsed-right.shortUsed) > 0.001 {
			return left.shortUsed < right.shortUsed
		}
		if math.Abs(left.weeklyUsed-right.weeklyUsed) > 0.001 {
			return left.weeklyUsed < right.weeklyUsed
		}
		if left.reason.ThreadCount != right.reason.ThreadCount {
			return left.reason.ThreadCount < right.reason.ThreadCount
		}
		return left.account.CreatedAt < right.account.CreatedAt
	})
	return candidates[0].account, candidates[0].reason, nil
}

func routeUrgencyScore(now time.Time, weekly *RateLimitWindow, credits resetCreditMetadata) float64 {
	if weekly == nil {
		return -1
	}
	remaining := math.Max(0, math.Min(100, 100-weekly.UsedPercent))
	horizon := routingFallbackWindow
	if weekly.WindowDurationMins != nil && *weekly.WindowDurationMins > 0 {
		horizon = time.Duration(*weekly.WindowDurationMins) * time.Minute
	}
	if weekly.ResetsAt != nil {
		untilReset := time.Unix(*weekly.ResetsAt, 0).Sub(now)
		if untilReset > 0 {
			horizon = untilReset
		}
	}
	if horizon < routingMinimumWindow {
		horizon = routingMinimumWindow
	}
	urgency := remaining / horizon.Hours()
	if credits.Known && credits.AvailableCount > 0 {
		creditCount := min(credits.AvailableCount, routingResetBonusCreditCap)
		urgency *= 1 + float64(creditCount)*routingResetBonusPerCredit
	}
	return urgency
}

func (m *Multiplexer) AggregatedRateLimits(ctx context.Context) (*RateLimits, error) {
	limits, err := aggregateRateLimits(m.accountSnapshots(ctx, false))
	if err != nil {
		return nil, err
	}
	if preview := m.currentRateLimitPreview(); preview != nil && preview.Mode.isAllDepleted() {
		limits.RateLimitReachedType = "legacy_rate_limit_reached"
	}
	return limits, nil
}

func aggregateRateLimits(snapshots []AccountSnapshot) (*RateLimits, error) {
	primary := make([]*RateLimitWindow, 0, len(snapshots))
	secondary := make([]*RateLimitWindow, 0, len(snapshots))
	hasSubscription := false
	hasCapacity := false
	for _, snapshot := range snapshots {
		if !snapshot.Enabled || !snapshot.Connected || snapshot.AuthType != "chatgpt" {
			continue
		}
		hasSubscription = true
		if snapshot.RateLimits != nil {
			primary = append(primary, snapshot.RateLimits.Primary)
			secondary = append(secondary, snapshot.RateLimits.Secondary)
		}
		weekly, _ := longestAndShortestWindow(snapshot.RateLimits)
		if weekly == nil || weekly.UsedPercent < 100 {
			hasCapacity = true
		}
	}
	if !hasSubscription {
		return nil, errors.New("no enabled ChatGPT subscription is connected")
	}
	result := &RateLimits{
		Primary:   averageRateLimitWindow(primary),
		Secondary: averageRateLimitWindow(secondary),
	}
	if !hasCapacity {
		result.RateLimitReachedType = "rate_limit_reached"
	}
	return result, nil
}

func averageRateLimitWindow(windows []*RateLimitWindow) *RateLimitWindow {
	var used float64
	var count int
	var longestDuration *int64
	var earliestReset *int64
	for _, window := range windows {
		if window == nil {
			continue
		}
		used += window.UsedPercent
		count++
		if window.WindowDurationMins != nil &&
			(longestDuration == nil || *window.WindowDurationMins > *longestDuration) {
			value := *window.WindowDurationMins
			longestDuration = &value
		}
		if window.ResetsAt != nil && (earliestReset == nil || *window.ResetsAt < *earliestReset) {
			value := *window.ResetsAt
			earliestReset = &value
		}
	}
	if count == 0 {
		return nil
	}
	return &RateLimitWindow{
		UsedPercent:        used / float64(count),
		WindowDurationMins: longestDuration,
		ResetsAt:           earliestReset,
	}
}

func longestAndShortestWindow(limits *RateLimits) (*RateLimitWindow, *RateLimitWindow) {
	if limits == nil {
		return nil, nil
	}
	windows := make([]*RateLimitWindow, 0, 2)
	if limits.Primary != nil {
		windows = append(windows, limits.Primary)
	}
	if limits.Secondary != nil {
		windows = append(windows, limits.Secondary)
	}
	if len(windows) == 0 {
		return nil, nil
	}
	sort.SliceStable(windows, func(i, j int) bool {
		return duration(windows[i]) < duration(windows[j])
	})
	return windows[len(windows)-1], windows[0]
}

func duration(window *RateLimitWindow) int64 {
	if window.WindowDurationMins == nil {
		return 0
	}
	return *window.WindowDurationMins
}

func contextWithControlTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 20*time.Second)
}
