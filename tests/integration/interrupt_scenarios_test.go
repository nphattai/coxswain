package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/claude"
	"github.com/nphattai/coxswain/internal/adapter/harness/codex"
	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/protocol/control"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
)

// harnesses runs a scenario against both the push (claude) and pull (codex) adapters, proving the handoff works
// regardless of how the leader is woken.
func harnesses() map[string]harness.Harness {
	return map[string]harness.Harness{"claude": claude.New(), "codex": codex.New()}
}

func seedWorking(t *testing.T, epic, story string) {
	t.Helper()
	if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: 1, Actor: state.Leader, From: state.Submitted, To: state.Working, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
}

func fold(t *testing.T, epic, story string) *state.StorySnap {
	t.Helper()
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	return state.Fold(events).Stories[story]
}

// Scenario 1: interrupt before the worker wrote a checkpoint. Resume must inject "no checkpoint", replay the brief,
// land on attempt+1, and still list the constraint from an already-handled steer.
func TestScenarioInterruptBeforeCheckpoint(t *testing.T) {
	for name, h := range harnesses() {
		t.Run(name, func(t *testing.T) {
			epic := t.TempDir()
			story := "s"
			seedWorking(t, epic, story)
			// A steer the worker handled becomes a durable constraint.
			path, err := inbox.Write(epic, story, "do not squash the migration commit", inbox.Steer, "")
			if err != nil {
				t.Fatal(err)
			}
			rec, err := inbox.List(epic, story)
			if err != nil || len(rec) != 1 {
				t.Fatalf("list: %v %d", err, len(rec))
			}
			if err := inbox.Handled(rec[0]); err != nil {
				t.Fatal(err)
			}
			_ = path

			b := fake.New()
			ctl := &control.Controller{EpicDir: epic, Backend: b, Harness: h}
			if err := ctl.Interrupt(story, backend.Session{ID: "x"}); err != nil {
				t.Fatal(err)
			}
			// Resume in the same worktree at attempt+1: the relaunch preflight needs the story's instructions and a git
			// worktree whose unlanded work it can account for (fm safe_checkpoint).
			write(t, filepath.Join(epic, "stories", story+".md"), "---\nid: s\n---\nbody\n")
			wt := t.TempDir()
			runGit(t, wt, "init", "-q")
			if _, err := ctl.Relaunch(story, wt, "was mid phase 1", backend.Session{}, backend.HarnessSpec{Name: name}, nil); err != nil {
				t.Fatal(err)
			}
			s := fold(t, epic, story)
			if s.State != state.Working || s.Attempt != 2 {
				t.Fatalf("resume want working attempt 2, got %s attempt %d", s.State, s.Attempt)
			}
			// Inject reports no checkpoint (the worker never wrote one) but still succeeds.
			inj, err := checkpoint.Inject(epic, story, 2, "abc")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(inj.Text, "No checkpoint") {
				t.Fatalf("expected first-attempt guidance:\n%s", inj.Text)
			}
			// The constraint survives: the handled steer is still on disk.
			handled, err := os.ReadDir(filepath.Join(epic, "inbox", story, "handled"))
			if err != nil || len(handled) != 1 {
				t.Fatalf("handled steer constraint lost: %v %d", err, len(handled))
			}
		})
	}
}

// Scenario 2: a crash after the side effect (fake recorded the stop) but before the confirming append leaves
// pending_external{intended_to}. Reconcile finishes it by probing and never repeats the stop.
func TestScenarioInterruptAfterSideEffect(t *testing.T) {
	for name, h := range harnesses() {
		t.Run(name, func(t *testing.T) {
			epic := t.TempDir()
			story := "s"
			seedWorking(t, epic, story)
			// First append of a park that then crashed before confirming.
			if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: 1, Actor: state.Leader, From: state.Working, To: state.PendingExternal, Evidence: map[string]any{"intended_to": "parked", "verb": "park"}, ExternalConfirmed: false}); err != nil {
				t.Fatal(err)
			}
			s := fold(t, epic, story)
			if s.State != state.PendingExternal || !s.PendingExternal {
				t.Fatalf("expected pending_external, got %s", s.State)
			}
			if s.LastEvent.Evidence["intended_to"] != "parked" {
				t.Fatalf("intended_to not recorded: %+v", s.LastEvent.Evidence)
			}
			b := fake.New()
			b.Liveness = backend.Settled // the worker really did stop
			ctl := &control.Controller{EpicDir: epic, Backend: b, Harness: h}
			if err := ctl.Reconcile(story, backend.Session{ID: "x"}); err != nil {
				t.Fatal(err)
			}
			if fold(t, epic, story).State != state.Parked {
				t.Fatalf("reconcile should land parked")
			}
			for _, c := range b.Calls {
				if c == "Stop" {
					t.Fatalf("resume repeated the side effect (Stop): %v", b.Calls)
				}
			}
		})
	}
}

// Scenario 3: an interrupt while a steer Write held the lock leaves an orphan temp. The orphan is never counted as a
// record and the next sequence is correct.
func TestScenarioInterruptDuringSteer(t *testing.T) {
	epic := t.TempDir()
	story := "s"
	// First real steer.
	if _, err := inbox.Write(epic, story, "first", inbox.Steer, ""); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: an orphan temp for seq 002.
	dir := inbox.Dir(epic, story)
	if err := os.WriteFile(filepath.Join(dir, "002.msg.999.deadbeef.tmp"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The orphan is not a record.
	recs, err := inbox.List(epic, story)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Seq != 1 {
		t.Fatalf("orphan temp counted as a record: %+v", recs)
	}
	// The next steer still gets 002 (the orphan does not consume a sequence).
	path, err := inbox.Write(epic, story, "second", inbox.Steer, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "002.msg" {
		t.Fatalf("next sequence wrong after orphan temp: %s", path)
	}
}
