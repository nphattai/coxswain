package watch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/state"
)

// Watcher process identity, liveness and signals, ported from firstmate bin/fm-wake-lib.sh fm_pid_identity,
// fm_poll_derived_grace and bin/fm-watch.sh watcher_stop_signals (pinned 1e0e773). cmd/cox (story w2-hooks) wires them
// into the pidfile claim, the turn-end guard and the watch command.

// ExitSignals are the signals that stop a watcher through its exit cleanup (pidfile release). Firstmate keeps HUP and
// TERM on the fatal path that runs the EXIT trap and traps INT (docs/watcher-continuity.md:117), so all three stop it.
var ExitSignals = []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM}

// DefaultGrace is the default beacon freshness window: max(300s, poll+60s) (fm_poll_derived_grace). A watcher touches
// its beacon once per poll, so the grace grows with the cadence while keeping the historical 300s floor.
var DefaultGrace = PollGrace(DefaultPoll)

// PollGrace derives the beacon freshness window for a poll interval: max(300s, poll+60s).
func PollGrace(poll time.Duration) time.Duration {
	if g := poll + 60*time.Second; g > 300*time.Second {
		return g
	}
	return 300 * time.Second
}

// procRoot is the Linux-compatible /proc the identity read prefers; a var so a test can point it at a fake tree.
var procRoot = "/proc"

// ProcIdentity returns a stable identity for pid that changes when the pid is reused: from /proc (stat field 22, the
// start time in clock ticks since boot, immune to wall-clock steps, plus the full NUL-separated cmdline) when readable,
// else `ps -o lstart= -o command=` pinned to LC_ALL=C so the date format is locale invariant. An error means the
// process is gone or cannot be identified.
func ProcIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	stat, serr := os.ReadFile(filepath.Join(dir, "stat"))
	cmdline, cerr := os.ReadFile(filepath.Join(dir, "cmdline"))
	if serr == nil && cerr == nil {
		// Fields after the last ')' (comm may contain spaces and parens); index 19 there is stat field 22.
		s := string(stat)
		fields := strings.Fields(s[strings.LastIndex(s, ")")+1:])
		if len(fields) < 20 {
			return "", fmt.Errorf("pid %d: short /proc stat", pid)
		}
		start := fields[19]
		if _, err := strconv.ParseUint(start, 10, 64); err != nil || len(cmdline) == 0 {
			return "", fmt.Errorf("pid %d: unreadable /proc identity", pid)
		}
		key := "proc-starttime"
		if runtime.GOOS == "linux" {
			key = "linux-starttime"
		}
		return fmt.Sprintf("%s=%s cmdline-hex=%x", key, start, cmdline), nil
	}
	out, err := psRun(append(os.Environ(), "LC_ALL=C"), "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "command=")
	if err != nil {
		return "", fmt.Errorf("pid %d: %w", pid, err)
	}
	id := strings.TrimLeft(strings.TrimRight(string(out), "\n"), " \t")
	if id == "" {
		return "", fmt.Errorf("pid %d: no such process", pid)
	}
	return id, nil
}

// psRun runs ps with env and args; a var so a test can observe the locale it is run under without executing a stub.
var psRun = func(env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("ps", args...)
	cmd.Env = env
	return cmd.Output()
}

// PidPath is the epic's watcher pidfile, <epic>/.cox/watch.pid. It holds a bare pid: other readers (doctor, epic close)
// parse the whole file as one integer.
func PidPath(epicDir string) string { return filepath.Join(epicDir, state.ControlDir, "watch.pid") }

// IdentityPath is the watcher's process-identity sidecar, <epic>/.cox/watch.identity (fm .watch.lock/pid-identity),
// written by the claimant next to watch.pid so a reused pid is never mistaken for the watcher.
func IdentityPath(epicDir string) string {
	return filepath.Join(epicDir, state.ControlDir, "watch.identity")
}

// ReadPid returns the pid in watch.pid and the identity recorded in the watch.identity sidecar ("" when absent), or
// pid 0 when the pidfile is absent or unparsable.
func ReadPid(epicDir string) (pid int, identity string) {
	b, err := os.ReadFile(PidPath(epicDir))
	if err != nil {
		return 0, ""
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, ""
	}
	if id, err := os.ReadFile(IdentityPath(epicDir)); err == nil {
		identity = strings.TrimSpace(string(id))
	}
	return pid, identity
}

// RecordIdentity writes pid's process identity to the watch.identity sidecar (atomically), for the claimant of
// watch.pid to call right after it writes the pid.
func RecordIdentity(epicDir string, pid int) error {
	id, err := ProcIdentity(pid)
	if err != nil {
		return err
	}
	return writeAtomic(IdentityPath(epicDir), []byte(id+"\n"))
}

// pidAlive reports whether pid names a running process (signal 0).
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// Healthy is the cheap liveness read a turn-end waiter calls (fm fm_watcher_healthy / fm_watcher_lock_matches_pid): the
// watcher pidfile names a live process, the identity sidecar is present and still matches that process (an identityless
// or mismatched pid is not a watcher - pid reuse), AND watch/lasttick is younger than grace (0 => DefaultGrace). A live
// watcher whose beacon went stale is wedged, not healthy.
func Healthy(epicDir string, now time.Time, grace time.Duration) bool {
	pid, identity := ReadPid(epicDir)
	if pid <= 0 || !pidAlive(pid) || identity == "" {
		return false
	}
	if got, err := ProcIdentity(pid); err != nil || got != identity {
		return false
	}
	info, err := os.Stat(filepath.Join(epicDir, state.ControlDir, "watch", "lasttick"))
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) < orDur(grace, DefaultGrace)
}

// writeAtomic publishes data at path through a temp file in the same directory and a rename, so a symlink planted at
// path is replaced rather than followed and a reader never sees a torn write (firstmate
// fm-watch-arm.test.sh:860). The directory is created when missing.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// sameDevice reports whether two files live on the same filesystem (find -xdev).
func sameDevice(a, b os.FileInfo) bool {
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return !ok1 || !ok2 || sa.Dev == sb.Dev
}
