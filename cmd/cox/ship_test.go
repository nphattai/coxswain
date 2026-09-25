package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/fake"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/verdict"
)

// greenMergeInput and greenForge are a captain-run merge of an open, green PR on the epic branch.
func greenForge() *fake.Forge {
	return &fake.Forge{F: fake.Fixture{
		PR:     forge.PR{Number: 9, Head: "deadbeef1234", Base: "epic/x", State: "open", Mergeable: true},
		Checks: []forge.Check{{Name: "ci", Status: "completed", Conclusion: "success"}},
	}}
}

func greenInput() verdict.MergeInput {
	return verdict.MergeInput{Selector: "9", EpicBranch: "epic/x", Production: "main", Method: "squash", Captain: true}
}

// A real green merge appends a `merged` event to the epic ledger with pr/head/method/by evidence (item 8). Base-behavior
// probe: on the base sha there is no cox ship merge and no merged event type.
func TestShipMergeAppendsLedgerRow(t *testing.T) {
	epic := t.TempDir()
	if rc := runShipMerge(epic, greenInput(), greenForge()); rc != 0 {
		t.Fatalf("green merge rc=%d, want 0", rc)
	}
	b, err := os.ReadFile(state.LedgerPath(epic))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	text := string(b)
	for _, want := range []string{`"type":"merged"`, `"pr":9`, `"method":"squash"`, `"by":"captain"`, "deadbeef1234"} {
		if !strings.Contains(text, want) {
			t.Errorf("ledger row missing %q:\n%s", want, text)
		}
	}
}

// --check decides but writes no ledger row and merges nothing.
func TestShipMergeCheckWritesNoLedger(t *testing.T) {
	epic := t.TempDir()
	in := greenInput()
	in.Check = true
	f := greenForge()
	if rc := runShipMerge(epic, in, f); rc != 0 {
		t.Fatalf("--check on a green PR rc=%d, want 0", rc)
	}
	if _, err := os.Stat(filepath.Join(epic, "ledger.jsonl")); !os.IsNotExist(err) {
		t.Errorf("--check must not write a ledger row (stat err=%v)", err)
	}
	if merged, _ := f.Merged(f.F.PR); merged {
		t.Error("--check must not merge")
	}
}

// A worker terminal (COX_STORY) is refused (exit 1) and writes no ledger row.
func TestShipMergeWorkerRefused(t *testing.T) {
	epic := t.TempDir()
	in := greenInput()
	in.Worker = true // as cmdShipMerge sets from COX_STORY
	if rc := runShipMerge(epic, in, greenForge()); rc != 1 {
		t.Fatalf("worker merge rc=%d, want 1 (refused)", rc)
	}
	if _, err := os.Stat(filepath.Join(epic, "ledger.jsonl")); !os.IsNotExist(err) {
		t.Errorf("a refused merge must not write a ledger row (stat err=%v)", err)
	}
}

// A pending PR is unknown (exit 3).
func TestShipMergePendingUnknownExit(t *testing.T) {
	epic := t.TempDir()
	f := greenForge()
	f.F.Checks = []forge.Check{{Name: "ci", Status: "in_progress"}}
	if rc := runShipMerge(epic, greenInput(), f); rc != 3 {
		t.Fatalf("pending PR rc=%d, want 3 (unknown)", rc)
	}
}

// #45 follow-up (a): the first run merges but cannot read the merge back (exit 3, no ledger row); the re-run finds the
// PR merged, is refused only by the state gate, and records the missing `merged` row once (exit 0). A third run is a
// plain refusal: the row is never duplicated.
func TestShipMergeRerunRecordsALandedMerge(t *testing.T) {
	epic := t.TempDir()
	f := greenForge()
	f.F.ReadBackLag = 99 // the forge never confirms inside this run
	if rc := runShipMerge(epic, greenInput(), f); rc != 3 {
		t.Fatalf("first run rc=%d, want 3 (unknown read-back)", rc)
	}
	if _, err := os.Stat(state.LedgerPath(epic)); !os.IsNotExist(err) {
		t.Fatal("an unconfirmed merge wrote a ledger row")
	}
	f.F.PR.State = "merged" // GitHub now reports it
	if rc := runShipMerge(epic, greenInput(), f); rc != 0 {
		t.Fatalf("re-run on the landed merge rc=%d, want 0 (recorded)", rc)
	}
	if rc := runShipMerge(epic, greenInput(), f); rc != 1 {
		t.Fatalf("third run rc=%d, want 1 (already recorded)", rc)
	}
	b, _ := os.ReadFile(state.LedgerPath(epic))
	if n := strings.Count(string(b), `"type":"merged"`); n != 1 || !strings.Contains(string(b), `"pr":9`) {
		t.Fatalf("ledger has %d merged row(s), want exactly 1 for PR 9:\n%s", n, b)
	}
	// A worker may not record it either.
	epic2 := t.TempDir()
	in := greenInput()
	in.Worker = true
	if rc := runShipMerge(epic2, in, f); rc != 1 {
		t.Errorf("a worker re-run rc=%d, want 1", rc)
	}
	if _, err := os.Stat(state.LedgerPath(epic2)); !os.IsNotExist(err) {
		t.Error("a worker re-run wrote a ledger row")
	}
}
