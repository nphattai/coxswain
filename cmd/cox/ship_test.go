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
