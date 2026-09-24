package watch

// The registered custom-check sweep, ported from firstmate bin/fm-watch.sh:2470-2590 (run_check_capture :1954,
// run_check_process :1887) and bin/fm-check-lib.sh fm_custom_check_snapshot_prepare, pinned 1e0e773. A check is a
// <epic>/.cox/<id>.check.sh that `cox watch check register` bound to its bytes (internal/supervision). Every
// CheckInterval the watcher runs each registered check from a private snapshot of the registered bytes, in the
// watcher's own environment, bounded by CheckTimeout; non-empty stdout is an urgent check wake. A check whose bytes no
// longer match its registration (or were never registered) is never run and is reported instead.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/supervision"
	"github.com/nphattai/coxswain/internal/wake"
)

const (
	DefaultCheckInterval = 300 * time.Second // FM_CHECK_INTERVAL: seconds between *.check.sh sweeps
	DefaultCheckTimeout  = 30 * time.Second  // FM_CHECK_TIMEOUT: seconds allowed per *.check.sh
)

// envSeconds reads a non-negative whole-seconds env knob (firstmate's FM_ names map to COX_).
func envSeconds(name string) (time.Duration, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// checkInterval is CheckInterval, else COX_CHECK_INTERVAL, else DefaultCheckInterval.
func (w *Watcher) checkInterval() time.Duration {
	if w.CheckInterval > 0 {
		return w.CheckInterval
	}
	if d, ok := envSeconds("COX_CHECK_INTERVAL"); ok {
		return d
	}
	return DefaultCheckInterval
}

// checkTimeout is CheckTimeout, else COX_CHECK_TIMEOUT, else DefaultCheckTimeout.
func (w *Watcher) checkTimeout() time.Duration {
	if w.CheckTimeout > 0 {
		return w.CheckTimeout
	}
	if d, ok := envSeconds("COX_CHECK_TIMEOUT"); ok && d > 0 {
		return d
	}
	return DefaultCheckTimeout
}

// checkPass is the sweep. Its cadence is the watch/check/last stamp, so it survives watcher restarts. Like
// firstmate's `wake`, the first check that reports ends the sweep; the rest wait for the next interval.
func (w *Watcher) checkPass() (int, error) {
	if w.sage("check", "last") < w.checkInterval() {
		return 0, nil
	}
	control := filepath.Join(w.EpicDir, state.ControlDir)
	checks, _ := filepath.Glob(filepath.Join(control, "*.check.sh"))
	sort.Strings(checks)
	var rejected []string
	for _, c := range checks {
		id := strings.TrimSuffix(filepath.Base(c), ".check.sh")
		out, ok := w.runCheck(control, id)
		if !ok {
			rejected = append(rejected, c)
			continue
		}
		if out == "" {
			continue
		}
		if err := w.appendCheck("check: "+c+": "+out, map[string]any{"check": c}); err != nil {
			return 0, err
		}
		w.sstamp("check", "last")
		return 1, nil
	}
	if len(rejected) > 0 {
		if err := w.appendCheck("check: rejected unauthenticated state checks: "+strings.Join(rejected, " "),
			map[string]any{"checks": rejected}); err != nil {
			return 0, err
		}
		w.sstamp("check", "last")
		return 1, nil
	}
	w.sstamp("check", "last")
	return 0, nil
}

// appendCheck queues one check wake; the stamp is written only after it is durable, so a failed append re-runs.
func (w *Watcher) appendCheck(note string, ev map[string]any) error {
	ev["by"] = "watch"
	wk := wake.Wake{Epic: filepath.Base(w.EpicDir), Story: "_watch", Kind: wake.KindCheck, Note: truncate(note, 400), Evidence: ev}
	if len(note) > 400 {
		wk.Full = note
	}
	_, err := wake.Append(w.EpicDir, wk)
	return err
}

// runCheck runs one registered check from a 0600 snapshot of exactly the bytes its registration vouches for and
// returns its stdout with trailing newlines trimmed (a shell $(...)). Output goes to a private file, as fm's
// run_check_capture does, so a background child cannot hold the capture open; the check's whole process group is
// killed once it exits, times out, or the watcher stops (fm_active_check_stop, watcher_cleanup). ok=false means the
// check is not authenticated and did not run.
func (w *Watcher) runCheck(control, id string) (string, bool) {
	body, ok := supervision.RegisteredBytes(control, id)
	if !ok {
		return "", false
	}
	snap, err := privateFile(control, ".cox-custom-check.*", body)
	if err != nil {
		return "", true // could not stage the run: nothing to report
	}
	defer os.Remove(snap)
	out, err := privateFile(control, ".cox-check-output.*", nil)
	if err != nil {
		return "", true
	}
	defer os.Remove(out)
	outFile, err := os.OpenFile(out, os.O_WRONLY, 0)
	if err != nil {
		return "", true
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.checkTimeout())
	defer cancel()
	go func() { // a stopping watcher takes its running check down with it
		select {
		case <-w.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	cmd := exec.CommandContext(ctx, "bash", snap)
	cmd.Stdout = outFile // stderr is discarded (fm: 2>/dev/null); the environment is the watcher's own
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := cmd.Start(); err != nil {
		outFile.Close()
		return "", true // could not start: nothing to report
	}
	_ = cmd.Wait()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // anything the check left running in its group
	outFile.Close()
	b, _ := os.ReadFile(out)
	return strings.TrimRight(string(b), "\n"), true
}

// privateFile creates a 0600 file in dir holding b and returns its path.
func privateFile(dir, pattern string, b []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(f.Name(), 0o600)); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
