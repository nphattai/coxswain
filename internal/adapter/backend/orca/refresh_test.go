package orca

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/busy"
)

// B-34a: Orca exits non-zero on an ok=false envelope and still prints it on stdout. call must surface Orca's code and
// message, not the bare exit status, and a worktree create on an unregistered repo must say how to register it.
func TestCallParsesEnvelopeOnNonZeroExit(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 1").Run() // a real *exec.ExitError, as exec.Command.Output returns
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":{"code":"repo_not_found","message":"No repo matches path:/tmp/app"}}`), exitErr
	}
	_, err := c.WorktreeCreate("/tmp/app", "epic/e1", "main")
	if err == nil {
		t.Fatal("worktree create on an unregistered repo must fail")
	}
	for _, want := range []string{"repo_not_found", "No repo matches path:/tmp/app", "orca repo add --path /tmp/app`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "exit status") {
		t.Errorf("error still reports the bare exit status: %v", err)
	}
}

// A failed process whose stdout is not an envelope keeps the process error (nothing to parse, nothing invented).
func TestCallKeepsExitErrorWithoutEnvelope(t *testing.T) {
	c := New("run")
	c.run = func(args ...string) ([]byte, error) { return []byte("boom\n"), errors.New("exit status 2") }
	if _, err := c.call("status", "--json"); err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatalf("err = %v; want the process error", err)
	}
}

// B-34a against the real binary: `cox epic new` on a repo Orca has not registered fails with Orca's repo_not_found and
// the registration command for that checkout, not "exit status 1" (fake orca exits 1 with the envelope, as Orca does).
func TestBinaryEpicNewReportsRepoNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the cox binary")
	}
	root, _ := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	tmp := t.TempDir()
	cox := filepath.Join(tmp, "cox")
	build := exec.Command("go", "build", "-o", cox, "./cmd/cox")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cox: %v\n%s", err, out)
	}
	app, ws, bin := filepath.Join(tmp, "app"), filepath.Join(tmp, "ws"), filepath.Join(tmp, "bin")
	for _, d := range []string{app, filepath.Join(ws, "cox"), filepath.Join(ws, "proj"), bin} {
		must(t, os.MkdirAll(d, 0o755))
	}
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}} {
		if out, err := exec.Command("git", append([]string{"-C", app}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	pol, err := os.ReadFile(filepath.Join(root, "templates", "policy.json"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(ws, "cox", "policy.json"), pol, 0o644))
	must(t, os.WriteFile(filepath.Join(ws, "cox", "workspace.json"), []byte(`{"projects":[{"name":"proj","path":"proj"}],"repos":[{"alias":"app","path":"`+app+`","production":"main"}],"hosts":[{"name":"local"}]}`), 0o644))
	must(t, os.WriteFile(filepath.Join(bin, "orca"), []byte(`#!/bin/sh
case "$1 $2" in
'worktree create') echo '{"ok":false,"error":{"code":"repo_not_found","message":"No repo matches the selector"}}'; exit 1 ;;
*) echo '{"ok":true,"result":{}}' ;;
esac
`), 0o755))
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ORCA_") && !strings.HasPrefix(kv, "COX_") {
			env = append(env, kv)
		}
	}
	cmd := exec.Command(cox, "epic", "new", "proj", "e1", "--repo", "app", "--no-push")
	cmd.Dir = ws
	cmd.Env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("epic new on an unregistered repo succeeded: %s", out)
	}
	for _, want := range []string{"repo_not_found", "orca repo add --path " + app} {
		if !strings.Contains(string(out), want) {
			t.Errorf("epic new output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "exit status") {
		t.Errorf("epic new still reports a bare exit status:\n%s", out)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// B-01: a terminal-plane worker on a local dialog (agents[] "waiting") is on a dialog even while its harness-owned busy
// record says busy - the harness is mid-turn by its own hook. Dialog reports it; Composer keeps the busy record's answer,
// so the callers that refuse on busy (story done/release) are unchanged.
func TestDialogSeesWaitingDespiteBusyRecord(t *testing.T) {
	epic := t.TempDir()
	if _, err := busy.Arm(epic, "w1", "claude", []string{"hook", "dispatch", "interrupt", "recovery"}); err != nil {
		t.Fatal(err)
	}
	c := New("run")
	c.Plane = "terminal"
	c.Epic = epic
	state := "waiting"
	c.run = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "terminal show"):
			return []byte(`{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}`), nil
		case strings.Contains(joined, "worktree ps"):
			return []byte(`{"ok":true,"result":{"worktrees":[{"agents":[{"paneKey":"T:L","state":"` + state + `"}]}]}}`), nil
		}
		return nil, errors.New("no route: " + joined)
	}
	s := backend.Session{Kind: SessionKindTerminal, Handle: "term_w", Story: "w1"}
	var _ backend.DialogReader = c
	if !c.Dialog(s) {
		t.Fatal("waiting agent with a busy record: Dialog = false, want true")
	}
	if got, _ := c.Composer(s); got != backend.ComposerBusy {
		t.Fatalf("Composer = %q, want busy (the busy record still answers Composer)", got)
	}
	state = "working"
	if c.Dialog(s) {
		t.Fatal("working agent: Dialog = true, want false")
	}
	c.Plane = ""
	state = "waiting"
	if c.Dialog(s) {
		t.Fatal("orchestration plane has no agents[] state: Dialog must be false")
	}
}

