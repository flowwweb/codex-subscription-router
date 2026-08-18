package mux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/state"
)

const profileStatsCacheTTL = 5 * time.Minute

type usageBucket struct {
	StartDate string `json:"start_date"`
	Tokens    int64  `json:"tokens"`
}

type profileInvocation struct {
	Type       string  `json:"type"`
	PluginID   *string `json:"plugin_id"`
	PluginName *string `json:"plugin_name"`
	SkillID    *string `json:"skill_id"`
	SkillName  *string `json:"skill_name"`
	UsageCount int64   `json:"usage_count"`
}

type whamProfileStats struct {
	LifetimeTokens                    int64               `json:"lifetime_tokens"`
	PeakDailyTokens                   int64               `json:"peak_daily_tokens"`
	CurrentStreakDays                 int64               `json:"current_streak_days"`
	LongestStreakDays                 int64               `json:"longest_streak_days"`
	TotalThreads                      int64               `json:"total_threads"`
	LongestRunningTurnSec             int64               `json:"longest_running_turn_sec"`
	FastModeUsagePercentage           float64             `json:"fast_mode_usage_percentage"`
	TotalSkillsUsed                   int64               `json:"total_skills_used"`
	UniqueSkillsUsed                  int64               `json:"unique_skills_used"`
	MostUsedReasoningEffort           string              `json:"most_used_reasoning_effort"`
	MostUsedReasoningEffortPercentage float64             `json:"most_used_reasoning_effort_percentage"`
	DailyUsageBuckets                 []usageBucket       `json:"daily_usage_buckets"`
	CumulativeDailyUsageBuckets       []usageBucket       `json:"cumulative_daily_usage_buckets"`
	WeeklyUsageBuckets                []usageBucket       `json:"weekly_usage_buckets"`
	TopInvocations                    []profileInvocation `json:"top_invocations"`
	WorkspaceRank                     any                 `json:"workspace_rank"`
	WorkspaceTotalUserCount           any                 `json:"workspace_total_user_count"`
}

type whamProfile struct {
	Profile  json.RawMessage  `json:"profile"`
	Stats    whamProfileStats `json:"stats"`
	Metadata struct {
		StatsAsOf   string `json:"stats_as_of"`
		GeneratedAt string `json:"generated_at"`
		StatsError  any    `json:"stats_error"`
	} `json:"metadata"`
}

type CombinedProfileAccount struct {
	ID              string            `json:"id"`
	Label           string            `json:"label"`
	PlanLabel       string            `json:"planLabel,omitempty"`
	ProfileImageURL string            `json:"profileImageUrl,omitempty"`
	Stats           *whamProfileStats `json:"stats,omitempty"`
	StatsAsOf       string            `json:"statsAsOf,omitempty"`
}

type CombinedProfile struct {
	Profile  whamProfile              `json:"profile"`
	Accounts []CombinedProfileAccount `json:"accounts"`
	Partial  bool                     `json:"partial"`
}

type profileFetchResult struct {
	account state.Account
	profile whamProfile
	err     error
}

type profileStatsCacheEntry struct {
	profile   whamProfile
	expiresAt time.Time
	loading   chan struct{}
	err       error
}

