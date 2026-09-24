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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/hooks"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
	"github.com/nphattai/coxswain/internal/watch"
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

// fmRepairLine is cox's exact repair instruction for a blocked epic (firstmate's REQUIRED_REASON).
func fmRepairLine(epic string) string {
	return "Watcher for " + filepath.Base(epic) + " is not alive; run: cox watch --epic " + epic + " --replace"
}

// fmWantBlock asserts firstmate's "expect_code 2 + REQUIRED_REASON": the guard reopened the turn with the repair line.
func fmWantBlock(t *testing.T, r fmGuardResult, epic, why string) {
	t.Helper()
	if !r.blocked() {
		t.Fatalf("%s: want a block (reopen, exit 2), got code=%d proceed=%v out=%q", why, r.code, r.proceed, r.out)
	}
	if !strings.Contains(r.out, fmRepairLine(epic)) {
		t.Fatalf("%s: block must carry the exact repair line %q, got %q", why, fmRepairLine(epic), r.out)
	}
}

// fmWantSilent asserts firstmate's "exit 0, no output": the guard let the turn go on and said nothing.
func fmWantSilent(t *testing.T, r fmGuardResult, why string) {
	t.Helper()
	if !r.proceed || r.out != "" {
		t.Fatalf("%s: want a silent allow, got code=%d proceed=%v out=%q", why, r.code, r.proceed, r.out)
	}
}

