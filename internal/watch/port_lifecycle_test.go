//go:build port

package watch

// Port tests, wave 1 (story cox-supervision-port-turnend): the watcher-lifecycle half of firstmate's watcher lock,
// watch arm and recovery loop suites, translated case by case against the watcher run loop (Run, evictReason,
// markTick, watch/lasttick, watch/log) under the DESIGN translation contract. Firstmate is pinned at 1e0e773, read
// only. Every case is a t.Run named TestFMLifecycle/<suite>/<case> with its `// fm:` citation (`-run 'FM/<suite>'`
// selects it); a firstmate-only case is an `// n/a:` comment and a report row.
//
// The top-level name and the gap helper carry a Lifecycle/LC suffix because the triage story ports into this same
// package with its own TestFM-shaped matrix and notImplemented helper; the suffix keeps the two files compiling
// together without a shared fixture (contract rule 4).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
)

// notImplementedLC fails the case naming the cox mechanism firstmate pins and cox lacks (contract rule 3).
func notImplementedLC(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("cox gap: %s", mechanism)
}

// lcEpic makes a temp epic dir with a .cox control tree.
func lcEpic(t *testing.T) string {
	t.Helper()
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, state.ControlDir, "watch"), 0o755); err != nil {
		t.Fatal(err)
	}
	return epic
}

// lcWatcher is a watcher over a fake backend for epic.
func lcWatcher(epic string) *Watcher { return &Watcher{EpicDir: epic, Backend: fake.New()} }

// lcRun runs w.Run with a 10ms poll in the background. stop ends it; done closes when Run returns.
func lcRun(w *Watcher) (stop chan struct{}, done chan struct{}) {
	stop, done = make(chan struct{}), make(chan struct{})
	go func() { w.Run(stop, 10*time.Millisecond); close(done) }()
	return stop, done
}

// lcExited reports whether done closed within d (2s bounds every red case so it fails fast instead of hanging).
func lcExited(done chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestFMLifecycle is the translated matrix for the watcher run loop.
func TestFMLifecycle(t *testing.T) {
	t.Run("fm-watcher-lock", lcWatcherLock)
	t.Run("fm-watch-arm", lcWatchArm)
	t.Run("fm-watch-recovery-loop", lcWatchRecoveryLoop)
	t.Run("doc-watcher-continuity", lcDocWatcherContinuity)
}

func lcWatcherLock(t *testing.T)          {}
func lcWatchArm(t *testing.T)             {}
func lcWatchRecoveryLoop(t *testing.T)    {}
func lcDocWatcherContinuity(t *testing.T) {}
