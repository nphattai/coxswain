package epic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/state"
)

func signEpic(t *testing.T, synthesis string) string {
	t.Helper()
	epicDir := filepath.Join(t.TempDir(), "e1")
	if err := os.MkdirAll(filepath.Join(epicDir, "reports", "arena"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epicDir, "DESIGN.md"), []byte("# design\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if synthesis != "" {
		if err := os.WriteFile(filepath.Join(epicDir, "reports", "arena", "synthesis.md"), []byte(synthesis), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return epicDir
}

const filledSynthesis = `# synthesis
| role | claim | evidence | severity | verdict | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|
| adversary | flaw | cox/foo.go:2@abc1234 | epic-blocking | accepted | real | added guard | yes |
| reviewer | nit | cox/foo.go:1@abc1234 | minor | rejected | wrong read | none | yes |
`

func lastSigned(t *testing.T, epicDir string) *state.Event {
	t.Helper()
	events, _, err := state.Load(epicDir)
	if err != nil {
		t.Fatal(err)
	}
	var out *state.Event
	for i := range events {
		if events[i].Type == state.DesignSigned {
			out = &events[i]
		}
	}
	return out
}

func TestSignRequiresSynthesis(t *testing.T) {
	epicDir := signEpic(t, "")
	if err := Sign(epicDir, ""); err == nil || !strings.Contains(err.Error(), "no synthesis") {
		t.Fatalf("want no-synthesis error, got %v", err)
	}
}

func TestSignRefusesBlankVerdict(t *testing.T) {
	blank := `| role | claim | evidence | severity | verdict | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|
| adversary | flaw | cox/foo.go:2@abc | epic-blocking | | | | |
`
	epicDir := signEpic(t, blank)
	err := Sign(epicDir, "")
	if err == nil || !strings.Contains(err.Error(), "no verdict") {
		t.Fatalf("want blank-verdict error, got %v", err)
	}
}

func TestSignRecordsByAndRefusesReSign(t *testing.T) {
	epicDir := signEpic(t, filledSynthesis)
	if err := Sign(epicDir, "captain"); err != nil {
		t.Fatalf("first sign: %v", err)
	}
	ev := lastSigned(t, epicDir)
	if ev == nil || ev.Story != state.EpicStory || ev.Evidence["design_sha"] == nil || ev.Evidence["synthesis_sha"] == nil {
		t.Fatalf("design_signed event malformed: %+v", ev)
	}
	if ev.Evidence["by"] != "captain" {
		t.Errorf("--by not recorded: %+v", ev.Evidence)
	}

	// A second --sign is refused outright and points at --amend; nothing is appended.
	if err := Sign(epicDir, "captain"); err == nil || !strings.Contains(err.Error(), "already signed") {
		t.Fatalf("re-sign should be refused, got %v", err)
	}
	if err := Amend(epicDir, "contract widened"); err != nil {
		t.Fatalf("amend after sign: %v", err)
	}
}

// Sign writes design_signed to the durable, committed ledger.jsonl (so it survives a machine move), never to the
// machine-local .cox/events.jsonl (finding 2).
func TestSignWritesDurableLedger(t *testing.T) {
	epicDir := signEpic(t, filledSynthesis)
	if err := Sign(epicDir, "captain"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	ledger, err := os.ReadFile(state.LedgerPath(epicDir))
	if err != nil {
		t.Fatalf("ledger.jsonl not written: %v", err)
	}
	if !strings.Contains(string(ledger), "design_signed") {
		t.Errorf("ledger.jsonl missing design_signed:\n%s", ledger)
	}
	// The runtime log must not carry the durable signature.
	if b, err := os.ReadFile(state.EventsPath(epicDir)); err == nil && strings.Contains(string(b), "design_signed") {
		t.Errorf(".cox/events.jsonl must not carry design_signed:\n%s", b)
	}
	if signed, err := isSigned(epicDir); err != nil || !signed {
		t.Errorf("isSigned must read the ledger: signed=%v err=%v", signed, err)
	}
}

func TestSignRequiresCaptainAgrees(t *testing.T) {
	noAgrees := `| role | claim | evidence | severity | verdict | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|
| adversary | flaw | cox/foo.go:2@abc | epic-blocking | accepted | real | guard | |
`
	epicDir := signEpic(t, noAgrees)
	if err := Sign(epicDir, ""); err == nil || !strings.Contains(err.Error(), "captain agrees") {
		t.Fatalf("want captain-agrees refusal, got %v", err)
	}
}

func TestSignRefusesUnresolvedEpicBlocking(t *testing.T) {
	unresolved := `---
epic: e1
round2: no
---
| role | claim | evidence | severity | verdict | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|
| adversary | data loss on retry | cox/foo.go:2@abc1234 | epic-blocking | unresolved | needs a second look | none | yes |
`
	epicDir := signEpic(t, unresolved)
	err := Sign(epicDir, "")
	if err == nil || !strings.Contains(err.Error(), "round 2 required") {
		t.Fatalf("want round-2 refusal, got %v", err)
	}
	// The same claim resolved to accepted signs cleanly.
	resolved := strings.Replace(unresolved, "epic-blocking | unresolved", "epic-blocking | accepted", 1)
	epicDir2 := signEpic(t, resolved)
	if err := Sign(epicDir2, ""); err != nil {
		t.Fatalf("resolved epic-blocking should sign: %v", err)
	}
}

func TestAmendRequiresReason(t *testing.T) {
	epicDir := signEpic(t, filledSynthesis)
	if err := Amend(epicDir, ""); err == nil {
		t.Fatal("amend without reason should fail")
	}
	if err := Amend(epicDir, "dropped a field"); err != nil {
		t.Fatalf("amend: %v", err)
	}
	events, _, _ := state.Load(epicDir)
	found := false
	for _, e := range events {
		if e.Type == state.DesignAmended && e.Evidence["reason"] == "dropped a field" {
			found = true
		}
	}
	if !found {
		t.Error("design_amended event not found")
	}
}

// v3Synthesis builds a coxswain.arena.v3 synthesis with the six sections filled and the given claim rows (each row is the
// cells between the outer pipes for: role|claim|evidence|tier|severity|verified|verdict|question|reason|change|agrees).
func v3Synthesis(rows string) string {
	return `---
epic: e1
schema: coxswain.arena.v3
---
# synthesis

## Adopted decision
adopt the guard.

## Decisive evidence
tier-2 test at cox/foo.go:2@abc1234.

## Rejected alternatives
the global lock lost on throughput.

## Preserved locked decisions
ADR 0002 stays.

## Remaining uncertainty
none material.

## Verification gates
go test passes.

## Claims

| role | claim | evidence | tier | severity | verified | verdict | question | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|---|---|---|
` + rows
}

func TestSignV3AcceptsCleanSynthesis(t *testing.T) {
	rows := "| adversary | flaw | cox/foo.go:2@abc1234 | 2 | epic-blocking | pass | accepted | | real | guard | |\n"
	epicDir := signEpic(t, v3Synthesis(rows))
	if err := Sign(epicDir, "captain"); err != nil {
		t.Fatalf("clean v3 synthesis should sign (v3 does not require captain-agrees on every row): %v", err)
	}
}

func TestSignV3RefusesEmptySection(t *testing.T) {
	s := strings.Replace(v3Synthesis("| adversary | flaw | cox/foo.go:2@abc1234 | 2 | epic-blocking | pass | accepted | | real | guard | |\n"),
		"none material.", "<what is still unknown and how it will be watched>", 1)
	epicDir := signEpic(t, s)
	if err := Sign(epicDir, ""); err == nil || !strings.Contains(err.Error(), "Remaining uncertainty") {
		t.Fatalf("want empty-section refusal naming the section, got %v", err)
	}
}

func TestSignV3RefusesUnverifiedEpicBlocking(t *testing.T) {
	rows := "| adversary | flaw | cox/foo.go:2@abc1234 | 2 | epic-blocking | unknown | accepted | | real | guard | |\n"
	epicDir := signEpic(t, v3Synthesis(rows))
	if err := Sign(epicDir, ""); err == nil || !strings.Contains(err.Error(), "not verified pass") {
		t.Fatalf("want unverified-epic-blocking refusal, got %v", err)
	}
}

func TestSignV3RefusesOpenCaptainDecision(t *testing.T) {
	rows := "| adversary | policy call | cox/foo.go:2@abc1234 | 3 | significant | pass | captain_decision | which bar? | leader unsure | none | |\n"
	epicDir := signEpic(t, v3Synthesis(rows))
	if err := Sign(epicDir, ""); err == nil || !strings.Contains(err.Error(), "captain_decision") {
		t.Fatalf("want open-captain_decision refusal, got %v", err)
	}
	// Answered (captain agrees filled) signs.
	answered := strings.Replace(v3Synthesis(rows), "captain_decision | which bar? | leader unsure | none | |", "captain_decision | which bar? | leader unsure | none | keep low |", 1)
	epicDir2 := signEpic(t, answered)
	if err := Sign(epicDir2, ""); err != nil {
		t.Fatalf("answered captain_decision should sign: %v", err)
	}
}

func TestSignV3RefusesRoundOver3(t *testing.T) {
	epicDir := filepath.Join(t.TempDir(), "e1")
	dir := filepath.Join(epicDir, "reports", "arena")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epicDir, "DESIGN.md"), []byte("# d\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := "| adversary | flaw | cox/foo.go:2@abc1234 | 2 | epic-blocking | pass | accepted | | real | guard | |\n"
	if err := os.WriteFile(filepath.Join(dir, "synthesis-round-4.md"), []byte(v3Synthesis(rows)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("synthesis-round-4.md", filepath.Join(dir, "synthesis.md")); err != nil {
		t.Fatal(err)
	}
	if err := Sign(epicDir, ""); err == nil || !strings.Contains(err.Error(), "3-round maximum") {
		t.Fatalf("want round>3 refusal, got %v", err)
	}
}

// B-04: a captain's no-arena ruling signs without a synthesis; the event carries no_arena and the ruling, a missing
// ruling is refused, and a second sign is refused like any other.
func TestSignNoArena(t *testing.T) {
	epicDir := signEpic(t, "")
	if err := SignNoArena(epicDir, "captain", "  "); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("no-arena sign without a ruling: err %v, want a --reason refusal", err)
	}
	if err := SignNoArena(epicDir, "captain", "lite epic, no arena"); err != nil {
		t.Fatalf("no-arena sign: %v", err)
	}
	ev := lastSigned(t, epicDir)
	if ev == nil || ev.Evidence["no_arena"] != true || ev.Evidence["reason"] != "lite epic, no arena" || ev.Evidence["by"] != "captain" || ev.Evidence["design_sha"] == nil {
		t.Fatalf("design_signed evidence = %+v", ev)
	}
	if _, ok := ev.Evidence["synthesis_sha"]; ok {
		t.Error("a no-arena signature carries a synthesis sha")
	}
	if err := SignNoArena(epicDir, "", "again"); err == nil || !strings.Contains(err.Error(), "already signed") {
		t.Errorf("re-sign: err %v, want already signed", err)
	}
}
