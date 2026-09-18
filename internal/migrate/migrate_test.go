package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/state"
)

// A v1 .run shaped like bin/test/run-state.sh produces (append-only, last value wins): one working story, one parked
// (dispatch cleared, parked set), one done (dispatch cleared, done set).
const fixtureRun = `run=run_old
run=run_test
task.A=task_1
dispatch.A=ctx_1
term.A=term_a
task.B=task_2
dispatch.B=ctx_2
term.B=term_b
dispatch.B=
term.B=
parked.B=2026-09-15
task.C=task_3
dispatch.C=ctx_3
done.C=2026-09-15
dispatch.C=
`

func writeRun(t *testing.T) string {
	t.Helper()
	epic := filepath.Join(t.TempDir(), "e1")
	if err := os.MkdirAll(epic, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, ".run"), []byte(fixtureRun), 0o644); err != nil {
		t.Fatal(err)
	}
	return epic
}

func TestReadPlan(t *testing.T) {
	epic := writeRun(t)
	p, err := Read(epic)
	if err != nil {
		t.Fatal(err)
	}
	if p.Run != "run_test" {
		t.Errorf("run = %q, want run_test (last value wins)", p.Run)
	}
	want := map[string]struct {
		final   state.State
		session bool
	}{
		"A": {state.Working, true},
		"B": {state.Parked, false},
		"C": {state.Completed, false},
	}
	if len(p.Stories) != 3 {
		t.Fatalf("got %d stories, want 3: %+v", len(p.Stories), p.Stories)
	}
	for _, sp := range p.Stories {
		w, ok := want[sp.ID]
		if !ok {
			t.Fatalf("unexpected story %s", sp.ID)
		}
		if sp.Final != w.final {
			t.Errorf("%s final = %q, want %q", sp.ID, sp.Final, w.final)
		}
		if (sp.Session != nil) != w.session {
			t.Errorf("%s session presence = %v, want %v", sp.ID, sp.Session != nil, w.session)
		}
	}
}

func TestApplyProducesCleanTree(t *testing.T) {
	epic := writeRun(t)
	p, err := Read(epic)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}

	// The fold replays to the final v1 state of each story.
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	snap := state.Fold(events)
	for id, want := range map[string]state.State{"A": state.Working, "B": state.Parked, "C": state.Completed} {
		if s := snap.Stories[id]; s == nil || s.State != want {
			t.Errorf("story %s folded to %v, want %v", id, s, want)
		}
	}
	// Every event is attributed to the migration and confirmed.
	for _, ev := range events {
		if ev.Actor != state.Migration || !ev.ExternalConfirmed {
			t.Errorf("event not a confirmed migration event: %+v", ev)
		}
	}

	// The live session is reconstructed from dispatch.<id> + term.<id>.
	if b, err := os.ReadFile(filepath.Join(epic, ".cox", "sessions", "A.json")); err != nil {
		t.Errorf("session A not written: %v", err)
	} else if !strings.Contains(string(b), "ctx_1") || !strings.Contains(string(b), "term_a") {
		t.Errorf("session A missing dispatch/term: %s", b)
	}
	if _, err := os.Stat(filepath.Join(epic, ".cox", "sessions", "B.json")); err == nil {
		t.Error("parked story B should have no live session")
	}

	// .cox/run written from run=.
	if b, _ := os.ReadFile(filepath.Join(epic, ".cox", "run")); string(b) != "run_test" {
		t.Errorf(".cox/run = %q, want run_test", b)
	}

	// The two invariants that make cox doctor clean: no live .run, and a v2 event log present.
	if _, err := os.Stat(filepath.Join(epic, ".run")); !os.IsNotExist(err) {
		t.Error(".run should be renamed away (doctor flags a live .run next to .cox)")
	}
	if _, err := os.Stat(filepath.Join(epic, ".run.migrated")); err != nil {
		t.Errorf(".run.migrated not present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(epic, ".cox", "events.jsonl")); err != nil {
		t.Errorf(".cox/events.jsonl not present: %v", err)
	}
}

// A second --apply is refused: it would double every event.
func TestApplyRefusesWhenEventsExist(t *testing.T) {
	epic := writeRun(t)
	p, err := Read(epic)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	before, _, _ := state.Load(epic)
	if err := Apply(p); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second apply should refuse, got %v", err)
	}
	after, _, _ := state.Load(epic)
	if len(after) != len(before) {
		t.Fatalf("event count changed on refused apply: %d -> %d", len(before), len(after))
	}
}
