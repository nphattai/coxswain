package epic

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// attachBackend checks out an EXISTING branch into a fresh worktree (no -b), which is what re-attach needs: the epic
// branch already exists and must not be recreated. It records the repo refs it was handed so a test can assert attach
// resolved them from the local workspace. Every other method is the gitBackend stub.
type attachBackend struct {
	gitBackend
	gotRepos []string
}

func (a *attachBackend) WorktreeCreate(repo, branch, base string) (backend.Worktree, error) {
	a.gotRepos = append(a.gotRepos, repo)
	path := filepath.Join(a.wtBase, "wt-"+strings.ReplaceAll(branch, "/", "-"))
	// Mirror orca/git: an existing local branch is checked out as-is; a missing one is created from the base ref (which
	// attach sets to origin/<branch> on a fresh clone).
	var cmd *exec.Cmd
	if localBranchExists(a.repo, branch) {
		cmd = exec.Command("git", "-C", a.repo, "worktree", "add", path, branch)
	} else {
		cmd = exec.Command("git", "-C", a.repo, "worktree", "add", "-b", branch, path, base)
	}
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

	if err := Attach(AttachOptions{Runtime: &attachBackend{gitBackend: gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err != nil {
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
	if err := Attach(AttachOptions{Runtime: &attachBackend{gitBackend: gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err == nil {
		t.Error("attach must refuse when .cox already exists")
	}
}

// A clone of an epic dir that carries ledger.jsonl but no .cox reports signed after cox epic attach, without
// re-signing: the signature travels with git in the committed ledger, and attach does not replay it (finding 2).
func TestAttachOnCloneWithLedgerReportsSigned(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo)
	if _, err := New(NewOptions{Runtime: &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}, Workspace: ws, WsRoot: wsRoot,
		Project: "proj", Slug: "att", Repos: []string{"app"}, NoPush: true}); err != nil {
		t.Fatal(err)
	}
	epicDir := filepath.Join(wsRoot, "proj", "epics", "att")

	// The signature lives in the committed ledger; the epic was never signed on this machine.
	if err := state.AppendLedger(epicDir, state.Event{Type: state.DesignSigned, Epic: "att", Story: state.EpicStory, Actor: state.Captain, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}

	// Simulate a fresh clone: remove the worktree, the alias symlink, and .cox - but keep the tracked ledger.jsonl.
	link := filepath.Join(epicDir, "app")
	target, _ := filepath.EvalSymlinks(link)
	_, _ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", target).CombinedOutput()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(epicDir, ".cox")); err != nil {
		t.Fatal(err)
	}

	if err := Attach(AttachOptions{Runtime: &attachBackend{gitBackend: gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	signed, err := isSigned(epicDir)
	if err != nil || !signed {
		t.Fatalf("a clone with a ledger must report signed after attach: signed=%v err=%v", signed, err)
	}
	// Attach did not replay the signature into the runtime log; the ledger is the source.
	if b, err := os.ReadFile(state.EventsPath(epicDir)); err == nil && strings.Contains(string(b), "design_signed") {
		t.Errorf("attach must not replay design_signed into .cox/events.jsonl:\n%s", b)
	}
}

// Attach resolves each alias's ref from the LOCAL workspace.json, not the (possibly stale) absolute path recorded in
// the tracked repos file - the captain's reinstall-on-another-machine case.
func TestAttachResolvesRefFromLocalWorkspace(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo) // workspace registers app -> repo (the local checkout)
	if _, err := New(NewOptions{Runtime: &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}, Workspace: ws, WsRoot: wsRoot,
		Project: "proj", Slug: "reinstall", Repos: []string{"app"}, NoPush: true}); err != nil {
		t.Fatal(err)
	}
	epicDir := filepath.Join(wsRoot, "proj", "epics", "reinstall")
	// Simulate a clone from another machine: the tracked repos file carries a stale absolute path.
	if err := os.WriteFile(filepath.Join(epicDir, "repos"), []byte("app /stale/checkout/from/another/machine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fresh clone: worktree and .cox gone, branch kept.
	link := filepath.Join(epicDir, "app")
	target, _ := filepath.EvalSymlinks(link)
	_, _ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", target).CombinedOutput()
	_ = os.Remove(link)
	_ = os.RemoveAll(filepath.Join(epicDir, ".cox"))

	be := &attachBackend{gitBackend: gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}
	if err := Attach(AttachOptions{Runtime: be, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if len(be.gotRepos) != 1 || be.gotRepos[0] != repo {
		t.Fatalf("attach used ref %v, want the local workspace path %q (not the stale repos-file path)", be.gotRepos, repo)
	}
}

// When only .cox was discarded and a clean worktree still exists on the epic branch, attach reuses it rather than asking
// the backend to create a second worktree on the same (already-checked-out) branch.
func TestAttachReusesCleanWorktree(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo)
	if _, err := New(NewOptions{Runtime: &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}, Workspace: ws, WsRoot: wsRoot,
		Project: "proj", Slug: "reuse", Repos: []string{"app"}, NoPush: true}); err != nil {
		t.Fatal(err)
	}
	epicDir := filepath.Join(wsRoot, "proj", "epics", "reuse")
	before, _ := filepath.EvalSymlinks(filepath.Join(epicDir, "app"))
	// Discard ONLY .cox; the clean worktree and symlink survive.
	if err := os.RemoveAll(filepath.Join(epicDir, ".cox")); err != nil {
		t.Fatal(err)
	}
	be := &attachBackend{gitBackend: gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}
	if err := Attach(AttachOptions{Runtime: be, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err != nil {
		t.Fatalf("attach with a surviving clean worktree must succeed: %v", err)
	}
	if len(be.gotRepos) != 0 {
		t.Errorf("attach must reuse the clean worktree, not create a new one (WorktreeCreate called %d time(s))", len(be.gotRepos))
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox", "epic.json")); err != nil {
		t.Errorf(".cox/epic.json not recreated: %v", err)
	}
	if after, _ := filepath.EvalSymlinks(filepath.Join(epicDir, "app")); after != before {
		t.Errorf("symlink target changed: %q -> %q", before, after)
	}
}

// On a true fresh clone the epic branch exists only on origin (no local refs/heads/<slug>). Attach fetches it and bases
// the worktree on the fetched origin/<slug>, creating the local tracking branch - it must not fail on the missing local
// branch (PR#3 review round 2, finding 2).
func TestAttachFreshCloneCreatesLocalBranchFromOrigin(t *testing.T) {
	// A seed repo with main + epic/fresh, pushed to a bare origin.
	seed := makeRepo(t)
	origin := t.TempDir()
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("", "init", "-q", "--bare", origin)
	git(seed, "checkout", "-q", "-b", "epic/fresh")
	git(seed, "commit", "-q", "--allow-empty", "-m", "epic work")
	git(seed, "checkout", "-q", "main")
	git(seed, "remote", "add", "origin", origin)
	git(seed, "push", "-q", "origin", "main", "epic/fresh")

	// The clone this machine works from: origin/epic/fresh exists, local epic/fresh does not.
	clone := t.TempDir()
	git("", "clone", "-q", origin, clone)
	if localBranchExists(clone, "epic/fresh") {
		t.Fatal("precondition: clone must not have a local epic/fresh branch")
	}

	wsRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	seedWs := &workspace.Workspace{Repos: []workspace.Repo{{Alias: "app", Path: clone, Production: "main"}}}
	if _, err := workspace.Init(wsRoot, seedWs); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Load(wsRoot)
	// A cloned epic dir with no .cox, its repos file, and a DESIGN.md.
	epicDir := filepath.Join(wsRoot, "proj", "epics", "fresh")
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"DESIGN.md": "# fresh\n", "repos": "app " + clone + "\n"} {
		if err := os.WriteFile(filepath.Join(epicDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	be := &attachBackend{gitBackend: gitBackend{t: t, repo: clone, wtBase: t.TempDir()}}
	if err := Attach(AttachOptions{Runtime: be, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err != nil {
		t.Fatalf("fresh-clone attach failed: %v", err)
	}
	if !localBranchExists(clone, "epic/fresh") {
		t.Error("attach must create the local epic/fresh branch from origin/epic/fresh")
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox", "epic.json")); err != nil {
		t.Errorf(".cox/epic.json not recreated: %v", err)
	}
}

// TestAttachAdoptsWorktreeWithoutSymlink: when a worktree is still on the epic branch but its alias symlink is gone,
// attach finds it by branch in the worktree list and adopts it, re-creating the symlink and asking the backend for
// nothing (B-40: never a second worktree on an already-checked-out branch).
func TestAttachAdoptsWorktreeWithoutSymlink(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo)
	if _, err := New(NewOptions{Runtime: &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}, Workspace: ws, WsRoot: wsRoot,
		Project: "proj", Slug: "adopt", Repos: []string{"app"}, NoPush: true}); err != nil {
		t.Fatal(err)
	}
	epicDir := filepath.Join(wsRoot, "proj", "epics", "adopt")
	target, _ := filepath.EvalSymlinks(filepath.Join(epicDir, "app"))
	// Remove ONLY the alias symlink and .cox; the worktree on epic/adopt survives with no symlink pointing at it.
	if err := os.Remove(filepath.Join(epicDir, "app")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(epicDir, ".cox")); err != nil {
		t.Fatal(err)
	}
	be := &attachBackend{gitBackend: gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}
	if err := Attach(AttachOptions{Runtime: be, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if len(be.gotRepos) != 0 {
		t.Errorf("attach must adopt the existing worktree, not create one (WorktreeCreate called %d time(s))", len(be.gotRepos))
	}
	if after, err := filepath.EvalSymlinks(filepath.Join(epicDir, "app")); err != nil || after != target {
		t.Errorf("symlink must be re-created pointing at the adopted worktree: got %q want %q err=%v", after, target, err)
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox", "epic.json")); err != nil {
		t.Errorf(".cox/epic.json not recreated: %v", err)
	}
}

// renameBackend mimics Orca renaming the branch (epic/<slug> -> <user>/epic-<slug>) when the epic branch is already
// checked out elsewhere: its WorktreeCreate checks out the RENAMED branch and reports it. It records removals so the
// test can assert the duplicate worktree was torn down again.
type renameBackend struct {
	gitBackend
	renamed string
	removed []string
}

func (b *renameBackend) WorktreeCreate(repo, branch, base string) (backend.Worktree, error) {
	path := filepath.Join(b.wtBase, "wt-renamed")
	out, err := exec.Command("git", "-C", b.repo, "worktree", "add", "-b", b.renamed, path, base).CombinedOutput()
	if err != nil {
		return backend.Worktree{}, &execErr{string(out), err}
	}
	return backend.Worktree{Path: path, Branch: b.renamed}, nil
}

func (b *renameBackend) WorktreeRemove(wt backend.Worktree) error {
	b.removed = append(b.removed, wt.Path)
	_, _ = exec.Command("git", "-C", b.repo, "worktree", "remove", "--force", wt.Path).CombinedOutput()
	return nil
}

// TestAttachRefusesRenamedBranch: a backend that puts the worktree on a renamed branch is detected; attach removes the
// duplicate worktree and refuses, and deletes the renamed local branch because it carries no unique work and is not on
// origin - never the canonical branch or a branch on origin (B-40, F01).
func TestAttachRefusesRenamedBranch(t *testing.T) {
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
	git(clone, "checkout", "-q", "-b", "epic/rename")
	git(clone, "commit", "-q", "--allow-empty", "-m", "epic work")
	git(clone, "push", "-q", "origin", "main", "epic/rename")
	git(clone, "checkout", "-q", "main")

	wsRoot := t.TempDir()
	seedWs := &workspace.Workspace{Repos: []workspace.Repo{{Alias: "app", Path: clone, Production: "main"}}}
	if _, err := workspace.Init(wsRoot, seedWs); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Load(wsRoot)
	epicDir := filepath.Join(wsRoot, "proj", "epics", "rename")
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"DESIGN.md": "# rename\n", "repos": "app " + clone + "\n"} {
		if err := os.WriteFile(filepath.Join(epicDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	be := &renameBackend{gitBackend: gitBackend{t: t, repo: clone, wtBase: t.TempDir()}, renamed: "nphattai/epic-rename"}
	err := Attach(AttachOptions{Runtime: be, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir})
	if err == nil || !strings.Contains(err.Error(), "renamed branch") {
		t.Fatalf("want a rename refusal, got %v", err)
	}
	if len(be.removed) != 1 {
		t.Errorf("the duplicate worktree must be removed, removed=%v", be.removed)
	}
	if localBranchExists(clone, "nphattai/epic-rename") {
		t.Error("a renamed local branch with no unique commits and not on origin must be deleted")
	}
	if !localBranchExists(clone, "epic/rename") {
		t.Error("the canonical epic branch must survive")
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox")); !os.IsNotExist(err) {
		t.Errorf("a refused attach must not create .cox/, err=%v", err)
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
	if err := Attach(AttachOptions{Runtime: &attachBackend{gitBackend: gitBackend{t: t, repo: repo, wtBase: t.TempDir()}}, Workspace: ws, WsRoot: wsRoot, EpicDir: epicDir}); err == nil {
		t.Error("attach must refuse a dirty worktree")
	}
}
