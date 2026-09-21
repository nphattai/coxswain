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

// noopRemoveBackend records WorktreeRemove but does NOT actually remove the worktree, so the post-remove verification
// (item 4b) has a path that is still registered to catch - the B-39 case where the backend reported ok yet left the
// checkout in place.
type noopRemoveBackend struct{ *gitBackend }

func (b *noopRemoveBackend) WorktreeRemove(wt backend.Worktree) error {
	b.calls = append(b.calls, "WorktreeRemove")
	b.removed = append(b.removed, wt.Path)
	return nil // no-op
}

// TestCloseNoRuntimeWritesMarker: a v1-migrated / never-attached epic (no .cox/) is archived by writing .cox.closed
// with closed.json noting no_runtime, rather than failing at the archive rename (B-38).
func TestCloseNoRuntimeWritesMarker(t *testing.T) {
	epicDir := t.TempDir()
	var out strings.Builder
	if err := Close(CloseOptions{EpicDir: epicDir, Yes: true, Out: &out}); err != nil {
		t.Fatalf("no-runtime close must succeed: %v", err)
	}
	cj := filepath.Join(epicDir, ".cox.closed", "closed.json")
	b, err := os.ReadFile(cj)
	if err != nil {
		t.Fatalf(".cox.closed/closed.json not written: %v", err)
	}
	if !strings.Contains(string(b), `"no_runtime": true`) {
		t.Errorf("closed.json must note no_runtime: %s", b)
	}
	if !strings.Contains(out.String(), "(no runtime)") {
		t.Errorf("steps must print as vacuous: %q", out.String())
	}
}

// TestCloseFailsWhenWorktreeStillRegistered: a remove that returns ok but leaves the worktree registered fails the
// step (item 4b), does not archive, and records close.incomplete.json.
func TestCloseFailsWhenWorktreeStillRegistered(t *testing.T) {
	epicDir, base, wtPath := closeFixture(t, false)
	rt := &noopRemoveBackend{gitBackend: base}
	var out strings.Builder
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: true, Out: &out})
	if err == nil {
		t.Fatal("close must fail when the worktree is still registered after remove")
	}
	if !strings.Contains(out.String(), "FAILED at remove-worktrees") || !strings.Contains(out.String(), "still registered") {
		t.Errorf("want a FAILED at remove-worktrees ... still registered message, got %q", out.String())
	}
	if fileExists(filepath.Join(epicDir, ".cox.closed")) {
		t.Error(".cox must NOT be archived when a worktree is still registered")
	}
	if !fileExists(filepath.Join(epicDir, ".cox", "close.incomplete.json")) {
		t.Error("close.incomplete.json must be written")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("the no-op backend left the worktree, so it should still exist: %v", err)
	}
}

