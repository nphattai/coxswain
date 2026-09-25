package watch

import (
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/wake"
)

type fakeQuota struct {
	snap    QuotaSnapshot
	targets []QuotaTarget
}

func (f fakeQuota) Read() (QuotaSnapshot, error) { return f.snap, nil }
func (f fakeQuota) Targets() []QuotaTarget       { return f.targets }
func (fakeQuota) LowPercent() int                { return 10 }
func (fakeQuota) PollInterval() time.Duration    { return 5 * time.Minute }

func countWakes(t *testing.T, epic string, kind wake.Kind) int {
	t.Helper()
	wakes, err := wake.Load(epic)
	if err != nil {
		t.Fatalf("load wakes: %v", err)
	}
	n := 0
	for _, w := range wakes {
		if w.Kind == kind {
			n++
		}
	}
	return n
}

func classify(t *testing.T, r quota.Reading) (bool, bool) {
	t.Helper()
	urgent, _, ok := classifyQuotaLow(r, 10)
	return urgent, ok
}

func TestClassifyQuotaLow(t *testing.T) {
	if u, ok := classify(t, quota.Reading{Runway: quota.RunwayExhaustedNow}); !ok || !u {
		t.Fatalf("exhausted_now must be urgent quota_low")
	}
	if u, ok := classify(t, quota.Reading{Known: true, PercentRemaining: 5, Runway: quota.RunwayUnknown}); !ok || !u {
		t.Fatalf("percent below low must be urgent quota_low")
	}
	if _, ok := classify(t, quota.Reading{Known: true, PercentRemaining: 50, Runway: quota.RunwayProjected, UsableRunwaySeconds: 3600}); ok {
		t.Fatalf("a projected runway is not a firstmate wake condition (only <threshold or exhausted_now)")
	}
	if _, ok := classify(t, quota.Reading{Known: true, PercentRemaining: 50, Runway: quota.RunwayProjected, UsableRunwaySeconds: 200000}); ok {
		t.Fatalf("projected with ample runway must not fire")
	}
	if _, ok := classify(t, quota.Reading{Known: true, PercentRemaining: 50, Runway: quota.RunwayThroughReset}); ok {
		t.Fatalf("healthy through_reset must not fire")
	}
}

// quota_low fires once per (harness, resetsAt) and re-arms on a new reset window.
func TestQuotaLowDedup(t *testing.T) {
	w := &Watcher{EpicDir: t.TempDir(), Quota: fakeQuota{}}
	tgt := QuotaTarget{Harness: "claude", Role: "worker", Story: "s1"}
	snap := func(reset string) QuotaSnapshot {
		r := quota.Reading{Harness: "claude", Known: true, Runway: quota.RunwayExhaustedNow, ResetsAt: reset, Source: quota.SourceQuotaAxi}
		return QuotaSnapshot{Merged: []quota.Reading{r}, Auto: []quota.Reading{r}}
	}
	if _, urgent, _ := w.quotaWakesFor(tgt, snap("R1")); !urgent {
		t.Fatalf("first exhausted must be urgent")
	}
	if _, _, _ = w.quotaWakesFor(tgt, snap("R1")); countWakes(t, w.EpicDir, wake.KindQuotaLow) != 1 {
		t.Fatalf("same reset must not refire: got %d", countWakes(t, w.EpicDir, wake.KindQuotaLow))
	}
	_, _, _ = w.quotaWakesFor(tgt, snap("R2"))
	if countWakes(t, w.EpicDir, wake.KindQuotaLow) != 2 {
		t.Fatalf("new reset must refire: got %d", countWakes(t, w.EpicDir, wake.KindQuotaLow))
	}
}

// The cadence gate schedules the next poll and refuses a second poll until it passes; backoff pushes it further out.
func TestQuotaCadence(t *testing.T) {
	w := &Watcher{EpicDir: lcEpic(t), Quota: fakeQuota{}}
	now := time.Now()
	if !w.quotaDue(now) {
		t.Fatalf("first poll must be due")
	}
	w.scheduleQuotaNext(now, 5*time.Minute)
	if w.quotaDue(now.Add(time.Minute)) {
		t.Fatalf("within interval must not be due")
	}
	if !w.quotaDue(now.Add(6 * time.Minute)) {
		t.Fatalf("past interval must be due")
	}
	w.scheduleQuotaBackoff(now)
	if w.quotaDue(now.Add(6 * time.Minute)) {
		t.Fatalf("backoff should push next out past the base interval")
	}
}
