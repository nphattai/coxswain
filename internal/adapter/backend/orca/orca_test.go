package orca

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/busy"
)

var _ backend.Backend = New("run")

// withRunner builds a client whose command runner returns canned output chosen by a substring of the joined args.
func withRunner(routes map[string]struct {
	out []byte
	err error
}) *Client {
	c := New("run_1")
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		for key, r := range routes {
			if strings.Contains(joined, key) {
				return r.out, r.err
			}
		}
		// Default the run-binding guard to "already bound to run_1" so a mutation test that does not care about
		// rebinding makes no run-use calls; a test that does care routes run-current explicitly.
		if strings.Contains(joined, "run-current") {
			return []byte(`{"ok":true,"result":{"run":{"id":"run_1"}}}`), nil
		}
		return nil, errors.New("no route for: " + joined)
	}
	return c
}

// A mutation rebinds the coordinator terminal to this client's run when it is bound elsewhere, then restores the
// previous binding after. The recorded command order proves the run-current / run-use / mutation / run-use sequence.
func TestMutateRebindsAndRestores(t *testing.T) {
	var calls []string
	c := New("run_1")
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if strings.Contains(joined, "run-current") {
			return []byte(`{"ok":true,"result":{"run":{"id":"run_e2e"}}}`), nil
		}
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	if _, err := c.Stop(backend.Session{ID: "ctx_9"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"orchestration run-current --json",
		"orchestration run-use --id run_1 --json",
		"orchestration worker-stop --dispatch ctx_9 --json",
		"orchestration run-use --id run_e2e --json",
	}
	if len(calls) != len(want) {
		t.Fatalf("command order = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q (full: %v)", i, calls[i], want[i], calls)
		}
	}
}

// When the terminal is already bound to this client's run, a mutation makes no run-use call at all.
func TestMutateSkipsWhenAlreadyBound(t *testing.T) {
	var calls []string
	c := New("run_1")
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if strings.Contains(joined, "run-current") {
			return []byte(`{"ok":true,"result":{"run":{"id":"run_1"}}}`), nil
		}
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	if _, err := c.Stop(backend.Session{ID: "ctx_9"}); err != nil {
		t.Fatal(err)
	}
	for _, g := range calls {
		if strings.Contains(g, "run-use") {
			t.Fatalf("no rebind expected when already bound, got %v", calls)
		}
	}
	if len(calls) != 2 || !strings.Contains(calls[1], "worker-stop") {
		t.Fatalf("want run-current then worker-stop, got %v", calls)
	}
}

