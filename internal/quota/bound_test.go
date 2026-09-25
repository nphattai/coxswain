package quota

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A quota-axi that wedges while a TERM-ignoring descendant holds its stdout cannot keep Read past its bound, and the
// descendant does not outlive it (delta group E, firstmate ac2ed3b; FM/fm-timeout-lib pins the helper itself).
func TestReadBoundedWhenADescendantHoldsTheOutput(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	bin := filepath.Join(dir, "quota-axi")
	script := "#!/bin/sh\n[ \"$1\" = warm ] && exit 0\n( trap '' TERM; exec sleep 30 ) &\necho $! > '" + pidFile + "'\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Warm it: the first exec of a new file on macOS can take seconds under load, longer than the bound (B-63).
	if err := exec.Command(bin, "warm").Run(); err != nil {
		t.Fatal(err)
	}
	q := &QuotaAxi{Config: QuotaAxiConfig{Binary: bin, Timeout: time.Second}}
	done := make(chan []Reading, 1)
	go func() { rs, _ := q.Read(context.Background()); done <- rs }()
	select {
	case rs := <-done:
		for _, r := range rs {
			if r.Known || !strings.Contains(r.Reason, "timed out") {
				t.Errorf("a wedged quota-axi must read unknown with a timed-out reason: %+v", r)
			}
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Read was still waiting 6s after its 1s bound")
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("the wedged quota-axi's descendant outlived the bound")
	}
}
