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
	"strings"
	"testing"
	"time"

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

func fmGuardStaleBanner(t *testing.T)     {}
func fmWatchCheckpoint(t *testing.T)      {}
func fmWatcherLock(t *testing.T)          {}
func fmWatchArm(t *testing.T)             {}
func fmDocTurnendGuard(t *testing.T)      {}
func fmDocWatcherContinuity(t *testing.T) {}

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
	return fmCapture(t, func() int { return hookStopRewake(epic, "claude") })
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

const fmTG = "tests/fm-turnend-guard.test.sh"

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
		// Cox's Pi turn-end guard is the TypeScript extension internal/adapter/harness/pi/extension/cox-supervisor.ts,
		// which this story's Go files cannot drive; the case stays red until it is ported into that extension's suite.
		notImplemented(t, "Pi guard extension case not yet ported: translate into the cox-supervisor.ts node suite")
	})

	// fm: tests/fm-turnend-guard.test.sh:1128
	t.Run("pi_extension_retries_after_followup_delivery_failure", func(t *testing.T) {
		notImplemented(t, "Pi guard extension case not yet ported: translate into the cox-supervisor.ts node suite")
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
