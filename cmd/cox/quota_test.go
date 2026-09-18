package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/state"
)

// A worker (a real COX_STORY) cannot set or unset manual quota; only the leader may.
func TestQuotaSetRefusesWorker(t *testing.T) {
	epic := t.TempDir()
	t.Setenv("COX_STORY", "m11")
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if code := quotaSet([]string{"claude", "15", "--until", until, "--epic", epic}); code == 0 {
		t.Fatalf("worker set should be refused")
	}
	if rs, _ := (&quota.Manual{EpicDir: epic}).Read(context.Background()); len(rs) != 0 {
		t.Fatalf("worker must not have written an entry: %+v", rs)
	}
}

// The leader sets a manual entry, which is written and recorded as a quota_manual_set event; unset clears it.
func TestQuotaSetUnsetLeader(t *testing.T) {
	epic := t.TempDir()
	t.Setenv("COX_STORY", "_leader")
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if code := quotaSet([]string{"claude", "15%", "--until", until, "--epic", epic}); code != 0 {
		t.Fatalf("leader set failed: code %d", code)
	}
	rs, _ := (&quota.Manual{EpicDir: epic}).Read(context.Background())
	if len(rs) != 1 || !rs[0].Known || rs[0].PercentRemaining != 15 {
		t.Fatalf("leader entry not written: %+v", rs)
	}
	if !hasQuotaManualEvent(t, epic) {
		t.Fatalf("quota_manual_set event not recorded")
	}
	if code := quotaUnset([]string{"claude", "--epic", epic}); code != 0 {
		t.Fatalf("leader unset failed: code %d", code)
	}
	if rs, _ := (&quota.Manual{EpicDir: epic}).Read(context.Background()); len(rs) != 0 {
		t.Fatalf("entry not cleared: %+v", rs)
	}
}

// A --until in the past is refused.
func TestQuotaSetRejectsPastUntil(t *testing.T) {
	epic := t.TempDir()
	t.Setenv("COX_STORY", "_leader")
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if code := quotaSet([]string{"claude", "15", "--until", past, "--epic", epic}); code == 0 {
		t.Fatalf("past until should be refused")
	}
}

// The dispatch gate blocks exhausted_now (unless forced), warns on a known low, and passes otherwise and on unknown.
func TestQuotaGateDecision(t *testing.T) {
	exhausted := quota.Reading{Known: true, Runway: quota.RunwayExhaustedNow}
	if block, _ := quotaGateDecision(exhausted, 10, false); !block {
		t.Fatalf("exhausted_now must block")
	}
	if block, _ := quotaGateDecision(exhausted, 10, true); block {
		t.Fatalf("--force-quota must override exhausted_now")
	}
	if block, warn := quotaGateDecision(quota.Reading{Known: true, PercentRemaining: 5, Runway: quota.RunwayUnknown}, 10, false); block || !warn {
		t.Fatalf("low-but-not-exhausted must warn, not block: block=%v warn=%v", block, warn)
	}
	if block, warn := quotaGateDecision(quota.Reading{Known: false, Runway: quota.RunwayUnknown}, 10, false); block || warn {
		t.Fatalf("unknown must neither block nor warn")
	}
	if block, warn := quotaGateDecision(quota.Reading{Known: true, PercentRemaining: 80, Runway: quota.RunwayThroughReset}, 10, false); block || warn {
		t.Fatalf("healthy must pass silently")
	}
}

func TestQuotaTailKeepsUnknownAndShowsRecentLastKnown(t *testing.T) {
	unknown := quota.Reading{Known: false, Reason: "rate limited"}
	known := quota.Reading{Known: true, PercentRemaining: 42, ObservedAt: "2026-09-16T08:00:00Z"}
	got := quotaTail(unknown, known)
	if !strings.Contains(got, "rate limited") || !strings.Contains(got, "last known 42% at 2026-09-16T08:00:00Z") {
		t.Fatalf("unknown context missing: %q", got)
	}
	if got := quotaPercent(unknown); got != "unknown" {
		t.Fatalf("last known context must not replace unknown, got %q", got)
	}
}

func hasQuotaManualEvent(t *testing.T, epic string) bool {
	t.Helper()
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	for _, e := range events {
		if e.Type == state.QuotaManualSet {
			return true
		}
	}
	return false
}
