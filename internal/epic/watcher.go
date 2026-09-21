package epic

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// watcherProcArgs returns the command line of a running process, used to prove a live pid really is this epic's watcher
// before close signals it. It is a package var so a test can inject a fake without spawning a real `cox watch`.
var watcherProcArgs = psArgs

// psArgs reads a pid's full command line via `ps -o args=` (POSIX; works on macOS and Linux). A dead pid or a ps error
// returns "".
func psArgs(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// stopWatcher terminates the epic's watcher before the archive, so a live watcher can never recreate .cox/ after it is
// moved to .cox.closed (finding 13). It reads <epic>/.cox/watch.pid: an absent or dead pid is a no-op. A live pid is
// signalled ONLY when its command line proves it is this epic's watcher (`cox watch ... --epic <this epic dir>`); a live
// pid that cannot be proven is reported and NEVER signalled, and close refuses to archive so the operator stops it by
// hand (arena round 1, adversary-1-1, accepted). The proof guards against a reused pid the recorded watcher left behind.
func (o *CloseOptions) stopWatcher() error {
	pid := readPidFile(filepath.Join(o.EpicDir, ".cox", "watch.pid"))
	if pid <= 0 || !procAlive(pid) {
		return nil // no live watcher to stop
	}
	args, _ := watcherProcArgs(pid)
	if !isThisEpicWatcher(args, o.EpicDir) {
		return fmt.Errorf("watch.pid %d is alive but its command line (%q) does not prove it is %s's watcher; stop it by hand, then re-run close",
			pid, args, filepath.Base(o.EpicDir))
	}
	if err := killWait(pid, 5*time.Second); err != nil {
		return err
	}
	fmt.Fprintf(o.out(), "ok: stopped watcher (pid %d)\n", pid)
	return nil
}

// isThisEpicWatcher reports whether a process command line is a `cox watch` for this epic dir. It requires the watch
// verb, the --epic flag, and the epic dir path (or its base, tolerating a relative path passed to cox watch).
//
// ponytail: substring match on the epic dir, not an argv parse. The pid already came from this epic's own watch.pid, so
// this only rules out a reused pid; parse argv if two epics ever share a dir suffix.
func isThisEpicWatcher(args, epicDir string) bool {
	if args == "" {
		return false
	}
	if !strings.Contains(args, "watch") || !strings.Contains(args, "--epic") {
		return false
	}
	return strings.Contains(args, epicDir) || strings.Contains(args, filepath.Base(epicDir))
}

// readPidFile reads a pidfile and returns the pid, or 0 when absent or unparsable.
func readPidFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// procAlive reports whether pid is a running process (signal 0 probe).
func procAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// killWait sends SIGTERM to pid and polls until it exits or timeout elapses.
func killWait(pid int, timeout time.Duration) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("signal watcher pid %d: %w", pid, err)
	}
	_ = p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !procAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("watcher pid %d did not exit within %s", pid, timeout)
}
