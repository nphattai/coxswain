package epic

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// attachBackend checks out an EXISTING branch into a fresh worktree (no -b), which is what re-attach needs: the epic
// branch already exists and must not be recreated. Every other method is the gitBackend stub.
type attachBackend struct {
	gitBackend
}

func (a *attachBackend) WorktreeCreate(repo, branch, base string) (backend.Worktree, error) {
	path := filepath.Join(a.wtBase, "wt-"+strings.ReplaceAll(branch, "/", "-"))
	cmd := exec.Command("git", "-C", a.repo, "worktree", "add", path, branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return backend.Worktree{}, &execErr{string(out), err}
	}
	return backend.Worktree{Path: path, Branch: branch}, nil
}

// gitBranches returns the repo's local branches, sorted, for the "branch set unchanged" assertion.
func gitBranches(t *testing.T, repo string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "branch", "--format=%(refname:short)").Output()
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	var bs []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			bs = append(bs, l)
		}
	}
	return bs
}

// Attach on a clone that has the epic dir but no .cox recreates .cox/epic.json, the worktree on the existing branch, and
// the alias symlink, and the branch set is identical before and after (no branch created or deleted).
func TestAttachRecreatesStateWithoutTouchingBranches(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo)
	wtBase := t.TempDir()
	if _, err := New(NewOptions{Runtime: &gitBackend{t: t, repo: repo, wtBase: wtBase}, Workspace: ws, WsRoot: wsRoot,
		Project: "proj", Slug: "att", Repos: []string{"app"}, NoPush: true}); err != nil {
		t.Fatal(err)
	}
	epicDir := filepath.Join(wsRoot, "proj", "epics", "att")
	before := gitBranches(t, repo)

	// Simulate a fresh clone: remove the worktree (branch kept), the alias symlink, and .cox.
	link := filepath.Join(epicDir, "app")
	target, _ := filepath.EvalSymlinks(link)
	_, _ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", target).CombinedOutput()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(epicDir, ".cox")); err != nil {
		t.Fatal(err)
	}

	if err := Attach(AttachOptions{Runtime: &attachBackend{gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox", "epic.json")); err != nil {
		t.Errorf(".cox/epic.json not recreated: %v", err)
	}
	if tgt, err := filepath.EvalSymlinks(filepath.Join(epicDir, "app")); err != nil || tgt == "" {
		t.Errorf("alias symlink not recreated: %v", err)
	}
	if after := gitBranches(t, repo); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("branch set changed: before %v after %v", before, after)
	}

	// Refuse on an existing .cox/.
	if err := Attach(AttachOptions{Runtime: &attachBackend{gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err == nil {
		t.Error("attach must refuse when .cox already exists")
	}
}

// Attach refuses when a still-present worktree is dirty (uncommitted work a re-checkout would risk).
func TestAttachRefusesDirtyWorktree(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo)
	if _, err := New(NewOptions{Runtime: &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}, Workspace: ws, WsRoot: wsRoot,
		Project: "proj", Slug: "dirty", Repos: []string{"app"}, NoPush: true}); err != nil {
		t.Fatal(err)
	}
	epicDir := filepath.Join(wsRoot, "proj", "epics", "dirty")
	target, err := filepath.EvalSymlinks(filepath.Join(epicDir, "app"))
	if err != nil {
		t.Fatal(err)
	}
	// Dirty the worktree, then discard .cox and attempt attach.
	if err := os.WriteFile(filepath.Join(target, "uncommitted.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(epicDir, ".cox")); err != nil {
		t.Fatal(err)
	}
	if err := Attach(AttachOptions{Runtime: &attachBackend{gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err == nil {
		t.Error("attach must refuse a dirty worktree")
	}
}
