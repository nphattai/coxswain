package orca

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stubOrca puts a fake `orca` running script first on PATH, and execs it once untimed (`orca warm` exits at once): macOS
// may scan a freshly written script for seconds before its first exec, which must not be charged to a bound under test.
func stubOrca(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "orca")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = warm ] && exit 0\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(path, "warm").Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Every orca verb the adapter runs has one bound by its class: reads 10s, starts/stops/sends 30s, worktree create|rm 120s.
func TestOrcaBoundByVerbClass(t *testing.T) {
	for args, want := range map[string]time.Duration{
		"terminal read --terminal t --screen --json": ReadBound,
		"terminal show --terminal t --json":          ReadBound,
		"terminal list --json":                       ReadBound,
		"orchestration check --run r --json":         ReadBound,
		"orchestration worker-read --json":           ReadBound,
		"orchestration run-current --json":           ReadBound,
		"worktree ps --json":                         ReadBound,
		"status --json":                              ReadBound,
		"terminal create --json":                     ActBound,
		"terminal send --terminal t --text x":        ActBound,
		"orchestration task-create --json":           ActBound,
		"orchestration worker-stop --json":           ActBound,
		"orchestration reply --id m --body b":        ActBound,
		"worktree create --repo r --json":            WorktreeBound,
		"worktree rm --worktree w --json":            WorktreeBound,
		"some-new-verb":                              ActBound,
	} {
		if got := orcaBound(strings.Fields(args)); got != want {
			t.Errorf("orcaBound(%q) = %s, want %s", args, got, want)
		}
	}
}

func TestFMTimeoutLibOrcaExec(t *testing.T) {
	t.Run("FM/fm-timeout-lib/passes_the_command_status_and_output_through", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:46@a8572f6
		// An ok=false envelope on stdout with a non-zero exit still reaches call(), which reports Orca's own code (B-34a).
		stubOrca(t, `echo '{"ok":false,"error":{"code":"repo_not_found","message":"no such repo"}}'; exit 7`+"\n")
		out, err := execOrca("worktree", "ps", "--json")
		if err == nil || err.Error() != "exit status 7" || !strings.Contains(string(out), "repo_not_found") {
			t.Fatalf("status and stdout must pass through: out=%q err=%v", out, err)
		}
		_, err = New("r").call("worktree", "ps", "--json")
		var oe *Error
		if !errors.As(err, &oe) || oe.Code != "repo_not_found" {
			t.Fatalf("call must still read the envelope: %v", err)
		}
	})

	t.Run("FM/fm-timeout-lib/a_descendant_holding_the_output_cannot_outlast_the_bound", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:114@a8572f6
		defer func(r time.Duration) { ReadBound = r }(ReadBound)
		ReadBound = time.Second
		pidFile := filepath.Join(t.TempDir(), "pid")
		stubOrca(t, "trap '' TERM; sleep 30 & echo $! > "+pidFile+"; sleep 30\n")
		start := time.Now()
		_, err := execOrca("terminal", "read", "--terminal", "t", "--json")
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("a hung orca read held the caller %s past a 1s bound", el)
		}
		if err == nil || !strings.Contains(err.Error(), "timed out after 1s") {
			t.Fatalf("a bound hit must name the bound, got %v", err)
		}
		b, _ := os.ReadFile(pidFile)
		pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		if perr != nil {
			t.Fatalf("stub wrote no descendant pid: %q", b)
		}
		// Poll: the reaped descendant is re-parented and collected a moment later (boundexec port_test gone()).
		for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL) // do not leak it past the test
			t.Errorf("the TERM-ignoring descendant %d survived the bound", pid)
		}
	})
}