// When the terminal is unbound (run:null), a mutation binds to this run and leaves it bound (nothing to restore).
func TestMutateBindsUnboundTerminalWithoutRestore(t *testing.T) {
	var calls []string
	c := New("run_1")
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if strings.Contains(joined, "run-current") {
			return []byte(`{"ok":true,"result":{"run":null}}`), nil
		}
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	if _, err := c.Stop(backend.Session{ID: "ctx_9"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"orchestration run-current --json",
		"orchestration run-use --id run_1 --json",
		"orchestration worker-stop --dispatch ctx_9 --json",
	}
	if len(calls) != len(want) {
		t.Fatalf("command order = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
}

func TestMapLiveness(t *testing.T) {
	cases := map[string]backend.Liveness{
		"live": backend.Alive, "alive": backend.Alive,
		"settled": backend.Settled, "closed": backend.Settled, "failed": backend.Settled,
	}
	for in, want := range cases {
		got, err := mapLiveness(in)
		if err != nil || got != want {
			t.Errorf("mapLiveness(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "weird", "sleepy"} {
		got, err := mapLiveness(in)
		if got != backend.Unknown || err == nil {
			t.Errorf("mapLiveness(%q) = %v, %v; want Unknown + error", in, got, err)
		}
	}
}

func TestWorktreeCreateParsesAndStripsRef(t *testing.T) {
	c := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"worktree create": {out: []byte(`{"ok":true,"result":{"worktree":{"path":"/wt/x","branch":"refs/heads/story/x"}}}`)},
	})
	wt, err := c.WorktreeCreate("repo", "story/x", "base")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Path != "/wt/x" || wt.Branch != "story/x" {
		t.Fatalf("got %+v", wt)
	}
}

func TestRepoSelector(t *testing.T) {
	cases := map[string]string{
		"acme-inside":              "name:acme-inside",
		"ExampleOrg/acme-partner":  "name:ExampleOrg/acme-partner",
		"/Users/tai/Work/coxswain": "path:/Users/tai/Work/coxswain",
	}
	for in, want := range cases {
		if got := repoSelector(in); got != want {
			t.Errorf("repoSelector(%q) = %q, want %q", in, got, want)
		}
	}
}

// An absolute-path repo (Orca returns name:null for it) is addressed with a path selector on worktree create.
func TestWorktreeCreateUsesPathSelectorForAbsolutePath(t *testing.T) {
	var got string
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte(`{"ok":true,"result":{"worktree":{"path":"/wt/x","branch":"story/x"}}}`), nil
	}
	if _, err := c.WorktreeCreate("/Users/tai/Work/coxswain", "story/x", "base"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--repo path:/Users/tai/Work/coxswain") {
		t.Fatalf("absolute-path repo must use path selector, got %q", got)
	}
}

// Orca names the branch from the worktree name (username prefix, slashes flattened); when the canonical branch does not
// exist yet, WorktreeCreate renames the on-disk branch to it (never deleting a branch).
func TestWorktreeCreateRenamesBranchToRequested(t *testing.T) {
	var gitCalls []string
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":true,"result":{"worktree":{"path":"/wt/x","branch":"nphattai/epic-e2e-1"}}}`), nil
	}
	c.git = func(args ...string) ([]byte, error) {
		gitCalls = append(gitCalls, strings.Join(args, " "))
		if len(args) >= 3 && args[2] == "branch" && args[len(args)-1] == "--show-current" {
			return []byte("nphattai/epic-e2e-1\n"), nil
		}
		if len(args) >= 3 && args[2] == "show-ref" {
			return nil, errors.New("not found") // target branch absent -> rename path
		}
		return nil, nil
	}
	wt, err := c.WorktreeCreate("/repo", "epic/e2e-1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "epic/e2e-1" {
		t.Fatalf("branch should be canonicalized, got %q", wt.Branch)
	}
	renamed := false
	for _, g := range gitCalls {
		if strings.Contains(g, "branch -m epic/e2e-1") {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("expected a branch -m rename, git calls: %v", gitCalls)
	}
}

// When the canonical branch already exists (a parked story resumed into a fresh worktree), git branch -m would fail, so
// WorktreeCreate switches to the existing branch and leaves the mangled branch untouched (F01).
func TestWorktreeCreateSwitchesToExistingBranch(t *testing.T) {
	var gitCalls []string
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":true,"result":{"worktree":{"path":"/wt/x","branch":"nphattai/story-e2e-hello"}}}`), nil
	}
	c.git = func(args ...string) ([]byte, error) {
		gitCalls = append(gitCalls, strings.Join(args, " "))
		if len(args) >= 3 && args[2] == "branch" && args[len(args)-1] == "--show-current" {
			return []byte("nphattai/story-e2e-hello\n"), nil
		}
		if len(args) >= 3 && args[2] == "show-ref" {
			return nil, nil // target branch exists -> switch path
		}
		if len(args) >= 3 && args[2] == "rev-list" {
			return []byte("0\n"), nil // no unmerged commits -> safe to reset to base
		}
		return nil, nil
	}
	wt, err := c.WorktreeCreate("/repo", "story/e2e-hello", "main")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "story/e2e-hello" {
		t.Fatalf("branch should be canonicalized, got %q", wt.Branch)
	}
	for _, g := range gitCalls {
		if strings.Contains(g, "branch -m") {
			t.Fatalf("must not rename onto an existing branch, git calls: %v", gitCalls)
		}
	}
	switched := false
	for _, g := range gitCalls {
		if strings.Contains(g, "switch story/e2e-hello") {
			switched = true
		}
	}
	if !switched {
		t.Fatalf("expected a switch to the existing branch, git calls: %v", gitCalls)
	}
	// A11: after switching onto the reused branch, it is reset to the requested base so a stale HEAD is refreshed.
	reset := false
	for _, g := range gitCalls {
		if strings.Contains(g, "reset --hard main") {
			reset = true
		}
	}
	if !reset {
		t.Fatalf("expected reset --hard main to bring the reused branch to base, git calls: %v", gitCalls)
	}
}

// A11 live: a reused branch at a stale HEAD with no unmerged commits is reset to base; a branch that is ahead of base is
// refused rather than reset (its commits must not be discarded). Uses a real temp git repo.
func TestWorktreeCreateResetsReusedBranchToBase(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a"), []byte("1"), 0o644)
	git("add", ".")
	git("commit", "-qm", "A")
	git("branch", "arena/adversary-r1") // reused branch pinned at the old HEAD (A)
	os.WriteFile(filepath.Join(repo, "b"), []byte("2"), 0o644)
	git("add", ".")
	git("commit", "-qm", "B") // main advances to B; the pack's HEAD
	base := git("rev-parse", "main")

	// The worktree is the repo itself; the run returns a mangled branch, git is real.
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":true,"result":{"worktree":{"path":"` + repo + `","branch":"nphattai/arena-adversary-r1"}}}`), nil
	}
	if _, err := c.WorktreeCreate("/repo", "arena/adversary-r1", "main"); err != nil {
		t.Fatalf("reset path: %v", err)
	}
	if head := git("rev-parse", "HEAD"); head != base {
		t.Fatalf("reused branch HEAD = %s, want base %s (should have been reset)", head, base)
	}
	if cur := git("branch", "--show-current"); cur != "arena/adversary-r1" {
		t.Fatalf("current branch = %q, want arena/adversary-r1", cur)
	}

	// Now the branch is ahead of base: add a commit on it, move main elsewhere, and expect a refusal.
	os.WriteFile(filepath.Join(repo, "c"), []byte("3"), 0o644)
	git("add", ".")
	git("commit", "-qm", "C-on-branch") // arena/adversary-r1 is now ahead of main
	aheadHead := git("rev-parse", "HEAD")
	git("switch", "-q", "main")
	if _, err := c.WorktreeCreate("/repo", "arena/adversary-r1", "main"); err == nil || !strings.Contains(err.Error(), "not in base") {
		t.Fatalf("a branch ahead of base must be refused, got %v", err)
	}
	// The ahead branch was not reset (its commit survives).
	if h := git("rev-parse", "arena/adversary-r1"); h != aheadHead {
		t.Fatalf("refused branch was modified: %s != %s", h, aheadHead)
	}
}

// Spawn resolves the dispatch via worker-list and the terminal via worker-show, because worker-start returns neither.
func TestSpawnResolvesDispatchAndTerminal(t *testing.T) {
	c := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"task-create":  {out: []byte(`{"ok":true,"result":{"task":{"id":"task_9"}}}`)},
		"worker-start": {out: []byte(`{"ok":true,"result":{"stage":"input_accepted"}}`)},
		// last matching taskId wins; an earlier stale dispatch for the same task is ignored.
		"worker-list": {out: []byte(`{"ok":true,"result":{"workers":[{"dispatchId":"ctx_old","taskId":"task_9"},{"dispatchId":"ctx_other","taskId":"task_7"},{"dispatchId":"ctx_new","taskId":"task_9"}]}}`)},
		"worker-show": {out: []byte(`{"ok":true,"result":{"terminal":{"handle":"term_abc"}}}`)},
	})
	s, err := c.Spawn(backend.Worktree{Path: "/wt/x"}, backend.HarnessSpec{Name: "claude"}, backend.Brief{Text: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "ctx_new" || s.Handle != "term_abc" || s.Kind != "orca" {
		t.Fatalf("got %+v", s)
	}
}

// A worker-show that fails or reports no handle leaves the handle empty but still spawns (handle is best-effort).
func TestSpawnEmptyTerminalIsNotFatal(t *testing.T) {
	c := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"task-create":  {out: []byte(`{"ok":true,"result":{"task":{"id":"task_9"}}}`)},
		"worker-start": {out: []byte(`{"ok":true,"result":{"stage":"input_accepted"}}`)},
		"worker-list":  {out: []byte(`{"ok":true,"result":{"workers":[{"dispatchId":"ctx_new","taskId":"task_9"}]}}`)},
		"worker-show":  {out: []byte(`{"ok":true,"result":{"terminal":{"handle":""}}}`)},
	})
	s, err := c.Spawn(backend.Worktree{Path: "/wt/x"}, backend.HarnessSpec{Name: "claude"}, backend.Brief{Text: "do it"})
	if err != nil || s.ID != "ctx_new" || s.Handle != "" {
		t.Fatalf("got %+v err=%v", s, err)
	}
}