// CombinedProfile returns the native /wham/profiles/me shape with activity
// merged across connected subscriptions. Identity remains the controller
// account's identity, while usage is summed and streaks are recomputed from
// the merged daily activity calendar.
func (m *Multiplexer) CombinedProfile(ctx context.Context) (CombinedProfile, error) {
	snapshots := m.Accounts(ctx)
	accounts := make([]state.Account, 0, len(snapshots))
	descriptors := make([]CombinedProfileAccount, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !snapshot.Connected || snapshot.AuthType != "chatgpt" {
			continue
		}
		account, ok := m.store.Account(snapshot.ID)
		if !ok {
			continue
		}
		accounts = append(accounts, account)
		descriptors = append(descriptors, CombinedProfileAccount{
			ID: snapshot.ID, Label: snapshot.Label, PlanLabel: snapshot.PlanLabel,
			ProfileImageURL: snapshot.ProfileImageURL,
		})
	}
	if len(accounts) == 0 {
		return CombinedProfile{}, errors.New("no connected ChatGPT subscriptions")
	}

	results := make(chan profileFetchResult, len(accounts))
	for _, account := range accounts {
		go func(account state.Account) {
			profile, err := m.fetchWhamProfileCached(ctx, account)
			results <- profileFetchResult{account: account, profile: profile, err: err}
		}(account)
	}

	profiles := make([]profileFetchResult, 0, len(accounts))
	partial := false
	for range accounts {
		select {
		case result := <-results:
			if result.err != nil {
				partial = true
				continue
			}
			profiles = append(profiles, result)
		case <-ctx.Done():
			return CombinedProfile{}, ctx.Err()
		}
	}
	if len(profiles) == 0 {
		return CombinedProfile{}, errors.New("profile stats are unavailable for every subscription")
	}

	sort.SliceStable(profiles, func(i, j int) bool {
		return profiles[i].account.Controller && !profiles[j].account.Controller
	})
	combined := profiles[0].profile
	combined.Stats = aggregateProfileStats(profiles)
	combined.Metadata.StatsError = nil
	combined.Metadata.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	combined.Metadata.StatsAsOf = commonStatsAsOf(profiles)
	partial = partial || statsDatesDiffer(profiles)
	for index := range descriptors {
		for _, result := range profiles {
			if result.account.ID != descriptors[index].ID {
				continue
			}
			stats := result.profile.Stats
			descriptors[index].Stats = &stats
			descriptors[index].StatsAsOf = result.profile.Metadata.StatsAsOf
			break
		}
	}
	return CombinedProfile{Profile: combined, Accounts: descriptors, Partial: partial}, nil
}

// AccountProfile returns one subscription's native profile payload without
// refreshing unrelated accounts.
func (m *Multiplexer) AccountProfile(ctx context.Context, accountID string) (CombinedProfile, error) {
	selected, found := m.store.Account(accountID)
	if !found {
		return CombinedProfile{}, fmt.Errorf("profile account %q is unavailable", accountID)
	}
	snapshot, err := m.accountSnapshotWithProfile(ctx, accountID, false)
	if err != nil {
		return CombinedProfile{}, err
	}
	if !snapshot.Connected || snapshot.AuthType != "chatgpt" {
		return CombinedProfile{}, fmt.Errorf("profile account %q is unavailable", accountID)
	}
	profile, err := m.fetchWhamProfileCached(ctx, selected)
	if err != nil {
		return CombinedProfile{}, err
	}
	stats := profile.Stats
	descriptors := []CombinedProfileAccount{{
		ID: snapshot.ID, Label: snapshot.Label, PlanLabel: snapshot.PlanLabel,
		ProfileImageURL: snapshot.ProfileImageURL, Stats: &stats, StatsAsOf: profile.Metadata.StatsAsOf,
	}}
	return CombinedProfile{Profile: profile, Accounts: descriptors}, nil
}

func (m *Multiplexer) fetchWhamProfileCached(ctx context.Context, account state.Account) (whamProfile, error) {
	cacheKey := account.ID
	credentials, credentialErr := readAuthFile(filepath.Join(account.CodexHome, "auth.json"))
	if credentialErr == nil && credentials.Tokens.AccountID != "" {
		cacheKey += "|" + credentials.Tokens.AccountID
	}
	now := m.now()
	m.profileStatsMu.Lock()
	if cached, ok := m.profileStatsCache[cacheKey]; ok {
		if cached.loading != nil {
			loading := cached.loading
			m.profileStatsMu.Unlock()
			select {
			case <-loading:
				m.profileStatsMu.Lock()
				completed, completedOK := m.profileStatsCache[cacheKey]
				if completedOK && completed.loading == nil {
					profile, err := completed.profile, completed.err
					m.profileStatsMu.Unlock()
					return profile, err
				}
				m.profileStatsMu.Unlock()
				return m.fetchWhamProfileCached(ctx, account)
			case <-ctx.Done():
				return whamProfile{}, ctx.Err()
			}
		}
		if now.Before(cached.expiresAt) {
			m.profileStatsMu.Unlock()
			return cached.profile, cached.err
		}
	}
	loading := make(chan struct{})
	m.profileStatsCache[cacheKey] = profileStatsCacheEntry{loading: loading}
	m.profileStatsMu.Unlock()

	profile, err := whamProfile{}, credentialErr
	if err == nil {
		profile, err = fetchWhamProfileWithCredentials(ctx, m.profileClient, profileURL, credentials)
		if err == nil {
			latest, latestErr := readAuthFile(filepath.Join(account.CodexHome, "auth.json"))
			if latestErr != nil || latest.Tokens.AccountID != credentials.Tokens.AccountID {
				err = errors.New("account credentials changed during stats request")
			}
		}
	}
	m.profileStatsMu.Lock()
	entry := profileStatsCacheEntry{profile: profile, expiresAt: now.Add(profileStatsCacheTTL), err: err}
	if err != nil {
		entry.expiresAt = now.Add(10 * time.Second)
	}
	m.profileStatsCache[cacheKey] = entry
	close(loading)
	m.profileStatsMu.Unlock()
	return profile, err
}

