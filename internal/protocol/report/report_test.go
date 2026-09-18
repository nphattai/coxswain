package report

import (
	"testing"

	"github.com/nphattai/coxswain/internal/wake"
)

// Two `report done` calls in a row append two worker_done wakes: there is no completion cap on this plane (ADR 0012).
func TestReportDoneTwiceAppendsTwoWorkerDone(t *testing.T) {
	epic := t.TempDir()
	if _, err := Report(epic, "m10", 1, KindDone, "first done", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Report(epic, "m10", 1, KindDone, "second done", map[string]any{"pr": "42"}); err != nil {
		t.Fatal(err)
	}
	wakes, err := wake.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	dones := 0
	for _, w := range wakes {
		if w.Kind == wake.KindWorkerDone {
			dones++
		}
	}
	if dones != 2 {
		t.Fatalf("want 2 worker_done wakes, got %d (%+v)", dones, wakes)
	}
}

// report status maps to a status wake carrying the note as the body; stuck maps to a stuck wake.
func TestReportKindsMapToWakeKinds(t *testing.T) {
	epic := t.TempDir()
	if _, err := Report(epic, "m10", 1, KindStatus, "phase 2 building", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Report(epic, "m10", 1, KindStuck, "blocked on env", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Report(epic, "m10", 1, "bogus", "x", nil); err == nil {
		t.Fatal("unknown report kind must error")
	}
	wakes, _ := wake.Load(epic)
	var gotStatus, gotStuck bool
	for _, w := range wakes {
		switch w.Kind {
		case wake.KindStatus:
			gotStatus = true
			if w.Note != "phase 2 building" {
				t.Errorf("status wake note = %q", w.Note)
			}
		case wake.KindStuck:
			gotStuck = true
		}
	}
	if !gotStatus || !gotStuck {
		t.Fatalf("missing status/stuck wake: status=%v stuck=%v", gotStatus, gotStuck)
	}
}

// report question allocates a durable id and appends an input_required wake carrying evidence.question.
func TestQuestionAppendsInputRequiredWake(t *testing.T) {
	epic := t.TempDir()
	id, err := Question(epic, "m10", 1, "which port range?")
	if err != nil {
		t.Fatal(err)
	}
	if id != "q001" {
		t.Fatalf("first id = %q, want q001", id)
	}
	wakes, _ := wake.Load(epic)
	found := false
	for _, w := range wakes {
		if w.Kind == wake.KindInputRequired && w.Evidence["question"] == "q001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no input_required wake with evidence.question=q001: %+v", wakes)
	}
}