// No worker carries the task id -> Spawn errors rather than returning an empty dispatch.
func TestSpawnErrorsWhenNoDispatch(t *testing.T) {
	c := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"task-create":  {out: []byte(`{"ok":true,"result":{"task":{"id":"task_9"}}}`)},
		"worker-start": {out: []byte(`{"ok":true,"result":{"stage":"input_accepted"}}`)},
		"worker-list":  {out: []byte(`{"ok":true,"result":{"workers":[{"dispatchId":"ctx_other","taskId":"task_7"}]}}`)},
	})
	_, err := c.Spawn(backend.Worktree{Path: "/wt/x"}, backend.HarnessSpec{Name: "claude"}, backend.Brief{Text: "do it"})
	if err == nil || !strings.Contains(err.Error(), "no dispatch found for task task_9") {
		t.Fatalf("want no-dispatch error, got %v", err)
	}
}

func TestCallSurfacesOrcaError(t *testing.T) {
	c := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"worktree create": {out: []byte(`{"ok":false,"error":{"message":"repo not found"}}`)},
	})
	_, err := c.WorktreeCreate("repo", "b", "base")
	if err == nil || !strings.Contains(err.Error(), "repo not found") {
		t.Fatalf("want surfaced orca error, got %v", err)
	}
}

func TestProbeLiveAndError(t *testing.T) {
	c := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"worker-read": {out: []byte(`{"ok":true,"result":{"status":{"liveness":"live"}}}`)},
	})
	live, err := c.Probe(backend.Session{ID: "ctx_1"})
	if err != nil || live != backend.Alive {
		t.Fatalf("live probe: %v %v", live, err)
	}

	// A failing call resolves to Unknown with an error, never Settled (F08).
	c2 := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"worker-read": {err: errors.New("exit 1")},
	})
	live, err = c2.Probe(backend.Session{ID: "ctx_1"})
	if live != backend.Unknown || err == nil {
		t.Fatalf("failed probe should be Unknown+error, got %v %v", live, err)
	}
}

func TestStopConfirmation(t *testing.T) {
	c := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"worker-stop": {out: []byte(`{"ok":true,"result":{}}`)},
	})
	if confirmed, err := c.Stop(backend.Session{ID: "ctx_1"}); err != nil || !confirmed {
		t.Fatalf("confirmed=%v err=%v", confirmed, err)
	}

	c2 := withRunner(map[string]struct {
		out []byte
		err error
	}{
		"worker-stop": {out: []byte(`{"ok":false,"error":{"message":"gone"}}`)},
	})
	if confirmed, err := c2.Stop(backend.Session{ID: "ctx_1"}); err == nil || confirmed {
		t.Fatalf("unconfirmed stop expected, got confirmed=%v err=%v", confirmed, err)
	}
}

// Check reads via `orchestration check --run` and returns the batch's delivery id; Ack("") is a no-op that never
// touches the runner.
func TestMailboxCheckReadsCheckAndReturnsDelivery(t *testing.T) {
	var gotArgs string
	c := New("run_1")
	c.run = func(args ...string) ([]byte, error) {
		gotArgs = strings.Join(args, " ")
		return []byte(`{"ok":true,"result":{"deliveryId":"d_9","count":1,"messages":[{"id":"m1","from_handle":"h","to_handle":"run:run_1","subject":"s","body":"b","type":"status","read":0}]}}`), nil
	}
	mb := c.Mail()
	msgs, delivery, err := mb.Check()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotArgs, "orchestration check --run run_1") {
		t.Fatalf("Check must read the run-scoped check reader, got %q", gotArgs)
	}
	if len(msgs) != 1 || msgs[0].ID != "m1" || msgs[0].Read {
		t.Fatalf("got %+v", msgs)
	}
	if delivery != "d_9" {
		t.Fatalf("check must return the delivery id, got %q", delivery)
	}
	if err := mb.Ack(""); err != nil {
		t.Fatalf("Ack(\"\") should be a no-op, got %v", err)
	}
}