// redispatch is a real-git fixture for B-22: a bare origin, a host clone with epic/e1, and a fake orca whose worktree
// create does what Orca does - a new worktree on a username-mangled branch cut from the base.
type redispatch struct {
	t                  *testing.T
	origin, host, peer string
	n                  int
}

func newRedispatch(t *testing.T) *redispatch {
	r := &redispatch{t: t, origin: filepath.Join(t.TempDir(), "origin.git"), host: filepath.Join(t.TempDir(), "host"), peer: filepath.Join(t.TempDir(), "peer")}
	r.git("", "init", "-q", "--bare", "-b", "main", r.origin)
	r.git("", "clone", "-q", r.origin, r.peer)
	r.commit(r.peer, "init")
	r.git(r.peer, "push", "-q", "origin", "HEAD:main", "HEAD:refs/heads/epic/e1")
	r.git("", "clone", "-q", r.origin, r.host)
	r.git(r.host, "branch", "epic/e1", "origin/epic/e1")
	return r
}

func (r *redispatch) git(dir string, args ...string) string {
	r.t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *redispatch) commit(dir, msg string) string {
	r.git(dir, "commit", "-q", "--allow-empty", "-m", msg)
	return r.git(dir, "rev-parse", "HEAD")
}

// create runs WorktreeCreate for story/s1 from epic/e1 through a client whose orca makes a fresh mangled worktree.
func (r *redispatch) create() (string, error) { return r.createBranch("story/s1") }

func (r *redispatch) createBranch(branch string) (string, error) {
	r.n++
	wt := filepath.Join(r.t.TempDir(), "wt")
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		mangled := "nphattai/x-" + string(rune('a'+r.n))
		r.git(r.host, "worktree", "add", "-q", "-b", mangled, wt, "epic/e1")
		return []byte(`{"ok":true,"result":{"worktree":{"path":"` + wt + `","branch":"` + mangled + `"}}}`), nil
	}
	_, err := c.WorktreeCreate(r.host, branch, "epic/e1")
	return wt, err
}

