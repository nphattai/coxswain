package herdr

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/busy"
)

var _ backend.Backend = New("sess")

func TestClassify(t *testing.T) {
	cases := []struct {
		status, code string
		want         backend.Liveness
		wantErr      bool
	}{
		{"working", "", backend.Alive, false},
		{"idle", "", backend.Alive, false}, // idle agent is still registered = alive (liveness, not busy)
		{"blocked", "", backend.Alive, false},
		{"", "agent_not_found", backend.Settled, false},
		{"", "pane_not_found", backend.Settled, false},
		{"", "", backend.Unknown, true},      // ok but no status
		{"weird", "", backend.Unknown, true}, // unrecognized status
		{"", "some_other_err", backend.Unknown, true},
	}
	for _, c := range cases {
		got, err := classify(c.status, c.code)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("classify(%q,%q) = %v,%v; want %v,err=%v", c.status, c.code, got, err, c.want, c.wantErr)
		}
	}
}

func TestProbeAlive(t *testing.T) {
	c := New("sess")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":true,"result":{"agent":{"agent_status":"working"}}}`), nil
	}
	live, err := c.Probe(backend.Session{ID: "pane:1"})
	if err != nil || live != backend.Alive {
		t.Fatalf("got %v %v", live, err)
	}
}

// A structured error code on a non-zero exit is classified, not treated as an opaque failure.
func TestProbeErrorCodeSettled(t *testing.T) {
	c := New("sess")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":{"code":"agent_not_found"}}`), errors.New("exit 1")
	}
	live, err := c.Probe(backend.Session{ID: "pane:1"})
	if err != nil || live != backend.Settled {
		t.Fatalf("agent_not_found should be Settled, got %v %v", live, err)
	}
}

// A bare failure with no structured code is Unknown (never inferred gone).
func TestProbeOpaqueFailureUnknown(t *testing.T) {
	c := New("sess")
	c.run = func(args ...string) ([]byte, error) {
		return []byte("boom"), errors.New("exit 1")
	}
	live, err := c.Probe(backend.Session{ID: "pane:1"})
	if live != backend.Unknown || err == nil {
		t.Fatalf("opaque failure should be Unknown+error, got %v %v", live, err)
	}
}

// fakeHerdr records every joined herdr arg line and answers workspace create / pane list; everything else returns ok.
func fakeHerdr(calls *[]string) func(...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		*calls = append(*calls, joined)
		switch {
		case strings.Contains(joined, "workspace create"):
			return []byte(`{"ok":true,"result":{"workspace":{"workspace_id":"ws_1"}}}`), nil
		case strings.Contains(joined, "pane list"):
			return []byte(`{"ok":true,"result":{"panes":[{"pane_id":"pane_1"}]}}`), nil
		case strings.Contains(joined, "agent get"):
			return []byte(`{"ok":false,"error":{"code":"agent_not_found"}}`), errors.New("exit 1")
		default:
			return []byte(`{"ok":true,"result":{}}`), nil
		}
	}
}

// Spawn opens a workspace in the worktree, resolves its pane, and types the launch line + Enter; the session's pane id
// is the resolved pane.
func TestSpawnTypesLaunchIntoPane(t *testing.T) {
	var calls []string
	c := New("sess")
	c.run = fakeHerdr(&calls)
	sess, err := c.Spawn(backend.Worktree{Path: "/wt/m10"},
		backend.HarnessSpec{Name: "claude", Argv: []string{"claude", "Your task is the story file /epics/v2/stories/m10.md - read it in full and follow its Working rules exactly."}},
		backend.Brief{StoryPath: "/epics/v2/stories/m10.md"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Kind != SessionKind || sess.ID != "pane_1" || sess.Handle != "sess" {
		t.Fatalf("session = %+v, want herdr/pane_1/sess", sess)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"--session sess workspace create --cwd /wt/m10 --label m10 --no-focus",
		"--session sess pane list --workspace ws_1",
		"pane send-text pane_1 COX_EPIC=",
		"COX_PLANE=terminal 'claude'",
		"pane send-keys pane_1 Enter",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("spawn missing %q in:\n%s", want, joined)
		}
	}
}

// Send rings the pane (send-text + Enter) and reports a real ring; Stop closes the pane and confirms via a settled probe.
func TestSendAndStop(t *testing.T) {
	var calls []string
	c := New("sess")
	c.run = fakeHerdr(&calls)
	rang, err := c.Send(backend.Session{ID: "pane_1"}, "leader here")
	if err != nil || !rang {
		t.Fatalf("Send rang=%v err=%v", rang, err)
	}
	confirmed, err := c.Stop(backend.Session{ID: "pane_1"})
	if err != nil || !confirmed {
		t.Fatalf("Stop confirmed=%v err=%v", confirmed, err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"pane send-text pane_1 leader here", "pane send-keys pane_1 Enter", "pane close pane_1", "agent get pane_1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

// Interrupt sends Ctrl-C to the pane.
func TestInterrupt(t *testing.T) {
	var calls []string
	c := New("sess")
	c.run = fakeHerdr(&calls)
	if err := c.Interrupt(backend.Session{ID: "pane_1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "pane send-keys pane_1 C-c") {
		t.Errorf("interrupt must send C-c: %v", calls)
	}
}

// WorktreeCreate cuts a real git worktree on a new branch; WorktreeRemove removes it and the branch survives (F01).
func TestWorktreeCreateAndRemoveKeepsBranch(t *testing.T) {
	repo := t.TempDir()
	base := t.TempDir() // worktree base
	gitInit(t, repo)
	c := New("sess")
	c.WorktreeBase = base

	wt, err := c.WorktreeCreate(repo, "story/m10", "main")
	if err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	if wt.Branch != "story/m10" {
		t.Fatalf("branch = %q", wt.Branch)
	}
	if out, _ := exec.Command("git", "-C", repo, "worktree", "list").Output(); !strings.Contains(string(out), wt.Path) {
		t.Fatalf("worktree not listed: %s", out)
	}
	if err := c.WorktreeRemove(wt); err != nil {
		t.Fatalf("worktree remove: %v", err)
	}
	// The branch must still exist after removal.
	if err := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/story/m10").Run(); err != nil {
		t.Fatalf("branch story/m10 must survive worktree removal (F01)")
	}
}

// gitInit makes a repo with one commit on main so worktree add has a base.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("commit", "--allow-empty", "-m", "root")
}

// --- Harness-owned busy state consult (DESIGN wave-3 item 3) ---
// herdr has no text composer classifier (Composer was always "unknown"), so before this change a busy-reporting harness
// on herdr could never be seen idle. Now Composer consults the busy record first, the same code path Orca uses.
func TestComposerConsultsBusyRecord(t *testing.T) {
	epic := t.TempDir()
	gen, err := busy.Arm(epic, "w1")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	c := New("sess")
	c.Epic = epic
	// armed (busy)
	if cs, _ := c.Composer(backend.Session{Story: "w1"}); cs != backend.ComposerBusy {
		t.Fatalf("armed busy -> Composer %q, want busy", cs)
	}
	if err := busy.Apply(epic, "w1", busy.Idle, gen, "pi-ext", "e"); err != nil {
		t.Fatal(err)
	}
	if cs, _ := c.Composer(backend.Session{Story: "w1"}); cs != backend.ComposerEmpty {
		t.Fatalf("harness idle -> Composer %q, want empty", cs)
	}
	// no record / no story -> unknown (fallback preserved)
	if cs, _ := c.Composer(backend.Session{Story: "other"}); cs != backend.ComposerUnknown {
		t.Fatalf("no record -> Composer %q, want unknown", cs)
	}
}
