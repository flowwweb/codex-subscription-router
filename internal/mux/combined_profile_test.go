package mux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchWhamProfileRequiresUsableStatsMetadata(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid", body: `{"stats":{},"metadata":{"stats_as_of":"2026-08-18T08:00:00Z","stats_error":null}}`},
		{name: "provider error", body: `{"stats":{},"metadata":{"stats_as_of":"2026-08-18T08:00:00Z","stats_error":{"message":"temporarily unavailable"}}}`, wantErr: true},
		{name: "missing timestamp", body: `{"stats":{},"metadata":{"stats_error":null}}`, wantErr: true},
		{name: "invalid timestamp", body: `{"stats":{},"metadata":{"stats_as_of":"not-a-timestamp","stats_error":null}}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			var credentials authFile
			credentials.Tokens.AccessToken = "test-token"
			credentials.Tokens.AccountID = "test-account"
			_, err := fetchWhamProfileWithCredentials(context.Background(), server.Client(), server.URL, credentials)
			if (err != nil) != test.wantErr {
				t.Fatalf("fetch error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestCommonStatsAsOfComparesInstants(t *testing.T) {
	first := whamProfile{}
	first.Metadata.StatsAsOf = "2026-08-18T10:00:00+02:00"
	second := whamProfile{}
	second.Metadata.StatsAsOf = "2026-08-18T09:00:00Z"
	profiles := []profileFetchResult{
		{profile: first},
		{profile: second},
	}
	if got := commonStatsAsOf(profiles); got != "2026-08-18T08:00:00Z" {
		t.Fatalf("common stats timestamp = %q", got)
	}
	if statsDatesDiffer(profiles) {
		t.Fatal("equivalent timestamps were treated as different")
	}
}

func TestAggregateProfileStatsMergesActivity(t *testing.T) {
	pluginID := "browser@openai-bundled"
	first := profileFetchResult{}
	first.profile.Stats = whamProfileStats{
		LifetimeTokens: 100, TotalThreads: 10, LongestRunningTurnSec: 40,
		FastModeUsagePercentage: 20, TotalSkillsUsed: 4, UniqueSkillsUsed: 3,
		MostUsedReasoningEffort: "high", MostUsedReasoningEffortPercentage: 60,
		DailyUsageBuckets: []usageBucket{
			{StartDate: "2026-08-12", Tokens: 10},
			{StartDate: "2026-08-13", Tokens: 20},
		},
		TopInvocations: []profileInvocation{{Type: "plugin", PluginID: &pluginID, UsageCount: 2}},
	}
	second := profileFetchResult{}
	second.profile.Stats = whamProfileStats{
		LifetimeTokens: 200, TotalThreads: 30, LongestRunningTurnSec: 80,
		FastModeUsagePercentage: 60, TotalSkillsUsed: 6, UniqueSkillsUsed: 5,
		MostUsedReasoningEffort: "xhigh", MostUsedReasoningEffortPercentage: 70,
		DailyUsageBuckets: []usageBucket{
			{StartDate: "2026-08-13", Tokens: 30},
			{StartDate: "2026-08-14", Tokens: 40},
		},
		TopInvocations: []profileInvocation{{Type: "plugin", PluginID: &pluginID, UsageCount: 5}},
	}

	got := aggregateProfileStatsAt(
		[]profileFetchResult{first, second},
		time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	)
	if got.LifetimeTokens != 300 || got.TotalThreads != 40 {
		t.Fatalf("usage totals were not combined: %#v", got)
	}
	if got.PeakDailyTokens != 50 {
		t.Fatalf("peak should be based on merged daily totals, got %d", got.PeakDailyTokens)
	}
	if got.CurrentStreakDays != 3 || got.LongestStreakDays != 3 {
		t.Fatalf("unexpected merged streaks current=%d longest=%d", got.CurrentStreakDays, got.LongestStreakDays)
	}
	if got.LongestRunningTurnSec != 80 || got.FastModeUsagePercentage != 50 {
		t.Fatalf("unexpected duration or weighted fast-mode value: %#v", got)
	}
	if len(got.TopInvocations) != 1 || got.TopInvocations[0].UsageCount != 7 {
		t.Fatalf("invocations were not merged: %#v", got.TopInvocations)
	}
	if len(got.DailyUsageBuckets) != 3 || got.CumulativeDailyUsageBuckets[2].Tokens != 100 {
		t.Fatalf("daily activity was not merged: %#v", got.DailyUsageBuckets)
	}
}
