package watch

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/wake"
)

// Quota pass windows (DESIGN / brief M11): poll every 5 minutes (through the 60s projection cache) with a cross-process
// lock, +-30s jitter so watchers do not align, and exponential backoff to 30 minutes on a read error.
const (
	defaultQuotaPoll = 5 * time.Minute
	quotaJitter      = 30 * time.Second
	quotaMaxBackoff  = 30 * time.Minute
)

// QuotaTarget is one (harness, model) the watcher checks quota for, tagged by role/story for the wake note. The leader
// harness (policy harness.leader.default) and every working story's harness/model are targets.
type QuotaTarget struct {
	Harness string
	Model   string
	Role    string // "leader" | "worker"
	Story   string // the working story id for a worker target; "" for the leader
}

// QuotaSnapshot is one quota read. quota_low is evaluated over Merged (a manual low fires too). Auto (the automatic-only
// readings) is kept for callers that report the source; the watcher never wakes on it.
type QuotaSnapshot struct {
	Merged     []quota.Reading
	Auto       []quota.Reading
	CalledLive bool
}

// QuotaProbe supplies the quota pass with data and thresholds it cannot compute itself (policy, story frontmatter, the
// merged read through the cache). cmd/cox implements it. A nil Watcher.Quota disables the pass entirely.
type QuotaProbe interface {
	Read() (QuotaSnapshot, error)
	Targets() []QuotaTarget
	LowPercent() int
	PollInterval() time.Duration
}

// quotaPass reads quota at most once per poll interval (under a cross-process lock), counts the call, and raises a
// quota_low wake for each working story's harness whose quota is below low_percent or exhausted now, the only two
// conditions firstmate's quota watch fires on (bin/fm-procevent-quota.sh:12-15@a8572f6). An unknown or unreadable
// source never wakes (B-19, B-55b: firstmate keeps polling), and the leader-role target is never woken for (B-25: the
// leader's own account is observe-only). It returns the wakes appended and whether an urgent one was raised.
func (w *Watcher) quotaPass() (int, bool, error) {
	if w.Quota == nil {
		return 0, false, nil
	}
	now := w.now()
	unlock, ok := w.quotaLock()
	if !ok {
		return 0, false, nil // another watcher holds the quota lock this tick
	}
	defer unlock()
	if !w.quotaDue(now) {
		return 0, false, nil
	}

	snap, err := w.Quota.Read()
	w.logQuotaCall(now, snap.CalledLive, err)
	if err != nil {
		w.scheduleQuotaBackoff(now)
		return 0, false, nil
	}
	w.scheduleQuotaNext(now, w.quotaPoll())

	appended := 0
	urgent := false
	for _, t := range w.Quota.Targets() {
		if t.Role == "leader" {
			continue
		}
		n, u, appErr := w.quotaWakesFor(t, snap)
		if appErr != nil {
			return appended, urgent, appErr
		}
		appended += n
		urgent = urgent || u
	}
	return appended, urgent, nil
}

func (w *Watcher) quotaPoll() time.Duration {
	if w.Quota != nil {
		if p := w.Quota.PollInterval(); p > 0 {
			return p
		}
	}
	return defaultQuotaPoll
}

// quotaWakesFor raises quota_low once per harness+resetsAt for one target.
func (w *Watcher) quotaWakesFor(t QuotaTarget, snap QuotaSnapshot) (int, bool, error) {
	appended := 0
	urgent := false
	merged := quota.Pick(snap.Merged, t.Harness, t.Model)
	urg, note, ok := classifyQuotaLow(merged, w.Quota.LowPercent())
	if ok && w.quotaLowShouldFire(t.Harness, merged.ResetsAt) {
		if _, err := wake.Append(w.EpicDir, wake.Wake{
			Epic:  filepath.Base(w.EpicDir),
			Story: quotaStory(t),
			Kind:  wake.KindQuotaLow,
			Note:  fmt.Sprintf("%s (%s): %s", quotaScope(t), t.Role, note),
			Evidence: map[string]any{
				"harness": t.Harness, "model": t.Model, "role": t.Role,
				"percent": merged.PercentRemaining, "runway": merged.Runway, "source": merged.Source,
			},
		}); err != nil {
			return appended, urgent, err
		}
		appended++
		urgent = urgent || urg
		w.quotaLowMark(t.Harness, merged.ResetsAt)
	}
	return appended, urgent, nil
}

