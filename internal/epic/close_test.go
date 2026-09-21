package epic

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/env"
)

// closeFixture builds an epic dir with one session, one resources entry, and one real story worktree wired to .cox/wt.
func closeFixture(t *testing.T, dirty bool) (epicDir string, rt *gitBackend, wtPath string) {
	t.Helper()
	repo := makeRepo(t)
	t.Setenv("HOME", t.TempDir())
	epicDir = t.TempDir()
	cox := filepath.Join(epicDir, ".cox")
	for _, d := range []string{"sessions", "wt"} {
		if err := os.MkdirAll(filepath.Join(cox, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A real story worktree.
	wtPath = filepath.Join(t.TempDir(), "wt-story-s1")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "story/s1", wtPath, "main").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(wtPath, "scratch"), []byte("wip"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Session + resources + wt pointer.
	sess, _ := json.Marshal(backend.Session{Kind: "fake", ID: "sess1"})
	if err := os.WriteFile(filepath.Join(cox, "sessions", "s1.json"), sess, 0o644); err != nil {
		t.Fatal(err)
	}
	res := `{"stories":{"s1":{"port":3400,"confirmed_released":false}}}`
	if err := os.WriteFile(filepath.Join(cox, "resources.json"), []byte(res), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cox, "wt", "s1"), []byte(wtPath), 0o644); err != nil {
		t.Fatal(err)
	}
	rt = &gitBackend{t: t, repo: repo, wtBase: t.TempDir(), stopOK: true}
	return epicDir, rt, wtPath
}

func TestCloseStopsBeforeRemoveAndArchives(t *testing.T) {
	epicDir, rt, _ := closeFixture(t, false)
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	// Worker stop must come before the worktree removal.
	stopIdx, rmIdx := indexOf(rt.calls, "Stop"), indexOf(rt.calls, "WorktreeRemove")
	if stopIdx < 0 || rmIdx < 0 || stopIdx > rmIdx {
		t.Fatalf("stop must precede remove, calls=%v", rt.calls)
	}
	// Archived only after all steps.
	if _, err := os.Stat(filepath.Join(epicDir, ".cox.closed")); err != nil {
		t.Fatalf(".cox not archived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox")); !os.IsNotExist(err) {
		t.Fatalf(".cox should be gone after archive, err=%v", err)
	}
}

func TestCloseUnconfirmedStopDoesNotArchive(t *testing.T) {
	epicDir, rt, _ := closeFixture(t, false)
	rt.stopOK = false
	rt.live = backend.Unknown // probe cannot confirm settled
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: true})
	if err == nil {
		t.Fatal("close must fail when a worker is not confirmed stopped")
	}
	// No archive; incomplete record written; worktree never removed.
	if _, err := os.Stat(filepath.Join(epicDir, ".cox.closed")); !os.IsNotExist(err) {
		t.Fatalf(".cox must NOT be archived on failure")
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox", "close.incomplete.json")); err != nil {
		t.Fatalf("close.incomplete.json missing: %v", err)
	}
	if indexOf(rt.calls, "WorktreeRemove") >= 0 {
		t.Fatalf("worktree must not be removed after a failed worker stop")
	}
}

