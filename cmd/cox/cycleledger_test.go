package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The ledger caps come from COX_WATCH_CYCLE_LOG_MAX_BYTES / COX_WATCH_CYCLE_LOG_KEEP_LINES (fm-watch-arm.sh:84-88):
// crossing the byte cap trims to the last KEEP complete records (tail -n KEEP), never one fewer.
func TestAppendCycleHonorsEnvCaps(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, controlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COX_WATCH_CYCLE_LOG_MAX_BYTES", "1000")
	t.Setenv("COX_WATCH_CYCLE_LOG_KEEP_LINES", "2")
	for i := 0; i < 5; i++ {
		appendCycle(epic, cycleRecord{armPid: i + 1, watcherPid: i + 1, origin: "started", startedAt: time.Now(), exitCode: "0", signal: "none", reason: "clean-exit"})
	}
	b, err := os.ReadFile(cycleLogPath(epic))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(b) > 1000 || len(lines) != 2 || !strings.HasPrefix(lines[0], "arm_pid=4\t") || !strings.HasPrefix(lines[1], "arm_pid=5\t") {
		t.Fatalf("want the last two complete records within 1000 bytes, got %d bytes:\n%s", len(b), b)
	}
}

func TestCycleLogCapRejectsInvalidValues(t *testing.T) {
	for _, v := range []string{"", "0", "-5", "+5", " 5", "abc"} {
		t.Setenv("COX_WATCH_CYCLE_LOG_KEEP_LINES", v)
		if got := cycleLogKeepLines(); got != 1000 {
			t.Errorf("KEEP_LINES=%q: want the default 1000, got %d", v, got)
		}
	}
}
