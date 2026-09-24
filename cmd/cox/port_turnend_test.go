//go:build port

package main

// Port tests, wave 1 (story cox-supervision-port-turnend): firstmate's turn-end guard, stale banner, watch checkpoint,
// watcher lock and watch arm suites translated case by case against cox's hooks (DESIGN "Translation contract").
// Source: /Users/tainguyen/Work/henrylab/references/firstmate pinned at 1e0e773, read only. Every case is a t.Run named
// TestFM/<suite>/<case> carrying its `// fm:` citation; a case about a firstmate-only surface is an `// n/a:` comment
// in firstmate order and a row in reports/cox-supervision-port-turnend.md. Red is the deliverable: nothing here
// changes cox, and a gap calls notImplemented rather than t.Skip.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/state"
)

// notImplemented fails the case naming the cox mechanism firstmate pins and cox lacks (contract rule 3).
func notImplemented(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("cox gap: %s", mechanism)
}

// fmEpic makes a temp epic dir with a .cox control tree and one working story per name. With no names it has no open
// story (firstmate's "nothing in flight").
func fmEpic(t *testing.T, stories ...string) string {
	t.Helper()
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, controlDir, "watch"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range stories {
		fmOpen(t, epic, s)
	}
	return epic
}

// fmOpen dispatches story s into working (firstmate: state/<task>.meta appears).
func fmOpen(t *testing.T, epic, s string) {
	t.Helper()
	if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: s, Attempt: 1, Actor: state.Leader, From: state.Submitted, To: state.Working, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
}

// fmBeacon writes watch/lasttick aged by age (firstmate: touch state/.last-watcher-beat).
func fmBeacon(t *testing.T, epic string, age time.Duration) {
	t.Helper()
	p := filepath.Join(epic, controlDir, "watch", "lasttick")
	at := time.Now().Add(-age)
	if err := os.WriteFile(p, []byte(at.UTC().Format(time.RFC3339)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// fmWatchPid records pid as the epic's watcher (firstmate: record_watcher_lock).
func fmWatchPid(t *testing.T, epic string, pid int) {
	t.Helper()
	if err := os.WriteFile(watchPidPath(epic), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fmDeadPid returns the pid of a process that has exited and been reaped (firstmate: nonexistent_pid).
func fmDeadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// fmLiveChild starts a long sleep standing in for a live foreign process (firstmate: `sleep 60 &`); cleanup kills it.
func fmLiveChild(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// fmGuardResult is one turn-boundary guard run (cox hook stop-rewake, guard layer only).
type fmGuardResult struct {
	code     int
	proceed  bool // true: the guard let the turn go on to the idle wait (firstmate: silent allow)
	out      string
	launched []string
}

// blocked reports firstmate's "exit 2": the guard reopened the turn.
func (r fmGuardResult) blocked() bool { return !r.proceed && r.code == 2 }

// fmLaunchRefused stands in for a watcher that cannot be restarted (a wedged pid, a launch error).
func fmLaunchRefused(string) error { return errWatchRefused }

// fmGuard runs the turn-boundary guard for epic exactly as hookStopRewake computes it (guardEpics over the explicit
// epic), with launch standing in for launchWatcher so no real `cox watch` is spawned. blocks is the per-terminal budget
// file ("" = no ORCA_TERMINAL_HANDLE). A nil launch fails the case if the guard tries to restart.
func fmGuard(t *testing.T, epic string, launch func(string) error, blocks string) fmGuardResult {
	t.Helper()
	var out bytes.Buffer
	r := fmGuardResult{}
	cfg := rewakeCfg{
		epics: []string{epic}, guardEpics: guardEpics(epic), out: &out, stdout: &out, sleep: noSleep, blocksPath: blocks,
		launch: func(ep string) error {
			r.launched = append(r.launched, ep)
			if launch == nil {
				t.Errorf("guard tried to restart the watcher for %s", ep)
				return errWatchRefused
			}
			return launch(ep)
		},
	}
	r.code, r.proceed = cfg.guardWatchers()
	r.out = out.String()
	return r
}

// fmBlocks is a fresh per-terminal block-budget file path.
func fmBlocks(t *testing.T) string { return filepath.Join(t.TempDir(), "cox-rewake-test.blocks") }

// TestFM is the translated matrix. Suites run in the order the story lists them.
func TestFM(t *testing.T) {
	t.Run("fm-turnend-guard", fmTurnendGuard)
	t.Run("fm-guard-stale-banner", fmGuardStaleBanner)
	t.Run("fm-watch-checkpoint", fmWatchCheckpoint)
	t.Run("fm-watcher-lock", fmWatcherLock)
	t.Run("fm-watch-arm", fmWatchArm)
	t.Run("doc-turnend-guard", fmDocTurnendGuard)
	t.Run("doc-watcher-continuity", fmDocWatcherContinuity)
}

func fmTurnendGuard(t *testing.T)         {}
func fmGuardStaleBanner(t *testing.T)     {}
func fmWatchCheckpoint(t *testing.T)      {}
func fmWatcherLock(t *testing.T)          {}
func fmWatchArm(t *testing.T)             {}
func fmDocTurnendGuard(t *testing.T)      {}
func fmDocWatcherContinuity(t *testing.T) {}
