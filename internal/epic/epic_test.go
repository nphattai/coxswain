package epic

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/workspace"
)

// gitBackend is a Backend whose WorktreeCreate makes a real git worktree (so worktree.Ensure's on-disk verification
// passes) and whose WorktreeRemove records the removed path. Every other method is a no-op stub.
type gitBackend struct {
	t        *testing.T
	repo     string
	wtBase   string
	removed  []string
	calls    []string
	stopOK   bool
	stopErr  error
	live     backend.Liveness
	probeErr error
}

func (g *gitBackend) WorktreeCreate(repo, branch, base string) (backend.Worktree, error) {
	path := filepath.Join(g.wtBase, "wt-"+strings.ReplaceAll(branch, "/", "-"))
	cmd := exec.Command("git", "-C", g.repo, "worktree", "add", "-b", branch, path, base)
	if out, err := cmd.CombinedOutput(); err != nil {
		return backend.Worktree{}, &execErr{string(out), err}
	}
	return backend.Worktree{Path: path, Branch: branch}, nil
}
func (g *gitBackend) WorktreeRemove(wt backend.Worktree) error {
	g.calls = append(g.calls, "WorktreeRemove")
	g.removed = append(g.removed, wt.Path)
	_, _ = exec.Command("git", "-C", g.repo, "worktree", "remove", "--force", wt.Path).CombinedOutput()
	return nil
}
func (g *gitBackend) Spawn(backend.Worktree, backend.HarnessSpec, backend.Brief) (backend.Session, error) {
	return backend.Session{Kind: "fake"}, nil
}
func (g *gitBackend) Send(backend.Session, string) (bool, error) { return true, nil }
func (g *gitBackend) Interrupt(backend.Session) error            { return nil }
func (g *gitBackend) Stop(backend.Session) (bool, error) {
	g.calls = append(g.calls, "Stop")
	return g.stopOK, g.stopErr
}
func (g *gitBackend) Probe(backend.Session) (backend.Liveness, error) {
	g.calls = append(g.calls, "Probe")
	if g.probeErr != nil {
		return backend.Unknown, g.probeErr
	}
	return g.live, nil
}
func (g *gitBackend) Composer(backend.Session) (string, error) { return backend.ComposerUnknown, nil }
func (g *gitBackend) WorkerList() ([]backend.Worker, error)    { return nil, nil }
func (g *gitBackend) Mail() backend.Mailbox                    { return nil }

type execErr struct {
	out string
	err error
}

func (e *execErr) Error() string { return e.err.Error() + ": " + e.out }

// makeRepo creates a git repo with an initial commit on branch main and returns its path.
func makeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return repo
}

// setupWorkspace writes cox/workspace.json + cox/policy.json under a temp ws root, registering one repo pointing at
// the given git repo path with production=main. It sets HOME to a temp dir so trustWorktree never edits the real file.
func setupWorkspace(t *testing.T, repoPath string) (wsRoot string, ws *workspace.Workspace) {
	t.Helper()
	wsRoot = t.TempDir()
	t.Setenv("HOME", t.TempDir())
	seed := &workspace.Workspace{Repos: []workspace.Repo{{Alias: "app", Path: repoPath, Production: "main"}}}
	if _, err := workspace.Init(wsRoot, seed); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Load(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	return wsRoot, ws
}

func TestNewCreatesEpicLayout(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo)
	if err := os.MkdirAll(filepath.Join(wsRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}
	epicDir, err := New(NewOptions{
		Runtime: rt, Workspace: ws, WsRoot: wsRoot, Project: "proj", Slug: "demo-epic",
		Repos: []string{"app"}, NoPush: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// repos file, DESIGN.md rendered, epic.env, symlink, epic.json.
	if b, _ := os.ReadFile(filepath.Join(epicDir, "repos")); !strings.Contains(string(b), "app "+repo) {
		t.Errorf("repos file wrong: %s", b)
	}
	design, _ := os.ReadFile(filepath.Join(epicDir, "DESIGN.md"))
	if !strings.Contains(string(design), "demo-epic - design") || strings.Contains(string(design), "{{slug}}") {
		t.Errorf("DESIGN not rendered: %s", design)
	}
	ee, _ := os.ReadFile(filepath.Join(epicDir, "epic.env"))
	if !strings.Contains(string(ee), "API_PORT=3333") || !strings.Contains(string(ee), "STORY_PORT_BASE=3400") {
		t.Errorf("epic.env ports wrong: %s", ee)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(epicDir, "app"))
	if err != nil || target == "" {
		t.Errorf("alias symlink missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(epicDir, ".cox", "epic.json")); err != nil {
		t.Errorf("epic.json missing: %v", err)
	}
}

func TestNewRefusesExistingEpic(t *testing.T) {
	repo := makeRepo(t)
	wsRoot, ws := setupWorkspace(t, repo)
	rt := &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}
	opts := NewOptions{Runtime: rt, Workspace: ws, WsRoot: wsRoot, Project: "proj", Slug: "dup", Repos: []string{"app"}, NoPush: true}
	if _, err := New(opts); err != nil {
		t.Fatal(err)
	}
	rt2 := &gitBackend{t: t, repo: repo, wtBase: t.TempDir()}
	opts.Runtime = rt2
	if _, err := New(opts); err == nil {
		t.Fatal("New must refuse an existing epic dir")
	}
}
