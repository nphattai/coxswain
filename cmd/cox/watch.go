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
	"syscall"
	"time"

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

// watcherIssue returns a non-empty ISSUE string when the watcher is not alive but the epic still has an active story
// (working or input_required): nobody is delivering its wakes, and nothing else surfaces it (the dogfood gap, M14).
func watcherIssue(epicDir string, wi watchInfo) string {
	if wi.Alive {
		return ""
	}
	open, err := watch.OpenStories(epicDir)
	if err != nil || len(open) == 0 {
		return ""
	}
	return fmt.Sprintf("watcher not alive for %s but %d story(ies) still active (%s); run cox watch --epic %s --replace",
		filepath.Base(epicDir), len(open), strings.Join(open, ","), epicDir)
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

// publishWatcherDowntime records that the epic's watcher was down (pid is the stale holder) before its lock is cleared,
// so the wakes queued during the downtime are re-surfaced to the leader (fm-watch.sh resurface_after_downtime,
// "check: rearm-resurface").
func publishWatcherDowntime(epicDir string, pid int) error {
	_, err := wake.Append(epicDir, wake.Wake{Epic: filepath.Base(epicDir), Kind: wake.KindStatus,
		Note: fmt.Sprintf("check: rearm-resurface - the watcher (pid %d) was down; drain the wakes queued during the downtime", pid)})
	return err
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
	release, err := claimWatchPid(*epicDir, *replace)
	if err != nil {
		return fail("%v", err)
	}
	defer release()
	if pid := readPid(watchPidPath(*epicDir)); pid != os.Getpid() {
		fmt.Printf("watcher: attached pid=%d (a verified healthy watcher survived the replace)\n", pid)
		return 0
	}
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, watch.ExitSignals...) // HUP, INT and TERM all run the pidfile release (docs/watcher-continuity.md:117)
	go func() {
		<-sig
		close(stop)
		// Firstmate's watcher dies on HUP/TERM at once, running its exit cleanup even mid-poll: give the pass in flight a
		// short grace to finish, then release the pidfile and exit without waiting for it.
		time.Sleep(watcherStopGrace)
		release()
		os.Exit(0)
	}()
	w.Run(stop, 5*time.Second)
	return 0
}

// watcherStopGrace bounds how long a signalled watcher lets its in-flight pass finish before it exits anyway.
var watcherStopGrace = 2 * time.Second