// TestCloseStopErrorProbeSettled covers the Orca case where Stop errors (it closed the terminal itself) but the
// worker has actually settled: a Settled probe overrides the Stop error and close proceeds; Alive or Unknown fails.
func TestCloseStopErrorProbeSettled(t *testing.T) {
	cases := []struct {
		name     string
		live     backend.Liveness
		probeErr bool
		wantErr  bool
	}{
		{"stop error + settled -> ok", backend.Settled, false, false},
		{"stop error + alive -> fail", backend.Alive, false, true},
		{"stop error + unknown -> fail", backend.Unknown, false, true},
		{"stop error + probe error -> fail", backend.Unknown, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			epicDir, rt, _ := closeFixture(t, false)
			rt.stopOK = false
			rt.stopErr = errors.New("Terminal closed by operator request")
			rt.live = tc.live
			if tc.probeErr {
				rt.probeErr = errors.New("probe failed")
			}
			alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
			err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: true})
			archived := fileExists(filepath.Join(epicDir, ".cox.closed"))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected close to fail (ownership kept, not archived)")
				}
				if archived {
					t.Fatal(".cox must NOT be archived when the worker is not confirmed settled")
				}
				return
			}
			if err != nil {
				t.Fatalf("Settled probe should let close proceed despite the Stop error: %v", err)
			}
			if !archived {
				t.Fatal(".cox should be archived once the probe confirms settled")
			}
		})
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// startFakeWatcher launches a real long-lived process and records its pid in <epic>/.cox/watch.pid, so close's
// stop-watcher step has a live pid to reason about. The process is killed on cleanup if close did not.
func startFakeWatcher(t *testing.T, epicDir string) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake watcher: %v", err)
	}
	pid := cmd.Process.Pid
	// Reap on exit so a SIGTERM'd process does not linger as a zombie that a signal-0 probe still reports alive. In
	// production the watcher is not close's child, so the OS reaps it; the goroutine reproduces that here.
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if err := os.WriteFile(filepath.Join(epicDir, ".cox", "watch.pid"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	return pid
}

// A proven watcher (its command line names this epic's `cox watch --epic <dir>`) is stopped before the archive.
func TestCloseStopsProvenWatcher(t *testing.T) {
	epicDir, rt, _ := closeFixture(t, false)
	pid := startFakeWatcher(t, epicDir)
	old := watcherProcArgs
	watcherProcArgs = func(int) (string, error) { return "cox watch --epic " + epicDir, nil }
	t.Cleanup(func() { watcherProcArgs = old })

	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	if err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if procAlive(pid) {
		t.Errorf("a proven watcher must be stopped before archive (pid %d still alive)", pid)
	}
	if !fileExists(filepath.Join(epicDir, ".cox.closed")) {
		t.Errorf(".cox must be archived once the watcher is stopped")
	}
}

// A live pid whose command line does NOT prove it is this epic's watcher is never signalled, and close refuses to
// archive (arena round 1, adversary-1-1).
func TestCloseRefusesUnprovableWatcher(t *testing.T) {
	epicDir, rt, _ := closeFixture(t, false)
	pid := startFakeWatcher(t, epicDir)
	old := watcherProcArgs
	watcherProcArgs = func(int) (string, error) { return "sleep 30", nil } // no cox watch / --epic proof
	t.Cleanup(func() { watcherProcArgs = old })

	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: true})
	if err == nil {
		t.Fatal("close must refuse to archive while an unprovable pid is alive")
	}
	if !procAlive(pid) {
		t.Errorf("an unprovable pid must never be signalled (pid %d was killed)", pid)
	}
	if fileExists(filepath.Join(epicDir, ".cox.closed")) {
		t.Errorf(".cox must NOT be archived when the watcher could not be stopped")
	}
	if !fileExists(filepath.Join(epicDir, ".cox", "close.incomplete.json")) {
		t.Errorf("an aborted close must record close.incomplete.json")
	}
}

func TestCloseKeepsDirtyWorktree(t *testing.T) {
	epicDir, rt, wtPath := closeFixture(t, true)
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: false})
	if err != nil {
		t.Fatal(err)
	}
	// A dirty worktree is kept (not a failure), so the archive still proceeds.
	if indexOf(rt.calls, "WorktreeRemove") >= 0 {
		t.Fatalf("dirty worktree must be kept, not removed")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("kept worktree should still exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox.closed")); err != nil {
		t.Fatalf("close should archive even when a worktree is kept: %v", err)
	}
}

func TestCloseDryRunChangesNothing(t *testing.T) {
	epicDir, rt, _ := closeFixture(t, false)
	var out strings.Builder
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	if err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("dry run should print a plan: %q", out.String())
	}
	if len(rt.calls) != 0 {
		t.Fatalf("dry run must not call the backend, calls=%v", rt.calls)
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox")); err != nil {
		t.Fatalf(".cox must survive a dry run: %v", err)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