// `check --run` is run-scoped, but Check still filters each message to this run's to_handle as a guard. A blank Run
// errors rather than reading mail unscoped.
func TestMailboxCheckFiltersByRun(t *testing.T) {
	c := New("run_1")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":true,"result":{"deliveryId":"d_1","messages":[
			{"id":"a","to_handle":"run:run_1","type":"worker_done"},
			{"id":"b","to_handle":"run:run_2","type":"worker_done"},
			{"id":"d","to_handle":"run:run_1","type":"status"}
		]}}`), nil
	}
	msgs, delivery, err := c.Mail().Check()
	if err != nil {
		t.Fatal(err)
	}
	if delivery != "d_1" {
		t.Fatalf("want delivery d_1, got %q", delivery)
	}
	if len(msgs) != 2 || msgs[0].ID != "a" || msgs[1].ID != "d" {
		t.Fatalf("Check must keep only run_1 mail, got %+v", msgs)
	}

	// A client with no run refuses rather than reading mail unscoped.
	blank := New("")
	blank.run = c.run
	if _, _, err := blank.Mail().Check(); err == nil || !strings.Contains(err.Error(), "no run id") {
		t.Fatalf("blank run must error, got %v", err)
	}
}

// WorktreeRemove must detach BEFORE rm, and must NOT call rm if the detach fails (detach is what protects the branch).
func TestWorktreeRemoveDetachesBeforeRm(t *testing.T) {
	var calls []string
	c := New("run")
	c.git = func(args ...string) ([]byte, error) {
		calls = append(calls, "git "+strings.Join(args, " "))
		return nil, nil
	}
	c.run = func(args ...string) ([]byte, error) {
		calls = append(calls, "orca "+strings.Join(args, " "))
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	if err := c.WorktreeRemove(backend.Worktree{Path: "/wt/x"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 ||
		!strings.Contains(calls[0], "git -C /wt/x switch --detach") ||
		!strings.Contains(calls[1], "orca worktree rm --worktree path:/wt/x --force") {
		t.Fatalf("wrong order/commands: %#v", calls)
	}

	// Detach fails -> rm is never called, and the error is surfaced.
	calls = nil
	c.git = func(args ...string) ([]byte, error) {
		calls = append(calls, "git")
		return nil, errors.New("detach boom")
	}
	c.run = func(args ...string) ([]byte, error) {
		calls = append(calls, "orca")
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	err := c.WorktreeRemove(backend.Worktree{Path: "/wt/x"})
	if err == nil || !strings.Contains(err.Error(), "detach boom") {
		t.Fatalf("want detach error, got %v", err)
	}
	if len(calls) != 1 || calls[0] != "git" {
		t.Fatalf("rm must not run after a failed detach: %#v", calls)
	}
}

// On a real git repo, the detach WorktreeRemove performs leaves the story branch intact (F01). Orca's rm is stubbed
// (no Orca in unit tests); the point is that detaching the worktree does not delete its branch.
func TestWorktreeRemoveDetachKeepsBranchRealRepo(t *testing.T) {
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo := t.TempDir()
	git(repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "-A")
	git(repo, "commit", "-q", "-m", "init")
	wtPath := filepath.Join(t.TempDir(), "wt")
	git(repo, "worktree", "add", "-q", "-b", "story/x", wtPath)

	c := New("run")
	c.run = func(args ...string) ([]byte, error) { return []byte(`{"ok":true,"result":{}}`), nil } // stub orca rm
	if err := c.WorktreeRemove(backend.Worktree{Path: wtPath}); err != nil {
		t.Fatal(err)
	}

	// The branch must still exist after detach+rm, and the worktree HEAD must be detached.
	out, err := exec.Command("git", "-C", repo, "branch", "--list", "story/x").Output()
	if err != nil || !strings.Contains(string(out), "story/x") {
		t.Fatalf("branch story/x should survive detach, got %q err=%v", out, err)
	}
	head, _ := exec.Command("git", "-C", wtPath, "symbolic-ref", "-q", "HEAD").Output()
	if strings.TrimSpace(string(head)) != "" {
		t.Fatalf("worktree HEAD should be detached, still on %q", head)
	}
}

// B-16: WorktreeRemove refuses a branch that is not on origin (git ls-remote --heads finds nothing), never calling
// `orca worktree rm` for it; a branch on origin proceeds (detach + rm), and an explicit force overrides the guard.
func TestWorktreeRemoveRefusesBranchNotOnOrigin(t *testing.T) {
	newClient := func(onOrigin bool) (*Client, *bool) {
		rmCalled := false
		c := New("run")
		c.git = func(args ...string) ([]byte, error) {
			if len(args) > 0 && contains(args, "ls-remote") {
				if onOrigin {
					return []byte("abc123\trefs/heads/epic/x\n"), nil
				}
				return nil, nil // not on origin
			}
			return nil, nil
		}
		c.run = func(args ...string) ([]byte, error) {
			if contains(args, "rm") {
				rmCalled = true
			}
			return []byte(`{"ok":true,"result":{}}`), nil
		}
		return c, &rmCalled
	}

	// Not on origin -> refuse, and rm is never called.
	c, rmCalled := newClient(false)
	err := c.WorktreeRemove(backend.Worktree{Path: "/wt/x", Branch: "epic/x"})
	if err == nil || !strings.Contains(err.Error(), "not on origin") {
		t.Fatalf("want a not-on-origin refusal, got %v", err)
	}
	if *rmCalled {
		t.Error("orca worktree rm must not be called for a branch not on origin (B-16)")
	}

	// On origin -> proceeds.
	c2, rmCalled2 := newClient(true)
	if err := c2.WorktreeRemove(backend.Worktree{Path: "/wt/x", Branch: "epic/x"}); err != nil {
		t.Fatalf("a branch on origin must be removable: %v", err)
	}
	if !*rmCalled2 {
		t.Error("orca worktree rm must run for a branch on origin")
	}

	// Explicit force overrides the guard even when not on origin.
	c3, rmCalled3 := newClient(false)
	if err := c3.WorktreeRemove(backend.Worktree{Path: "/wt/x", Branch: "epic/x", Force: true}); err != nil {
		t.Fatalf("force must override the guard: %v", err)
	}
	if !*rmCalled3 {
		t.Error("force must allow orca worktree rm to run")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestClassifyComposer(t *testing.T) {
	cases := []struct {
		name string
		tail []string
		want string
	}{
		{"bare prompt", []string{"⏺ done", "❯"}, "empty"},
		{"prompt above status", []string{"❯", "Opus 4.8 | 30k (12%)"}, "empty"},
		{"box above status", []string{"╭─ transcript", "Sonnet 4 | 5k (2%)"}, "empty"},
		{"typed prompt", []string{"❯ keep going"}, "pending"},
		{"typed above status", []string{"❯ keep going", "Opus 4.8 | 30k (12%)"}, "pending"},
		{"spinner busy", []string{"✻─Thinking…"}, "busy"},
		{"compacting busy", []string{"Compacting conversation…"}, "busy"},
		{"unknown log", []string{"some log line"}, "unknown"},
		{"empty tail", nil, "unknown"},
		// A raw (non-screen) stream tail is truncated garbage and must classify unknown - the reason composerState
		// passes --screen so it gets the rendered rows (the "prompt above status" case) instead.
		{"raw stream truncated", []string{"  B"}, "unknown"},
		// F10: the real six-row --screen tail of an idle worker, footer row last. Dropping the footer makes the status
		// line the last row so it classifies empty.
		{"idle with footer", []string{
			"✻ Crunched for 11m 52s · done 5:32 PM",
			"╰─",
			"❯",
			"╭─",
			"  Opus 4.8 | high | 503k (50%) | 5h 62% → 19:00 | 7d 52%",
			"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
		}, "empty"},
		// A typed prompt above the status line, with the same footer, is pending.
		{"typed with footer", []string{
			"❯ keep going",
			"  Opus 4.8 | high | 503k (50%)",
			"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
		}, "pending"},
		// Codex footer (fixtures from a live codex run, M14): the placeholder prompt is empty, above its status line too.
		{"codex idle placeholder", []string{"› Ask Codex to do anything"}, "empty"},
		{"codex placeholder above status", []string{
			"› Ask Codex to do anything",
			"gpt-5.6-sol · high · Context 60% used · weekly 87% left · Main [default]",
		}, "empty"},
		{"codex status line only", []string{"gpt-5.6-sol · high · Context 60% used · weekly 87% left · Main [default]"}, "empty"},
		{"codex typed", []string{"› fix the failing test"}, "pending"},
		{"codex typed above status", []string{
			"› fix the failing test",
			"gpt-5.6-sol · high · Context 60% used · weekly 87% left",
		}, "pending"},
	}
	for _, tc := range cases {
		if got := classifyComposer(tc.tail); got != tc.want {
			t.Errorf("classifyComposer(%q) = %q, want %q", tc.tail, got, tc.want)
		}
	}
}

// The doorbell types into the terminal only when the composer is empty, and reports rang accordingly.
func TestSendDoorbellRingsOnlyOnEmptyComposer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tail     string
		wantRang bool
	}{
		{"empty", `["⏺ done","❯"]`, true},
		{"pending", `["❯ typing"]`, false},
		{"busy", `["Compacting conversation…"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := false
			c := New("run_1")
			c.run = func(args ...string) ([]byte, error) {
				j := strings.Join(args, " ")
				switch {
				case strings.Contains(j, "terminal read"):
					// The composer read must ask for the rendered screen, not the raw stream tail (which is
					// truncated and would classify unknown, so an empty composer never rings).
					if !strings.Contains(j, "--screen") {
						t.Fatalf("terminal read must pass --screen, got: %s", j)
					}
					return []byte(`{"ok":true,"result":{"terminal":{"tail":` + tc.tail + `}}}`), nil
				case strings.Contains(j, "terminal send"):
					sent = true
					return []byte(`{"ok":true,"result":{}}`), nil
				}
				return nil, errors.New("no route: " + j)
			}
			rang, err := c.Send(backend.Session{Handle: "term_x"}, "ring")
			if err != nil {
				t.Fatal(err)
			}
			if rang != tc.wantRang {
				t.Fatalf("rang=%v want %v", rang, tc.wantRang)
			}
			if sent != tc.wantRang {
				t.Fatalf("terminal send called=%v want %v", sent, tc.wantRang)
			}
		})
	}
}

