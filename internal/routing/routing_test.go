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
