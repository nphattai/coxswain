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

// testQuotas is the observe-only ladder input: unknown readings for both harnesses, so the ADR-0011 ladder still records
// "quota unknown, skipped".
func testQuotas() []quota.Reading {
	return []quota.Reading{quota.For(harness.Capability{Name: "claude"}), quota.For(harness.Capability{Name: "codex"})}
}

// sp is a spendPriority pointer helper.
func sp(v float64) *float64 { return &v }

// knownReading builds a fresh, known quota reading for a (harness, model) with a spendPriority and runway.
func knownReading(h, model string, pct int, spendPriority *float64, runway string, usableRunway int64) quota.Reading {
	return quota.Reading{
		Harness: h, Model: model, Known: true, PercentRemaining: pct,
		Runway: runway, UsableRunwaySeconds: usableRunway, Source: quota.SourceQuotaAxi,
		SpendPriority: spendPriority,
	}
}

// rulePolicy builds a policy whose routing carries the given rules and default profiles.
func rulePolicy(rules []workspace.RoutingRule, def []workspace.RoutingProfile) *workspace.Policy {
	p := testPolicy()
	p.Routing.Rules = rules
	p.Routing.DefaultProfiles = def
	return p
}

func candByHarness(cands []Candidate, h string) *Candidate {
	for i := range cands {
		if cands[i].Harness == h {
			return &cands[i]
		}
	}
	return nil
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

// --- item 10: rules, profile arrays, three gates, spendPriority ranking, escalation ---

// A matched rule ranks its eligible profiles by spendPriority (argmax) and every profile is accounted for as a candidate.
func TestDecideRuleRanksBySpendPriority(t *testing.T) {
	rules := []workspace.RoutingRule{{
		When:     "backend work",
		Profiles: []workspace.RoutingProfile{{Harness: "claude", Model: "opus"}, {Harness: "codex", Model: "gpt"}},
	}}
	readings := []quota.Reading{
		knownReading("claude", "opus", 80, sp(0.10), quota.RunwayThroughReset, quota.NoRunway),
		knownReading("codex", "gpt", 40, sp(0.50), quota.RunwayThroughReset, quota.NoRunway),
	}
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if ch.Escalate {
		t.Fatalf("should resolve, not escalate: %+v", ch)
	}
	if ch.Harness != "codex" {
		t.Fatalf("argmax spendPriority = %q, want codex (0.50 > 0.10)", ch.Harness)
	}
	if ch.SpendPriority == nil || *ch.SpendPriority != 0.50 {
		t.Fatalf("chosen spendPriority = %v, want 0.50", ch.SpendPriority)
	}
	if len(ch.Candidates) != 2 {
		t.Fatalf("every candidate must be accounted for, got %d", len(ch.Candidates))
	}
	if ch.Rule != "rule=1" || ch.Resolver != "leader" {
		t.Fatalf("rule/resolver not recorded: rule=%q resolver=%q", ch.Rule, ch.Resolver)
	}
}

// Gate 1: a credential-attention reading and an exhausted_now reading are both ineligible, with the reason; the eligible
// third profile wins.
func TestDecideGate1Eligibility(t *testing.T) {
	rules := []workspace.RoutingRule{{When: "w", Profiles: []workspace.RoutingProfile{
		{Harness: "claude"}, {Harness: "codex"}, {Harness: "pi"},
	}}}
	readings := []quota.Reading{
		{Harness: "claude", Known: false, Source: quota.SourceQuotaAxi, Attention: "credential attention: keychain_prompt_required", Runway: quota.RunwayUnknown},
		knownReading("codex", "", 5, sp(0.9), quota.RunwayExhaustedNow, 0),
		knownReading("pi", "", 70, sp(0.2), quota.RunwayThroughReset, quota.NoRunway),
	}
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), threeCards(), readings, nil)
	if ch.Escalate {
		t.Fatalf("pi is eligible, should not escalate: %+v", ch)
	}
	if ch.Harness != "pi" {
		t.Fatalf("gate 1 should leave only pi eligible, got %q", ch.Harness)
	}
	if c := candByHarness(ch.Candidates, "claude"); c == nil || c.Eligible || !strings.Contains(c.Reason, "credential attention") {
		t.Fatalf("claude must be ineligible for credential attention: %+v", c)
	}
	if c := candByHarness(ch.Candidates, "codex"); c == nil || c.Eligible || !strings.Contains(c.Reason, "exhausted_now") {
		t.Fatalf("codex must be ineligible for exhausted_now: %+v", c)
	}
}

