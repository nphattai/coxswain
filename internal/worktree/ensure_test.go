package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
)

// initRepo makes a real git repo at dir with one commit on branch.
func initRepo(t *testing.T, dir, branch string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
}

func TestEnsureCreateFails(t *testing.T) {
	b := fake.New()
	b.FailNext("WorktreeCreate", nil)
	_, err := Ensure(b, "repo", "story/x", "origin/epic/e")
	if err == nil {
		t.Fatal("expected error when create fails")
	}
}

// The backend may claim a branch, but Ensure trusts git: a real checkout on the wrong branch is an error.
func TestEnsureWrongBranch(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, "other")
	b := fake.New()
	b.CreatedPath = dir
	b.CreatedBranch = "story/x" // backend lies
	_, err := Ensure(b, "repo", "story/x", "base")
	if err == nil {
		t.Fatal("expected error when checkout is on the wrong branch")
	}
	if !strings.Contains(err.Error(), "story/x") {
		t.Fatalf("error should name expected branch: %v", err)
	}
}

func TestEnsureSuccess(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, "story/x")
	b := fake.New()
	b.CreatedPath = dir
	wt, err := Ensure(b, "repo", "story/x", "base")
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if wt.Path != dir || wt.Branch != "story/x" {
		t.Fatalf("got %+v", wt)
	}
}

func TestEnsureMissingPath(t *testing.T) {
	b := fake.New()
	b.CreatedPath = filepath.Join(t.TempDir(), "does-not-exist")
	_, err := Ensure(b, "repo", "story/x", "base")
	if err == nil {
		t.Fatal("expected error when path does not exist")
	}
}

// The package must contain no branch-deletion command anywhere (F01). The verification call `branch --show-current`
// is allowed; only deletion forms are forbidden.
func TestNoBranchDeletionInSource(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{`"-D"`, `"-d"`, "--delete", "branch -D"}
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range forbidden {
			if strings.Contains(string(src), tok) {
				t.Errorf("%s contains forbidden branch-deletion token %q", f, tok)
			}
		}
	}
}
