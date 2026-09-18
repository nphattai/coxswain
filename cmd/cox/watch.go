package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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

// claimWatchPid writes this process's pid to <epic>/.cox/watch.pid. It refuses (naming the pid) when the file already
// names another live process, unless replace is set, which SIGTERMs that process and waits for it to exit first. The
// returned release removes the pidfile only while it still holds our pid, so a watcher that was itself replaced does
// not delete its successor's file.
//
// ponytail: read-check-write is not atomic across two watchers starting at the same instant; the dispatch-side
// startWatcher already gates on a live pidfile, so the residual race is a redundant watcher that this guard rejects on
// the next start. Add a lockfile only if simultaneous cold starts prove real.
func claimWatchPid(epicDir string, replace bool) (func(), error) {
	path := watchPidPath(epicDir)
	if pid := readPid(path); pid > 0 && pid != os.Getpid() && processAlive(pid) {
		if !replace {
			return nil, fmt.Errorf("watcher already running (pid %d); use --replace to take over", pid)
		}
		if err := killAndWait(pid, 5*time.Second); err != nil {
			return nil, err
		}
	}
	if err := writeCoxFile(epicDir, "watch.pid", strconv.Itoa(os.Getpid())); err != nil {
		return nil, err
	}
	return func() {
		if readPid(path) == os.Getpid() {
			_ = os.Remove(path)
		}
	}, nil
}

// readPid reads a pidfile and returns the pid, or 0 when the file is absent or unparsable.
func readPid(path string) int {
	pid, err := strconv.Atoi(readTrimmed(path))
	if err != nil {
		return 0
	}
	return pid
}

// killAndWait sends SIGTERM to pid and polls until it exits or timeout elapses.
func killAndWait(pid int, timeout time.Duration) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("replace watcher pid %d: %w", pid, err)
	}
	_ = p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("watcher pid %d did not exit within %s", pid, timeout)
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
	w := &watch.Watcher{
		EpicDir:  *epicDir,
		Backend:  b,
		Leader:   readLeader(*epicDir),
		Sessions: loadAllSessions(*epicDir),
		Quota:    newQuotaProbe(*epicDir),
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
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		close(stop)
	}()
	w.Run(stop, 5*time.Second)
	return 0
}