// Gate 2: a profile floor the story's effort does not meet makes that profile ineligible.
func TestDecideGate2ReasoningFloor(t *testing.T) {
	rules := []workspace.RoutingRule{{When: "w", Profiles: []workspace.RoutingProfile{
		{Harness: "claude", Floor: "high"}, {Harness: "codex"},
	}}}
	readings := []quota.Reading{
		knownReading("claude", "", 90, sp(0.9), quota.RunwayThroughReset, quota.NoRunway),
		knownReading("codex", "", 50, sp(0.1), quota.RunwayThroughReset, quota.NoRunway),
	}
	// effort=low does not meet floor=high, so the high-spendPriority claude is dropped and codex wins.
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if ch.Harness != "codex" {
		t.Fatalf("gate 2 should drop claude (floor high > effort low), got %q", ch.Harness)
	}
	if c := candByHarness(ch.Candidates, "claude"); c == nil || c.Eligible || !strings.Contains(c.Reason, "below reasoning-class floor") {
		t.Fatalf("claude must be floor-ineligible: %+v", c)
	}
}

// Gate 3: a projected_exhaustion runway below the min runway makes a profile ineligible even with the top spendPriority.
func TestDecideGate3RunwayFeasibility(t *testing.T) {
	rules := []workspace.RoutingRule{{When: "w", Profiles: []workspace.RoutingProfile{
		{Harness: "claude"}, {Harness: "codex"},
	}}}
	readings := []quota.Reading{
		knownReading("claude", "", 90, sp(0.9), quota.RunwayProjected, 600), // 10 min left, below the 4h floor
		knownReading("codex", "", 50, sp(0.1), quota.RunwayThroughReset, quota.NoRunway),
	}
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if ch.Harness != "codex" {
		t.Fatalf("gate 3 should drop claude (600s < 4h), got %q", ch.Harness)
	}
	if c := candByHarness(ch.Candidates, "claude"); c == nil || c.Eligible || !strings.Contains(c.Reason, "gate 3") {
		t.Fatalf("claude must be runway-ineligible: %+v", c)
	}
}

// A spendPriority tie within the epsilon escalates rather than being broken by array order.
func TestDecideTieEscalates(t *testing.T) {
	rules := []workspace.RoutingRule{{When: "w", Profiles: []workspace.RoutingProfile{
		{Harness: "claude"}, {Harness: "codex"},
	}}}
	readings := []quota.Reading{
		knownReading("claude", "", 80, sp(0.500), quota.RunwayThroughReset, quota.NoRunway),
		knownReading("codex", "", 80, sp(0.505), quota.RunwayThroughReset, quota.NoRunway), // within default epsilon 0.01
	}
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if !ch.Escalate || !strings.Contains(ch.EscalateReason, "tie") {
		t.Fatalf("a spendPriority tie must escalate: %+v", ch)
	}
	if ch.Harness != "" {
		t.Fatalf("an escalate must not pick a harness, got %q", ch.Harness)
	}
}