// B-22: a re-dispatch of a story whose branch is on origin lands on the pushed work in each local-branch case, and a
// diverged local branch is refused with both sides intact. The epic has moved on since the story was cut, as it does.
func TestWorktreeCreateReusesOriginBranch(t *testing.T) {
	setup := func(t *testing.T) (*redispatch, string) {
		r := newRedispatch(t)
		r.git(r.peer, "switch", "-q", "-c", "story/s1", "origin/epic/e1")
		pushed := r.commit(r.peer, "story work")
		r.git(r.peer, "push", "-q", "origin", "story/s1")
		r.git(r.peer, "switch", "-q", "-c", "e", "origin/epic/e1")
		r.commit(r.peer, "epic moves on")
		r.git(r.peer, "push", "-q", "origin", "HEAD:epic/e1")
		r.git(r.host, "fetch", "-q", "origin")
		r.git(r.host, "branch", "-f", "epic/e1", "origin/epic/e1")
		return r, pushed
	}
	t.Run("no local branch", func(t *testing.T) {
		r, pushed := setup(t)
		wt, err := r.create()
		if err != nil {
			t.Fatal(err)
		}
		if h := r.git(wt, "rev-parse", "HEAD"); h != pushed {
			t.Fatalf("worktree HEAD %s, want the pushed story/s1 %s (not a fresh cut from the epic)", h, pushed)
		}
		if b := r.git(wt, "branch", "--show-current"); b != "story/s1" {
			t.Fatalf("branch %q, want story/s1", b)
		}
		if up := r.git(wt, "rev-parse", "--abbrev-ref", "story/s1@{upstream}"); up != "origin/story/s1" {
			t.Fatalf("upstream %q, want origin/story/s1", up)
		}
	})
	t.Run("stale local branch behind origin", func(t *testing.T) {
		r, pushed := setup(t)
		r.git(r.host, "branch", "story/s1", pushed+"~1")
		wt, err := r.create()
		if err != nil {
			t.Fatal(err)
		}
		if h := r.git(wt, "rev-parse", "HEAD"); h != pushed {
			t.Fatalf("worktree HEAD %s, want fast-forwarded to %s", h, pushed)
		}
	})
	t.Run("local ahead of origin keeps unpushed work", func(t *testing.T) {
		r, pushed := setup(t)
		r.git(r.host, "branch", "story/s1", "origin/story/s1")
		r.git(r.host, "switch", "-q", "story/s1")
		local := r.commit(r.host, "unpushed")
		r.git(r.host, "switch", "-q", "--detach")
		wt, err := r.create()
		if err != nil {
			t.Fatal(err)
		}
		if h := r.git(wt, "rev-parse", "HEAD"); h != local {
			t.Fatalf("worktree HEAD %s, want the unpushed local %s on top of %s", h, local, pushed)
		}
	})
	t.Run("branch deleted on origin is not adopted", func(t *testing.T) {
		r, pushed := setup(t)
		r.git(r.host, "fetch", "-q", "origin") // the host saw story/s1 once: a remote-tracking ref exists
		r.git(r.peer, "push", "-q", "origin", "--delete", "story/s1")
		wt, err := r.create()
		if err != nil {
			t.Fatal(err)
		}
		if h, tip := r.git(wt, "rev-parse", "HEAD"), r.git(r.host, "rev-parse", "epic/e1"); h != tip || h == pushed {
			t.Fatalf("worktree HEAD %s, want the epic tip %s (a stale origin/story/s1 is not pushed work)", h, tip)
		}
	})
	t.Run("non-story branch on origin keeps the base rules", func(t *testing.T) {
		r, _ := setup(t)
		r.git(r.peer, "push", "-q", "origin", "origin/story/s1:refs/heads/arena/a-r1")
		wt, err := r.createBranch("arena/a-r1")
		if err != nil {
			t.Fatal(err)
		}
		if h, tip := r.git(wt, "rev-parse", "HEAD"), r.git(r.host, "rev-parse", "epic/e1"); h != tip {
			t.Fatalf("worktree HEAD %s, want the base %s (origin adoption is for story/ branches only)", h, tip)
		}
	})
	t.Run("diverged is refused", func(t *testing.T) {
		r, pushed := setup(t)
		r.git(r.host, "branch", "story/s1", pushed+"~1")
		r.git(r.host, "switch", "-q", "story/s1")
		local := r.commit(r.host, "other host's work")
		r.git(r.host, "switch", "-q", "--detach")
		_, err := r.create()
		if err == nil || !strings.Contains(err.Error(), "diverged") {
			t.Fatalf("err = %v, want a diverged refusal", err)
		}
		if h := r.git(r.host, "rev-parse", "story/s1"); h != local {
			t.Fatalf("refused branch moved: %s != %s", h, local)
		}
	})
}
