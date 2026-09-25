package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B-22 against the real binary: `cox story dispatch` of a story whose branch is already on origin (a re-dispatch on a
// new host) puts the worktree on origin/story/<id>'s pushed commit, not on a fresh cut from the epic tip. The fake orca
// creates the worktree the way Orca does (a username-mangled branch from the base) and refuses the later terminal
// spawn, so the run stops right after worktree.Ensure; the worktree is what is checked.
func TestBinaryRedispatchReusesOriginBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the cox binary")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	tmp := t.TempDir()
	cox := filepath.Join(tmp, "cox")
	build := exec.Command("go", "build", "-o", cox, "./cmd/cox")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cox: %v\n%s", err, out)
	}
	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	origin, peer, app, wt := filepath.Join(tmp, "origin.git"), filepath.Join(tmp, "peer"), filepath.Join(tmp, "app"), filepath.Join(tmp, "wt")
	git("init", "-q", "--bare", "-b", "main", origin)
	git("clone", "-q", origin, peer)
	git("-C", peer, "commit", "-q", "--allow-empty", "-m", "init")
	git("-C", peer, "push", "-q", "origin", "HEAD:main", "HEAD:refs/heads/epic/e1")
	git("-C", peer, "switch", "-q", "-c", "story/s1")
	git("-C", peer, "commit", "-q", "--allow-empty", "-m", "story work from the first host")
	git("-C", peer, "push", "-q", "origin", "story/s1")
	pushed := git("-C", peer, "rev-parse", "HEAD")
	git("clone", "-q", origin, app) // the new host: no local story/s1
	git("-C", app, "branch", "-q", "epic/e1", "origin/epic/e1")

	ws, bin := filepath.Join(tmp, "ws"), filepath.Join(tmp, "bin")
	epic := filepath.Join(ws, "epics", "e1")
	for _, d := range []string{filepath.Join(ws, "cox"), filepath.Join(epic, "stories"), bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := os.ReadFile(filepath.Join(root, "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, s string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ws, "cox", "policy.json"), string(pol), 0o644)
	write(filepath.Join(ws, "cox", "workspace.json"), `{"projects":[{"name":"proj","path":"proj"}],"repos":[{"alias":"app","path":"`+app+`","production":"main"}],"hosts":[{"name":"local"}]}`, 0o644)
	write(filepath.Join(epic, "stories", "s1.md"), "---\nid: s1\nrepo: app\nharness: claude\n---\nbody\n", 0o644)
	write(filepath.Join(bin, "orca"), `#!/bin/sh
case "$1 $2" in
'worktree create') git -C '`+app+`' worktree add -q -b nphattai/story-s1 '`+wt+`' epic/e1 >/dev/null 2>&1
  echo '{"ok":true,"result":{"worktree":{"path":"`+wt+`","branch":"nphattai/story-s1"}}}' ;;
'orchestration run-create') echo '{"ok":true,"result":{"run":{"id":"run_fake"}}}' ;;
*) echo '{"ok":false,"error":{"message":"fake orca"}}'; exit 1 ;;
esac
`, 0o755)

	var env []string
	for _, kv := range gitEnv {
		if !strings.HasPrefix(kv, "ORCA_") && !strings.HasPrefix(kv, "COX_") {
			env = append(env, kv)
		}
	}
	cmd := exec.Command(cox, "story", "dispatch", "s1", "--epic", epic, "--allow-unsandboxed")
	cmd.Dir = ws
	cmd.Env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "COX_PLANE=terminal", "ORCA_TERMINAL_HANDLE=")
	out, _ := cmd.CombinedOutput() // the fake refuses the spawn; the worktree is the evidence
	if b, err := exec.Command("git", "-C", wt, "branch", "--show-current").Output(); err != nil || strings.TrimSpace(string(b)) != "story/s1" {
		t.Fatalf("dispatch left no story/s1 worktree (branch %q, err %v); cox said:\n%s", b, err, out)
	}
	if h := git("-C", wt, "rev-parse", "HEAD"); h != pushed {
		t.Fatalf("re-dispatched worktree HEAD %s, want the pushed origin/story/s1 %s (a fresh cut from the epic orphans it); cox said:\n%s", h, pushed, out)
	}
}
