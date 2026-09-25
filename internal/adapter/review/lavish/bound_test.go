package lavish

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A lavish-axi that wedges while a TERM-ignoring descendant holds its stdout cannot keep Poll past its bound, and the
// descendant does not outlive it (delta group E, firstmate ac2ed3b; FM/fm-timeout-lib pins the helper itself).
func TestPollBoundedWhenADescendantHoldsTheOutput(t *testing.T) {
	old := pollBuffer
	pollBuffer = 500 * time.Millisecond
	defer func() { pollBuffer = old }()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	bin := filepath.Join(dir, "lavish-axi")
	script := "#!/bin/sh\n[ \"$1\" = warm ] && exit 0\n( trap '' TERM; exec sleep 30 ) &\necho $! > '" + pidFile + "'\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Warm it: the first exec of a new file on macOS can take seconds under load, longer than the bound (B-63).
	if err := exec.Command(bin, "warm").Run(); err != nil {
		t.Fatal(err)
	}
	epic := t.TempDir()
	done := make(chan error, 1)
	go func() {
		var out, errb bytes.Buffer
		_, err := Poll(Config{Binary: bin}, epic, filepath.Join(epic, "arena.html"), 500*time.Millisecond, nil, &out, &errb)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a wedged poll returned no error")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Poll was still waiting 6s after its 1s bound")
	}
	if !descendantGone(t, pidFile) {
		t.Error("the wedged lavish-axi's descendant outlived the bound")
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