func fetchWhamProfileWithCredentials(ctx context.Context, client *http.Client, endpoint string, credentials authFile) (whamProfile, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return whamProfile{}, fmt.Errorf("create combined profile request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credentials.Tokens.AccessToken)
	request.Header.Set("ChatGPT-Account-ID", credentials.Tokens.AccountID)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Codex Subscription Router")
	response, err := client.Do(request)
	if err != nil {
		return whamProfile{}, fmt.Errorf("fetch combined profile: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, profileMaxBytes+1))
	if err != nil {
		return whamProfile{}, fmt.Errorf("read combined profile: %w", err)
	}
	if len(data) > profileMaxBytes {
		return whamProfile{}, errors.New("combined profile response is too large")
	}
	if response.StatusCode != http.StatusOK {
		return whamProfile{}, fmt.Errorf("fetch combined profile: status %d", response.StatusCode)
	}
	var profile whamProfile
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&profile); err != nil {
		return whamProfile{}, fmt.Errorf("decode combined profile: %w", err)
	}
	if profile.Metadata.StatsError != nil {
		return whamProfile{}, errors.New("profile stats are unavailable")
	}
	if strings.TrimSpace(profile.Metadata.StatsAsOf) == "" {
		return whamProfile{}, errors.New("profile stats timestamp is unavailable")
	}
	if _, err := time.Parse(time.RFC3339, profile.Metadata.StatsAsOf); err != nil {
		return whamProfile{}, errors.New("profile stats timestamp is invalid")
	}
	return profile, nil
}

func aggregateProfileStats(profiles []profileFetchResult) whamProfileStats {
	return aggregateProfileStatsAt(profiles, time.Now().UTC())
}

func aggregateProfileStatsAt(profiles []profileFetchResult, today time.Time) whamProfileStats {
	var combined whamProfileStats
	daily := make(map[string]int64)
	invocations := make(map[string]profileInvocation)
	reasoningWeights := make(map[string]float64)
	var totalFastWeight float64
	var totalReasoningWeight float64
	for _, result := range profiles {
		stats := result.profile.Stats
		combined.LifetimeTokens += stats.LifetimeTokens
		combined.TotalThreads += stats.TotalThreads
		combined.TotalSkillsUsed += stats.TotalSkillsUsed
		combined.UniqueSkillsUsed += stats.UniqueSkillsUsed
		if stats.LongestRunningTurnSec > combined.LongestRunningTurnSec {
			combined.LongestRunningTurnSec = stats.LongestRunningTurnSec
		}
		weight := float64(stats.TotalThreads)
		if weight <= 0 {
			weight = 1
		}
		totalFastWeight += stats.FastModeUsagePercentage * weight
		totalReasoningWeight += weight
		if stats.MostUsedReasoningEffort != "" {
			reasoningWeights[stats.MostUsedReasoningEffort] += stats.MostUsedReasoningEffortPercentage * weight / 100
		}
		for _, bucket := range stats.DailyUsageBuckets {
			daily[bucket.StartDate] += bucket.Tokens
		}
		for _, invocation := range stats.TopInvocations {
			key := invocation.Type + "|" + stringValue(invocation.PluginID) + "|" + stringValue(invocation.SkillID)
			current := invocations[key]
			if current.Type == "" {
				current = invocation
			} else {
				current.UsageCount += invocation.UsageCount
			}
			invocations[key] = current
		}
	}
	if totalReasoningWeight > 0 {
		combined.FastModeUsagePercentage = totalFastWeight / totalReasoningWeight
	}
	for effort, weight := range reasoningWeights {
		if weight > reasoningWeights[combined.MostUsedReasoningEffort] {
			combined.MostUsedReasoningEffort = effort
		}
	}
	if totalReasoningWeight > 0 {
		combined.MostUsedReasoningEffortPercentage = reasoningWeights[combined.MostUsedReasoningEffort] / totalReasoningWeight * 100
	}

	dates := make([]string, 0, len(daily))
	for date := range daily {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	var cumulative int64
	weekly := make(map[string]int64)
	for _, date := range dates {
		tokens := daily[date]
		combined.DailyUsageBuckets = append(combined.DailyUsageBuckets, usageBucket{StartDate: date, Tokens: tokens})
		cumulative += tokens
		combined.CumulativeDailyUsageBuckets = append(combined.CumulativeDailyUsageBuckets, usageBucket{StartDate: date, Tokens: cumulative})
		if tokens > combined.PeakDailyTokens {
			combined.PeakDailyTokens = tokens
		}
		if parsed, err := time.Parse("2006-01-02", date); err == nil {
			weekday := (int(parsed.Weekday()) + 6) % 7
			week := parsed.AddDate(0, 0, -weekday).Format("2006-01-02")
			weekly[week] += tokens
		}
	}
	weeks := make([]string, 0, len(weekly))
	for week := range weekly {
		weeks = append(weeks, week)
	}
	sort.Strings(weeks)
	for _, week := range weeks {
		combined.WeeklyUsageBuckets = append(combined.WeeklyUsageBuckets, usageBucket{StartDate: week, Tokens: weekly[week]})
	}
	combined.CurrentStreakDays, combined.LongestStreakDays = mergedStreaks(dates, today)

	for _, invocation := range invocations {
		combined.TopInvocations = append(combined.TopInvocations, invocation)
	}
	sort.SliceStable(combined.TopInvocations, func(i, j int) bool {
		return combined.TopInvocations[i].UsageCount > combined.TopInvocations[j].UsageCount
	})
	if len(combined.TopInvocations) > 5 {
		combined.TopInvocations = combined.TopInvocations[:5]
	}
	return combined
}

func mergedStreaks(dates []string, today time.Time) (int64, int64) {
	active := make(map[string]bool, len(dates))
	for _, date := range dates {
		active[date] = true
	}
	var longest, run int64
	var previous time.Time
	for _, value := range dates {
		date, err := time.Parse("2006-01-02", value)
		if err != nil {
			continue
		}
		if !previous.IsZero() && date.Sub(previous) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		previous = date
	}
	anchor := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if !active[anchor.Format("2006-01-02")] {
		anchor = anchor.AddDate(0, 0, -1)
	}
	var current int64
	for active[anchor.Format("2006-01-02")] {
		current++
		anchor = anchor.AddDate(0, 0, -1)
	}
	return current, longest
}

func commonStatsAsOf(profiles []profileFetchResult) string {
	var earliest time.Time
	for _, profile := range profiles {
		observed, err := time.Parse(time.RFC3339, profile.profile.Metadata.StatsAsOf)
		if err != nil {
			continue
		}
		if earliest.IsZero() || observed.Before(earliest) {
			earliest = observed
		}
	}
	if earliest.IsZero() {
		return ""
	}
	return earliest.UTC().Format(time.RFC3339Nano)
}

func statsDatesDiffer(profiles []profileFetchResult) bool {
	if len(profiles) < 2 {
		return false
	}
	first, err := time.Parse(time.RFC3339, profiles[0].profile.Metadata.StatsAsOf)
	if err != nil {
		return true
	}
	for _, profile := range profiles[1:] {
		observed, parseErr := time.Parse(time.RFC3339, profile.profile.Metadata.StatsAsOf)
		if parseErr != nil || !observed.Equal(first) {
			return true
		}
	}
	return false
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