// classifyQuotaLow decides whether a reading warrants a quota_low wake (firstmate condition_status,
// bin/fm-procevent-quota.sh:116@a8572f6): exhausted_now (even under unknown headroom), or a known percent strictly below
// low_percent (where a manual low fires, a manual reading being percent-only). Both are urgent. A projected runway, an
// unknown reading or a healthy one never wakes.
func classifyQuotaLow(r quota.Reading, lowPercent int) (urgent bool, note string, ok bool) {
	if r.Runway == quota.RunwayExhaustedNow {
		return true, "exhausted now", true
	}
	if r.Known && r.PercentRemaining < lowPercent {
		return true, fmt.Sprintf("%d%% remaining, below low_percent %d", r.PercentRemaining, lowPercent), true
	}
	return false, "", false
}

func quotaScope(t QuotaTarget) string {
	if t.Model != "" {
		return t.Harness + "/" + t.Model
	}
	return t.Harness
}

// quotaStory is the wake's story field: the working story for a worker target, else the reserved leader story id so a
// leader-harness quota wake attaches to a real story key.
func quotaStory(t QuotaTarget) string {
	if t.Story != "" {
		return t.Story
	}
	return "_leader"
}

// --- cadence, lock, telemetry, dedup state (all under <epic>/.cox/watch/quota) ---

func (w *Watcher) quotaDir() string { return filepath.Join(w.watchDir(), "quota") }

// quotaLock takes a non-blocking cross-process exclusive lock so only one watcher polls quota per tick. ok=false means
// another process holds it (this watcher skips the pass this tick).
func (w *Watcher) quotaLock() (func(), bool) {
	if err := os.MkdirAll(w.quotaDir(), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(filepath.Join(w.quotaDir(), "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true
}

// quotaDue reports whether the next-run time has passed (or is unset). Caller holds the quota lock.
func (w *Watcher) quotaDue(now time.Time) bool {
	sec, err := strconv.ParseInt(w.readWatch("quota", "next"), 10, 64)
	if err != nil {
		return true
	}
	return !now.Before(time.Unix(sec, 0))
}

// scheduleQuotaNext sets the next poll to now + interval with +-jitter and clears the backoff.
func (w *Watcher) scheduleQuotaNext(now time.Time, interval time.Duration) {
	jitter := time.Duration(rand.Int63n(int64(2*quotaJitter+1))) - quotaJitter
	w.writeWatch("quota", "next", strconv.FormatInt(now.Add(interval+jitter).Unix(), 10))
	w.clearWatchFile("quota", "backoff")
}

// scheduleQuotaBackoff sets the next poll after a read error, doubling the backoff up to 30 minutes.
func (w *Watcher) scheduleQuotaBackoff(now time.Time) {
	prev := w.quotaPoll()
	if s, err := strconv.ParseInt(w.readWatch("quota", "backoff"), 10, 64); err == nil && s > 0 {
		prev = time.Duration(s) * time.Second
	}
	next := prev * 2
	if next > quotaMaxBackoff {
		next = quotaMaxBackoff
	}
	w.writeWatch("quota", "backoff", strconv.FormatInt(int64(next/time.Second), 10))
	w.writeWatch("quota", "next", strconv.FormatInt(now.Add(next).Unix(), 10))
}

// logQuotaCall appends one telemetry line to <epic>/.cox/quota-calls.log for every poll, so provider call volume is
// measurable before the interval is tuned (an accepted claim: measure before tuning).
func (w *Watcher) logQuotaCall(now time.Time, calledLive bool, err error) {
	line := fmt.Sprintf("%s live=%t", now.UTC().Format(time.RFC3339), calledLive)
	if err != nil {
		line += " err=" + strings.ReplaceAll(err.Error(), "\n", " ")
	}
	path := filepath.Join(w.EpicDir, ".cox", "quota-calls.log")
	if f, e := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); e == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
}

// quotaLowShouldFire reports whether a quota_low has not yet fired for this (harness, resetsAt) window.
func (w *Watcher) quotaLowShouldFire(harness, resetsAt string) bool {
	return w.readWatch("quotalow", harness) != quotaLowKey(resetsAt)
}

// quotaLowMark records that a quota_low fired for this (harness, resetsAt) window.
func (w *Watcher) quotaLowMark(harness, resetsAt string) {
	w.writeWatch("quotalow", harness, quotaLowKey(resetsAt))
}

func quotaLowKey(resetsAt string) string {
	if resetsAt == "" {
		return "no-reset"
	}
	return resetsAt
}

// --- small watch-file helpers under quotaDir ---

func (w *Watcher) readWatch(sub, key string) string {
	b, err := os.ReadFile(filepath.Join(w.quotaDir(), sub+"-"+key))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (w *Watcher) writeWatch(sub, key, val string) {
	if err := os.MkdirAll(w.quotaDir(), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(w.quotaDir(), sub+"-"+key), []byte(val), 0o644)
}

func (w *Watcher) clearWatchFile(sub, key string) {
	_ = os.Remove(filepath.Join(w.quotaDir(), sub+"-"+key))
}
