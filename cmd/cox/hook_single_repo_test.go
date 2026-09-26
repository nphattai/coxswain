package main

import (
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/wake"
)

// In an in-repo workspace every worker worktree carries the leader's hook settings, and its launch env sets
// COX_EPIC/COX_STORY. prompt-drain and stop-rewake are leader-only: with COX_STORY set they exit 0 with one stderr line
// and never touch the epic, so a worker never drains or waits on the leader's wake queue.
func TestWakeHooksSkipInWorkerEnv(t *testing.T) {
	epic := t.TempDir()
	seedWake(t, epic, wake.KindWorkerDone) // urgent: an ungated drain prints it, an ungated rewake exits 2
	t.Setenv("COX_EPIC", epic)
	t.Setenv("COX_STORY", "x")
	t.Setenv("ORCA_TERMINAL_HANDLE", "")
	t.Setenv("REWAKE_MAX_WAIT", "0")
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"prompt-drain"}, {"stop-rewake", "--guard=false"}} {
		var code int
		out, errOut := captureStdErrOut(t, func() { code = cmdHook(args) })
		if code != 0 || out != "" {
			t.Errorf("%s in a worker: code=%d stdout=%q, want 0 and silent", args[0], code, out)
		}
		if lines := strings.Split(strings.TrimSpace(errOut), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "leader hook skipped") {
			t.Errorf("%s in a worker: stderr=%q, want one skip line", args[0], errOut)
		}
	}
	if ws, _ := wake.Drain(epic, true); len(ws) != 1 {
		t.Errorf("worker hooks must leave the leader's wake queue alone: %d unread", len(ws))
	}
}

// The leader's own pseudo-story is not a worker: its wake hooks still run.
func TestWakeHooksRunForLeaderStory(t *testing.T) {
	epic := t.TempDir()
	seedWake(t, epic, wake.KindWorkerDone)
	t.Setenv("COX_EPIC", epic)
	t.Setenv("COX_STORY", leaderStory)
	t.Setenv("ORCA_TERMINAL_HANDLE", "")
	t.Chdir(t.TempDir())
	var code int
	out, _ := captureStdErrOut(t, func() { code = cmdHook([]string{"prompt-drain"}) })
	if code != 0 || !strings.Contains(out, "Watcher wakes") {
		t.Errorf("leader prompt-drain: code=%d stdout=%q, want the wake attached", code, out)
	}
}
