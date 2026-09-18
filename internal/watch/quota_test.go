package watch

import (
	"os"
	"path/filepath"
	"strconv"
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
func (fakeQuota) MinRunwaySeconds() int64        { return 24 * 3600 }
func (fakeQuota) PollInterval() time.Duration    { return 5 * time.Minute }
func (fakeQuota) HealthDebounce() time.Duration  { return 60 * time.Minute }

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
	urgent, _, ok := classifyQuotaLow(r, 10, 24*3600)
	return urgent, ok
}

func TestClassifyQuotaLow(t *testing.T) {
	if u, ok := classify(t, quota.Reading{Runway: quota.RunwayExhaustedNow}); !ok || !u {
		t.Fatalf("exhausted_now must be urgent quota_low")
	}
	if u, ok := classify(t, quota.Reading{Known: true, PercentRemaining: 5, Runway: quota.RunwayUnknown}); !ok || !u {
		t.Fatalf("percent below low must be urgent quota_low")
	}
	if u, ok := classify(t, quota.Reading{Known: true, PercentRemaining: 50, Runway: quota.RunwayProjected, UsableRunwaySeconds: 3600}); !ok || u {
		t.Fatalf("projected within horizon must be routine quota_low")
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
	tgt := QuotaTarget{Harness: "claude", Role: "leader"}
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

// quota_health requires two consecutive unknown observations, including on a cold start. A Known reading resets the
// streak but does not bypass the per-harness debounce when the source flaps back to unknown.
func TestQuotaHealthConsecutiveUnknownAndDebounce(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	w := &Watcher{EpicDir: t.TempDir(), Quota: fakeQuota{}, Now: func() time.Time { return now }}
	tgt := QuotaTarget{Harness: "claude", Role: "leader"}
	unknown := QuotaSnapshot{Auto: []quota.Reading{{Harness: "claude", Known: false, Runway: quota.RunwayUnknown, Reason: "rate limited"}}, Merged: []quota.Reading{{Harness: "claude", Known: false, Runway: quota.RunwayUnknown}}}
	known := QuotaSnapshot{Auto: []quota.Reading{{Harness: "claude", Known: true, PercentRemaining: 80, Runway: quota.RunwayThroughReset}}, Merged: []quota.Reading{{Harness: "claude", Known: true, PercentRemaining: 80, Runway: quota.RunwayThroughReset}}}

	// A first-ever unknown starts the streak but does not wake until it persists for a second poll.
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	if n := countWakes(t, w.EpicDir, wake.KindQuotaHealth); n != 0 {
		t.Fatalf("first unknown must not wake, got %d", n)
	}
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	if n := countWakes(t, w.EpicDir, wake.KindQuotaHealth); n != 1 {
		t.Fatalf("second consecutive cold-start unknown must wake once, got %d", n)
	}

	// Known resets the streak. Two more unknowns inside the debounce window do not open a new alert episode.
	_, _, _ = w.quotaWakesFor(tgt, known)
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	if n := countWakes(t, w.EpicDir, wake.KindQuotaHealth); n != 1 {
		t.Fatalf("Known/unknown flap inside debounce must not wake, got %d", n)
	}

	// Once the debounce expires, a new Known then persistent unknown episode may wake again.
	now = now.Add(61 * time.Minute)
	_, _, _ = w.quotaWakesFor(tgt, known)
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	if n := countWakes(t, w.EpicDir, wake.KindQuotaHealth); n != 2 {
		t.Fatalf("persistent unknown after debounce must wake again, got %d", n)
	}
}

func TestQuotaHealthCountsPollsNotDuplicateTargets(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	unknown := quota.Reading{Harness: "claude", Known: false, Runway: quota.RunwayUnknown, Reason: "rate limited"}
	probe := fakeQuota{
		snap: QuotaSnapshot{Auto: []quota.Reading{unknown}, Merged: []quota.Reading{unknown}},
		targets: []QuotaTarget{
			{Harness: "claude", Role: "leader"},
			{Harness: "claude", Role: "worker", Story: "s1"},
		},
	}
	w := &Watcher{EpicDir: t.TempDir(), Quota: probe, Now: func() time.Time { return now }}
	if _, _, err := w.quotaPass(); err != nil {
		t.Fatal(err)
	}
	if n := countWakes(t, w.EpicDir, wake.KindQuotaHealth); n != 0 {
		t.Fatalf("duplicate targets must count as one first poll, got %d wakes", n)
	}
	now = now.Add(6 * time.Minute)
	if _, _, err := w.quotaPass(); err != nil {
		t.Fatal(err)
	}
	if n := countWakes(t, w.EpicDir, wake.KindQuotaHealth); n != 1 {
		t.Fatalf("second poll must wake exactly once per harness, got %d", n)
	}
}

func TestQuotaHealthMigratesLegacyFiredMarkerIntoDebounce(t *testing.T) {
	now := time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC)
	w := &Watcher{EpicDir: t.TempDir(), Quota: fakeQuota{}, Now: func() time.Time { return now }}
	tgt := QuotaTarget{Harness: "claude", Role: "leader"}
	unknown := QuotaSnapshot{Auto: []quota.Reading{{Harness: "claude", Known: false, Runway: quota.RunwayUnknown, Reason: "rate limited"}}}

	w.writeWatch("quotaknown", "claude", "0")
	w.markWatchFile("quotahealthfired", "claude")
	legacy := filepath.Join(w.quotaDir(), "quotahealthfired-claude")
	firedAt := now.Add(-10 * time.Minute)
	if err := os.Chtimes(legacy, firedAt, firedAt); err != nil {
		t.Fatal(err)
	}
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	_, _, _ = w.quotaWakesFor(tgt, unknown)
	if n := countWakes(t, w.EpicDir, wake.KindQuotaHealth); n != 0 {
		t.Fatalf("legacy wake inside debounce must not fire again, got %d", n)
	}
	if got := w.readWatch("quotahealthlast", "claude"); got != strconv.FormatInt(firedAt.Unix(), 10) {
		t.Fatalf("legacy wake timestamp not migrated: got %q", got)
	}
}

// The cadence gate schedules the next poll and refuses a second poll until it passes; backoff pushes it further out.
func TestQuotaCadence(t *testing.T) {
	w := &Watcher{EpicDir: t.TempDir(), Quota: fakeQuota{}}
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
