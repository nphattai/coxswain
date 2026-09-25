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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/supervision"
	"github.com/nphattai/coxswain/internal/wake"
)

// notImplementedLC fails the case naming the cox mechanism firstmate pins and cox lacks (contract rule 3).
func notImplementedLC(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("cox gap: %s", mechanism)
}

// lcEpic makes a temp epic dir with a .cox control tree (the watcher never creates .cox itself, mkdirControl).
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

// lcWatchPid records pid as the epic's watcher in <epic>/.cox/watch.pid, the file `cox watch` claims.
func lcWatchPid(t *testing.T, epic string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(epic, state.ControlDir, "watch.pid"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lcLiveChild starts a long sleep standing in for another live process; cleanup kills it.
func lcLiveChild(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

var (
	lcCoxOnce sync.Once
	lcCoxPath string
	lcCoxErr  error
)

// lcCoxBin builds the real cox binary once per test binary (the ledger is written by `cox watch` and the stop-rewake
// waiter, which live in cmd/cox).
func lcCoxBin(t *testing.T) string {
	t.Helper()
	lcCoxOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lc-cox-bin")
		if err != nil {
			lcCoxErr = err
			return
		}
		lcCoxPath = filepath.Join(dir, "cox")
		cmd := exec.Command("go", "build", "-o", lcCoxPath, "./cmd/cox")
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			lcCoxErr = fmt.Errorf("build cox: %v\n%s", err, out)
		}
	})
	if lcCoxErr != nil {
		t.Fatal(lcCoxErr)
	}
	return lcCoxPath
}

// lcCoxEnv is a real cox child's environment: a fake orca that fails every call (the watcher reports its mailbox
// unreadable and keeps running), a fake run id, and a private TMPDIR for the waiter's lock.
func lcCoxEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "orca"), []byte("#!/bin/sh\necho '{\"ok\":false,\"error\":{\"message\":\"fake orca\"}}'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return append(append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "ORCA_RUN_ID=run-fake"), extra...)
}

// lcCox starts a real cox child; cleanup kills it. done closes once it has been reaped (ProcessState is then set).
func lcCox(t *testing.T, env []string, args ...string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	cmd := exec.Command(lcCoxBin(t), args...)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	return cmd, done
}

// lcProc is a real cox child and the channel that closes once it has been reaped.
type lcProc struct {
	cmd  *exec.Cmd
	done chan struct{}
	epic string
}

func (p lcProc) pid() int { return p.cmd.Process.Pid }

// lcCoxWatch starts `cox watch --epic` and waits until it holds watch.pid and has published its own beacon: a watcher
// links its predecessor's ledger record before its first pass, so a fresh beacon proves the link is written.
func lcCoxWatch(t *testing.T, epic string, env ...string) lcProc {
	t.Helper()
	_ = os.Remove(filepath.Join(epic, state.ControlDir, "watch", "lasttick")) // a predecessor's beacon proves nothing
	cmd, done := lcCox(t, lcCoxEnv(t, env...), "watch", "--epic", epic)
	if !lcWaitFor(20*time.Second, func() bool {
		pid, _ := ReadPid(epic)
		_, err := os.Stat(filepath.Join(epic, state.ControlDir, "watch", "lasttick"))
		return pid == cmd.Process.Pid && err == nil
	}) {
		t.Fatal("the real watcher never claimed watch.pid and published its beacon")
	}
	return lcProc{cmd, done, epic}
}

// lcCoxStop signals a real watcher and waits until it has exited (its exit record is written before it exits).
func lcCoxStop(t *testing.T, p lcProc, s syscall.Signal) {
	t.Helper()
	_ = p.cmd.Process.Signal(s)
	if !lcExited(p.done, 20*time.Second) {
		t.Fatalf("the watcher did not exit on %v", s)
	}
}

