package routing

import (
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/workspace"
)

func testPolicy() *workspace.Policy {
	p := &workspace.Policy{}
	p.Harness.Worker = workspace.HarnessRole{Options: []string{"claude", "codex", "omp"}, Default: "claude"}
	p.Harness.Leader = workspace.HarnessRole{Options: []string{"claude", "codex"}, Default: "claude"}
	return p
}

func testCards() map[string]harness.Capability {
	return map[string]harness.Capability{
		"claude": {Name: "claude", Roles: []harness.Role{harness.RoleLeader, harness.RoleWorker}, Telemetry: true},
		"codex":  {Name: "codex", Roles: []harness.Role{harness.RoleLeader, harness.RoleWorker}, Telemetry: false},
	}
}

func testQuotas() map[string]quota.Reading {
	return quota.Readings([]harness.Capability{
		{Name: "claude", Telemetry: true}, {Name: "codex", Telemetry: false},
	})
}

func reasonsHave(reasons []string, sub string) bool {
	for _, r := range reasons {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

// A story that pins its harness in frontmatter is honored verbatim, no routing.
func TestDecideFixedFrontmatter(t *testing.T) {
	ch := Decide(Story{ID: "s", Harness: "codex", Model: "gpt-x"}, testPolicy(), testCards(), testQuotas(), nil)
	if ch.Harness != "codex" || ch.Model != "gpt-x" {
		t.Fatalf("fixed frontmatter not honored: %+v", ch)
	}
	if !reasonsHave(ch.Reasons, "pins harness=codex") {
		t.Fatalf("missing pin reason: %+v", ch.Reasons)
	}
}

// With a below-bar baseline (n=1), routing keeps the policy default and says so, and drops an option with no card.
func TestDecideBelowBarKeepsDefault(t *testing.T) {
	rows := []BaselineRow{{Story: "s1", Harness: "claude", Condition: "bare", Result: "pass", LeaderFixes: 0}}
	ch := Decide(Story{ID: "s", Harness: "auto"}, testPolicy(), testCards(), testQuotas(), rows)
	if ch.Harness != "claude" {
		t.Fatalf("below-bar harness = %q, want claude (default)", ch.Harness)
	}
	if len(ch.CitedRows) != 0 {
		t.Fatalf("below-bar cited rows = %v, want none", ch.CitedRows)
	}
	if !reasonsHave(ch.Reasons, "baseline rows 1 < 12: default kept") {
		t.Fatalf("missing below-bar reason: %+v", ch.Reasons)
	}
	if !reasonsHave(ch.Reasons, "omp dropped: no capability card") {
		t.Fatalf("omp with no card must be dropped: %+v", ch.Reasons)
	}
	if !reasonsHave(ch.Reasons, "quota for claude unknown") {
		t.Fatalf("quota unknown must be recorded: %+v", ch.Reasons)
	}
}

// At or above the bar (12 measured rows), routing may choose a non-default harness and must cite the rows behind it.
func TestDecideAboveBarChoosesAndCites(t *testing.T) {
	var rows []BaselineRow
	for i := 0; i < 6; i++ {
		rows = append(rows, BaselineRow{Story: "s", Harness: "claude", Condition: "bare", Result: "pass", LeaderFixes: 2})
		rows = append(rows, BaselineRow{Story: "s", Harness: "codex", Condition: "bare", Result: "pass", LeaderFixes: 0})
	}
	ch := Decide(Story{ID: "s", Harness: "auto"}, testPolicy(), testCards(), testQuotas(), rows)
	if ch.Harness != "codex" {
		t.Fatalf("above-bar harness = %q, want codex (fewest fixes)", ch.Harness)
	}
	if len(ch.CitedRows) == 0 {
		t.Fatalf("a non-default choice must cite rows: %+v", ch)
	}
	if !reasonsHave(ch.Reasons, "chose codex over default claude") {
		t.Fatalf("missing override reason: %+v", ch.Reasons)
	}
}

// threeCardPolicy and threeCards add pi as a third card-fit candidate (worker role).
func threeCardPolicy() *workspace.Policy {
	p := &workspace.Policy{}
	p.Harness.Worker = workspace.HarnessRole{Options: []string{"claude", "codex", "pi"}, Default: "claude"}
	p.Harness.Leader = workspace.HarnessRole{Options: []string{"claude", "codex", "pi"}, Default: "claude"}
	return p
}

func threeCards() map[string]harness.Capability {
	c := testCards()
	c["pi"] = harness.Capability{Name: "pi", Roles: []harness.Role{harness.RoleLeader, harness.RoleWorker}, Telemetry: true}
	return c
}

// Pi is a third card-fit candidate, but with no measured baseline rows it is never auto-routed even above the bar:
// bestByBaseline drops a candidate with no rows, so codex (measured, fewest fixes) wins and pi is only listed as a fit.
func TestDecidePiThirdCandidateDroppedUntilBaseline(t *testing.T) {
	var rows []BaselineRow
	for i := 0; i < 6; i++ {
		rows = append(rows, BaselineRow{Story: "s", Harness: "claude", Condition: "bare", Result: "pass", LeaderFixes: 2})
		rows = append(rows, BaselineRow{Story: "s", Harness: "codex", Condition: "bare", Result: "pass", LeaderFixes: 0})
	}
	ch := Decide(Story{ID: "s", Harness: "auto"}, threeCardPolicy(), threeCards(), testQuotas(), rows)
	if !reasonsHave(ch.Reasons, "card-fit candidates: claude, codex, pi") {
		t.Fatalf("pi must be a third card-fit candidate: %+v", ch.Reasons)
	}
	if ch.Harness == "pi" {
		t.Fatalf("pi has no measured rows and must not be auto-routed, got %q", ch.Harness)
	}
	if ch.Harness != "codex" {
		t.Fatalf("codex (measured, fewest fixes) should win, got %q", ch.Harness)
	}
}

// Once Pi has its own measured baseline rows that beat the default, it becomes eligible and is chosen with citations.
func TestDecidePiEligibleWithBaseline(t *testing.T) {
	var rows []BaselineRow
	for i := 0; i < 6; i++ {
		rows = append(rows, BaselineRow{Story: "s", Harness: "claude", Condition: "bare", Result: "pass", LeaderFixes: 2})
		rows = append(rows, BaselineRow{Story: "s", Harness: "pi", Condition: "bare", Result: "pass", LeaderFixes: 0})
	}
	ch := Decide(Story{ID: "s", Harness: "auto"}, threeCardPolicy(), threeCards(), testQuotas(), rows)
	if ch.Harness != "pi" {
		t.Fatalf("pi with fewest fixes should be chosen once it has rows, got %q", ch.Harness)
	}
	if len(ch.CitedRows) == 0 || !reasonsHave(ch.Reasons, "chose pi over default claude") {
		t.Fatalf("pi choice must cite rows and record the override: %+v", ch)
	}
}

// Above the bar but with the default already the best, routing keeps the default (no citation, clear reason).
func TestDecideAboveBarDefaultWins(t *testing.T) {
	var rows []BaselineRow
	for i := 0; i < 6; i++ {
		rows = append(rows, BaselineRow{Story: "s", Harness: "claude", Condition: "bare", Result: "pass", LeaderFixes: 0})
		rows = append(rows, BaselineRow{Story: "s", Harness: "codex", Condition: "bare", Result: "pass", LeaderFixes: 3})
	}
	ch := Decide(Story{ID: "s", Harness: "auto"}, testPolicy(), testCards(), testQuotas(), rows)
	if ch.Harness != "claude" || len(ch.CitedRows) != 0 {
		t.Fatalf("default should win: %+v", ch)
	}
	if !reasonsHave(ch.Reasons, "no candidate beats default") {
		t.Fatalf("missing default-kept reason: %+v", ch.Reasons)
	}
}