// On the terminal plane the doorbell decides from Orca's agents[] state first (M14): idle|done ring, working defers,
// waiting is skipped:permission (ErrAgentPromptBlocked). A missing agents[] entry falls back to text classification,
// which now recognizes the codex footer and rings an idle codex composer. A send refused with agent_prompt_blocked is
// also skipped:permission.
func TestRingTerminalByAgentState(t *testing.T) {
	const okSend = `{"ok":true,"result":{}}`
	const blockedSend = `{"ok":false,"error":{"code":"agent_prompt_blocked","message":"pane waiting on approval"}}`
	for _, tc := range []struct {
		name       string
		state      string // agents[] state; "" => no entry (fallback to text)
		readTail   string // terminal read tail for the fallback
		sendResp   string
		wantRang   bool
		wantSent   bool
		wantBlockt bool
	}{
		{name: "idle rings", state: "idle", sendResp: okSend, wantRang: true, wantSent: true},
		{name: "done rings", state: "done", sendResp: okSend, wantRang: true, wantSent: true},
		{name: "working defers", state: "working", wantRang: false, wantSent: false},
		{name: "waiting is permission blocked", state: "waiting", wantRang: false, wantSent: false, wantBlockt: true},
		{name: "send refused is permission blocked", state: "idle", sendResp: blockedSend, wantRang: false, wantSent: true, wantBlockt: true},
		{name: "no entry falls back to codex footer", state: "", readTail: `["› Ask Codex to do anything","gpt-5.6-sol · high · Context 60% used · weekly 87% left"]`, sendResp: okSend, wantRang: true, wantSent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := false
			c := New("run")
			c.Plane = "terminal"
			c.run = func(args ...string) ([]byte, error) {
				j := strings.Join(args, " ")
				switch {
				case strings.Contains(j, "terminal show"):
					return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"tab1","leafId":"leaf1"}}}`), nil
				case strings.Contains(j, "worktree ps"):
					if tc.state != "" {
						return []byte(`{"ok":true,"result":{"worktrees":[{"agents":[{"paneKey":"tab1:leaf1","state":"` + tc.state + `"}]}]}}`), nil
					}
					return []byte(`{"ok":true,"result":{"worktrees":[]}}`), nil
				case strings.Contains(j, "terminal read"):
					return []byte(`{"ok":true,"result":{"terminal":{"tail":` + tc.readTail + `}}}`), nil
				case strings.Contains(j, "terminal send"):
					sent = true
					return []byte(tc.sendResp), nil
				}
				return nil, errors.New("no route: " + j)
			}
			rang, err := c.Send(backend.Session{Kind: SessionKindTerminal, ID: "term_x", Handle: "term_x"}, "ring")
			if rang != tc.wantRang {
				t.Errorf("rang=%v want %v (err=%v)", rang, tc.wantRang, err)
			}
			if sent != tc.wantSent {
				t.Errorf("terminal send called=%v want %v", sent, tc.wantSent)
			}
			if tc.wantBlockt {
				if !errors.Is(err, backend.ErrAgentPromptBlocked) {
					t.Errorf("want ErrAgentPromptBlocked, got %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// With no terminal handle, Send falls back to orchestration mail and never claims a ring.
func TestSendNoHandleFallsBackToMail(t *testing.T) {
	var got string
	c := New("run_1")
	c.run = func(args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	rang, err := c.Send(backend.Session{ID: "ctx_1"}, "x")
	if err != nil || rang {
		t.Fatalf("no-handle send must not ring, rang=%v err=%v", rang, err)
	}
	if !strings.Contains(got, "orchestration send --to dispatch:ctx_1") {
		t.Fatalf("expected mail fallback, got %q", got)
	}
}

// Interrupt sends to the terminal handle; a Session with no handle is an error, not a silent no-op.
func TestInterrupt(t *testing.T) {
	var got string
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	if err := c.Interrupt(backend.Session{Handle: "term_abc"}); err != nil {
		t.Fatal(err)
	}
	if got != "terminal send --terminal term_abc --interrupt --json" {
		t.Fatalf("wrong interrupt command: %q", got)
	}

	if err := c.Interrupt(backend.Session{Handle: ""}); err == nil || !strings.Contains(err.Error(), "no terminal handle") {
		t.Fatalf("empty handle must error, got %v", err)
	}
}

// --- terminal plane (ADR 0012) ---

// recorder returns a Client on the terminal plane whose runner records every joined arg line and answers a few known
// commands: terminal create returns a handle, terminal read returns a busy composer (so confirmBusy passes at once),
// everything else returns ok. It fails the test on any orchestration command, which the terminal plane must never emit.
func recorder(t *testing.T, calls *[]string) *Client {
	t.Helper()
	c := New("run_1")
	c.Plane = "terminal"
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		*calls = append(*calls, joined)
		if strings.Contains(joined, "orchestration") {
			t.Errorf("terminal plane must not call orchestration: %q", joined)
		}
		switch {
		case strings.Contains(joined, "terminal create"):
			return []byte(`{"ok":true,"result":{"terminal":{"handle":"term_new","paneKey":"tab:leaf"}}}`), nil
		case strings.Contains(joined, "terminal read"):
			return []byte(`{"ok":true,"result":{"terminal":{"tail":["✳ Percolating… (5s)"]}}}`), nil
		default:
			return []byte(`{"ok":true,"result":{}}`), nil
		}
	}
	return c
}

func TestSpawnTerminalPlaneCreatesAndTypesLaunch(t *testing.T) {
	t.Setenv("COX_SPAWN_CONFIRM", "1s")
	var calls []string
	c := recorder(t, &calls)
	brief := backend.Brief{StoryPath: "/epics/v2/stories/m10.md"}
	spec := backend.HarnessSpec{Name: "claude", Argv: []string{"claude", "Your task is the story file /epics/v2/stories/m10.md - read it in full and follow its Working rules exactly."}}
	sess, err := c.Spawn(backend.Worktree{Path: "/wt/m10"}, spec, brief)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Kind != SessionKindTerminal || sess.ID != "term_new" || sess.Handle != "term_new" {
		t.Fatalf("session = %+v, want kind=orca-terminal id=handle=term_new", sess)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "terminal create --worktree path:/wt/m10 --title m10 --json") {
		t.Errorf("missing terminal create with worktree path + title: %v", calls)
	}
	// The launch line carries the cox env and the harness command, and never orchestration.
	var send string
	for _, c := range calls {
		if strings.Contains(c, "terminal send") && strings.Contains(c, "--enter") {
			send = c
		}
	}
	for _, want := range []string{"COX_PLANE=terminal", "COX_STORY='m10'", "COX_EPIC='/epics/v2'", "claude", "read it in full"} {
		if !strings.Contains(send, want) {
			t.Errorf("launch line missing %q: %q", want, send)
		}
	}
}

// A terminal-send failure closes the just-created terminal (no orphan) and fails the spawn.
func TestSpawnTerminalClosesOnSendFailure(t *testing.T) {
	t.Setenv("COX_SPAWN_CONFIRM", "1s")
	var calls []string
	c := New("run_1")
	c.Plane = "terminal"
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.Contains(joined, "terminal create"):
			return []byte(`{"ok":true,"result":{"terminal":{"handle":"term_x"}}}`), nil
		case strings.Contains(joined, "terminal send"):
			return nil, errors.New("send boom")
		default:
			return []byte(`{"ok":true,"result":{}}`), nil
		}
	}
	if _, err := c.Spawn(backend.Worktree{Path: "/wt/m10"}, backend.HarnessSpec{Name: "claude"}, backend.Brief{StoryPath: "/e/stories/m10.md"}); err == nil {
		t.Fatal("send failure must fail spawn")
	}
	if !strings.Contains(strings.Join(calls, "\n"), "terminal close --terminal term_x") {
		t.Errorf("send failure must close the orphan terminal: %v", calls)
	}
}

// Probe on the terminal plane maps the pane's agents[] state to liveness using a real (anonymized) worktree ps fixture.
// terminal show resolves the handle to tabId:leafId; the fixture supplies working/idle/dead panes and a no-agent pane.
func TestProbeTerminalPlaneMapsAgentState(t *testing.T) {
	ps, err := os.ReadFile(filepath.Join("testdata", "worktree-ps.json"))
	if err != nil {
		t.Fatal(err)
	}
	// show routes a handle to a tab/leaf; the map below fakes distinct panes per handle.
	panes := map[string][2]string{
		"term_a":       {"TAB_A", "LEAF_A"}, // working -> Alive
		"term_b":       {"TAB_B", "LEAF_B"}, // idle    -> Alive
		"term_c":       {"TAB_C", "LEAF_C"}, // dead    -> Settled
		"term_noagent": {"TAB_Z", "LEAF_Z"}, // no entry -> Unknown
	}
	newClient := func(handle string, connected bool, stale bool) *Client {
		c := New("run")
		c.Plane = "terminal"
		c.run = func(args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "terminal show"):
				if stale {
					return []byte(`{"ok":false,"error":{"code":"terminal_handle_stale","message":"gone"}}`), errors.New("exit 1")
				}
				p := panes[handle]
				return []byte(fmt.Sprintf(`{"ok":true,"result":{"terminal":{"connected":%v,"tabId":%q,"leafId":%q}}}`, connected, p[0], p[1])), nil
			case strings.Contains(joined, "worktree ps"):
				return ps, nil
			}
			return nil, errors.New("no route: " + joined)
		}
		return c
	}
	cases := []struct {
		handle    string
		connected bool
		stale     bool
		want      backend.Liveness
	}{
		{"term_a", true, false, backend.Alive},
		{"term_b", true, false, backend.Alive},
		{"term_c", true, false, backend.Settled},
		{"term_noagent", true, false, backend.Unknown},
		{"term_a", false, false, backend.Settled}, // disconnected endpoint is gone regardless of agents[]
		{"term_a", true, true, backend.Settled},   // stale handle is gone
	}
	for _, tc := range cases {
		c := newClient(tc.handle, tc.connected, tc.stale)
		got, err := c.Probe(backend.Session{Kind: SessionKindTerminal, Handle: tc.handle})
		if tc.want == backend.Settled && tc.stale {
			// stale returns Settled with no error
		}
		if err != nil && tc.want != backend.Unknown {
			t.Errorf("%s connected=%v stale=%v: unexpected err %v", tc.handle, tc.connected, tc.stale, err)
		}
		if got != tc.want {
			t.Errorf("%s connected=%v stale=%v: liveness=%v, want %v", tc.handle, tc.connected, tc.stale, got, tc.want)
		}
	}
}

// M10b: on the terminal plane, Composer reports "blocked" when the pane's agents[] state is "waiting" (a worker on a
// local approval/input prompt); any other state falls back to the text-tail classification. A waiting agent is also
// Alive to Probe (mapAgentState), never gone.
func TestComposerBlockedOnWaitingAgent(t *testing.T) {
	newClient := func(agentState string, tail string) *Client {
		c := New("run")
		c.Plane = "terminal"
		c.run = func(args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "terminal show"):
				return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}`), nil
			case strings.Contains(joined, "worktree ps"):
				return []byte(fmt.Sprintf(`{"ok":true,"result":{"worktrees":[{"agents":[{"paneKey":"T:L","state":%q}]}]}}`, agentState)), nil
			case strings.Contains(joined, "terminal read"):
				return []byte(fmt.Sprintf(`{"ok":true,"result":{"terminal":{"tail":[%q]}}}`, tail)), nil
			}
			return nil, errors.New("no route: " + joined)
		}
		return c
	}
	// waiting -> blocked, and Probe reads it as Alive.
	c := newClient("waiting", "❯")
	if got, _ := c.Composer(backend.Session{Kind: SessionKindTerminal, Handle: "term_w"}); got != backend.ComposerBlocked {
		t.Fatalf("waiting agent Composer = %q, want blocked", got)
	}
	if got, err := c.Probe(backend.Session{Kind: SessionKindTerminal, Handle: "term_w"}); got != backend.Alive || err != nil {
		t.Fatalf("waiting agent Probe = %v err=%v, want Alive", got, err)
	}
	// working -> falls back to the text tail (here a compaction row => busy).
	c2 := newClient("working", "Compacting conversation")
	if got, _ := c2.Composer(backend.Session{Kind: SessionKindTerminal, Handle: "term_x"}); got != backend.ComposerBusy {
		t.Fatalf("working agent Composer = %q, want busy (text fallback)", got)
	}
}

