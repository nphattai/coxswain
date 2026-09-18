package artifact

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const synthFixture = `---
epic: demo
---
# Arena synthesis - demo

## Claims

| id | role | claim | evidence | tier | severity | verified | verdict | question | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|---|---|---|---|
| adversary-1-1 | adversary | poll clears prompts on delivery | lav:1 | 3 | epic-blocking | pass | accepted | | ack is delivery | advisory | yes |
| reviewer-1-1 | reviewer | which cell owner | x:1 | 3 | significant | pass | captain_decision | who owns the cell? | | | |
`

func TestGenerateArenaSynth(t *testing.T) {
	epic := t.TempDir()
	write(t, filepath.Join(epic, "reports", "arena", "synthesis-round-1.md"), synthFixture)

	shaFn := func(b []byte) string { s := sha256.Sum256(b); return fmt.Sprintf("%x", s)[:12] }
	out, err := GenerateArenaSynth(epic, 1, shaFn)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(out)
	body := string(page)
	// The captain_decision row gets a data-claim-id input; the answered row does not.
	if !strings.Contains(body, `data-claim-id="reviewer-1-1"`) {
		t.Fatalf("captain_decision claim should carry a data-claim-id input:\n%s", body)
	}
	if strings.Contains(body, `data-claim-id="adversary-1-1"`) {
		t.Fatalf("an answered claim should not carry an input")
	}
	if !strings.Contains(body, "who owns the cell?") {
		t.Fatalf("captain decision question not rendered")
	}
	// Sidecar records the synthesis content sha for the stale guard.
	cards, err := List(epic)
	if err != nil || len(cards) != 1 {
		t.Fatalf("list: %v %d", err, len(cards))
	}
	if cards[0].Kind != KindArena || cards[0].SynthesisSHA != shaFn([]byte(synthFixture)) {
		t.Fatalf("arena sidecar: %+v", cards[0])
	}
}

func TestGenerateDesign(t *testing.T) {
	epic := t.TempDir()
	write(t, filepath.Join(epic, "DESIGN.md"), "# Demo design\n\nBody paragraph.\n")
	write(t, filepath.Join(epic, "reports", "arena", "synthesis.md"), synthFixture)
	decisions := filepath.Join(epic, "decisions")
	write(t, filepath.Join(decisions, "0001-x.md"), "# 0001 - keep it lazy\n\n- Status: Accepted (captain)\n")
	write(t, filepath.Join(decisions, "0002-y.md"), "# 0002 - dropped idea\n\n- Status: Superseded by 0001\n")

	out, err := GenerateDesign(epic, filepath.Join(epic, "DESIGN.md"), filepath.Join(epic, "reports", "arena", "synthesis.md"), decisions)
	if err != nil {
		t.Fatal(err)
	}
	body := readFile(t, out)
	for _, want := range []string{"<h1>Demo design</h1>", "Decisions in force", "0001 - keep it lazy", "Latest arena synthesis"} {
		if !strings.Contains(body, want) {
			t.Errorf("design missing %q", want)
		}
	}
	if strings.Contains(body, "dropped idea") {
		t.Errorf("a superseded decision should not be listed")
	}
}

func TestGeneratePlanAndCompare(t *testing.T) {
	epic := t.TempDir()
	planDir := filepath.Join(epic, "plandir")
	write(t, filepath.Join(planDir, "plan.md"), "---\ntitle: demo plan\n---\n# Demo plan\n\nOverview.\n")
	write(t, filepath.Join(planDir, "phase-01-core.md"), "---\ntitle: \"M1 - core\"\nstatus: done\n---\nCore body.\n")
	write(t, filepath.Join(planDir, "phase-02-arena.md"), "---\ntitle: \"M2 - arena\"\nstatus: pending\n---\nArena body.\n")

	out, err := GeneratePlan(epic, planDir)
	if err != nil {
		t.Fatal(err)
	}
	body := readFile(t, out)
	if !strings.Contains(body, "M1 - core") || !strings.Contains(body, ">done<") {
		t.Errorf("plan should show phase title and status:\n%s", body)
	}

	// Architecture mentions section 01 (matches "core" via a shared word) but not arena -> arena is MISSING.
	arch := `<html><body><h2 id="s1">01 core system</h2><p>the core</p></body></html>`
	write(t, filepath.Join(epic, "architecture.html"), arch)
	cout, err := GenerateCompare(epic, planDir, filepath.Join(epic, "architecture.html"))
	if err != nil {
		t.Fatal(err)
	}
	cbody := readFile(t, cout)
	if !strings.Contains(cbody, "MISSING") {
		t.Errorf("comparison should flag a missing counterpart:\n%s", cbody)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