// lcCoxWaiter starts the stop-rewake waiter (the attached arm) for epic and waits until its single-waiter lock is
// published, which it writes only after its exit-signal handler is live - or until it has already exited (a waiter
// with a wake queued delivers it and releases the lock at once).
func lcCoxWaiter(t *testing.T, epic string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	tmp := t.TempDir()
	cmd, done := lcCox(t, lcCoxEnv(t, "TMPDIR="+tmp, "ORCA_TERMINAL_HANDLE=term_lc"), "hook", "stop-rewake", "--epic", epic)
	lock := filepath.Join(tmp, "cox-rewake-term_lc.lock")
	if !lcWaitFor(20*time.Second, func() bool {
		_, err := os.Stat(lock)
		return err == nil || lcExited(done, 0)
	}) {
		t.Fatal("the waiter never published its lock")
	}
	return cmd, done
}

// lcLedger reads the cycle-exit ledger.
func lcLedger(epic string) string {
	b, _ := os.ReadFile(filepath.Join(epic, state.ControlDir, "watch-cycle-exits.log"))
	return string(b)
}

// lcWaitFor polls cond every 10ms up to d.
func lcWaitFor(d time.Duration, cond func() bool) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// lcWatcherLock translates the watcher-side half of tests/fm-watcher-lock.test.sh (the rest is in
// cmd/cox/port_turnend_test.go under the same suite name).
func lcWatcherLock(t *testing.T) {
	// fm: tests/fm-watcher-lock.test.sh:560
	t.Run("watcher_self_evicts_on_lock_takeover", func(t *testing.T) {
		// The running watcher (this process holds watch.pid) must stand down once watch.pid names another live process,
		// and must not clobber the new holder's pidfile.
		epic := lcEpic(t)
		lcWatchPid(t, epic, os.Getpid())
		stop, done := lcRun(lcWatcher(epic))
		defer func() { close(stop); lcExited(done, 2*time.Second) }() // never leave Run writing into a removed TempDir
		if !lcWaitFor(2*time.Second, func() bool {
			_, err := os.Stat(filepath.Join(epic, state.ControlDir, "watch", "lasttick"))
			return err == nil
		}) {
			t.Fatal("watcher did not publish its beacon")
		}
		other := lcLiveChild(t)
		lcWatchPid(t, epic, other)
		if !lcExited(done, 2*time.Second) {
			t.Fatal("watcher did not self-evict after watch.pid was taken over by another live process")
		}
		if b, _ := os.ReadFile(filepath.Join(epic, state.ControlDir, "watch.pid")); string(b) != strconv.Itoa(other) {
			t.Fatalf("the self-evicting watcher clobbered the new holder's pidfile: %q", b)
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:667
	t.Run("attached_arm_signal_is_recorded_in_cycle_ledger", func(t *testing.T) {
		// cox's attached arm is the stop-rewake waiter over a live `cox watch`; the ledger is <epic>/.cox/watch-cycle-exits.log.
		epic := lcEpic(t)
		w := lcCoxWatch(t, epic)
		arm, armDone := lcCoxWaiter(t, epic)
		if err := arm.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("could not signal the attached arm: %v", err)
		}
		if !lcExited(armDone, 10*time.Second) {
			t.Fatal("the signalled attached arm did not exit")
		}
		if code := arm.ProcessState.ExitCode(); code != 143 {
			t.Fatalf("attached arm did not exit with TERM status (got %d)", code)
		}
		want := regexp.MustCompile(`arm_pid=` + strconv.Itoa(arm.Process.Pid) + `\twatcher_pid=` + strconv.Itoa(w.pid()) +
			`\torigin=attached\t.*exit_code=143\tsignal=TERM\treason=arm-interrupted\t`)
		if !want.MatchString(lcLedger(epic)) {
			t.Fatalf("attached arm signal was not recorded in the lifecycle ledger:\n%s", lcLedger(epic))
		}
		if lcExited(w.done, 200*time.Millisecond) {
			t.Fatal("signaling an attached arm terminated the peer watcher")
		}
	})

	// fm: tests/fm-watcher-lock.test.sh:899
	t.Run("cycle_exit_ledger_links_successor_and_stays_bounded", func(t *testing.T) {
		// A cox cycle is one `cox watch` lifetime (the watcher does not exit on a wake); its attached waiter closes on the
		// delivered wake (fm's actionable-check is cox's attached-delivered-wake), and the next started watcher links the
		// ended cycle's record as its verified successor.
		epic := lcEpic(t)
		control := filepath.Join(epic, state.ControlDir)
		check := filepath.Join(control, "task.check.sh")
		if err := os.WriteFile(check, []byte("#!/usr/bin/env bash\nprintf 'done: synthetic cycle\\n'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := supervision.Register(control, "task"); err != nil {
			t.Fatalf("could not register cycle-ledger check: %v", err)
		}
		first := lcCoxWatch(t, epic, "COX_CHECK_INTERVAL=0")
		if !lcWaitFor(10*time.Second, func() bool {
			ws, _ := wake.Load(epic)
			for _, w := range ws {
				if w.Kind == wake.KindCheck {
					return true
				}
			}
			return false
		}) {
			t.Fatal("the first cycle did not queue its actionable check wake")
		}
		arm, armDone := lcCoxWaiter(t, epic)
		if !lcExited(armDone, 10*time.Second) {
			t.Fatal("the attached arm did not close on the delivered check wake")
		}
		if !regexp.MustCompile(`arm_pid=` + strconv.Itoa(arm.Process.Pid) + `\t.*origin=attached\t.*reason=attached-delivered-wake\t.*successor=none`).MatchString(lcLedger(epic)) {
			t.Fatalf("first ledger record omitted its actionable classification:\n%s", lcLedger(epic))
		}
		if err := supervision.Unregister(control, "task"); err != nil {
			t.Fatal(err)
		}
		lcCoxStop(t, first, syscall.SIGTERM)
		successor := lcCoxWatch(t, epic)
		if !regexp.MustCompile(`watcher_pid=` + strconv.Itoa(first.pid()) + `\torigin=started\t.*successor=started:` + strconv.Itoa(successor.pid()) + `\n`).MatchString(lcLedger(epic)) {
			t.Fatalf("predecessor ledger record was not linked to its verified successor:\n%s", lcLedger(epic))
		}
		lcCoxStop(t, successor, syscall.SIGHUP)

		// Enough short cycles to cross a deliberately small cap; only complete records are kept.
		for i := 0; i < 6; i++ {
			lcCoxStop(t, lcCoxWatch(t, epic, "COX_WATCH_CYCLE_LOG_MAX_BYTES=1400", "COX_WATCH_CYCLE_LOG_KEEP_LINES=2"), syscall.SIGHUP)
		}
		ledger := lcLedger(epic)
		if len(ledger) > 1400 {
			t.Fatalf("cycle ledger exceeded its configured cap (%d bytes)", len(ledger))
		}
		record := regexp.MustCompile(`^arm_pid=.*watcher_pid=.*started_at=.*ended_at=.*exit_code=.*signal=.*reason=.*beacon_age=.*lock_before=.*lock_after=.*successor=`)
		for _, l := range strings.Split(strings.TrimSuffix(ledger, "\n"), "\n") {
			if !record.MatchString(l) {
				t.Fatalf("bounded lifecycle ledger contains a partial or malformed record: %q", l)
			}
		}
	})
}

// lcWatchArm translates the watcher-side case of tests/fm-watch-arm.test.sh.
func lcWatchArm(t *testing.T) {
	// fm: tests/fm-watch-arm.test.sh:860
	t.Run("downtime_marker_does_not_follow_symlink", func(t *testing.T) {
		// Watcher-published state must never follow a symlink planted in its place: cox's beacon (markTick) is the
		// state file its watcher publishes every pass.
		epic := lcEpic(t)
		sentinel := filepath.Join(t.TempDir(), "sentinel")
		if err := os.WriteFile(sentinel, []byte("must remain intact\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		tick := filepath.Join(epic, state.ControlDir, "watch", "lasttick")
		if err := os.Symlink(sentinel, tick); err != nil {
			t.Fatal(err)
		}
		lcWatcher(epic).markTick()
		if b, _ := os.ReadFile(sentinel); string(b) != "must remain intact\n" {
			t.Fatalf("the beacon write followed a symlink and overwrote its target: %q", b)
		}
		if fi, err := os.Lstat(tick); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			t.Fatalf("the beacon was not published as a regular file: %v %v", fi, err)
		}
	})
}

// lcWatchRecoveryLoop translates tests/fm-watch-recovery-loop.test.sh.
func lcWatchRecoveryLoop(t *testing.T) {
	// fm: tests/fm-watch-recovery-loop.test.sh:67
	t.Run("unacknowledged_recovery_is_announced_once_per_generation", func(t *testing.T) {
		// An unacknowledged backlog is announced to the leader once, not re-announced every cycle past the old ~52s
		// loop period, and the watcher keeps running. Cox's announcement is the leader doorbell (nudgeLeader).
		epic := lcEpic(t)
		if err := os.WriteFile(filepath.Join(epic, state.ControlDir, "leader"), []byte("term_leader"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: "seed", Kind: wake.KindStuck, Note: "seed recovery"}); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
		b := fake.New()
		w := &Watcher{EpicDir: epic, Backend: b, Now: func() time.Time { return now }}
		for elapsed := 0; elapsed <= 60; elapsed += 5 {
			now = now.Add(5 * time.Second)
			if _, err := w.Tick(); err != nil {
				t.Fatal(err)
			}
		}
		rings := 0
		for _, c := range b.Calls {
			if c == "Send" {
				rings++
			}
		}
		if rings != 1 {
			t.Fatalf("an unacknowledged backlog must be announced exactly once across 60s of ticks, got %d doorbells", rings)
		}
	})

	// fm: tests/fm-watch-recovery-loop.test.sh:171
	t.Run("handling_successor_does_not_go_blind", func(t *testing.T) {
		// A successor watcher started while a recovery is pending enters its poll loop and surfaces a real worker event
		// within a bounded startup-and-poll budget, instead of going blind.
		epic := lcEpic(t)
		if _, err := wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: "seed", Kind: wake.KindStatus, Note: "pending recovery"}); err != nil {
			t.Fatal(err)
		}
		b := fake.New()
		mb := b.Mail().(*fake.Mailbox)
		mb.Delivery = "dlv"
		// The fake mailbox is not safe for concurrent use, so the worker event is queued before the loop starts; the
		// watcher must still reach it from inside its poll loop.
		mb.Queue = []backend.Message{{ID: "relay_crew", From: "dispatch:ctx_crew", Type: "worker_done", Subject: "crew finished its task", Payload: `{"dispatchId":"ctx_crew"}`}}
		w := &Watcher{EpicDir: epic, Backend: b}
		stop, done := lcRun(w)
		defer func() { close(stop); lcExited(done, 2*time.Second) }()
		if !lcWaitFor(2*time.Second, func() bool {
			ws, _ := wake.Drain(epic, true)
			for _, x := range ws {
				if x.Kind == wake.KindWorkerDone {
					return true
				}
			}
			return false
		}) {
			t.Fatal("the successor watcher did not surface the worker event within its poll budget")
		}
		if lcExited(done, 0) {
			t.Fatal("the successor watcher exited instead of supervising")
		}
	})
}

// lcDocWatcherContinuity translates the watcher-side predicates of docs/watcher-continuity.md that no suite case
// already pins.
func lcDocWatcherContinuity(t *testing.T) {
	// fm: docs/watcher-continuity.md:116
	t.Run("only_the_watcher_writes_the_beacon", func(t *testing.T) {
		// Only the watcher's own loop touches the beacon; no helper (cox watch --once, a probe) can make a wedged
		// watcher look healthy.
		epic := lcEpic(t)
		if _, err := lcWatcher(epic).Tick(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(epic, state.ControlDir, "watch", "lasttick")); err == nil {
			t.Fatal("a one-shot pass outside the watcher loop wrote the beacon")
		}
	})
}