// TestCloseIgnoresBackendOwnedUntracked: an untracked .orca/ artifact (Orca screenshot drop) does not make a clean
// worktree look dirty, so close still removes it (B-39).
func TestCloseIgnoresBackendOwnedUntracked(t *testing.T) {
	epicDir, rt, wtPath := closeFixture(t, false)
	if err := os.MkdirAll(filepath.Join(wtPath, ".orca", "drops"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, ".orca", "drops", "x.png"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	if err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: false}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if indexOf(rt.calls, "WorktreeRemove") < 0 {
		t.Error("a worktree dirty only with a backend-owned .orca/ artifact must be removed, not kept")
	}
}

// originFixture builds an epic whose one worktree is on epic/x with a bare origin. When ahead is true the branch has a
// commit beyond origin/epic/x (not landed); otherwise its tip equals origin/epic/x (landed via origin, no upstream, B-21).
func originFixture(t *testing.T, ahead bool) (epicDir string, rt *gitBackend, wtPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	origin := t.TempDir()
	git("", "init", "-q", "--bare", origin)
	clone := makeRepo(t)
	git(clone, "remote", "add", "origin", origin)
	git(clone, "checkout", "-q", "-b", "epic/x")
	git(clone, "commit", "-q", "--allow-empty", "-m", "epic work")
	git(clone, "push", "-q", "origin", "main", "epic/x")
	git(clone, "checkout", "-q", "main")

	epicDir = t.TempDir()
	cox := filepath.Join(epicDir, ".cox")
	for _, d := range []string{"sessions", "wt"} {
		if err := os.MkdirAll(filepath.Join(cox, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wtPath = filepath.Join(t.TempDir(), "wt-epic-x")
	git(clone, "worktree", "add", wtPath, "epic/x") // on the existing local branch, no upstream set
	if ahead {
		git(wtPath, "commit", "-q", "--allow-empty", "-m", "unpushed local")
	}
	if err := os.WriteFile(filepath.Join(cox, "wt", "s1"), []byte(wtPath), 0o644); err != nil {
		t.Fatal(err)
	}
	rt = &gitBackend{t: t, repo: clone, wtBase: t.TempDir(), stopOK: true}
	return epicDir, rt, wtPath
}

// TestCloseRemovesLandedNoUpstreamBranch: a branch with no upstream whose tip is contained in origin/<branch> is landed
// and removed (B-21).
func TestCloseRemovesLandedNoUpstreamBranch(t *testing.T) {
	epicDir, rt, wtPath := originFixture(t, false)
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	if err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: false, StoriesOnly: true}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if indexOf(rt.calls, "WorktreeRemove") < 0 {
		t.Error("a no-upstream branch contained in origin must be removed")
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("landed worktree should be removed, err=%v", err)
	}
}

// TestCloseKeepsBranchAheadOfOrigin: a branch with a commit beyond origin/<branch> is not landed and is kept.
func TestCloseKeepsBranchAheadOfOrigin(t *testing.T) {
	epicDir, rt, wtPath := originFixture(t, true)
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	if err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: false, StoriesOnly: true}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if indexOf(rt.calls, "WorktreeRemove") >= 0 {
		t.Error("a branch ahead of origin must be kept, not removed")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("kept worktree should still exist: %v", err)
	}
}

// TestCloseRefusesNonLeaderTerminal: close from a terminal that is not the recorded leader is refused; --captain, or the
// leader terminal itself, proceeds (item 4d).
func TestCloseRefusesNonLeaderTerminal(t *testing.T) {
	writeLeader := func(epicDir string) {
		if err := os.WriteFile(filepath.Join(epicDir, ".cox", "leader"), []byte("term_leader\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Wrong terminal -> refused, nothing archived, no backend calls.
	epicDir, rt, _ := closeFixture(t, false)
	writeLeader(epicDir)
	alloc := &env.Allocator{EpicDir: epicDir, Ops: env.RealOps()}
	err := Close(CloseOptions{EpicDir: epicDir, Runtime: rt, Alloc: alloc, Yes: true, Force: true, TerminalHandle: "term_worker"})
	if err == nil || !strings.Contains(err.Error(), "not the leader") {
		t.Fatalf("wrong terminal must be refused with an owner message, got %v", err)
	}
	if fileExists(filepath.Join(epicDir, ".cox.closed")) {
		t.Error("a refused close must not archive")
	}
	if len(rt.calls) != 0 {
		t.Errorf("a refused close must not touch the backend, calls=%v", rt.calls)
	}

	// --captain proceeds.
	epicDir2, rt2, _ := closeFixture(t, false)
	writeLeader(epicDir2)
	alloc2 := &env.Allocator{EpicDir: epicDir2, Ops: env.RealOps()}
	if err := Close(CloseOptions{EpicDir: epicDir2, Runtime: rt2, Alloc: alloc2, Yes: true, Force: true, TerminalHandle: "term_worker", Captain: true}); err != nil {
		t.Fatalf("--captain must proceed: %v", err)
	}
	if !fileExists(filepath.Join(epicDir2, ".cox.closed")) {
		t.Error("--captain close must archive")
	}

	// The leader terminal itself proceeds.
	epicDir3, rt3, _ := closeFixture(t, false)
	writeLeader(epicDir3)
	alloc3 := &env.Allocator{EpicDir: epicDir3, Ops: env.RealOps()}
	if err := Close(CloseOptions{EpicDir: epicDir3, Runtime: rt3, Alloc: alloc3, Yes: true, Force: true, TerminalHandle: "term_leader"}); err != nil {
		t.Fatalf("the leader terminal must proceed: %v", err)
	}
	if !fileExists(filepath.Join(epicDir3, ".cox.closed")) {
		t.Error("leader close must archive")
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