// M10b: the launch is confirmed the moment an agents[] entry appears for the pane (any state), not only on a busy
// composer, so a cold harness start that registered but has not rendered a busy composer is not warned on. Here the
// agent appears on the 3rd worktree ps poll.
func TestConfirmLaunchWaitsForAgentsEntry(t *testing.T) {
	c := New("run")
	c.Plane = "terminal"
	c.confirmPoll = time.Millisecond
	c.LaunchConfirmS = 5
	psCalls := 0
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "terminal show"):
			return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}`), nil
		case strings.Contains(joined, "worktree ps"):
			psCalls++
			if psCalls < 3 {
				return []byte(`{"ok":true,"result":{"worktrees":[{"agents":[]}]}}`), nil // registered but no entry yet
			}
			return []byte(`{"ok":true,"result":{"worktrees":[{"agents":[{"paneKey":"T:L","state":"idle"}]}]}}`), nil
		case strings.Contains(joined, "terminal read"):
			return []byte(`{"ok":true,"result":{"terminal":{"tail":["still starting"]}}}`), nil // composer not busy
		}
		return nil, errors.New("no route: " + joined)
	}
	if !c.confirmLaunch(backend.Session{Kind: SessionKindTerminal, Handle: "term_new"}) {
		t.Fatal("confirmLaunch should confirm once agents[] appears")
	}
	if psCalls < 3 {
		t.Fatalf("expected >=3 ps polls before the agent appeared, got %d", psCalls)
	}
}

// When neither an agents[] entry nor a busy composer appears within the window, confirmLaunch returns false (the caller
// warns; liveness owns detection). Verified with a 1ms window so no real second elapses.
func TestConfirmLaunchWindowElapses(t *testing.T) {
	t.Setenv("COX_SPAWN_CONFIRM", "1ms")
	c := New("run")
	c.Plane = "terminal"
	c.confirmPoll = time.Millisecond
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "terminal show"):
			return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}`), nil
		case strings.Contains(joined, "worktree ps"):
			return []byte(`{"ok":true,"result":{"worktrees":[{"agents":[]}]}}`), nil
		case strings.Contains(joined, "terminal read"):
			return []byte(`{"ok":true,"result":{"terminal":{"tail":["nope"]}}}`), nil
		}
		return nil, errors.New("no route: " + joined)
	}
	if c.confirmLaunch(backend.Session{Kind: SessionKindTerminal, Handle: "term_new"}) {
		t.Fatal("confirmLaunch should not confirm when nothing appears in the window")
	}
}

