package mux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/state"
)

var errNoSubscriptionCapacity = errors.New("no enabled ChatGPT subscription has capacity")

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
	ID        string          `json:"id"`
	AccountID string          `json:"accountId"`
	Mode      string          `json:"mode"`
	State     LoginState      `json:"state"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	StartedAt int64           `json:"startedAt"`
	UpdatedAt int64           `json:"updatedAt"`
	ExpiresAt int64           `json:"expiresAt"`
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
					Controller: account.Controller, CreatedAt: account.CreatedAt, Error: err.Error(),
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
	m.loginMu.Unlock()
	account, ok := m.store.Account(id)
	if !ok {
		return LoginAttempt{}, fmt.Errorf("account %q not found", id)
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
			return LoginAttempt{}, fmt.Errorf("account %q is unavailable: %w", id, err)
		}
	}
	attemptID, err := opaqueID()
	if err != nil {
		return LoginAttempt{}, err
	}
	now := m.now().Unix()
	attempt := LoginAttempt{
		ID: attemptID, AccountID: id, Mode: mode, State: LoginPending,
		StartedAt: now, UpdatedAt: now, ExpiresAt: m.now().Add(loginAttemptLifetime).Unix(),
	}
	m.loginMu.Lock()
	if existingID := m.loginKeys[key]; existingID != "" {
		ready := m.loginReady[existingID]
		m.loginMu.Unlock()
		return m.waitForLoginStart(ctx, existingID, ready)
	}
	if existingID := m.pendingLoginForAccountLocked(id); existingID != "" {
		m.loginKeys[key] = existingID
		ready := m.loginReady[existingID]
		m.loginMu.Unlock()
		return m.waitForLoginStart(ctx, existingID, ready)
	}
	m.loginKeys[key] = attempt.ID
	m.loginAttempts[attempt.ID] = attempt
	m.loginReady[attempt.ID] = make(chan struct{})
	m.loginMu.Unlock()
	params, _ := json.Marshal(map[string]any{"type": mode})
	response, err := child.Request(ctx, "account/login/start", params)
	if err != nil {
		m.loginMu.Lock()
		attempt = m.loginAttempts[attempt.ID]
		attempt.State = LoginFailed
		attempt.Error = err.Error()
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[attempt.ID] = attempt
		close(m.loginReady[attempt.ID])
		delete(m.loginReady, attempt.ID)
		m.loginMu.Unlock()
		m.publish(Event{Type: "account-login", AccountID: id, Data: attempt})
		m.stopDisabledLoginChild(id)
		return attempt, err
	}
	m.loginMu.Lock()
	attempt = m.loginAttempts[attempt.ID]
	attempt.Result = append(json.RawMessage(nil), response.Result...)
	attempt.UpdatedAt = m.now().Unix()
	m.loginAttempts[attempt.ID] = attempt
	close(m.loginReady[attempt.ID])
	delete(m.loginReady, attempt.ID)
	m.loginMu.Unlock()
	m.publish(Event{Type: "account-login", AccountID: id, Data: attempt})
	if !account.Enabled {
		m.scheduleDisabledLoginExpiry(attempt.ID, attempt.ExpiresAt)
	}
	return attempt, nil
}

func (m *Multiplexer) pendingLoginForAccountLocked(accountID string) string {
	now := m.now().Unix()
	for id, attempt := range m.loginAttempts {
		if attempt.AccountID == accountID && attempt.State == LoginPending && now < attempt.ExpiresAt {
			return id
		}
	}
	return ""
}

func (m *Multiplexer) scheduleDisabledLoginExpiry(attemptID string, expiresAt int64) {
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
		m.loginAttempts[attemptID] = attempt
		expired = true
	}
	result := cloneLoginAttempt(attempt)
	m.loginMu.Unlock()
	if expired {
		m.stopDisabledLoginChild(attempt.AccountID)
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
	m.loginMu.Unlock()
	if err := m.Logout(ctx, attempt.AccountID); err != nil {
		return cloneLoginAttempt(attempt), fmt.Errorf("cancel login: %w", err)
	}
	m.loginMu.Lock()
	attempt = m.loginAttempts[attemptID]
	if attempt.State != LoginPending {
		m.loginMu.Unlock()
		return cloneLoginAttempt(attempt), nil
	}
	attempt.State = LoginCancelled
	attempt.Error = "login cancelled"
	attempt.UpdatedAt = m.now().Unix()
	m.loginAttempts[attemptID] = attempt
	m.loginMu.Unlock()
	m.publish(Event{Type: "account-login", AccountID: attempt.AccountID, Data: attempt})
	m.stopDisabledLoginChild(attempt.AccountID)
	return cloneLoginAttempt(attempt), nil
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

func (m *Multiplexer) accountSnapshotWithProfile(ctx context.Context, accountID string, includeProfile bool) (AccountSnapshot, error) {
	account, ok := m.store.Account(accountID)
	if !ok {
		return AccountSnapshot{}, fmt.Errorf("account %q not found", accountID)
	}
	child, ok := m.child(accountID)
	if !ok {
		if !account.Enabled {
			return AccountSnapshot{
				ID: account.ID, Label: account.Label, Enabled: false, Connected: account.LastKnownConnected,
				Controller: account.Controller, CreatedAt: account.CreatedAt,
				ThreadCount: m.store.ThreadCounts()[account.ID],
			}, nil
		}
		return AccountSnapshot{}, fmt.Errorf("account %q app-server is unavailable", accountID)
	}
	params := json.RawMessage(`{"refreshToken":false}`)
	accountResponse, err := child.Request(ctx, "account/read", params)
	if err != nil {
		return AccountSnapshot{}, err
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

func (m *Multiplexer) completeLoginAttempts(accountID string) {
	m.loginMu.Lock()
	for id, attempt := range m.loginAttempts {
		if attempt.AccountID != accountID || attempt.State != LoginPending {
			continue
		}
		attempt.State = LoginSucceeded
		attempt.Error = ""
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[id] = attempt
	}
	m.loginMu.Unlock()
	m.stopDisabledLoginChild(accountID)
}

func (m *Multiplexer) stopDisabledLoginChild(accountID string) {
	account, ok := m.store.Account(accountID)
	if ok && !account.Enabled {
		_ = m.stopChild(accountID)
	}
}

func (m *Multiplexer) cancelLoginAttemptsForAccount(accountID string) {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	for id, attempt := range m.loginAttempts {
		if attempt.AccountID != accountID || attempt.State != LoginPending {
			continue
		}
		attempt.State = LoginCancelled
		attempt.Error = "account removed"
		attempt.UpdatedAt = m.now().Unix()
		m.loginAttempts[id] = attempt
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
