package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// wedged writes <dir>/<name>: a probe that wedges while a TERM-ignoring descendant holds its stdout, the descendant's
// pid in <dir>/<name>.pid.
func wedged(t *testing.T, dir, name string) string {
	t.Helper()
	pidFile := filepath.Join(dir, name+".pid")
	script := "#!/bin/sh\n[ \"$1\" = warm ] && exit 0\n( trap '' TERM; exec sleep 30 ) &\necho $! > '" + pidFile + "'\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Warm it: the first exec of a new file on macOS can take seconds under load, longer than the bound (B-63).
	if err := exec.Command(filepath.Join(dir, name), "warm").Run(); err != nil {
		t.Fatal(err)
	}
	return pidFile
}

// within fails the test when f outlasts limit.
func within(t *testing.T, limit time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s was still waiting %s after its bound", what, limit)
	}
}

func descendantGone(t *testing.T, file string) bool {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return false
}

// Every doctor probe runs through the one bounded exec: a wedged probe whose descendant holds its output ends at the
// bound and leaves nothing behind (delta group E, firstmate ac2ed3b; FM/fm-timeout-lib pins the helper itself).
func TestProbesAreBounded(t *testing.T) {
	old := probeTimeout
	probeTimeout = time.Second
	defer func() { probeTimeout = old }()
	dir := t.TempDir()
	t.Setenv("PATH", dir+":/usr/bin:/bin")

	t.Run("reachable", func(t *testing.T) {
		pid := wedged(t, dir, "orcawedged")
		var c Check
		within(t, 6*time.Second, "Reachable", func() { c = Reachable("orcawedged", nil, time.Second) })
		if c.Status != StatusUnknown {
			t.Errorf("a wedged probe = %v, want unknown", c)
		}
		if !descendantGone(t, pid) {
			t.Error("the wedged probe's descendant outlived the bound")
		}
	})

	t.Run("version", func(t *testing.T) {
		for _, tc := range []struct{ bin, typ string }{{"git", "v1"}, {"cox", "v2"}} {
			pid := wedged(t, dir, tc.bin)
			var v string
			within(t, 6*time.Second, tc.bin+" version", func() { v = version(t.TempDir(), tc.typ) })
			if v != "unknown" {
				t.Errorf("a wedged %s version = %q, want unknown", tc.bin, v)
			}
			if !descendantGone(t, pid) {
				t.Errorf("the wedged %s's descendant outlived the bound", tc.bin)
			}
		}
	})

	t.Run("ps", func(t *testing.T) {
		pid := wedged(t, dir, "ps")
		var procs []WatchProc
		within(t, 6*time.Second, "ListWatchProcs", func() { procs = ListWatchProcs() })
		if procs != nil {
			t.Errorf("a wedged ps listed %v, want nothing", procs)
		}
		if !descendantGone(t, pid) {
			t.Error("the wedged ps's descendant outlived the bound")
		}
	})
}