// A worktree ps that cannot be read is Unknown with an error, never gone (F08).
func TestProbeTerminalUnreadablePsIsUnknown(t *testing.T) {
	c := New("run")
	c.Plane = "terminal"
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "terminal show") {
			return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}`), nil
		}
		return nil, errors.New("ps boom")
	}
	got, err := c.Probe(backend.Session{Handle: "term_x"})
	if got != backend.Unknown || err == nil {
		t.Fatalf("unreadable ps must be Unknown+error, got %v err=%v", got, err)
	}
}

// Stop on the terminal plane closes the terminal and confirms via a probe: a closed terminal (connected:false with an
// exitCause) confirms; a still-connected terminal after close is unconfirmed (ownership kept).
func TestStopTerminalPlaneConfirmsOnClosedTerminal(t *testing.T) {
	closed := false
	c := New("run")
	c.Plane = "terminal"
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "terminal close"):
			if !strings.Contains(joined, "--terminal term_z") {
				t.Errorf("close must address the handle: %q", joined)
			}
			closed = true
			return []byte(`{"ok":true,"result":{"close":{"ptyKilled":true}}}`), nil
		case strings.Contains(joined, "terminal show"):
			if closed {
				return []byte(`{"ok":true,"result":{"terminal":{"connected":false,"exitCause":{"code":"closed"},"tabId":"T","leafId":"L"}}}`), nil
			}
			return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}`), nil
		}
		return nil, errors.New("no route: " + joined)
	}
	confirmed, err := c.Stop(backend.Session{Kind: SessionKindTerminal, Handle: "term_z"})
	if err != nil || !confirmed {
		t.Fatalf("closed terminal must confirm stop: confirmed=%v err=%v", confirmed, err)
	}
}