// fmWorkspace makes a cox workspace with one epic (proj/epics/<slug>) holding the given working stories and returns
// the workspace root and the epic dir.
func fmWorkspace(t *testing.T, slug string, stories ...string) (ws, epic string) {
	t.Helper()
	ws = t.TempDir()
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"repos":[{"alias":"a","path":"/x","production":"main"}]}`)
	epic = filepath.Join(ws, "proj", "epics", slug)
	if err := os.MkdirAll(filepath.Join(epic, controlDir, "watch"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range stories {
		fmOpen(t, epic, s)
	}
	return ws, epic
}

// fmStopHook runs the real `cox hook stop-rewake --epic <epic>` entry point (hookStopRewake) as the claude leader
// terminal "fm-leader": a per-test TMPDIR holds the single-waiter lock and the block budget, the watcher binary is a
// stub that exits at once (so a restart always fails, never spawning cox), and REWAKE_MAX_WAIT=0 so a guard that lets
// the turn go on returns without waiting. lockPid > 0 pre-writes the single-waiter lock naming that pid; blocks > 0
// pre-writes the spent budget. stdin is /dev/null. Returns the exit code and everything written to stderr+stdout.
func fmStopHook(t *testing.T, epic string, lockPid, blocks int) (int, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("ORCA_TERMINAL_HANDLE", "fm-leader")
	t.Setenv("REWAKE_MAX_WAIT", "0")
	stubWatcher(t, 2*time.Second, "exit 1")
	if lockPid > 0 {
		mustWrite(t, filepath.Join(tmp, "cox-rewake-fm-leader.lock"), strconv.Itoa(lockPid))
	}
	if blocks > 0 {
		mustWrite(t, rewakeBlocksPath("fm-leader"), strconv.Itoa(blocks))
	}
	return fmCapture(t, func() int { return hookStopRewake(epic, "claude", true) })
}

// fmCapture runs fn with os.Stdin at /dev/null and os.Stdout/os.Stderr redirected to a temp file, returning fn's
// result and the captured text.
func fmCapture(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	inPrev, outPrev, errPrev := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = null, f, f
	code := func() int {
		defer func() { os.Stdin, os.Stdout, os.Stderr = inPrev, outPrev, errPrev }()
		return fn()
	}()
	_ = null.Close()
	_ = f.Close()
	b, _ := os.ReadFile(f.Name())
	return code, string(b)
}

// fmBlockCount reads the per-terminal block budget file (0 when absent).
func fmBlockCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// fmHealthy makes epic's watcher live (this test process holds watch.pid) with a fresh beacon.
func fmHealthy(t *testing.T, epic string) {
	t.Helper()
	fmWatchPid(t, epic, os.Getpid())
	fmBeacon(t, epic, 0)
}

// fmDead makes epic's watcher dead (a reaped pid) with no beacon.
func fmDead(t *testing.T, epic string) {
	t.Helper()
	fmWatchPid(t, epic, fmDeadPid(t))
	_ = os.Remove(filepath.Join(epic, controlDir, "watch", "lasttick"))
}

// fmWantOpenCount asserts the block text names the unsupervised work, as firstmate's "<n> task(s) in flight".
func fmWantOpenCount(t *testing.T, out string, n int) {
	t.Helper()
	if !strings.Contains(out, strconv.Itoa(n)+" story(ies)") {
		t.Fatalf("block must name the %d open story(ies) left unsupervised, got %q", n, out)
	}
}

// fmTurnendGuard translates tests/fm-turnend-guard.test.sh. Firstmate's predicate layer (fm-supervision-lib) maps to
// watcherHealthy + watch.OpenStories; its hook layer (fm-turnend-guard.sh) maps to the stop-rewake guard
// (guardWatchers, driven through fmGuard) and, where the single-waiter lock or the TMPDIR budget matter, to the real
// hookStopRewake (fmStopHook). "Unrestartable" is firstmate's hook never arming itself: cox's guard first restarts a
// dead watcher, so an unhealthy case stubs the restart as refused unless the case is about the restart.
func fmTurnendGuard(t *testing.T) {
	// --- PREDICATE ---

	// fm: tests/fm-turnend-guard.test.sh:35
	t.Run("predicate_healthy_no_inflight", func(t *testing.T) {
		epic := fmEpic(t)
		if g := guardEpics(epic); len(g) != 0 {
			t.Fatalf("no open story must need no supervision, guardEpics=%v", g)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:45
	t.Run("predicate_unhealthy_no_beacon", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmWatchPid(t, epic, os.Getpid())
		if len(guardEpics(epic)) != 1 {
			t.Fatal("one open story must need supervision")
		}
		if watcherHealthy(epic, time.Now()) {
			t.Fatal("a beacon never written must not read as healthy")
		}
		if wi := watcherInfo(epic); wi.HasTick {
			t.Fatalf("absent beacon must read as never, got %+v", wi)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:56
	t.Run("predicate_unhealthy_stale_beacon", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmWatchPid(t, epic, os.Getpid())
		fmBeacon(t, epic, time.Since(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
		if watcherHealthy(epic, time.Now()) {
			t.Fatal("an ancient beacon must not read as fresh")
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:66
	t.Run("predicate_healthy_fresh_beacon", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		if !watcherHealthy(epic, time.Now()) {
			t.Fatal("a beacon written just now by a live watcher must read as fresh")
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:78
	t.Run("predicate_queue_pending_flag", func(t *testing.T) {
		epic := fmEpic(t)
		if w, _ := wake.Drain(epic, true); len(w) != 0 {
			t.Fatalf("empty/absent wake queue must not read as pending, got %d", len(w))
		}
		if _, err := wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: "s", Kind: wake.KindStatus, Note: "record"}); err != nil {
			t.Fatal(err)
		}
		if w, _ := wake.Drain(epic, true); len(w) == 0 {
			t.Fatal("a non-empty wake queue must read as pending")
		}
	})

	// n/a: predicate_x_mode_needs_supervision (fm: tests/fm-turnend-guard.test.sh:89) - relay/X-mode polling is firstmate-only.

	// fm: tests/fm-turnend-guard.test.sh:100
	t.Run("predicate_source_needs_supervision", func(t *testing.T) {
		// A registered process-event source needs supervision with no task in flight; cox's only supervision need is an
		// open story (watch.OpenStories), so a source-only epic is never guarded.
		notImplemented(t, "supervision-need registry: a registered source/check needs supervision with no open story")
	})

	// fm: tests/fm-turnend-guard.test.sh:121
	t.Run("predicate_registered_check_needs_supervision", func(t *testing.T) {
		notImplemented(t, "supervision-need registry: a registered source/check needs supervision with no open story")
	})

	// fm: tests/fm-turnend-guard.test.sh:132
	t.Run("predicate_registered_check_survives_rebinding_drift", func(t *testing.T) {
		notImplemented(t, "supervision-need registry: a registered source/check needs supervision with no open story")
	})

	// fm: tests/fm-turnend-guard.test.sh:143
	t.Run("predicate_unregistered_check_needs_nothing", func(t *testing.T) {
		epic := fmEpic(t)
		mustWrite(t, filepath.Join(epic, controlDir, "rogue.check.sh"), "#!/usr/bin/env bash\nexit 0\n")
		if g := guardEpics(epic); len(g) != 0 {
			t.Fatalf("a check with no registration must not arm supervision, guardEpics=%v", g)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:155
	t.Run("predicate_task_pr_poll_is_not_a_custom_check", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		mustWrite(t, filepath.Join(epic, controlDir, "s1.check.sh"), "#!/usr/bin/env bash\nexit 0\n")
		mustWrite(t, filepath.Join(epic, controlDir, "s1.pr-poll"), "")
		open, err := watch.OpenStories(epic)
		if err != nil || len(open) != 1 || open[0] != "s1" {
			t.Fatalf("the open story itself must be the one supervision need, got %v %v", open, err)
		}
	})

	// n/a: predicate_relay_shim_is_not_a_custom_check (fm: tests/fm-turnend-guard.test.sh:168) - relay/X-mode shim is firstmate-only.

	// --- HOOK ---

	// fm: tests/fm-turnend-guard.test.sh:300
	t.Run("hook_silent_when_no_work_in_flight", func(t *testing.T) {
		epic := fmEpic(t)
		fmWantSilent(t, fmGuard(t, epic, nil, fmBlocks(t)), "no open story")
	})

	// fm: tests/fm-turnend-guard.test.sh:309
	t.Run("hook_blocks_when_fresh_beacon_has_no_live_lock", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmBeacon(t, epic, 0) // fresh beacon, no watch.pid at all
		fmWantBlock(t, fmGuard(t, epic, fmLaunchRefused, fmBlocks(t)), epic, "fresh beacon with no live watcher")
	})

	// fm: tests/fm-turnend-guard.test.sh:320
	t.Run("hook_blocks_source_only_home", func(t *testing.T) {
		notImplemented(t, "supervision-need registry: a registered source/check needs supervision with no open story")
	})

	// fm: tests/fm-turnend-guard.test.sh:331
	t.Run("hook_blocks_when_dead_lock_has_fresh_beacon", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmWatchPid(t, epic, fmDeadPid(t))
		fmBeacon(t, epic, 0)
		fmWantBlock(t, fmGuard(t, epic, fmLaunchRefused, fmBlocks(t)), epic, "dead watcher pid despite a fresh beacon")
	})

	// fm: tests/fm-turnend-guard.test.sh:344
	t.Run("hook_silent_with_live_lock_and_fresh_beacon", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		fmWantSilent(t, fmGuard(t, epic, nil, fmBlocks(t)), "live watcher with a fresh beacon")
	})

	// fm: tests/fm-turnend-guard.test.sh:365
	t.Run("hook_non_claude_health_ignores_claude_budget_contention", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		blocks := fmBlocks(t)
		mustWrite(t, blocks, "3")
		for _, h := range []string{"codex", "pi", "claude"} {
			var out bytes.Buffer
			cfg := rewakeCfg{epics: []string{epic}, guardEpics: guardEpics(epic), harness: h, out: &out, stdout: &out, sleep: noSleep, blocksPath: blocks,
				launch: func(string) error { t.Errorf("%s: healthy path restarted the watcher", h); return nil }}
			if _, proceed := cfg.guardWatchers(); !proceed || out.Len() != 0 {
				t.Fatalf("%s healthy path must allow silently, proceed=%v out=%q", h, proceed, out.String())
			}
			if n := fmBlockCount(blocks); n != 3 {
				t.Fatalf("%s healthy path mutated the block budget: %d", h, n)
			}
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:413
	t.Run("hook_blocks_with_live_lock_and_stale_beacon", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmWatchPid(t, epic, os.Getpid()) // live pid
		fmBeacon(t, epic, time.Since(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
		// The real launchWatcher: a live pid is refused (never --replace'd) without spawning anything.
		fmWantBlock(t, fmGuard(t, epic, launchWatcher, fmBlocks(t)), epic, "live watcher with an ancient beacon")
	})

	// fm: tests/fm-turnend-guard.test.sh:434
	t.Run("hook_blocks_when_unhealthy_in_primary", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmWantBlock(t, fmGuard(t, epic, fmLaunchRefused, fmBlocks(t)), epic, "open story with no watcher")
	})

	// fm: tests/fm-turnend-guard.test.sh:445
	t.Run("hook_blocks_from_fm_home_state", func(t *testing.T) {
		// The guard reads the active home, not the repo root: with no --epic, cox walks up from the cwd to the
		// workspace and guards every epic there regardless of watcher liveness (allEpics).
		ws, epic := fmWorkspace(t, "e1", "s1")
		t.Chdir(ws)
		g := guardEpics("")
		if len(g) != 1 || g[0] != epic {
			t.Fatalf("the workspace epic with open work must be guarded from the leader cwd, got %v", g)
		}
		fmWantBlock(t, fmGuard(t, epic, fmLaunchRefused, fmBlocks(t)), epic, "workspace epic with no watcher")
	})

	// n/a: hook_x_mode_reason_sources_cadence (fm: tests/fm-turnend-guard.test.sh:457) - relay/X-mode cadence config is firstmate-only.
	// n/a: hook_x_mode_only_blocks_in_default_mode (fm: tests/fm-turnend-guard.test.sh:470) - relay/X-mode polling is firstmate-only.

	// fm: tests/fm-turnend-guard.test.sh:480
	t.Run("hook_registered_check_only_blocks_with_check_banner", func(t *testing.T) {
		notImplemented(t, "supervision-need registry: a registered source/check needs supervision with no open story")
	})

	// fm: tests/fm-turnend-guard.test.sh:491
	t.Run("hook_ignores_repo_state_when_fm_home_set", func(t *testing.T) {
		// An explicit --epic narrows the guard: another epic's open work in the same workspace is not this guard's.
		ws, busy := fmWorkspace(t, "busy", "s1")
		quiet := filepath.Join(ws, "proj", "epics", "quiet")
		if err := os.MkdirAll(filepath.Join(quiet, controlDir), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(ws)
		if g := guardEpics(quiet); len(g) != 0 {
			t.Fatalf("--epic %s must ignore %s's open work, got %v", quiet, busy, g)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:503
	t.Run("hook_uses_state_override", func(t *testing.T) {
		// FM_STATE_OVERRIDE wins over FM_HOME/state: cox's COX_EPIC (the --epic default) wins over workspace discovery.
		ws, _ := fmWorkspace(t, "other")
		override := fmEpic(t, "s1")
		t.Chdir(ws)
		t.Setenv("COX_EPIC", override)
		g := guardEpics(os.Getenv("COX_EPIC"))
		if len(g) != 1 || g[0] != override {
			t.Fatalf("COX_EPIC must select the guarded epic over the cwd workspace, got %v", g)
		}
		fmWantBlock(t, fmGuard(t, override, fmLaunchRefused, fmBlocks(t)), override, "override epic with no watcher")
	})

	// fm: tests/fm-turnend-guard.test.sh:516
	t.Run("hook_loop_guard_allows_retry", func(t *testing.T) {
		// Default (non-Claude) mode: the stop_hook_active retry always allows, so one turn is forced at most once. Cox
		// reads no Stop envelope and bounds reopens only by the 3-block budget, so a codex retry blocks again.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		var out bytes.Buffer
		cfg := rewakeCfg{epics: []string{epic}, guardEpics: guardEpics(epic), harness: "codex", out: &out, stdout: &out, sleep: noSleep, blocksPath: blocks, launch: fmLaunchRefused}
		cfg.guardWatchers() // the forced continuation
		out.Reset()
		if _, proceed := cfg.guardWatchers(); proceed || out.Len() != 0 {
			// proceed=false with output is a second block; a loop-guarded retry must end the turn silently.
			t.Fatalf("the loop-guarded retry (stop_hook_active=true) must allow the stop silently, got out=%q", out.String())
		}
	})

	// n/a: hook_blocks_in_secondmate_own_home (fm: tests/fm-turnend-guard.test.sh:531) - secondmate homes are firstmate-only.
	// n/a: hook_silent_in_idle_secondmate_home (fm: tests/fm-turnend-guard.test.sh:544) - secondmate homes are firstmate-only.
	// n/a: hook_secondmate_loop_guard_allows_retry (fm: tests/fm-turnend-guard.test.sh:556) - secondmate homes are firstmate-only.
	// n/a: hook_secondmate_reinvoke_recovery_loop (fm: tests/fm-turnend-guard.test.sh:574) - secondmate homes are firstmate-only.
	// n/a: hook_silent_in_secondmate_child_worktree (fm: tests/fm-turnend-guard.test.sh:612) - secondmate homes are firstmate-only.
	// n/a: hook_blocks_in_treehouse_leased_secondmate_home (fm: tests/fm-turnend-guard.test.sh:629) - secondmate homes are firstmate-only.
	// n/a: hook_exempts_linked_worktree_with_stray_marker (fm: tests/fm-turnend-guard.test.sh:649) - the .fm-secondmate-home marker is firstmate-only.
	// n/a: hook_exempts_linked_worktree_with_non_ascii_marker (fm: tests/fm-turnend-guard.test.sh:666) - the .fm-secondmate-home marker is firstmate-only.

	// fm: tests/fm-turnend-guard.test.sh:679
	t.Run("hook_silent_in_crewmate_worktree", func(t *testing.T) {
		// A worker terminal carrying the leader epic's hooks must never guard it (filterLeaderEpics).
		epic := fmEpic(t, "s1")
		if err := state.WriteLeader(epic, "term_leader"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ORCA_TERMINAL_HANDLE", "term_worker")
		fmWantSilent(t, fmGuard(t, epic, nil, fmBlocks(t)), "worker terminal over an unwatched leader epic")
	})

	// n/a: hook_silent_without_jq (fm: tests/fm-turnend-guard.test.sh:691) - the jq dependency is shell-only; cox's hooks are one Go binary.

	// n/a: hook_silent_without_stdin (fm: tests/fm-turnend-guard.test.sh:707) - the empty-stdin fail-open exists because the shell guard cannot read its loop-guard fields without an envelope (docs/turnend-guard.md:65); cox reads no Stop envelope, so there is nothing to fail open on, and the missing loop guard itself is pinned by hook_loop_guard_allows_retry.

	// fm: tests/fm-turnend-guard.test.sh:717
	t.Run("hook_runs_fast", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		start := time.Now()
		fmGuard(t, epic, fmLaunchRefused, fmBlocks(t))
		if d := time.Since(start); d >= 3*time.Second {
			t.Fatalf("guard took %s, want well under a second (3s CI margin)", d)
		}
	})

	// n/a: grok_adapter_forces_one_resume_when_unhealthy (fm: tests/fm-turnend-guard.test.sh:728) - cox has no grok harness.
	// n/a: grok_adapter_loop_guard_skips_resume (fm: tests/fm-turnend-guard.test.sh:759) - cox has no grok harness.
	// n/a: grok_adapter_native_false_blocks_without_resume (fm: tests/fm-turnend-guard.test.sh:777) - cox has no grok harness.
	// n/a: grok_adapter_native_true_allows_without_resume (fm: tests/fm-turnend-guard.test.sh:792) - cox has no grok harness.
	// n/a: grok_adapter_snake_case_native_and_camel_precedence (fm: tests/fm-turnend-guard.test.sh:807) - cox has no grok harness.
	// n/a: grok_adapter_invalid_inputs_start_neither_path (fm: tests/fm-turnend-guard.test.sh:821) - cox has no grok harness.
	// n/a: grok_adapter_missing_jq_and_no_supervision_allow (fm: tests/fm-turnend-guard.test.sh:852) - cox has no grok harness.
	// n/a: tracked_claude_entries_inert_under_grok (fm: tests/fm-turnend-guard.test.sh:887) - cox has no grok harness.

	// fm: tests/fm-turnend-guard.test.sh:939
	t.Run("codex_hook_uses_process_pwd_when_payload_cwd_is_outside_root", func(t *testing.T) {
		// The Stop hook anchors to the hook process cwd, not the payload cwd: cox never reads a payload cwd and resolves
		// the workspace from the process cwd.
		ws, epic := fmWorkspace(t, "e1", "s1")
		t.Chdir(ws)
		g := guardEpics("")
		if len(g) != 1 || g[0] != epic {
			t.Fatalf("hook must resolve the workspace from its own cwd, got %v", g)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:964
	t.Run("codex_hook_ignores_nested_git_root_guard", func(t *testing.T) {
		// A nested git project inside the workspace does not capture the hook: resolution walks up to the workspace.
		ws, epic := fmWorkspace(t, "e1", "s1")
		nested := filepath.Join(ws, "projects", "other")
		deep := filepath.Join(nested, "deep", "path")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		gitInitRepo(t, nested)
		t.Chdir(deep)
		g := guardEpics("")
		if len(g) != 1 || g[0] != epic {
			t.Fatalf("hook in a nested git root must still guard the outer workspace, got %v", g)
		}
	})

	// n/a: opencode_plugin_anchors_guard_to_worktree (fm: tests/fm-turnend-guard.test.sh:1002) - cox has no OpenCode harness.
	// fm: tests/fm-turnend-guard.test.sh:1061
	t.Run("pi_extension_injects_once_per_logical_agent_run", func(t *testing.T) {
		// Cox's Pi turn-end guard is the TypeScript extension (cox-pi.ts + cox-supervisor.ts); the case is translated
		// into that extension's node suite (captain ruling 2026-09-24) and run from here by name.
		fmPiNodeCase(t, "FM/fm-turnend-guard/pi_extension_injects_once_per_logical_agent_run")
	})

	// fm: tests/fm-turnend-guard.test.sh:1128
	t.Run("pi_extension_retries_after_followup_delivery_failure", func(t *testing.T) {
		fmPiNodeCase(t, "FM/fm-turnend-guard/pi_extension_retries_after_followup_delivery_failure")
	})

	// --- --claude cooperative mode ---

	// fm: tests/fm-turnend-guard.test.sh:1257
	t.Run("hook_claude_mode_reblocks_stop_hook_active_when_unhealthy", func(t *testing.T) {
		// A loop-guarded (rewake-opened) stop while unhealthy and unrecovered re-blocks.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		fmGuard(t, epic, fmLaunchRefused, blocks)
		fmWantBlock(t, fmGuard(t, epic, fmLaunchRefused, blocks), epic, "second stop in the same unhealthy episode")
	})

	// n/a: hook_claude_mode_reblocks_x_mode_without_tasks (fm: tests/fm-turnend-guard.test.sh:1268) - relay/X-mode polling is firstmate-only.

	// fm: tests/fm-turnend-guard.test.sh:1279
	t.Run("hook_claude_mode_allows_when_autoarm_owner_alive", func(t *testing.T) {
		// A live auto-arm owner (cox: a restart the guard itself confirms) allows the stop.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		mustWrite(t, blocks, "3")
		r := fmGuard(t, epic, func(string) error { return nil }, blocks)
		if r.blocked() || r.code != 0 {
			t.Fatalf("a confirmed restart must end the turn without a block, got %+v", r)
		}
		// Firstmate also advances the failure progression exactly once per new live arming epoch (3 -> 4) and keeps
		// it idempotent on re-observation; cox keeps no arming-epoch ledger.
		notImplemented(t, "auto-arm epoch ledger: a live arming epoch advances the failure progression once, idempotently")
	})

	// fm: tests/fm-turnend-guard.test.sh:1305
	t.Run("hook_claude_mode_repeated_failed_to_arming_interleavings_reach_fail_open", func(t *testing.T) {
		// Failed -> arming interleavings make bounded monotonic progress: each arming step (cox: a restart the guard
		// confirms) ends its Stop with exit 0 while advancing the failure progression by exactly one, so repeated
		// arm-then-die cycles still reach the fail-open. Cox's confirmed restart never touches the budget.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		for i := 1; i <= 4; i++ {
			r := fmGuard(t, epic, func(string) error { return nil }, blocks)
			if r.code != 0 || r.blocked() {
				t.Fatalf("arming step %d must own its Stop (exit 0), got %+v", i, r)
			}
			if n := fmBlockCount(blocks); n != i {
				t.Fatalf("arming step %d must advance the failure progression to %d, count=%d", i, i, n)
			}
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:1339
	t.Run("hook_claude_mode_terminal_boundary_excludes_starting_owner", func(t *testing.T) {
		notImplemented(t, "auto-arm owner lock: the terminal fail-open boundary excludes a concurrently starting owner")
	})

	// fm: tests/fm-turnend-guard.test.sh:1392
	t.Run("hook_claude_mode_allows_on_fresh_rewake_epoch", func(t *testing.T) {
		// The stop whose rewake is already owned does not start a duplicate continuation: cox's single-waiter lock
		// names a live waiter for this terminal, so stop-rewake exits 0 at once.
		epic := fmEpic(t, "s1")
		code, out := fmStopHook(t, epic, os.Getpid(), 0)
		if code != 0 || out != "" {
			t.Fatalf("an owned rewake must allow silently, got %d %q", code, out)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:1408
	t.Run("hook_claude_mode_blocks_on_abandoned_autoarm_claim", func(t *testing.T) {
		// An owner lock left behind by a finished claim, still naming a live (unrelated) pid, must not pass for
		// recovery under way. Cox's single-waiter lock trusts kill -0 alone.
		epic := fmEpic(t, "s1", "s2")
		code, out := fmStopHook(t, epic, fmLiveChild(t), 0)
		if code != 2 {
			t.Fatalf("an abandoned waiter lock over a live unrelated pid must not end the turn blind, got %d %q", code, out)
		}
		fmWantOpenCount(t, out, 2)
	})

	// fm: tests/fm-turnend-guard.test.sh:1432
	t.Run("hook_claude_mode_blocks_on_pid_reused_arming_claim", func(t *testing.T) {
		// The lock's pid now belongs to an unrelated live process (this test process stands in for it); only a recorded
		// process identity tells that from a real waiter. Cox records a bare pid.
		epic := fmEpic(t, "s1", "s2")
		fmBeacon(t, epic, 0)
		code, out := fmStopHook(t, epic, os.Getpid(), 0)
		if code != 2 {
			t.Fatalf("a pid-reused waiter lock must not pass for recovery under way, got %d %q", code, out)
		}
		fmWantOpenCount(t, out, 2)
	})

	// fm: tests/fm-turnend-guard.test.sh:1459
	t.Run("hook_claude_mode_blocks_on_stuck_arming_claim", func(t *testing.T) {
		// A live owner frozen past grace with an equally stale beacon is not recovery under way.
		epic := fmEpic(t, "s1", "s2")
		fmWatchPid(t, epic, fmDeadPid(t))
		fmBeacon(t, epic, time.Since(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
		code, out := fmStopHook(t, epic, fmLiveChild(t), 0)
		if code != 2 {
			t.Fatalf("a live but stuck waiter with a stale beacon must not end the turn blind, got %d %q", code, out)
		}
		fmWantOpenCount(t, out, 2)
	})

	// n/a: hook_claude_mode_allows_on_open_generation_claim (fm: tests/fm-turnend-guard.test.sh:1484) - a live generation claim owning recovery with no watcher lock is the Claude auto-arm model (watcher only between turns), firstmate-only; cox runs a persistent watcher, whose healthy case is hook_silent_with_live_lock_and_fresh_beacon.

	// fm: tests/fm-turnend-guard.test.sh:1506
	t.Run("hook_claude_mode_blocks_on_stuck_generation_claim", func(t *testing.T) {
		// A live, identity-matched owner whose entry and beacon are past grace no longer allows a blind stop.
		epic := fmEpic(t, "s1", "s2")
		fmWatchPid(t, epic, os.Getpid())
		fmBeacon(t, epic, time.Since(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
		r := fmGuard(t, epic, launchWatcher, fmBlocks(t))
		fmWantBlock(t, r, epic, "live watcher stuck with a stale beacon")
		fmWantOpenCount(t, r.out, 2)
	})

	// fm: tests/fm-turnend-guard.test.sh:1530
	t.Run("hook_claude_mode_terminal_fail_open_clears_abandoned_claim", func(t *testing.T) {
		// Budget spent + an abandoned live-pid claim: the guard must clear the claim and take the loud attended
		// fail-open, not step aside silently.
		epic := fmEpic(t, "s1")
		lock := fmLiveChild(t)
		code, out := fmStopHook(t, epic, lock, 4)
		if code != 0 || !strings.Contains(out, "wedge") {
			t.Fatalf("the terminal path must end the turn with the loud fail-open warning, got %d %q", code, out)
		}
		if _, err := os.Stat(filepath.Join(os.Getenv("TMPDIR"), "cox-rewake-fm-leader.lock")); err == nil && rewakeWaiterAlive(filepath.Join(os.Getenv("TMPDIR"), "cox-rewake-fm-leader.lock")) {
			t.Fatal("the terminal path left the abandoned waiter lock in place")
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:1552
	t.Run("hook_claude_mode_preserves_fresh_failed_progression", func(t *testing.T) {
		notImplemented(t, "auto-arm epoch ledger: the first verified failed epoch owns its handoff without spending the block budget")
	})

	// fm: tests/fm-turnend-guard.test.sh:1573
	t.Run("hook_claude_mode_integrated_monotonic_fail_open", func(t *testing.T) {
		// Fresh auto-arm failures: the first failed epoch owns its Stop without spending the budget, later ones block
		// within it, and only a VERIFIED exhausted failure reaches the one attended fail-open. Cox's restart records no
		// failure evidence for the guard to verify, so the progression cannot be expressed past the in-budget blocks.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		for i := 1; i <= 3; i++ {
			if r := fmGuard(t, epic, fmLaunchRefused, blocks); !r.blocked() {
				t.Fatalf("failed epoch %d must block within the budget, got %+v", i, r)
			}
		}
		notImplemented(t, "verified-failure evidence: the restart records an exhausted failure (and its one notice) that the guard checks before failing open")
	})

	// fm: tests/fm-turnend-guard.test.sh:1644
	t.Run("hook_claude_mode_frozen_epoch_reaches_bounded_fail_open", func(t *testing.T) {
		// An inert auto-arm freezes the ledger: the budget must still count every consecutive re-block against the
		// unchanged epoch (cox counts every block), and the verified failure then reaches the bounded fail-open.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		for i := 1; i <= 3; i++ {
			if r := fmGuard(t, epic, fmLaunchRefused, blocks); !r.blocked() || fmBlockCount(blocks) != i {
				t.Fatalf("frozen-epoch re-block %d must block and be charged (count %d), got %+v count=%d", i, i, r, fmBlockCount(blocks))
			}
		}
		notImplemented(t, "verified-failure evidence: the restart records an exhausted failure (and its one notice) that the guard checks before failing open")
	})

	// fm: tests/fm-turnend-guard.test.sh:1713
	t.Run("hook_claude_mode_frozen_epoch_without_verified_failure_spends_budget_and_keeps_blocking", func(t *testing.T) {
		// With no verified failure the budget runs out yet every stop keeps blocking.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		for i := 1; i <= 5; i++ {
			if r := fmGuard(t, epic, fmLaunchRefused, blocks); !r.blocked() {
				t.Fatalf("unverified stop %d must keep blocking, got %+v", i, r)
			}
		}
		if n := fmBlockCount(blocks); n <= 3 {
			t.Fatalf("the budget must run out, count=%d", n)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:1732
	t.Run("hook_claude_mode_recovery_contention_is_not_ordinary_allow", func(t *testing.T) {
		notImplemented(t, "block-budget episode reset: a healthy guard resets the episode under a lock, preserving it on contention")
	})

	// fm: tests/fm-turnend-guard.test.sh:1766
	t.Run("hook_claude_mode_concurrent_recovery_resets_are_idempotent", func(t *testing.T) {
		notImplemented(t, "block-budget episode reset: a healthy guard resets the episode under a lock, preserving it on contention")
	})

	// fm: tests/fm-turnend-guard.test.sh:1802
	t.Run("hook_claude_mode_stale_rewake_epoch_blocks", func(t *testing.T) {
		// An ancient rewake (a waiter lock whose pid is gone) is not this event's recovery.
		epic := fmEpic(t, "s1")
		code, out := fmStopHook(t, epic, fmDeadPid(t), 0)
		if code != 2 || !strings.Contains(out, fmRepairLine(epic)) {
			t.Fatalf("a stale waiter lock must not allow a blind stop (block with the repair line), got %d %q", code, out)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:1813
	t.Run("hook_claude_mode_budget_without_verified_failure_keeps_blocking", func(t *testing.T) {
		// Budget exhaustion alone cannot permit a blind stop.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		for i := 1; i <= 4; i++ {
			if r := fmGuard(t, epic, fmLaunchRefused, blocks); !r.blocked() {
				t.Fatalf("block %d must exit 2 (budget exhaustion without a verified failure must not fail open), got %+v", i, r)
			}
		}
		if n := fmBlockCount(blocks); n <= 3 {
			t.Fatalf("four consecutive blocks must spend the budget, count=%d", n)
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:1828
	t.Run("hook_claude_mode_verified_failure_alarm_is_loud_and_once", func(t *testing.T) {
		// The premise is a verified exhausted failure with its notice consumed plus a spent budget; cox has no
		// failure evidence to seed, so the same fixture would be the unverified case below.
		notImplemented(t, "verified-failure evidence: the restart records an exhausted failure (and its one notice) that the guard checks before failing open")
	})

	// fm: tests/fm-turnend-guard.test.sh:1847
	t.Run("hook_claude_mode_fail_open_requires_notice_and_failure_epoch", func(t *testing.T) {
		// A spent budget alone (no verified failure evidence) must keep blocking.
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		mustWrite(t, blocks, "3")
		if r := fmGuard(t, epic, fmLaunchRefused, blocks); !r.blocked() {
			t.Fatalf("an exhausted budget without a verified failure must remain blocking, got %+v", r)
		}
	})

	// n/a: hook_claude_mode_away_mode_never_uses_stop_autoarm_fail_open (fm: tests/fm-turnend-guard.test.sh:1866) - away mode (afk daemon) is firstmate-only.

	// fm: tests/fm-turnend-guard.test.sh:1880
	t.Run("hook_claude_mode_allow_resets_budget", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		blocks := fmBlocks(t)
		if r := fmGuard(t, epic, fmLaunchRefused, blocks); !r.blocked() || fmBlockCount(blocks) == 0 {
			t.Fatalf("first block must record the budget, got %+v", r)
		}
		fmHealthy(t, epic)
		fmWantSilent(t, fmGuard(t, epic, nil, blocks), "healthy again")
		if _, err := os.Stat(blocks); err == nil {
			t.Fatalf("a healthy allow must reset the block budget, count=%d", fmBlockCount(blocks))
		}
	})

	// fm: tests/fm-turnend-guard.test.sh:1911
	t.Run("hook_claude_mode_waits_for_late_claim", func(t *testing.T) {
		// A bounded wait for the late claim instead of forcing a continuation: the real launchWatcher waits for the
		// restarted watcher's first tick (a stub that ticks after 0.4s inside a 3s window).
		epic := fmEpic(t, "s1")
		stubWatcher(t, 3*time.Second, `sleep 0.4; mkdir -p "$3/.cox/watch" && date > "$3/.cox/watch/lasttick" && exec sleep 30`)
		r := fmGuard(t, epic, launchWatcher, fmBlocks(t))
		if r.blocked() || r.code != 0 || r.out != "" {
			t.Fatalf("a late but confirmed restart must end the Stop silently (no forced continuation, no output), got %+v", r)
		}
	})

	// n/a: hook_claude_mode_secondmate_reblocks_like_primary (fm: tests/fm-turnend-guard.test.sh:1933) - secondmate homes are firstmate-only.
	// n/a: hook_away_daemon_allows_between_watcher_cycles (fm: tests/fm-turnend-guard.test.sh:1985) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_away_daemon_allows_over_dead_watcher_lock (fm: tests/fm-turnend-guard.test.sh:2006) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_away_mode_blocks_without_any_supervisor (fm: tests/fm-turnend-guard.test.sh:2026) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_away_mode_blocks_on_dead_daemon (fm: tests/fm-turnend-guard.test.sh:2035) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_away_mode_blocks_on_pid_reused_daemon (fm: tests/fm-turnend-guard.test.sh:2046) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_away_mode_blocks_on_stale_beacon (fm: tests/fm-turnend-guard.test.sh:2062) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_daemon_lock_is_ignored_without_away_mode (fm: tests/fm-turnend-guard.test.sh:2081) - the supervise-daemon lock is firstmate-only.
	// n/a: hook_away_daemon_allows_beacon_within_poll_derived_grace (fm: tests/fm-turnend-guard.test.sh:2109) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_away_daemon_blocks_dead_daemon_despite_poll_derived_grace (fm: tests/fm-turnend-guard.test.sh:2132) - away mode (afk daemon) is firstmate-only.
	// n/a: hook_away_daemon_blocks_beacon_older_than_poll_derived_grace (fm: tests/fm-turnend-guard.test.sh:2143) - away mode (afk daemon) is firstmate-only.

	// fm: tests/fm-turnend-guard.test.sh:2165
	t.Run("hook_no_afk_ignores_poll_derived_grace", func(t *testing.T) {
		// Outside away mode the strict grace applies: a 400s-old beacon under a live watcher blocks.
		epic := fmEpic(t, "s1")
		fmWatchPid(t, epic, os.Getpid())
		fmBeacon(t, epic, 400*time.Second)
		fmWantBlock(t, fmGuard(t, epic, launchWatcher, fmBlocks(t)), epic, "400s-old beacon")
	})
}

// fmPiNodeCase runs one translated case of the Pi extension's node suite (cox-supervisor.test.ts) by its exact name,
// with the cox/Orca session env stripped so a case never reads the caller's terminal. The case must run and pass.
func fmPiNodeCase(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is required to run the Pi guard case %s: %v", name, err)
	}
	suite := filepath.Join("..", "..", "internal", "adapter", "harness", "pi", "extension", "cox-supervisor.test.ts")
	cmd := exec.Command("node", "--test", "--test-timeout=60000", "--test-name-pattern=^"+regexp.QuoteMeta(name)+"$", suite)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "COX_") && !strings.HasPrefix(kv, "ORCA_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "# pass 1\n") || !strings.Contains(string(out), "# fail 0\n") {
		t.Fatalf("Pi guard case %s did not pass in the node suite (err %v):\n%s", name, err, out)
	}
}

// fmBanner is cox's pull-side watcher-down warning: the ISSUE line `cox doctor` and `cox state` print for an epic
// (watcherIssue over watcherInfo), firstmate's fm-guard.sh banner. "" means silent.
func fmBanner(epic string) string { return watcherIssue(epic, watcherInfo(epic)) }

// fmWantFullBanner asserts firstmate's "exactly one full WATCHER DOWN banner" carrying the actionable repair.
func fmWantFullBanner(t *testing.T, got, epic, why string) {
	t.Helper()
	if !strings.Contains(got, "cox watch --epic "+epic+" --replace") {
		t.Fatalf("%s: want the full actionable watcher-down banner, got %q", why, got)
	}
}

// fmWantReminder asserts firstmate's same-episode dedup: a later call in one down episode prints a concise reminder,
// not the full banner again.
func fmWantReminder(t *testing.T, full, got, why string) {
	t.Helper()
	if got == "" || got == full {
		t.Fatalf("%s: a repeated call in the same down episode must print a concise reminder, not the full banner again; got %q", why, got)
	}
}

// fmBannerEpic is make_guard_case: one epic with a working story and no watcher.
func fmBannerEpic(t *testing.T) string { return fmEpic(t, "task") }

// fmGuardStaleBanner translates tests/fm-guard-stale-banner.test.sh. Firstmate's fm-guard.sh is the pull warning other
// supervision commands run mid-turn; cox's is the watcherIssue line of `cox doctor` / `cox state` (fmBanner). Cox runs
// one persistent watcher per epic, i.e. firstmate's persistent model: the Claude auto-arm and Pi extension supervision
// models (watcher only between turns, extension-owned hand-offs) are firstmate-only, so their tolerance cases are n/a,
// while their "must stay loud" assertions translate against the persistent watcher.
func fmGuardStaleBanner(t *testing.T) {
	// fm: tests/fm-guard-stale-banner.test.sh:163
	t.Run("first_stale_call_prints_full_banner", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmWantFullBanner(t, fmBanner(epic), epic, "first stale call")
	})

	// n/a: full_banner_names_quiet_mode_when_active (fm: tests/fm-guard-stale-banner.test.sh:176) - quiet/away mode (afk daemon) is firstmate-only.

	// fm: tests/fm-guard-stale-banner.test.sh:192
	t.Run("repeated_same_episode_prints_reminder_only", func(t *testing.T) {
		epic := fmBannerEpic(t)
		first := fmBanner(epic)
		fmWantFullBanner(t, first, epic, "first stale call")
		fmWantReminder(t, first, fmBanner(epic), "second stale call")
	})

	// fm: tests/fm-guard-stale-banner.test.sh:210
	t.Run("fresh_beacon_without_live_watcher_stays_alarm", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmBeacon(t, epic, 0)
		fmWantFullBanner(t, fmBanner(epic), epic, "fresh leftover beacon, no live watcher")
	})

	// n/a: x_mode_without_live_watcher_stays_alarm (fm: tests/fm-guard-stale-banner.test.sh:220) - relay/X-mode polling is firstmate-only.

	// fm: tests/fm-guard-stale-banner.test.sh:231
	t.Run("healthy_recovery_rearms_next_stale_episode", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmWantFullBanner(t, fmBanner(epic), epic, "first stale episode")
		fmHealthy(t, epic)
		if b := fmBanner(epic); b != "" {
			t.Fatalf("banner must be silent after watcher recovery, got %q", b)
		}
		fmDead(t, epic)
		fmWantFullBanner(t, fmBanner(epic), epic, "second stale episode")
	})

	// fm: tests/fm-guard-stale-banner.test.sh:258
	t.Run("concurrent_same_episode_prints_one_full_banner", func(t *testing.T) {
		epic := fmBannerEpic(t)
		outs := make(chan string, 30)
		for i := 0; i < 30; i++ {
			go func() { outs <- fmBanner(epic) }()
		}
		full := 0
		var one string
		for i := 0; i < 30; i++ {
			if o := <-outs; strings.Contains(o, "--replace") {
				full++
				one = o
			}
		}
		if full != 1 {
			t.Fatalf("30 concurrent same-episode calls must claim exactly one full banner (29 reminders), got %d full: %q", full, one)
		}
	})

	// fm: tests/fm-guard-stale-banner.test.sh:283
	t.Run("home_isolation", func(t *testing.T) {
		a, b := fmBannerEpic(t), fmBannerEpic(t)
		a1 := fmBanner(a)
		fmWantFullBanner(t, a1, a, "epic A first call")
		fmWantFullBanner(t, fmBanner(b), b, "epic B first call (not suppressed by A)")
		fmWantReminder(t, a1, fmBanner(a), "epic A remembers its own episode")
	})

	// fm: tests/fm-guard-stale-banner.test.sh:299
	t.Run("queued_wake_warning_stays_independent", func(t *testing.T) {
		epic := fmBannerEpic(t)
		first := fmBanner(epic)
		fmWantFullBanner(t, first, epic, "first stale call")
		seedWake(t, epic, wake.KindStatus)
		second := fmBanner(epic)
		fmWantReminder(t, first, second, "same-episode call with a queued wake")
		if !strings.Contains(second, "wake") {
			t.Fatalf("the queued-wake warning must not be suppressed by the dedup, got %q", second)
		}
	})

	// fm: tests/fm-guard-stale-banner.test.sh:315
	t.Run("read_only_before_writable_does_not_consume_full_banner", func(t *testing.T) {
		// Needs the read-only vs writable caller split over the down-episode marker; cox has neither.
		notImplemented(t, "stale-banner episode dedup: a per-epic down-episode marker claimed once and kept by read-only callers")
	})

	// fm: tests/fm-guard-stale-banner.test.sh:335
	t.Run("read_only_during_episode_observes_without_mutating_marker", func(t *testing.T) {
		epic := fmBannerEpic(t)
		first := fmBanner(epic)
		fmWantReminder(t, first, fmBanner(epic), "read-only call during a claimed episode")
	})

	// fm: tests/fm-guard-stale-banner.test.sh:351
	t.Run("healthy_read_only_does_not_clear_marker", func(t *testing.T) {
		// Needs the per-epic down-episode marker a writable call claims; cox keeps none.
		notImplemented(t, "stale-banner episode dedup: a per-epic down-episode marker claimed once and kept by read-only callers")
	})

	// fm: tests/fm-guard-stale-banner.test.sh:373
	t.Run("read_only_never_mutates_stale_banner_state_files", func(t *testing.T) {
		epic := fmBannerEpic(t)
		before := fmTree(t, epic)
		fmBanner(epic)
		if after := fmTree(t, epic); after != before {
			t.Fatalf("a read-only stale call changed the control tree:\nbefore %s\nafter  %s", before, after)
		}
		quiet := fmEpic(t)
		if b := fmBanner(quiet); b != "" {
			t.Fatalf("no open story must stay silent, got %q", b)
		}
	})

	// n/a: autoarm_fresh_beacon_without_watcher_is_healthy (fm: tests/fm-guard-stale-banner.test.sh:397) - the Claude Stop auto-arm model (watcher only between turns) is firstmate-only; cox runs a persistent watcher.
	// n/a: autoarm_stale_beacon_alarms_with_correct_reason (fm: tests/fm-guard-stale-banner.test.sh:409) - auto-arm supervision model is firstmate-only.
	// n/a: autoarm_stale_episode_is_stable (fm: tests/fm-guard-stale-banner.test.sh:421) - auto-arm supervision model is firstmate-only.
	// n/a: autoarm_long_handling_turn_stays_silent (fm: tests/fm-guard-stale-banner.test.sh:439) - auto-arm supervision model is firstmate-only.
	// n/a: autoarm_long_turn_requires_every_healthy_signal (fm: tests/fm-guard-stale-banner.test.sh:460) - auto-arm supervision model is firstmate-only.
	// n/a: autoarm_open_claim_does_not_explain_stale_beacon (fm: tests/fm-guard-stale-banner.test.sh:519) - auto-arm supervision model is firstmate-only.

	// fm: tests/fm-guard-stale-banner.test.sh:540
	t.Run("autoarm_long_turn_does_not_silence_other_models", func(t *testing.T) {
		// The persistent model must alarm on a stale beacon even while the watcher pid is alive.
		epic := fmBannerEpic(t)
		fmWatchPid(t, epic, os.Getpid())
		fmBeacon(t, epic, time.Since(time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)))
		fmWantFullBanner(t, fmBanner(epic), epic, "live watcher pid with a stale beacon")
	})

	// fm: tests/fm-guard-stale-banner.test.sh:562
	t.Run("persistent_no_watcher_banner_names_missing_process", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmBeacon(t, epic, 0)
		b := fmBanner(epic)
		fmWantFullBanner(t, b, epic, "fresh beacon, no watcher process")
		if !strings.Contains(b, "not alive") || strings.Contains(b, "tick") {
			t.Fatalf("banner must name the missing watcher process, not blame the beacon: %q", b)
		}
	})

	// fm: tests/fm-guard-stale-banner.test.sh:576
	t.Run("persistent_no_watcher_episode_survives_beacon_touch", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmBeacon(t, epic, 0)
		first := fmBanner(epic)
		fmWantFullBanner(t, first, epic, "first no-watcher call")
		fmBeacon(t, epic, -time.Second) // the beacon mtime advances, still no live watcher
		fmWantReminder(t, first, fmBanner(epic), "same no-watcher episode after a beacon touch")
	})

	// n/a: extension_handoff_with_live_session_is_healthy (fm: tests/fm-guard-stale-banner.test.sh:602) - the Pi extension supervision model (extension-owned watcher hand-offs) is firstmate-only; cox's Pi runs the persistent cox watcher.
	// n/a: extension_handoff_with_empty_lock_is_healthy (fm: tests/fm-guard-stale-banner.test.sh:622) - extension supervision model is firstmate-only.

	// fm: tests/fm-guard-stale-banner.test.sh:643
	t.Run("extension_held_unhealthy_locks_stay_alarm", func(t *testing.T) {
		// Every held-but-unhealthy watcher record stays loud: dead pid, malformed pid, and a live pid that is not this
		// epic's watcher (firstmate's wrong-home / wrong-path / identity-mismatch: cox records a bare pid).
		for _, c := range []struct {
			name string
			pid  func() string
		}{
			{"dead-pid", func() string { return strconv.Itoa(fmDeadPid(t)) }},
			{"malformed-pid", func() string { return "not-a-pid" }},
			{"wrong-home", func() string { return strconv.Itoa(fmLiveChild(t)) }},
			{"wrong-path", func() string { return strconv.Itoa(fmLiveChild(t)) }},
			{"identity-mismatch", func() string { return strconv.Itoa(fmLiveChild(t)) }},
		} {
			epic := fmBannerEpic(t)
			mustWrite(t, watchPidPath(epic), c.pid())
			fmBeacon(t, epic, 0)
			if b := fmBanner(epic); !strings.Contains(b, "not alive") {
				t.Errorf("%s: a held unhealthy watcher record must alarm with no-watcher, got %q", c.name, b)
			}
		}
	})

	// fm: tests/fm-guard-stale-banner.test.sh:697
	t.Run("extension_without_ownership_evidence_stays_alarm", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmBeacon(t, epic, 0) // unheld (no watch.pid) with a fresh beacon
		b := fmBanner(epic)
		fmWantFullBanner(t, b, epic, "unheld watcher, fresh beacon")
		if !strings.Contains(b, "not alive") {
			t.Fatalf("banner must name the missing watcher process, got %q", b)
		}
	})

	// n/a: extension_ownership_needs_every_signal (fm: tests/fm-guard-stale-banner.test.sh:713) - extension supervision model ownership proof is firstmate-only.

	// fm: tests/fm-guard-stale-banner.test.sh:763
	t.Run("extension_stale_beacon_alarms_despite_live_session", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmWatchPid(t, epic, os.Getpid())
		fmBeacon(t, epic, time.Since(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))) // past any grace
		b := fmBanner(epic)
		fmWantFullBanner(t, b, epic, "beacon past grace under a live watcher pid")
		if !strings.Contains(b, "tick") {
			t.Fatalf("the stale-beacon banner must name the stale beacon, got %q", b)
		}
	})

	// fm: tests/fm-guard-stale-banner.test.sh:786
	t.Run("extension_handoff_keeps_queued_wake_warning", func(t *testing.T) {
		// Healthy watcher + a queued wake: the queued-wake warning (cox: prompt-drain attaching the unacked wake) still
		// fires and the watcher-down banner does not.
		epic := fmBannerEpic(t)
		fmHealthy(t, epic)
		seedWake(t, epic, wake.KindStuck)
		var out bytes.Buffer
		if runPromptDrain(epic, &out) == 0 || !strings.Contains(out.String(), "stuck") {
			t.Fatalf("the queued-wake warning must fire, got %q", out.String())
		}
		if b := fmBanner(epic); b != "" {
			t.Fatalf("a queued wake must not resurrect the watcher-down banner, got %q", b)
		}
	})

	// n/a: branch_actor_is_not_told_to_drain_queued_wakes (fm: tests/fm-guard-stale-banner.test.sh:812) - the Pi supervision branch actor is firstmate-only.

	// fm: tests/fm-guard-stale-banner.test.sh:841
	t.Run("persistent_model_ignores_pi_extension_evidence", func(t *testing.T) {
		epic := fmBannerEpic(t)
		mustWrite(t, filepath.Join(epic, controlDir, ".pi-watch-extension-loaded"), "sha256:x\n"+strconv.Itoa(os.Getpid())+"\n")
		fmBeacon(t, epic, 0)
		b := fmBanner(epic)
		if !strings.Contains(b, "not alive") {
			t.Fatalf("stray Pi markers must not silence the no-watcher banner, got %q", b)
		}
	})

	// fm: tests/fm-guard-stale-banner.test.sh:861
	t.Run("extension_live_watcher_is_healthy_without_ownership_evidence", func(t *testing.T) {
		epic := fmBannerEpic(t)
		fmHealthy(t, epic)
		if b := fmBanner(epic); b != "" {
			t.Fatalf("a live watcher with a fresh beacon must stay silent, got %q", b)
		}
	})

	// n/a: pi_harness_routes_itself_to_the_extension_model (fm: tests/fm-guard-stale-banner.test.sh:884) - extension supervision model routing is firstmate-only.
}

// fmTree lists every path and size under epic's control tree, to prove a read-only caller mutated nothing.
func fmTree(t *testing.T, epic string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(filepath.Join(epic, controlDir), func(p string, info os.FileInfo, err error) error {
		if err == nil {
			b.WriteString(p + ":" + strconv.FormatInt(info.Size(), 10) + ":" + info.ModTime().String() + ";")
		}
		return nil
	})
	return b.String()
}

// fmWatchCheckpoint translates tests/fm-watch-checkpoint.test.sh. Firstmate's bounded foreground checkpoint for a pull
// harness (Codex) is cox's `cox wake wait --epic <dir> --max <dur>` (exit 124 on a quiet window maps to cox's exit 3);
// the checkpoint's own singleton watcher start maps to `cox watch`'s claimWatchPid.
func fmWatchCheckpoint(t *testing.T) {
	// fm: tests/fm-watch-checkpoint.test.sh:18
	t.Run("quiet_checkpoint_exits_124_cleanly", func(t *testing.T) {
		epic := fmEpic(t)
		code, out := fmCapture(t, func() int { return wakeWait([]string{"--epic", epic, "--max", "1s"}) })
		if code != 3 {
			t.Fatalf("a quiet window must exit 3 (firstmate 124), got %d", code)
		}
		if _, err := os.Stat(watchPidPath(epic)); err == nil {
			t.Fatal("a quiet checkpoint must leave no watcher pid behind")
		}
		if !strings.Contains(out, "1s") {
			t.Fatalf("a quiet checkpoint must print a clean line naming the window (no actionable wake within 1s), got %q", out)
		}
	})

	// fm: tests/fm-watch-checkpoint.test.sh:31
	t.Run("signal_passes_through_and_exits_zero", func(t *testing.T) {
		epic := fmEpic(t)
		go func() {
			time.Sleep(time.Second)
			_, _ = wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: "demo", Kind: wake.KindWorkerDone, Note: "done: synthetic wake"})
		}()
		code, out := fmCapture(t, func() int { return wakeWait([]string{"--epic", epic, "--max", "8s"}) })
		if code != 0 || !strings.Contains(out, "synthetic wake") {
			t.Fatalf("a wake must pass through with exit 0, got %d %q", code, out)
		}
		if w, _ := wake.Drain(epic, true); len(w) != 1 {
			t.Fatalf("the wake must stay queued for drain, got %d", len(w))
		}
	})

	// fm: tests/fm-watch-checkpoint.test.sh:49
	t.Run("registered_check_uses_preserved_watcher_environment", func(t *testing.T) {
		notImplemented(t, "supervision-need registry: a registered source/check needs supervision with no open story")
	})

	// fm: tests/fm-watch-checkpoint.test.sh:69
	t.Run("existing_singleton_watcher_is_not_success", func(t *testing.T) {
		epic := fmEpic(t)
		fmWatchPid(t, epic, fmLiveChild(t))
		release, err := claimWatchPid(epic, false)
		if err == nil {
			release()
			t.Fatal("a second watcher start over a live singleton must fail, not succeed")
		}
		if !strings.Contains(err.Error(), "already running") {
			t.Fatalf("the refusal must say the watcher is already running, got %v", err)
		}
	})
}

// fmLiveChildExit runs script under sh as a stand-in live process and returns its pid and a channel that closes once it
// has exited (reaped), so a case can tell "still running" from "killed". Cleanup kills it.
func fmLiveChildExit(t *testing.T, script string) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })
	return cmd.Process.Pid, exited
}

// fmStillRunning reports whether exited has not closed (the process was not killed).
func fmStillRunning(exited <-chan struct{}) bool {
	select {
	case <-exited:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

// TestPortClaimChild is not a translated case: it is the child process fmClaimRace re-executes so several real
// processes contend for one epic's watch.pid (claimWatchPid keys on os.Getpid, so the race needs distinct processes).
// Without FM_CLAIM_EPIC it does nothing.
func TestPortClaimChild(t *testing.T) {
	epic := os.Getenv("FM_CLAIM_EPIC")
	if epic == "" {
		return
	}
	if at, err := strconv.ParseInt(os.Getenv("FM_CLAIM_AT"), 10, 64); err == nil {
		time.Sleep(time.Until(time.Unix(0, at)))
	}
	release, err := claimWatchPid(epic, false)
	if err != nil {
		fmt.Println("CLAIM lost:", err)
		return
	}
	fmt.Println("CLAIM won")
	time.Sleep(1500 * time.Millisecond) // hold the claim live so a late contender sees a live holder
	release()
}

// fmClaimRace starts n processes that all call claimWatchPid on epic at the same instant and returns how many won.
func fmClaimRace(t *testing.T, epic string, n int) int {
	t.Helper()
	// 3s lets every re-executed child finish starting under a loaded CI box before the shared instant, so the claims
	// really race (1s let early children win uncontested while late ones saw a live holder).
	at := strconv.FormatInt(time.Now().Add(3*time.Second).UnixNano(), 10)
	outs := make([]*bytes.Buffer, n)
	cmds := make([]*exec.Cmd, n)
	for i := range cmds {
		outs[i] = &bytes.Buffer{}
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestPortClaimChild$", "-test.count=1")
		cmds[i].Env = append(os.Environ(), "FM_CLAIM_EPIC="+epic, "FM_CLAIM_AT="+at)
		cmds[i].Stdout = outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Error(err) // callers run this off the test goroutine: Error, never Fatal
			cmds[i] = nil
		}
	}
	won := 0
	for i, c := range cmds {
		if c == nil {
			continue
		}
		_ = c.Wait()
		won += strings.Count(outs[i].String(), "CLAIM won")
	}
	return won
}

// fmWait runs the stop-rewake idle wait for epic (guard first, then the poll loop) over polls virtual polls, calling
// onPoll(i) at each poll so a case can change the world mid-wait (the watcher dies, a wake lands). It returns the exit
// code and output. A healthy watcher is required for the guard to let the wait start.
func fmWait(t *testing.T, epic string, polls int, onPoll func(i int)) (int, string) {
	t.Helper()
	var out bytes.Buffer
	i := 0
	cfg := rewakeCfg{
		epics: []string{epic}, guardEpics: guardEpics(epic), out: &out, stdout: &out, blocksPath: fmBlocks(t),
		maxWait: time.Duration(polls) * time.Second, batchMax: time.Hour, poll: time.Second,
		sleep:  func(time.Duration) { i++; onPoll(i) },
		launch: func(ep string) error { t.Errorf("the waiter restarted the watcher for %s", ep); return errWatchRefused },
	}
	return runStopRewake(cfg), out.String()
}

// fmWantWatcherDownNotice asserts the waiter surfaced that its watcher is gone (firstmate's typed
// "watcher: FAILED - cycle ended without an actionable reason").
func fmWantWatcherDownNotice(t *testing.T, code int, out, why string) {
	t.Helper()
	if code != 2 || !strings.Contains(out, "not alive") {
		t.Fatalf("%s: the waiter must reopen naming the dead watcher, got %d %q", why, code, out)
	}
}

// fmTickingWatcher stubs launchWatcher's `cox watch` with a script that, after a real watcher's startup time (0.5s:
// backend and policy load), records its pid as the epic's watcher and ticks.
func fmTickingWatcher(t *testing.T, wait time.Duration) {
	t.Helper()
	stubWatcher(t, wait, `sleep 0.5; mkdir -p "$3/.cox/watch" && echo $$ > "$3/.cox/watch.pid" && date > "$3/.cox/watch/lasttick" && exec sleep 30`)
}

// fmWatcherLock translates the cmd/cox half of tests/fm-watcher-lock.test.sh: the singleton (`cox watch`'s
// claimWatchPid on .cox/watch.pid), restart (--replace), the arm (the stop-rewake guard's launchWatcher restart and
// the idle waiter attached to a live watcher) and the pull guard. The watcher-side cases (self-eviction, the cycle
// ledger) live in internal/watch/port_lifecycle_test.go under the same suite name.
func fmWatcherLock(t *testing.T) {
	// fm: tests/fm-watcher-lock.test.sh:37
	t.Run("wait_deadline_reaps_a_stopped_child", func(t *testing.T) {
		// A stopped, TERM-resistant watcher: `cox watch --replace` (killAndWait) must still reap it within its deadline.
		pid, exited := fmLiveChildExit(t, `trap "" TERM; kill -STOP $$; exec sleep 300`)
		time.Sleep(200 * time.Millisecond)
		start := time.Now()
		err := killAndWait(pid, 2*time.Second)
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("killAndWait hung past its deadline: %s", d)
		}
		if fmStillRunning(exited) {
			t.Fatalf("a stopped TERM-resistant process survived the replace deadline (err=%v); it must escalate to KILL and reap", err)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:93
	t.Run("singleton_start", func(t *testing.T) {
		// Firstmate starts two watchers at once. One 2-way race is a coin flip against a read-check-write claim, so
		// the invariant is checked over 10 independent 2-way races run together: every one must leave one winner.
		wins := make(chan int, 10)
		for i := 0; i < 10; i++ {
			epic := fmEpic(t)
			go func() { wins <- fmClaimRace(t, epic, 2) }()
		}
		for i := 0; i < 10; i++ {
			if won := <-wins; won != 1 {
				t.Errorf("simultaneous watcher starts must leave exactly one live watcher, %d claimed watch.pid", won)
			}
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:126
	t.Run("stale_watch_lock_reclaimed", func(t *testing.T) {
		epic := fmEpic(t)
		dead := fmDeadPid(t)
		fmWatchPid(t, epic, dead)
		release, err := claimWatchPid(epic, false)
		if err != nil {
			t.Fatalf("a dead watcher's pidfile must be reclaimed, got %v", err)
		}
		defer release()
		if got := readPid(watchPidPath(epic)); got != os.Getpid() {
			t.Fatalf("stale pid %d was not replaced, watch.pid=%d", dead, got)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:158
	t.Run("live_stale_watch_lock_is_actionable", func(t *testing.T) {
		epic := fmEpic(t)
		fmWatchPid(t, epic, fmLiveChild(t))
		fmBeacon(t, epic, time.Since(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
		release, err := claimWatchPid(epic, false)
		if err == nil {
			release()
			t.Fatal("a new watcher must not silently no-op (or win) behind a live holder")
		}
		if !strings.Contains(err.Error(), "stale") {
			t.Fatalf("the refusal must explain the live holder's stale heartbeat, got %v", err)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:175
	t.Run("guard_warnings", func(t *testing.T) {
		// Down + two open stories + a queued wake: the watcher-down banner leads (open count, beacon age, fix command),
		// the queued-wakes warning follows it; a live fresh watcher with an empty queue is silent.
		epic := fmEpic(t, "s1", "s2")
		seedWake(t, epic, wake.KindStatus)
		_, out := fmCapture(t, func() int { return cmdState([]string{"--epic", epic, "--no-forge"}) })
		for _, want := range []string{"2 story(ies)", "cox watch --epic " + epic + " --replace", "never"} {
			if !strings.Contains(out, want) {
				t.Errorf("down banner must carry %q, got %q", want, out)
			}
		}
		banner, queue := strings.Index(out, "not alive"), strings.Index(out, "wake")
		if banner < 0 || queue < banner {
			t.Errorf("a queued-wakes warning must follow the watcher-down banner (drain, then repair), got %q", out)
		}
		fresh := fmEpic(t, "s1")
		fmHealthy(t, fresh)
		if _, out := fmCapture(t, func() int { return cmdState([]string{"--epic", fresh, "--no-forge"}) }); strings.Contains(out, "ISSUE") {
			t.Errorf("a live fresh watcher with an empty queue must not warn, got %q", out)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:253
	t.Run("lock_single_winner_under_concurrency", func(t *testing.T) {
		epic := fmEpic(t)
		if won := fmClaimRace(t, epic, 20); won != 1 {
			t.Fatalf("concurrent claims must yield exactly one winner, got %d", won)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:283
	t.Run("lock_steals_dead_pid_lock", func(t *testing.T) {
		epic := fmEpic(t)
		fmWatchPid(t, epic, fmDeadPid(t))
		release, err := claimWatchPid(epic, false)
		if err != nil {
			t.Fatalf("a dead-pid lock must be reclaimed by a single acquirer, got %v", err)
		}
		release()
	})

	// fm: tests/fm-watcher-lock.test.sh:302
	t.Run("lock_stale_steal_single_winner_under_concurrency", func(t *testing.T) {
		epic := fmEpic(t)
		fmWatchPid(t, epic, fmDeadPid(t))
		if won := fmClaimRace(t, epic, 20); won != 1 {
			t.Fatalf("concurrent steals of a dead-pid lock must yield exactly one winner, got %d", won)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:333
	t.Run("lock_live_steal_mutex_is_not_reclaimed", func(t *testing.T) {
		notImplemented(t, "atomic watcher lock: mkdir/owner-dir claim with a steal mutex (claimWatchPid is read-check-write)")
	})

	// fm: tests/fm-watcher-lock.test.sh:373
	t.Run("lock_does_not_steal_live_lock", func(t *testing.T) {
		epic := fmEpic(t)
		live := fmLiveChild(t)
		fmWatchPid(t, epic, live)
		release, err := claimWatchPid(epic, false)
		if err == nil {
			release()
			t.Fatal("a live-held lock must be refused")
		}
		if !strings.Contains(err.Error(), strconv.Itoa(live)) {
			t.Fatalf("the refusal must name the live holder pid %d, got %v", live, err)
		}
		if got := readPid(watchPidPath(epic)); got != live {
			t.Fatalf("the live holder's pid was clobbered: %d", got)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:402
	t.Run("lock_empty_pid_uses_minimum_grace", func(t *testing.T) {
		// A just-created, still-empty pidfile is a claim mid-acquire, not a free lock.
		epic := fmEpic(t)
		mustWrite(t, watchPidPath(epic), "")
		release, err := claimWatchPid(epic, false)
		if err == nil {
			release()
			t.Fatal("an empty mid-acquire pidfile must keep a minimum grace, not be stolen at once")
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:422
	t.Run("lock_late_claim_loses_after_recreate", func(t *testing.T) {
		notImplemented(t, "atomic watcher lock: mkdir/owner-dir claim with a steal mutex (claimWatchPid is read-check-write)")
	})

	// fm: tests/fm-watcher-lock.test.sh:454
	t.Run("lock_paused_mid_acquire_claim_fails_during_steal", func(t *testing.T) {
		notImplemented(t, "atomic watcher lock: mkdir/owner-dir claim with a steal mutex (claimWatchPid is read-check-write)")
	})

	// fm: tests/fm-watcher-lock.test.sh:483
	t.Run("watch_restart_rejects_reused_pid", func(t *testing.T) {
		// --replace over a pidfile whose pid now belongs to an unrelated process must not signal that process.
		epic := fmEpic(t)
		pid, exited := fmLiveChildExit(t, "exec sleep 300")
		fmWatchPid(t, epic, pid)
		if release, err := claimWatchPid(epic, true); err == nil {
			release()
		}
		if !fmStillRunning(exited) {
			t.Fatal("cox watch --replace killed an unrelated process whose pid the pidfile named (pid reuse)")
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:514
	t.Run("watch_restart_attaches_to_healthy_peer", func(t *testing.T) {
		// --replace over a verified healthy (TERM-resistant) peer attaches to it instead of fighting it.
		epic := fmEpic(t)
		pid, exited := fmLiveChildExit(t, `trap "" TERM; while :; do sleep 1; done`)
		fmWatchPid(t, epic, pid)
		fmBeacon(t, epic, 0)
		release, err := claimWatchPid(epic, true)
		if err == nil {
			release()
		}
		if err != nil || !fmStillRunning(exited) || readPid(watchPidPath(epic)) != pid {
			t.Fatalf("restart must attach to the healthy peer (no error, peer alive, pidfile unchanged), got err=%v pidfile=%d", err, readPid(watchPidPath(epic)))
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:590
	t.Run("arm_self_eviction_is_loud_without_successor", func(t *testing.T) {
		// The waiter's watcher self-evicts (another live pid takes watch.pid, the beacon stops) with no successor: the
		// waiter must turn that into a loud failure, not wait out MAX_WAIT.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		other := fmLiveChild(t)
		code, out := fmWait(t, epic, 10, func(i int) {
			if i == 1 {
				fmWatchPid(t, epic, other)
				fmBeacon(t, epic, time.Since(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
			}
		})
		fmWantWatcherDownNotice(t, code, out, "self-evicted watcher with no successor")
	})

	// fm: tests/fm-watcher-lock.test.sh:626
	t.Run("arm_attaches_and_waits_for_live_fresh_watcher", func(t *testing.T) {
		// The waiter attaches to a live fresh watcher (no restart, no failure) and fails loudly once that watcher dies
		// without a successor.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		code, out := fmWait(t, epic, 10, func(i int) {
			if i == 1 {
				fmDead(t, epic)
			}
		})
		fmWantWatcherDownNotice(t, code, out, "attached watcher died")
	})

	// fm: tests/fm-watcher-lock.test.sh:703
	t.Run("arm_starts_and_self_heals", func(t *testing.T) {
		for _, row := range []string{"clean", "dead-pid"} {
			epic := fmEpic(t, "s1")
			if row == "dead-pid" {
				fmWatchPid(t, epic, fmDeadPid(t))
				fmBeacon(t, epic, 0) // a fresh-looking leftover beacon must not read as healthy
			}
			fmTickingWatcher(t, 10*time.Second)
			r := fmGuard(t, epic, launchWatcher, fmBlocks(t))
			if len(r.launched) != 1 || r.blocked() {
				t.Fatalf("%s: the guard must start a watcher and confirm it, got %+v", row, r)
			}
			if pid := readPid(watchPidPath(epic)); pid <= 0 || !processAlive(pid) {
				t.Fatalf("%s: the restart was confirmed before the new watcher held watch.pid (holder pid %d is not alive): a leftover beacon must not confirm it", row, pid)
			}
			if row == "dead-pid" {
				if w, _ := wake.Drain(epic, true); len(w) == 0 {
					t.Fatalf("dead-pid: reclaiming a dead watcher must surface a recovery wake for the downtime, got none (out=%q)", r.out)
				}
			}
		}
	})

	// n/a: arm_hup_cleans_child_and_temp_output (fm: tests/fm-watcher-lock.test.sh:758) - the arm-owned one-shot watcher child is firstmate's auto-arm model; cox's watcher is detached and persistent, never a child of the waiter, and the waiter keeps no temp output.

	// fm: tests/fm-watcher-lock.test.sh:788
	t.Run("arm_propagates_immediate_wake_before_confirmation", func(t *testing.T) {
		// The restarted watcher's first pass yields an actionable wake: the same Stop must surface it.
		epic := fmEpic(t, "s1")
		var out bytes.Buffer
		cfg := rewakeCfg{epics: []string{epic}, guardEpics: guardEpics(epic), out: &out, stdout: &out, sleep: noSleep, blocksPath: fmBlocks(t),
			maxWait: time.Second, batchMax: time.Hour, poll: time.Second,
			launch: func(ep string) error { seedWake(t, ep, wake.KindWorkerDone); return nil }}
		if code := runStopRewake(cfg); code != 2 || !strings.Contains(out.String(), "worker_done") {
			t.Fatalf("an immediate wake from the restarted watcher must be propagated (reopen with it), got %d %q", code, out.String())
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:819
	t.Run("arm_waits_for_peer_beacon_after_child_stands_down", func(t *testing.T) {
		// A live peer holds the pidfile but has not ticked yet: the restart stands down and waits (bounded) for the
		// peer's beacon, then attaches, instead of reporting failure at once.
		epic := fmEpic(t, "s1")
		fmWatchPid(t, epic, fmLiveChild(t))
		ticked := make(chan struct{})
		go func() {
			defer close(ticked)
			time.Sleep(300 * time.Millisecond)
			_ = os.WriteFile(filepath.Join(epic, controlDir, "watch", "lasttick"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
		}()
		r := fmGuard(t, epic, launchWatcher, fmBlocks(t))
		<-ticked
		if r.blocked() {
			t.Fatalf("the restart must wait for the live peer's beacon and attach, not fail at once: %+v", r)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:870
	t.Run("arm_fails_loud_when_no_fresh_watcher_confirmable", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		pid, exited := fmLiveChildExit(t, "exec sleep 300")
		fmWatchPid(t, epic, pid)
		fmBeacon(t, epic, time.Since(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
		fmWantBlock(t, fmGuard(t, epic, launchWatcher, fmBlocks(t)), epic, "live unconfirmable holder, stale beacon")
		if !fmStillRunning(exited) {
			t.Fatal("the guard killed the unrelated live holder")
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:970
	t.Run("stopped_watcher_is_live_but_stale_then_exit_is_classified", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		pid, _ := fmLiveChildExit(t, `kill -STOP $$; exec sleep 300`)
		time.Sleep(200 * time.Millisecond)
		fmWatchPid(t, epic, pid)
		fmBeacon(t, epic, time.Since(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
		if !processAlive(pid) {
			t.Fatal("a SIGSTOPped watcher must still read as a live pid")
		}
		if watcherHealthy(epic, time.Now()) {
			t.Fatal("a SIGSTOPped watcher with a stale beacon must not read as healthy")
		}
		notImplemented(t, "watcher cycle-exit ledger: each watcher exit classified (signal/nonzero/clean) with its successor")
	})

	// fm: tests/fm-watcher-lock.test.sh:1005
	t.Run("pid_identity_is_locale_invariant", func(t *testing.T) {
		notImplemented(t, "watcher process identity: watch.pid records pid + start time/cmdline so pid reuse is detectable")
	})

	// fm: tests/fm-watcher-lock.test.sh:1071
	t.Run("proc_pid_identity_ignores_wall_clock_and_detects_pid_reuse", func(t *testing.T) {
		notImplemented(t, "watcher process identity: watch.pid records pid + start time/cmdline so pid reuse is detectable")
	})

	// fm: tests/fm-watcher-lock.test.sh:1102
	t.Run("stale_watch_reclaim_publishes_before_clear", func(t *testing.T) {
		notImplemented(t, "watcher-down recovery episode: durable downtime marker published before a stale lock is cleared")
	})

	// n/a: msys_pid_identity_uses_proc (fm: tests/fm-watcher-lock.test.sh:1144) - MSYS/Windows process identity; cox ships darwin and linux only.
}

// fmWatchArm translates the cmd/cox half of tests/fm-watch-arm.test.sh. Firstmate's arm attaches to a watcher cycle
// and closes with the cycle's reason; cox's is the stop-rewake idle waiter (fmWait) over a live watcher, and its re-arm
// is the guard's restart of a dead watcher. Firstmate's recovery episode (.watcher-down marker + recovery generation +
// rearm-resurface wake) has no cox counterpart; the durable wake queue with gen-based ack-through is the name-mapped
// part.
func fmWatchArm(t *testing.T) {
	// fm: tests/fm-watch-arm.test.sh:184
	t.Run("attached_arm_reports_the_delivered_wake", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		code, out := fmWait(t, epic, 10, func(i int) {
			if i == 1 {
				seedWake(t, epic, wake.KindWorkerDone)
			}
		})
		if code != 2 || !strings.Contains(out, "worker_done") || strings.Contains(out, "not alive") {
			t.Fatalf("the waiter must close on the delivered wake, not a failure, got %d %q", code, out)
		}
		notImplemented(t, "watcher cycle-exit ledger: each watcher exit classified (signal/nonzero/clean) with its successor")
	})

	// fm: tests/fm-watch-arm.test.sh:214
	t.Run("attached_arm_reports_the_delivered_wake_after_drain", func(t *testing.T) {
		// The handling turn drains and acks the delivered wake before the waiter looks: no false failure.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		_, out := fmWait(t, epic, 3, func(i int) {
			if i == 1 {
				seedWake(t, epic, wake.KindWorkerDone)
				w, _ := wake.Drain(epic, true)
				if err := wake.AckThrough(epic, w[len(w)-1].Gen); err != nil {
					t.Fatal(err)
				}
			}
		})
		if strings.Contains(out, "not alive") || strings.Contains(out, "FAILED") {
			t.Fatalf("an already-handled wake must not be reported as a failed cycle, got %q", out)
		}
	})

	// fm: tests/fm-watch-arm.test.sh:245
	t.Run("attached_arm_still_fails_on_a_wake_it_did_not_deliver", func(t *testing.T) {
		// A foreign producer queues a wake while the watcher dies: the waiter must still say the watcher is gone.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		code, out := fmWait(t, epic, 10, func(i int) {
			if i == 1 {
				seedWake(t, epic, wake.KindStatus)
				fmDead(t, epic)
			}
		})
		fmWantWatcherDownNotice(t, code, out, "watcher died while a foreign wake was queued")
	})

	// fm: tests/fm-watch-arm.test.sh:270
	t.Run("rearm_resurfaces_durable_queue_and_remote_open_decision", func(t *testing.T) {
		// Two durable wakes queued while no watcher ran: the re-arm (guard restart) must surface them, and a drain
		// must show both. (The remote secondmate decision half is firstmate-only.)
		epic := fmEpic(t, "s1")
		fmDead(t, epic)
		seedWake(t, epic, wake.KindStatus)
		seedWake(t, epic, wake.KindStatus)
		var out bytes.Buffer
		cfg := rewakeCfg{epics: []string{epic}, guardEpics: guardEpics(epic), out: &out, stdout: &out, sleep: noSleep, blocksPath: fmBlocks(t),
			maxWait: time.Second, batchMax: time.Hour, poll: time.Second, launch: func(string) error { return nil }}
		code := runStopRewake(cfg)
		if w, _ := wake.Drain(epic, true); len(w) != 2 {
			t.Fatalf("both downtime wakes must stay durable for the drain, got %d", len(w))
		}
		if code != 2 || !strings.Contains(out.String(), "cox wake drain") {
			t.Fatalf("the re-arm must surface the wakes queued during downtime, got %d %q", code, out.String())
		}
	})

	// fm: tests/fm-watch-arm.test.sh:398
	t.Run("slow_rearm_recovery_is_still_surfaced", func(t *testing.T) {
		epic := fmEpic(t, "s1")
		fmDead(t, epic)
		seedWake(t, epic, wake.KindStatus)
		var out bytes.Buffer
		cfg := rewakeCfg{epics: []string{epic}, guardEpics: guardEpics(epic), out: &out, stdout: &out, sleep: noSleep, blocksPath: fmBlocks(t),
			maxWait: time.Second, batchMax: time.Hour, poll: time.Second,
			launch: func(string) error { time.Sleep(1500 * time.Millisecond); return nil }}
		if code := runStopRewake(cfg); code != 2 || !strings.Contains(out.String(), "cox wake drain") {
			t.Fatalf("a slow re-arm must still surface the wake queued during downtime, got %d %q", code, out.String())
		}
	})

	// fm: tests/fm-watch-arm.test.sh:450
	t.Run("marker_publish_failure_retains_recovery_evidence", func(t *testing.T) {
		notImplemented(t, "watcher-down recovery episode: durable downtime marker published before a stale lock is cleared")
	})

	// fm: tests/fm-watch-arm.test.sh:480
	t.Run("delivery_gap_wake_is_recovered_once", func(t *testing.T) {
		// A wake queued after the handling drain is recovered once by the next waiter, which then stays stable.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		seedWake(t, epic, wake.KindWorkerDone)
		code, out := fmWait(t, epic, 3, func(int) {})
		if code != 2 || !strings.Contains(out, "worker_done") {
			t.Fatalf("the successor waiter must recover the gap wake, got %d %q", code, out)
		}
		w, _ := wake.Drain(epic, true)
		if err := wake.AckThrough(epic, w[len(w)-1].Gen); err != nil {
			t.Fatal(err)
		}
		if _, out := fmWait(t, epic, 3, func(int) {}); strings.Contains(out, "worker_done") {
			t.Fatalf("after the ack the next waiter must not replay the recovered wake, got %q", out)
		}
	})

	// fm: tests/fm-watch-arm.test.sh:519
	t.Run("interrupted_handling_is_redrained_on_rearm", func(t *testing.T) {
		// A delivered wake whose handling was interrupted (never acked) stays durable and is re-surfaced by the next
		// waiter; the completed replay acks it through its gen.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		seedWake(t, epic, wake.KindWorkerDone)
		for i := 0; i < 2; i++ {
			if code, out := fmWait(t, epic, 3, func(int) {}); code != 2 || !strings.Contains(out, "worker_done") {
				t.Fatalf("waiter %d must re-surface the unacked wake, got %d %q", i, code, out)
			}
		}
		w, _ := wake.Drain(epic, true)
		if err := wake.AckThrough(epic, w[len(w)-1].Gen); err != nil {
			t.Fatal(err)
		}
		if w, _ := wake.Drain(epic, true); len(w) != 0 {
			t.Fatalf("the acknowledged replay must leave the queue, got %d", len(w))
		}
	})

	// fm: tests/fm-watch-arm.test.sh:612
	t.Run("malformed_marker_is_quarantined_once", func(t *testing.T) {
		notImplemented(t, "watcher-down recovery episode: durable downtime marker published before a stale lock is cleared")
	})

	// fm: tests/fm-watch-arm.test.sh:638
	t.Run("recovery_consumption_serializes_queue_publication", func(t *testing.T) {
		// A wake published while a waiter is live after a handled recovery is surfaced.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		code, out := fmWait(t, epic, 5, func(i int) {
			if i == 2 {
				seedWake(t, epic, wake.KindStuck)
			}
		})
		if code != 2 || !strings.Contains(out, "stuck") {
			t.Fatalf("a wake published during the wait must be surfaced, got %d %q", code, out)
		}
	})

	// fm: tests/fm-watch-arm.test.sh:665
	t.Run("restart_preserves_recovery_across_reused_pid_lock", func(t *testing.T) {
		// Restart over a pidfile whose pid was reused: publish recovery, and never signal the unrelated process.
		epic := fmEpic(t, "s1")
		pid, exited := fmLiveChildExit(t, "exec sleep 300")
		fmWatchPid(t, epic, pid)
		if release, err := claimWatchPid(epic, true); err == nil {
			release()
		}
		if !fmStillRunning(exited) {
			t.Fatal("restart signaled the unrelated process whose pid was reused")
		}
		if w, _ := wake.Drain(epic, true); len(w) == 0 {
			t.Fatal("restart cleared the reused-pid lock without a recovery wake")
		}
	})

	// fm: tests/fm-watch-arm.test.sh:693
	t.Run("markerless_legacy_queue_is_recovered_on_arm", func(t *testing.T) {
		// A queue row left from before any recovery bookkeeping is still surfaced by the next waiter.
		epic := fmEpic(t, "s1")
		fmHealthy(t, epic)
		seedWake(t, epic, wake.KindWorkerDone)
		if code, out := fmWait(t, epic, 3, func(int) {}); code != 2 || !strings.Contains(out, "worker_done") {
			t.Fatalf("a legacy queued wake must be recovered, got %d %q", code, out)
		}
	})

	// fm: tests/fm-watch-arm.test.sh:721
	t.Run("handling_window_close_keeps_the_acknowledgement_valid", func(t *testing.T) {
		// The ack printed for a drain stays valid when newer wakes land during handling: it consumes only through its
		// gen, the newer one survives and is acked by the next drain.
		epic := fmEpic(t, "s1")
		seedWake(t, epic, wake.KindWorkerDone)
		first, _ := wake.Drain(epic, true)
		gen := first[len(first)-1].Gen
		seedWake(t, epic, wake.KindStatus)
		if err := wake.AckThrough(epic, gen); err != nil {
			t.Fatalf("the printed acknowledgement was rejected after a newer publication: %v", err)
		}
		rest, _ := wake.Drain(epic, true)
		if len(rest) != 1 || rest[0].Gen <= gen {
			t.Fatalf("the newer wake must survive the older ack, got %+v", rest)
		}
		if err := wake.AckThrough(epic, rest[0].Gen); err != nil {
			t.Fatal(err)
		}
		notImplemented(t, "watcher-down recovery episode: a recovery generation that the ack retires, kept across publications during handling")
	})

	// fm: tests/fm-watch-arm.test.sh:789
	t.Run("moved_generation_acknowledgement_is_self_healing", func(t *testing.T) {
		// Replaying a stale ack degrades safely: no error, no over-consumption; a larger ack consumes what it names.
		epic := fmEpic(t, "s1")
		seedWake(t, epic, wake.KindWorkerDone)
		first, _ := wake.Drain(epic, true)
		gen := first[len(first)-1].Gen
		if err := wake.AckThrough(epic, gen); err != nil {
			t.Fatal(err)
		}
		seedWake(t, epic, wake.KindStatus)
		if err := wake.AckThrough(epic, gen); err != nil {
			t.Fatalf("a replayed stale acknowledgement must degrade safely, got %v", err)
		}
		if w, _ := wake.Drain(epic, true); len(w) != 1 {
			t.Fatalf("a stale acknowledgement must not consume a wake above its gen, got %d left", len(w))
		}
		if err := wake.AckThrough(epic, 999); err != nil {
			t.Fatal(err)
		}
		if w, _ := wake.Drain(epic, true); len(w) != 0 {
			t.Fatalf("the sequence alone owns consumption, got %d left", len(w))
		}
		notImplemented(t, "watcher-down recovery episode: a moved recovery generation names its own remedy (re-run the drain)")
	})

	// n/a: arm_refuses_an_unusable_launch_confirm_window (fm: tests/fm-watch-arm.test.sh:893) - the FM_PROCEVENT_LAUNCH_CONFIRM_SECONDS knob is firstmate-only; cox's confirm window (watcherConfirmWait) is compiled in, with no operator input to validate.
}

// fmProbe stubs the leader-liveness probe: the recorded leader handle is live or dead, and a backend exists to ask.
func fmProbe(t *testing.T, live bool) {
	t.Helper()
	prev := probeLeaderHandle
	probeLeaderHandle = func(string, string) (bool, bool) { return live, true }
	t.Cleanup(func() { probeLeaderHandle = prev })
}

// fmDocTurnendGuard translates the predicates of docs/turnend-guard.md that no suite case already pins. The report
// lists every predicate considered and the suite case that pins the rest.
func fmDocTurnendGuard(t *testing.T) {
	// fm: docs/turnend-guard.md:34
	t.Run("foreign_live_owner_takes_diagnostic_exit", func(t *testing.T) {
		// A live session owner this terminal does not own: the Claude guard allows the stop safely (it cannot repair
		// without stealing the owner's lock) AND emits a read-only ownership diagnostic. Cox's analogue is a different,
		// still-live leader handle recorded in .cox/leader.
		ws, epic := fmWorkspace(t, "e1", "s1")
		t.Chdir(ws)
		if err := state.WriteLeader(epic, "term_owner"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ORCA_TERMINAL_HANDLE", "term_other")
		fmProbe(t, true)
		r := fmGuard(t, epic, nil, fmBlocks(t))
		if r.blocked() {
			t.Fatalf("a foreign live owner must not be blocked on (an unbounded loop), got %+v", r)
		}
		if !strings.Contains(r.out, "term_owner") {
			t.Fatalf("the safe exit must emit a read-only ownership diagnostic naming the live owner, got %q", r.out)
		}
	})

	// fm: docs/turnend-guard.md:38
	t.Run("dead_foreign_owner_keeps_ordinary_guard", func(t *testing.T) {
		// A dead (or malformed/absent) owner record does not satisfy the foreign-owner exception: the ordinary guard runs
		// and blocks a blind stop. Cox: the recorded leader handle is dead and this terminal leads the epic's workspace.
		ws, epic := fmWorkspace(t, "e1", "s1")
		t.Chdir(ws)
		if err := state.WriteLeader(epic, "term_dead"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ORCA_TERMINAL_HANDLE", "term_new")
		fmProbe(t, false)
		fmWantBlock(t, fmGuard(t, epic, fmLaunchRefused, fmBlocks(t)), epic, "dead recorded owner, unwatched epic")
	})

	// fm: docs/turnend-guard.md:71
	t.Run("grace_is_poll_derived_with_headroom", func(t *testing.T) {
		// A healthy watcher's beacon legitimately ages a full poll plus its pass time between touches, so the grace must
		// never drop below a floor with headroom (firstmate: a flat 300s default for the strict guard, max(300s, poll+60s)
		// where the grace derives from the poll). A live watcher whose beacon is poll + 59s old (a slow pass) is healthy.
		epic := fmEpic(t, "s1")
		fmWatchPid(t, epic, os.Getpid())
		fmBeacon(t, epic, watch.DefaultPoll+59*time.Second)
		if !watcherHealthy(epic, time.Now()) {
			t.Fatalf("a live watcher whose beacon is %s old (poll %s + a 59s pass) must still read healthy; cox's grace is 3 x poll = %s",
				watch.DefaultPoll+59*time.Second, watch.DefaultPoll, 3*watch.DefaultPoll)
		}
	})

	// fm: docs/turnend-guard.md:118
	t.Run("block_budget_is_below_claude_override", func(t *testing.T) {
		// The re-block budget defaults to 3, below Claude's own 8-block override, so the guard's bound bites first.
		if rewakeBlockBudget != 3 || rewakeBlockBudget >= 8 {
			t.Fatalf("block budget %d must be 3 and below Claude's 8-block override", rewakeBlockBudget)
		}
	})

	// fm: docs/turnend-guard.md:192
	t.Run("no_shell_ampersand_supervision", func(t *testing.T) {
		// No harness adapter manufactures supervision by backgrounding with a shell ampersand.
		shims, _ := filepath.Glob(filepath.Join("..", "..", "hooks", "*.sh"))
		if len(shims) == 0 {
			t.Fatal("no hook shims found under hooks/")
		}
		for _, p := range shims {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(b), "\n") {
				code := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
				if strings.HasSuffix(code, "&") && !strings.HasSuffix(code, "&&") {
					t.Errorf("%s backgrounds a process with a shell ampersand: %q", p, line)
				}
			}
		}
	})
}

// fmDocWatcherContinuity translates the cmd/cox-reachable predicates of docs/watcher-continuity.md that no suite
// case already pins (the watcher-side ones are in internal/watch/port_lifecycle_test.go).
func fmDocWatcherContinuity(t *testing.T) {
	// fm: docs/watcher-continuity.md:19
	t.Run("dead_session_owner_is_reclaimed_before_arming", func(t *testing.T) {
		// A session-lock owner that fails liveness is reclaimed before any arm state changes, so a leader restart does
		// not orphan the epic (B-54): the restarted terminal re-binds .cox/leader, and the watcher rings it next.
		ws, epic := fmWorkspace(t, "e1", "s1")
		t.Chdir(ws)
		if err := state.WriteLeader(epic, "term_dead"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ORCA_TERMINAL_HANDLE", "term_new")
		fmProbe(t, false)
		if g := guardEpics(epic); len(g) != 1 {
			t.Fatalf("the restarted leader must still guard its epic, got %v", g)
		}
		if got := readLeader(epic); got != "term_new" {
			t.Fatalf("the dead owner must be reclaimed (re-bound to the live terminal), .cox/leader=%q", got)
		}
	})

	// fm: docs/watcher-continuity.md:24
	t.Run("cycle_end_failure_is_benign_when_watcher_live", func(t *testing.T) {
		// After a failed arm the hook rechecks the watcher: when a live, fresh watcher now exists (a peer came up), the
		// failure is benign and the hook continues silently instead of reporting it.
		epic := fmEpic(t, "s1")
		r := fmGuard(t, epic, func(ep string) error {
			fmHealthy(t, ep) // a peer watcher came up while this restart lost the race
			return errWatchRefused
		}, fmBlocks(t))
		if r.blocked() {
			t.Fatalf("a failed restart with a live fresh watcher in place must be benign (recheck, continue silently), got %+v", r)
		}
	})

	// fm: docs/watcher-continuity.md:46
	t.Run("no_pretooluse_watcher_denial", func(t *testing.T) {
		// No PreToolUse hook denies fleet commands based on watcher status.
		var m struct {
			Hooks map[string]json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal(hooks.JSON, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m.Hooks["PreToolUse"]; ok {
			t.Fatal("the leader hook manifest registers a PreToolUse hook")
		}
	})

	// fm: docs/watcher-continuity.md:117
	t.Run("watcher_hup_runs_exit_cleanup", func(t *testing.T) {
		// HUP and TERM both run the watcher's exit cleanup (pidfile release), including mid-poll. cmdWatch notifies only
		// SIGTERM and SIGINT, so a HUP kills it without releasing .cox/watch.pid.
		notImplemented(t, "watcher HUP handling: SIGHUP must run the same pidfile release as TERM/INT")
	})
}
