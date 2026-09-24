package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/supervision"
	"github.com/nphattai/coxswain/internal/wake"
	"github.com/nphattai/coxswain/internal/watch"
)

// watchInfo is the doctor/state view of one epic's watcher: whether a watch.pid exists, its pid and whether that
// process is alive, and the age of the last completed tick from <epic>/.cox/watch/lasttick (the watcher writes it each
// pass, M14).
type watchInfo struct {
	Present  bool
	Pid      int
	Alive    bool
	LastTick time.Time
	HasTick  bool
}

// watcherInfo reads the epic's watch.pid and last-tick file into a watchInfo. A missing pidfile is Present=false; a
// pidfile naming a dead process is Present=true, Alive=false (the case the dogfood missed: the watcher died silently).
func watcherInfo(epicDir string) watchInfo {
	var wi watchInfo
	if pid := readPid(watchPidPath(epicDir)); pid > 0 {
		wi.Present = true
		wi.Pid = pid
		wi.Alive = processAlive(pid)
	}
	if b, err := os.ReadFile(filepath.Join(epicDir, controlDir, "watch", "lasttick")); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil {
			wi.LastTick = t
			wi.HasTick = true
		}
	}
	return wi
}

// watcherLine renders the watcher status for the human doctor/state output.
func watcherLine(wi watchInfo, now time.Time) string {
	if !wi.Present {
		return "watcher: none (no watch.pid; run cox watch --epic <dir>)"
	}
	alive := "dead"
	if wi.Alive {
		alive = "alive"
	}
	tick := "last tick unknown"
	if wi.HasTick {
		tick = "last tick " + agoStr(now.Sub(wi.LastTick)) + " ago"
	}
	return fmt.Sprintf("watcher: pid %d %s, %s", wi.Pid, alive, tick)
}

// watcherIssue is the read-only pull warning `cox doctor` prints for an epic (guardBanner, readOnly): "" when the epic
// needs no supervision or its watcher is healthy.
func watcherIssue(epicDir string) string { return guardBanner(epicDir, true) }

// staleBannerMu serializes this process's own episode claims; the file lock serializes processes (it keys on the pid).
var staleBannerMu sync.Mutex

// guardBanner is firstmate's pull-based guard (bin/fm-guard.sh, persistent-watcher model) for one epic, the warning
// supervision commands print mid-turn. When the epic needs supervision and its watcher is not healthy (watcherHealthy)
// it returns the full WATCHER DOWN banner once per down episode - keyed on the failing condition (no-watcher: a fresh
// beacon with no live identity-matched watcher; stale-beacon: otherwise), never on the beacon mtime - claimed under
// <epic>/.cox/guard-watcher-stale-banner(.lock), and a one-line reminder for every later call in that episode. A healthy
// or no-need call ends the episode. A read-only caller (`cox doctor`; firstmate's session-start with
// FM_GUARD_READ_ONLY=1) never creates, updates or clears the marker or its lock: it prints the full banner until a
// writable caller has claimed the episode, then the reminder. The queued-wakes warning is independent of the dedup and
// follows the banner.
func guardBanner(epicDir string, readOnly bool) string {
	marker := coxPath(epicDir, "guard-watcher-stale-banner")
	need := supervisionNeeds(epicDir)
	if !need.needed() {
		if !readOnly {
			_ = os.Remove(marker)
		}
		return ""
	}
	var b strings.Builder
	if !watcherHealthy(epicDir, time.Now()) {
		reason := "stale-beacon"
		if pathAge(beaconPath(epicDir)) < watch.DefaultGrace {
			reason = "no-watcher"
		}
		full := false
		if readOnly {
			full = readTrimmed(marker) != reason
		} else {
			full = claimStaleBanner(epicDir, reason)
		}
		grace := int(watch.DefaultGrace.Seconds())
		if full {
			const rule = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
			cause := fmt.Sprintf("no live watcher process holds this epic's lock (last beat: %s)", beaconDesc(epicDir))
			if reason == "stale-beacon" {
				cause = fmt.Sprintf("no watcher has a fresh beacon (watch/lasttick last beat: %s, grace %ds)", beaconDesc(epicDir), grace)
			}
			fmt.Fprintf(&b, "●%s\n●  WATCHER DOWN - SUPERVISION IS OFF\n", rule)
			fmt.Fprintf(&b, "●  %s, but %s.\n", need.desc(), cause)
			if readOnly {
				b.WriteString("●  This read-only check should report the lapse, not repair it.\n")
			} else {
				b.WriteString("●  Trust the emitted supervision protocol for this harness; do not use shell & for watcher repair.\n")
			}
			b.WriteString("●  This is a supervision warning only; the guarded operation WILL still run.\n")
			fmt.Fprintf(&b, "●  Watcher for %s is not alive; run: cox watch --epic %s --replace\n", filepath.Base(epicDir), epicDir)
			fmt.Fprintf(&b, "●%s\n", rule)
		} else {
			fmt.Fprintf(&b, "WARNING: watcher still down (same stale episode; last beat: %s, grace %ds) - full banner already printed this episode.\n", beaconDesc(epicDir), grace)
		}
	} else if !readOnly {
		_ = os.Remove(marker)
	}
	if w, err := wake.Drain(epicDir, true); err == nil && len(w) > 0 {
		if readOnly {
			fmt.Fprintf(&b, "WARNING: queued wakes pending - this read-only check leaves them untouched; drain them with cox wake drain --epic %s.\n", epicDir)
		} else {
			fmt.Fprintf(&b, "WARNING: queued wakes pending - drain them with cox wake drain --epic %s before anything else.\n", epicDir)
		}
	}
	return b.String()
}