func TestStopTerminalPlaneUnconfirmedWhenStillConnected(t *testing.T) {
	c := New("run")
	c.Plane = "terminal"
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "terminal close"):
			return []byte(`{"ok":true,"result":{}}`), nil
		case strings.Contains(joined, "terminal show"):
			return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}`), nil
		case strings.Contains(joined, "worktree ps"):
			return []byte(`{"ok":true,"result":{"worktrees":[]}}`), nil // no agent entry -> Unknown -> unconfirmed
		}
		return nil, errors.New("no route: " + joined)
	}
	confirmed, err := c.Stop(backend.Session{Kind: SessionKindTerminal, Handle: "term_z"})
	if err != nil || confirmed {
		t.Fatalf("still-connected terminal must be unconfirmed: confirmed=%v err=%v", confirmed, err)
	}
}

// On the terminal plane the mailbox is reduced: Check returns nothing, Ack is a no-op, and Send never emits an
// orchestration mail (a no-handle Send delivers nothing rather than falling back to orchestration send).
func TestTerminalPlaneReducedMailboxAndSend(t *testing.T) {
	var calls []string
	c := New("run_1")
	c.Plane = "terminal"
	c.run = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(`{"ok":true,"result":{}}`), nil
	}
	m := c.Mail()
	msgs, delivery, err := m.Check()
	if err != nil || len(msgs) != 0 || delivery != "" {
		t.Fatalf("terminal Check must be empty: msgs=%v delivery=%q err=%v", msgs, delivery, err)
	}
	if err := m.Ack("anything"); err != nil {
		t.Fatalf("terminal Ack must be a no-op: %v", err)
	}
	rang, err := c.Send(backend.Session{Kind: SessionKindTerminal, ID: "term_x"}, "hi") // no Handle
	if err != nil || rang {
		t.Fatalf("no-handle Send on terminal plane must deliver nothing: rang=%v err=%v", rang, err)
	}
	for _, g := range calls {
		if strings.Contains(g, "orchestration") {
			t.Fatalf("terminal plane must not call orchestration, got %q", g)
		}
	}
}

// --- Harness-owned busy state consult (DESIGN wave-3 item 3) ---
// FAIL_TO_PASS: on the old code the doorbell/composer read only the UI signal (agents[] / screen classifier), so a Pi
// worker (no agents[] entry, unrecognized TUI) was always "unknown" and never rang. Now the busy record is consulted
// first: harness idle rings even when the screen is unreadable, harness busy skips even when the screen looks empty, and
// only an absent/unknown record falls back to the UI signal.

func TestRingConsultsBusyRecordFirst(t *testing.T) {
	for _, tc := range []struct {
		name      string
		busyState string // "", "busy", "idle"
		tail      string // terminal read screen tail for the fallback
		wantRang  bool
	}{
		{name: "harness idle rings even when the screen is unreadable", busyState: busy.Idle, tail: `["garbage that classifies unknown"]`, wantRang: true},
		{name: "harness busy skips even when the screen looks empty", busyState: busy.Busy, tail: `["⏺ done","❯"]`, wantRang: false},
		{name: "unknown record falls back to the screen (empty -> ring)", busyState: "", tail: `["⏺ done","❯"]`, wantRang: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			epic := t.TempDir()
			gen, err := busy.Arm(epic, "w1", "pi", []string{"pi-ext", "dispatch", "interrupt", "recovery"})
			if err != nil {
				t.Fatalf("arm: %v", err)
			}
			switch tc.busyState {
			case busy.Idle, busy.Busy:
				if err := busy.Apply(epic, "w1", tc.busyState, gen, "pi-ext", "e"); err != nil {
					t.Fatalf("apply: %v", err)
				}
			default:
				// leave no usable state: re-arm invalidates, then remove the record so Read is unknown -> fallback
				os.Remove(busy.Path(epic, "w1"))
			}
			sent := false
			c := New("run_1")
			c.Epic = epic
			c.run = func(args ...string) ([]byte, error) {
				j := strings.Join(args, " ")
				switch {
				case strings.Contains(j, "terminal read"):
					return []byte(`{"ok":true,"result":{"terminal":{"tail":` + tc.tail + `}}}`), nil
				case strings.Contains(j, "terminal send"):
					sent = true
					return []byte(`{"ok":true,"result":{}}`), nil
				}
				return nil, errors.New("no route: " + j)
			}
			rang, err := c.Send(backend.Session{Handle: "term_x", Story: "w1"}, "ring")
			if err != nil {
				t.Fatal(err)
			}
			if rang != tc.wantRang || sent != tc.wantRang {
				t.Fatalf("rang=%v sent=%v, want %v", rang, sent, tc.wantRang)
			}
		})
	}
}

func TestComposerConsultsBusyRecordFirst(t *testing.T) {
	epic := t.TempDir()
	gen, _ := busy.Arm(epic, "w1", "pi", []string{"pi-ext", "dispatch", "interrupt", "recovery"})
	c := New("run_1")
	c.Epic = epic
	// The composer read must never be consulted while the harness reports a state; route it to a fatal so a fallthrough
	// is caught.
	c.run = func(args ...string) ([]byte, error) {
		t.Fatalf("Composer consulted the UI while the busy record was authoritative: %v", args)
		return nil, nil
	}
	// armed (busy) -> ComposerBusy
	if cs, _ := c.Composer(backend.Session{Handle: "term_x", Story: "w1"}); cs != backend.ComposerBusy {
		t.Fatalf("armed busy -> Composer %q, want busy", cs)
	}
	if err := busy.Apply(epic, "w1", busy.Idle, gen, "pi-ext", "e"); err != nil {
		t.Fatal(err)
	}
	if cs, _ := c.Composer(backend.Session{Handle: "term_x", Story: "w1"}); cs != backend.ComposerEmpty {
		t.Fatalf("harness idle -> Composer %q, want empty", cs)
	}
}
