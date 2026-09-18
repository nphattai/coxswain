package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/state"
)

// runWith writes a .run with two working stories (A: ctx_1, B: ctx_2) into a fresh epic dir.
func runWith(t *testing.T) string {
	t.Helper()
	epic := filepath.Join(t.TempDir(), "e1")
	if err := os.MkdirAll(epic, 0o755); err != nil {
		t.Fatal(err)
	}
	run := "run=run_x\n" +
		"task.A=task_1\ndispatch.A=ctx_1\nterm.A=term_a\n" +
		"task.B=task_2\ndispatch.B=ctx_2\nterm.B=term_b\n"
	if err := os.WriteFile(filepath.Join(epic, ".run"), []byte(run), 0o644); err != nil {
		t.Fatal(err)
	}
	return epic
}

func planOf(t *testing.T, epic string) *Plan {
	t.Helper()
	p, err := Read(epic)
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

func story(p *Plan, id string) *StoryPlan {
	for i := range p.Stories {
		if p.Stories[i].ID == id {
			return &p.Stories[i]
		}
	}
	return nil
}

func TestApplyLivenessKeepsAliveDropsDead(t *testing.T) {
	p := planOf(t, runWith(t))
	// A is still running; B has settled (succeeded).
	p.ApplyLiveness([]backend.Worker{
		{Dispatch: "ctx_1", State: "running"},
		{Dispatch: "ctx_2", State: "succeeded"},
	})

	a := story(p, "A")
	if a.Session == nil || a.Final != state.Working {
		t.Errorf("A (running) should keep its session and stay working: %+v", a)
	}
	b := story(p, "B")
	if b.Session != nil {
		t.Error("B (succeeded) must not keep a session")
	}
	if b.Final != state.PendingExternal {
		t.Errorf("B should land in pending_external, got %s", b.Final)
	}
	// The pending event names intended_to working and the last dispatch state, for cox reconcile.
	last := b.Events[len(b.Events)-1]
	if last.To != state.PendingExternal || last.Evidence["intended_to"] != string(state.Working) {
		t.Errorf("B last event not pending_external->working: %+v", last)
	}
	if last.Evidence["dispatch"] != "ctx_2" || b.Events[0].Evidence["last_dispatch"] != "succeeded" {
		t.Errorf("B missing dispatch/last_dispatch evidence: %+v", b.Events)
	}
}

// A dispatch Orca does not list at all is treated as gone.
func TestApplyLivenessMissingDispatchIsGone(t *testing.T) {
	p := planOf(t, runWith(t))
	p.ApplyLiveness([]backend.Worker{{Dispatch: "ctx_1", State: "running"}}) // ctx_2 absent
	b := story(p, "B")
	if b.Session != nil || b.Final != state.PendingExternal {
		t.Errorf("B (absent from worker-list) should be pending_external with no session: %+v", b)
	}
	if b.Events[0].Evidence["last_dispatch"] != "gone" {
		t.Errorf("B last_dispatch should be gone, got %v", b.Events[0].Evidence["last_dispatch"])
	}
}

// With no backend info (nil workers), Read's behavior is kept: the working session survives (the caller warns).
func TestApplyLivenessSkippedWhenNoBackend(t *testing.T) {
	p := planOf(t, runWith(t))
	// Simulate "no Orca": the caller simply does not call ApplyLiveness. Sessions stay as Read built them.
	if story(p, "A").Session == nil || story(p, "B").Session == nil {
		t.Error("without ApplyLiveness both working stories keep their sessions (old behavior)")
	}
}

// A dead working dispatch, once applied, folds to pending_external and reconcile can see intended_to.
func TestApplyLivenessFoldsToPendingExternal(t *testing.T) {
	epic := runWith(t)
	p := planOf(t, epic)
	p.ApplyLiveness([]backend.Worker{{Dispatch: "ctx_1", State: "running"}, {Dispatch: "ctx_2", State: "failed"}})
	if err := Apply(*p); err != nil {
		t.Fatal(err)
	}
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	snap := state.Fold(events)
	if s := snap.Stories["B"]; s == nil || s.State != state.PendingExternal || !s.PendingExternal {
		t.Fatalf("B should fold to pending_external (unconfirmed): %+v", s)
	}
	if _, err := os.Stat(filepath.Join(epic, ".cox", "sessions", "B.json")); !os.IsNotExist(err) {
		t.Error("B should have no session file")
	}
	if _, err := os.Stat(filepath.Join(epic, ".cox", "sessions", "A.json")); err != nil {
		t.Errorf("A (running) should keep its session file: %v", err)
	}
}
