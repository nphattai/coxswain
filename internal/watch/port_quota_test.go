package watch

import (
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/wake"
)

// seqQuota serves one snapshot per poll (the last repeats), like firstmate's QUOTA_AXI_COUNT fake that answers poll 1,
// then poll 2.
type seqQuota struct {
	snaps   []QuotaSnapshot
	polls   int
	targets []QuotaTarget
}

func (q *seqQuota) Read() (QuotaSnapshot, error) {
	i := q.polls
	if i >= len(q.snaps) {
		i = len(q.snaps) - 1
	}
	q.polls++
	return q.snaps[i], nil
}
func (q *seqQuota) Targets() []QuotaTarget    { return q.targets }
func (*seqQuota) LowPercent() int             { return 10 }
func (*seqQuota) PollInterval() time.Duration { return 5 * time.Minute }

// The pre-B-19 probe methods, kept so this file also builds against the base sha for the red run.
func (*seqQuota) MinRunwaySeconds() int64       { return 24 * 3600 }
func (*seqQuota) HealthDebounce() time.Duration { return time.Hour }

func quotaSnap(r quota.Reading) QuotaSnapshot {
	r.Harness = "codex"
	if r.Source == "" {
		r.Source = quota.SourceQuotaAxi
	}
	return QuotaSnapshot{Merged: []quota.Reading{r}, Auto: []quota.Reading{r}}
}

var (
	qHealthy       = quota.Reading{Known: true, PercentRemaining: 80, Runway: quota.RunwayThroughReset}
	qAtThreshold   = quota.Reading{Known: true, PercentRemaining: 10, Runway: quota.RunwayThroughReset}
	qBelow         = quota.Reading{Known: true, PercentRemaining: 9, Runway: quota.RunwayThroughReset}
	qExhausted     = quota.Reading{Known: true, PercentRemaining: 0, Runway: quota.RunwayExhaustedNow, ResetsAt: "R1"}
	qUnknown       = quota.Reading{Known: false, Runway: quota.RunwayUnknown, Reason: "rate limited"}
	qUnknownExhaus = quota.Reading{Known: false, Runway: quota.RunwayExhaustedNow, ResetsAt: "R1"}
)

// pollQuota runs n quota passes one poll interval apart over q's targets (default: one codex worker) and returns the
// quota wakes raised after each poll, cumulatively.
func pollQuota(t *testing.T, q *seqQuota, n int) []int {
	t.Helper()
	if q.targets == nil {
		q.targets = []QuotaTarget{{Harness: "codex", Role: "worker", Story: "s1"}}
	}
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	w := &Watcher{EpicDir: lcEpic(t), Quota: q, Now: func() time.Time { return now }}
	var out []int
	for i := 0; i < n; i++ {
		if _, _, err := w.quotaPass(); err != nil {
			t.Fatal(err)
		}
		out = append(out, countWakes(t, w.EpicDir, wake.KindQuotaLow)+countWakes(t, w.EpicDir, wake.KindQuotaHealth))
		now = now.Add(6 * time.Minute)
	}
	return out
}

// TestPortProceventQuota translates the wake-condition cases of tests/fm-procevent-quota.test.sh: firstmate's quota
// watch fires only when effectivePercentRemaining drops below the threshold or the runway is exhausted_now, and an
// unknown reading keeps polling without a wake (B-19, B-55b).
// n/a: help/arm/retire/provider-id/threshold-parse/schema-5-6 validation cases (:134-:273) are the procevent runner and
// the quota-axi schema validator (cox reads through internal/quota); `status: error` stopping the watch is the runner's
// terminal record (cox backs the poll off instead, quota.go scheduleQuotaBackoff).
func TestPortProceventQuota(t *testing.T) {
	const s = "FM/fm-procevent-quota/"

	t.Run(s+"poll_fires_only_after_quota_drops_below_the_threshold", func(t *testing.T) {
		// fm: tests/fm-procevent-quota.test.sh:211@a8572f6
		got := pollQuota(t, &seqQuota{snaps: []QuotaSnapshot{quotaSnap(qAtThreshold), quotaSnap(qBelow)}}, 3)
		if got[0] != 0 {
			t.Errorf("quota at the threshold fired before dropping below it")
		}
		if got[1] != 1 || got[2] != 1 {
			t.Errorf("quota below the threshold: want one wake, got %v", got)
		}
	})

	t.Run(s+"poll_detects_exhausted_runway_under_unknown_headroom", func(t *testing.T) {
		// fm: tests/fm-procevent-quota.test.sh:162@a8572f6
		got := pollQuota(t, &seqQuota{snaps: []QuotaSnapshot{quotaSnap(qUnknownExhaus)}}, 1)
		if got[0] != 1 {
			t.Errorf("unknown headroom with exhausted runway did not wake as exhausted: %v", got)
		}
	})

	t.Run(s+"poll_preserves_provider_level_unknown_quota", func(t *testing.T) {
		// fm: tests/fm-procevent-quota.test.sh:279@a8572f6 (unknown first, then exhausted on poll 2)
		got := pollQuota(t, &seqQuota{snaps: []QuotaSnapshot{quotaSnap(qUnknown), quotaSnap(qUnknown), quotaSnap(qExhausted)}}, 3)
		if got[0] != 0 || got[1] != 0 {
			t.Errorf("an unknown reading woke the leader (quota_health): %v", got)
		}
		if got[2] != 1 {
			t.Errorf("unknown quota did not continue to exhaustion: %v", got)
		}
	})

	t.Run(s+"poll_preserves_unknown_headroom_under_known_semantics", func(t *testing.T) {
		// fm: tests/fm-procevent-quota.test.sh:285@a8572f6 (a source with no machine-readable headroom, cox's Pi
		// SourceNone, B-55b: many polls, never a wake)
		none := qUnknown
		none.Source = quota.SourceNone
		got := pollQuota(t, &seqQuota{snaps: []QuotaSnapshot{quotaSnap(none)}}, 6)
		if got[5] != 0 {
			t.Errorf("an unknown-headroom source woke %d time(s) over 6 polls", got[5])
		}
	})
}

// B-25: the leader-role target is observe-only; its low or exhausted quota never wakes the leader. A worker on the same
// harness still does.
func TestQuotaNeverWakesForTheLeaderTarget(t *testing.T) {
	leader := []QuotaTarget{{Harness: "codex", Role: "leader"}}
	if got := pollQuota(t, &seqQuota{snaps: []QuotaSnapshot{quotaSnap(qBelow)}, targets: leader}, 2); got[1] != 0 {
		t.Errorf("a leader-role target raised %d quota wake(s)", got[1])
	}
	both := append(leader, QuotaTarget{Harness: "codex", Role: "worker", Story: "s1"})
	if got := pollQuota(t, &seqQuota{snaps: []QuotaSnapshot{quotaSnap(qExhausted)}, targets: both}, 1); got[0] != 1 {
		t.Errorf("a worker target beside the leader: want one wake, got %d", got[0])
	}
}

// A projected runway under min_runway is not a firstmate wake condition, and a healthy reading never wakes.
func TestQuotaProjectedRunwayDoesNotWake(t *testing.T) {
	proj := quota.Reading{Known: true, PercentRemaining: 50, Runway: quota.RunwayProjected, UsableRunwaySeconds: 3600}
	if got := pollQuota(t, &seqQuota{snaps: []QuotaSnapshot{quotaSnap(proj), quotaSnap(qHealthy)}}, 2); got[1] != 0 {
		t.Errorf("projected/healthy readings raised %d wake(s)", got[1])
	}
}