// claimStaleBanner is fm_guard_claim_stale_banner: true when this call owns the episode's full banner. The marker is one
// line (the episode key), re-checked under the lock so concurrent claims are idempotent; contention past the spin
// budget stays loud rather than dropping the alarm.
func claimStaleBanner(epicDir, key string) bool {
	staleBannerMu.Lock()
	defer staleBannerMu.Unlock()
	marker := coxPath(epicDir, "guard-watcher-stale-banner")
	lock := marker + ".lock"
	for i := 0; i < 50; i++ {
		if readTrimmed(marker) == key {
			return false
		}
		if _, err := tryLock(lock, nil); err == nil {
			seen := readTrimmed(marker)
			if seen != key {
				_ = os.WriteFile(marker, []byte(key+"\n"), 0o644)
			}
			releaseLock(lock)
			return seen != key
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// supervisionNeed is fm_supervision_status's need counts for an epic.
type supervisionNeed struct{ open, sources, checks int }

func (n supervisionNeed) needed() bool { return n.open+n.sources+n.checks > 0 }

// desc is the banner's need clause (firstmate's precedence: tasks, then sources, then checks).
func (n supervisionNeed) desc() string {
	switch {
	case n.open > 0:
		return fmt.Sprintf("%d story(ies) in flight", n.open)
	case n.sources > 0:
		return fmt.Sprintf("%d process-event source(s) registered", n.sources)
	default:
		return fmt.Sprintf("%d registered custom check(s)", n.checks)
	}
}

// supervisionNeeds counts what needs a watcher (fm_supervision_status): open stories (working or input_required),
// registered process-event sources and registered custom checks.
func supervisionNeeds(epicDir string) supervisionNeed {
	var n supervisionNeed
	if open, err := watch.OpenStories(epicDir); err == nil {
		n.open = len(open)
	}
	reg := supervision.Status(filepath.Join(epicDir, controlDir))
	n.sources, n.checks = reg.Sources, reg.Checks
	return n
}

// agoStr renders a short human age (12s, 4m, 2h) for the watcher's last-tick line.
func agoStr(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// watcherReplaceWait bounds how long --replace waits for a TERMed identity-matched watcher to exit before it attaches
// to a verified healthy survivor (fm-watch-arm.sh --restart: 50 x 0.1s).
var watcherReplaceWait = 5 * time.Second

// claimWatchPid claims <epic>/.cox/watch.pid for this process (fm-watch.sh's singleton lock acquisition, on the file
// primitive tryLock): exactly one of any number of concurrent starts wins, a live holder is refused (naming its pid and,
// when its beacon went stale, saying so), an empty mid-acquire pidfile keeps its grace, and a dead holder is reclaimed
// under the steal mutex after the downtime is published. The winner records its process identity in the
// .cox/watch.identity sidecar. With replace (fm-watch-arm.sh --restart) it first stops ONLY a holder whose recorded
// identity still matches (TERM, bounded wait); a reused or identityless pid is never signalled - its lock is reclaimed
// as stale - and a verified healthy holder that survives the TERM is attached to: the returned release is a no-op and
// watch.pid keeps naming the peer. The release removes the pidfile and sidecar only while they still name this process.
func claimWatchPid(epicDir string, replace bool) (func(), error) {
	path := watchPidPath(epicDir)
	if replace {
		pid, identity := watch.ReadPid(epicDir)
		if pid > 0 && pid != os.Getpid() && processAlive(pid) {
			if identity != "" && identityOf(pid) == identity {
				if !termAndWait(pid, watcherReplaceWait) {
					if watch.Healthy(epicDir, time.Now(), 0) {
						return func() {}, nil // attached to the verified healthy peer
					}
					if err := killAndWait(pid, 0); err != nil && processAlive(pid) {
						return nil, err
					}
				}
			} else if err := clearStaleWatchLock(epicDir, pid); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tryLock(path, func(stale int) error { return publishWatcherDowntime(epicDir, stale) }); err != nil {
		var held errLockHeld
		if !errors.As(err, &held) {
			return nil, err
		}
		if held.pid > 0 {
			if age := pathAge(beaconPath(epicDir)); exists(beaconPath(epicDir)) && age >= watch.DefaultGrace {
				return nil, fmt.Errorf("watcher: lock held by live pid %d but heartbeat is stale for %ds (>%ds); inspect or stop that watcher before re-arming (cox watch --epic %s --replace)",
					held.pid, int(age.Seconds()), int(watch.DefaultGrace.Seconds()), epicDir)
			} else if !exists(beaconPath(epicDir)) && pathAge(path) >= watch.DefaultGrace {
				return nil, fmt.Errorf("watcher: lock held by live pid %d but no heartbeat exists; inspect or stop that watcher before re-arming (cox watch --epic %s --replace)", held.pid, epicDir)
			}
			return nil, fmt.Errorf("watcher already running (pid %d); use --replace to take over", held.pid)
		}
		return nil, fmt.Errorf("watcher already running (a start is mid-acquire); use --replace to take over")
	}
	_ = watch.RecordIdentity(epicDir, os.Getpid())
	return func() {
		if readPid(path) == os.Getpid() {
			// Every watcher close publishes downtime before the lock goes (firstmate: release-lock transition), so the
			// next start re-surfaces what was queued while no watcher ran.
			_ = publishRecoveryDowntime(epicDir)
			_ = os.Remove(watch.IdentityPath(epicDir))
			_ = os.Remove(path)
		}
	}, nil
}

// clearStaleWatchLock is fm-watch-arm.sh clear_stale_recorded_watcher_lock: a live pid whose recorded identity does not
// verify is not this epic's watcher (pid reuse) - publish the downtime, then remove its lock without ever signalling it.
func clearStaleWatchLock(epicDir string, pid int) error {
	if err := publishWatcherDowntime(epicDir, pid); err != nil {
		return fmt.Errorf("watcher: FAILED - stale watcher recovery state could not be persisted: %w", err)
	}
	if readPid(watchPidPath(epicDir)) == pid {
		_ = os.Remove(watch.IdentityPath(epicDir))
		_ = os.Remove(watchPidPath(epicDir))
	}
	return nil
}

// readPid reads a pidfile and returns the pid, or 0 when the file is absent or unparsable.
func readPid(path string) int {
	pid, err := strconv.Atoi(readTrimmed(path))
	if err != nil {
		return 0
	}
	return pid
}

// termAndWait sends SIGTERM to pid and reports whether it exited within timeout.
func termAndWait(pid int, timeout time.Duration) bool {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGTERM)
	}
	for deadline := time.Now().Add(timeout); ; time.Sleep(50 * time.Millisecond) {
		if !processAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
	}
}

// killAndWait stops pid within a bounded time: SIGTERM and up to timeout for a clean exit, then SIGKILL and a short
// bounded wait, so a stopped or TERM-resistant process never hangs the caller past its deadline (firstmate
// wait_for_exit: "survived TERM; sending KILL"). It errors only when the process is still alive after the KILL.
func killAndWait(pid int, timeout time.Duration) error {
	if termAndWait(pid, timeout) {
		return nil
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGKILL)
	}
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if !processAlive(pid) {
			return nil
		}
	}
	return fmt.Errorf("watcher pid %d survived TERM and KILL", pid)
}

// cmdWatch implements `cox watch --epic <dir> [--once] [--replace]`. It builds a watcher over the epic's Orca run,
// wiring the saved worker sessions and the recorded leader handle, and either runs one Tick (--once) or the poll loop
// until signaled. On the loop path it claims <epic>/.cox/watch.pid so a second live watcher refuses to start (or takes
// over with --replace) and removes the pidfile on a clean exit.
func cmdWatch(args []string) int {
	if len(args) > 0 && args[0] == "check" {
		return watchCheck(args[1:])
	}
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	once := fs.Bool("once", false, "run a single watch pass and exit")
	replace := fs.Bool("replace", false, "kill an already-running watcher and take over the pidfile")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox watch --epic <dir> [--once] [--replace]")
	}
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("watch needs a live backend: set ORCA_RUN_ID or %s/.cox/run", *epicDir)
	}
	pol := loadPolicyQuiet(*epicDir)
	w := &watch.Watcher{
		EpicDir:      *epicDir,
		Backend:      b,
		Sessions:     loadAllSessions(*epicDir),
		Quota:        newQuotaProbe(*epicDir),
		AlarmChannel: pol.AlertsChannel(),
		BusyTurnMax:  time.Duration(pol.BusyTurnMaxMinutes()) * time.Minute, // 0 => watcher default (DefaultBusyTurnMax)
	}
	if *once {
		n, err := w.Tick()
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("watch tick: %d wake(s) appended\n", n)
		return 0
	}
	// Catch the exit signals before the claim, so one landing during start-up still runs the release (a signal that
	// reached Go's default action would kill the watcher with its pidfile behind).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, watch.ExitSignals...) // HUP, INT and TERM all run the pidfile release (docs/watcher-continuity.md:117)
	release, err := claimWatchPid(*epicDir, *replace)
	if err != nil {
		return fail("%v", err)
	}
	defer release()
	if pid := readPid(watchPidPath(*epicDir)); pid != os.Getpid() {
		fmt.Printf("watcher: attached pid=%d (a verified healthy watcher survived the replace)\n", pid)
		return 0
	}
	// A start after an announced-but-unacked episode is a new down stretch (a fresh generation); then announce what is
	// pending once (fm-watch.sh reopen_announced + arm_check + resurface_after_downtime).
	_ = recoveryReopenAnnounced(*epicDir)
	if _, err := recoveryArmCheck(*epicDir, 0); err != nil {
		return fail("watcher: recovery state could not be consumed safely; retaining stale lock evidence: %v", err)
	}
	// The cycle-exit ledger: this watcher is the verified successor of the last unlinked cycle, and records its own exit.
	linkCycleSuccessor(*epicDir, fmt.Sprintf("started:%d", os.Getpid()))
	cycle := cycleRecord{armPid: os.Getpid(), watcherPid: os.Getpid(), origin: "started", startedAt: time.Now(), lockBefore: lockSnapshot(*epicDir)}
	var recordOnce sync.Once
	recordExit := func(code, signal, reason string) {
		recordOnce.Do(func() {
			cycle.exitCode, cycle.signal, cycle.reason = code, signal, reason
			appendCycle(*epicDir, cycle)
		})
	}
	stop := make(chan struct{})
	go func() {
		s := <-sig
		n := 0
		if ss, ok := s.(syscall.Signal); ok {
			n = int(ss)
		}
		recordExit(strconv.Itoa(128+n), signalName(s), "signal-exit")
		close(stop)
		// Firstmate's watcher dies on HUP/TERM at once, running its exit cleanup even mid-poll: give the pass in flight a
		// short grace to finish, then release the pidfile and exit without waiting for it.
		time.Sleep(watcherStopGrace)
		release()
		os.Exit(0)
	}()
	w.Run(stop, 5*time.Second)
	recordExit("0", "none", "unexpected-clean-exit") // evicted, closed or replaced: the loop ended on its own
	return 0
}