// An approval-gated rule escalates before ranking, listing its candidates.
func TestDecideApprovalRuleEscalates(t *testing.T) {
	rules := []workspace.RoutingRule{{When: "risky", Approval: "captain", Profiles: []workspace.RoutingProfile{{Harness: "claude"}}}}
	readings := []quota.Reading{knownReading("claude", "", 90, sp(0.9), quota.RunwayThroughReset, quota.NoRunway)}
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if !ch.Escalate || !strings.Contains(ch.EscalateReason, "captain") {
		t.Fatalf("an approval-gated rule must escalate to the captain: %+v", ch)
	}
}

// An all-tight array (every profile ineligible or unrankable) escalates with "no rankable eligible candidate".
func TestDecideAllTightEscalates(t *testing.T) {
	rules := []workspace.RoutingRule{{When: "w", Profiles: []workspace.RoutingProfile{
		{Harness: "claude"}, {Harness: "codex"},
	}}}
	readings := []quota.Reading{
		knownReading("claude", "", 3, sp(0.9), quota.RunwayExhaustedNow, 0),
		{Harness: "codex", Known: true, PercentRemaining: 50, Runway: quota.RunwayThroughReset, Source: quota.SourceQuotaAxi}, // no spendPriority => unrankable
	}
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if !ch.Escalate || !strings.Contains(ch.EscalateReason, "no rankable eligible candidate") {
		t.Fatalf("an all-tight array must escalate: %+v", ch)
	}
	if c := candByHarness(ch.Candidates, "codex"); c == nil || !c.Eligible || c.Rankable {
		t.Fatalf("codex must be eligible-but-unrankable: %+v", c)
	}
}

// A story pin outside the matched rule's array is refused with the rule text; a pin the rule allows is honored.
func TestDecidePinOutsideRuleRefused(t *testing.T) {
	rules := []workspace.RoutingRule{{When: "backend only", Profiles: []workspace.RoutingProfile{{Harness: "claude"}}}}
	readings := []quota.Reading{knownReading("claude", "", 90, sp(0.9), quota.RunwayThroughReset, quota.NoRunway)}
	ch := Decide(Story{ID: "s", Harness: "codex", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if !ch.Escalate || !strings.Contains(ch.EscalateReason, "pins harness=codex") || !strings.Contains(ch.EscalateReason, "backend only") {
		t.Fatalf("a pin outside the rule must be refused with the rule text: %+v", ch)
	}
	// A pin the rule allows is honored.
	ch2 := Decide(Story{ID: "s", Harness: "claude", Route: "rule=1", Effort: "low"}, rulePolicy(rules, nil), testCards(), readings, nil)
	if ch2.Escalate || ch2.Harness != "claude" {
		t.Fatalf("a pin the rule allows must be honored: %+v", ch2)
	}
}

// Precedence: with no matched rule but a default profile array, Decide resolves the default array; with neither, it falls
// to the baseline ladder.
func TestDecidePrecedenceDefaultThenLadder(t *testing.T) {
	def := []workspace.RoutingProfile{{Harness: "codex"}, {Harness: "claude"}}
	readings := []quota.Reading{
		knownReading("claude", "", 90, sp(0.2), quota.RunwayThroughReset, quota.NoRunway),
		knownReading("codex", "", 50, sp(0.8), quota.RunwayThroughReset, quota.NoRunway),
	}
	// route "override" => skip rules, use default_profiles; codex has the higher spendPriority.
	ch := Decide(Story{ID: "s", Harness: "auto", Route: "override", Effort: "low"}, rulePolicy(nil, def), testCards(), readings, nil)
	if ch.Harness != "codex" || ch.Rule != "default" {
		t.Fatalf("default array should resolve to codex: %+v", ch)
	}
	// No rules, no default array => the baseline ladder (below bar keeps the policy default).
	ch2 := Decide(Story{ID: "s", Harness: "auto", Effort: "low"}, testPolicy(), testCards(), testQuotas(), nil)
	if ch2.Harness != "claude" || !reasonsHave(ch2.Reasons, "policy default harness=claude") {
		t.Fatalf("no rule/default should fall to the ladder default: %+v", ch2)
	}
}