// watchCheck implements `cox watch check register|unregister <id> --epic <dir>` (firstmate fm-check-register.sh /
// fm-check-unregister.sh): bind <epic>/.cox/<id>.check.sh to its bytes so the epic needs a watcher, or retire it.
func watchCheck(args []string) int {
	const usage = "cox watch check register|unregister <id> --epic <dir>"
	verb, rest := onePositional(args)
	id, rest := onePositional(rest)
	fs := flag.NewFlagSet("watch check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || id == "" {
		return usageErr(usage)
	}
	control := filepath.Join(*epicDir, controlDir)
	switch verb {
	case "register":
		if err := supervision.Register(control, id); err != nil {
			return fail("error: %v", err)
		}
		fmt.Printf("registered: .cox/%s.check.sh\n", id)
	case "unregister":
		if err := supervision.Unregister(control, id); err != nil {
			return fail("error: %v", err)
		}
		fmt.Printf("unregistered: .cox/%s.check.sh\n", id)
	default:
		return usageErr(usage)
	}
	return 0
}

// signalName is the short signal name the ledger records (HUP, INT, TERM).
func signalName(s os.Signal) string {
	switch s {
	case syscall.SIGHUP:
		return "HUP"
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGTERM:
		return "TERM"
	}
	return s.String()
}

// watcherStopGrace bounds how long a signalled watcher lets its in-flight pass finish before it exits anyway.
var watcherStopGrace = 2 * time.Second
