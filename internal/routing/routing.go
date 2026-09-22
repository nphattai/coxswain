// Package routing chooses the harness (and model) for a story. It is a review_when default, never a hard rule (ADR
// 0011): below the baseline bar it may only filter the policy options by capability-card fit and fall back to the
// policy default, and it always reports the reasons and the baseline rows it cited. Only a baseline table of at least
// 3 stories x 2 harnesses x 2 conditions (12 measured rows) unlocks choosing a non-default harness.
package routing

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/workspace"
)

// Bar is the number of measured baseline rows required before routing may choose a non-default harness: 3 stories x 2
// harnesses x 2 conditions.
const Bar = 12

// Story is the routing input drawn from a story's frontmatter. Harness "" or "auto" means "route me"; any other value
// pins the harness. Role defaults to worker (dispatch routes workers). Route is the leader-written match ("rule=<n>",
// 1-based, or "override", or "" for no match); Effort is the resolved reasoning-effort class the floor gate reads; Kind
// is the story kind (informational). (DESIGN wave-4 item 10.)
type Story struct {
	ID      string
	Harness string
	Model   string
	Role    harness.Role
	Route   string
	Effort  string
	Kind    string
}

// Candidate is one profile accounted for in a routing decision: its identity, gate results, and why it is eligible,
// unrankable, or ineligible. Every profile in a matched rule's (or the default) array appears here, so a decision never
// omits a candidate (DESIGN wave-4 item 10: account for every candidate).
type Candidate struct {
	Harness          string   `json:"harness"`
	Model            string   `json:"model,omitempty"`
	Effort           string   `json:"effort,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	Eligible         bool     `json:"eligible"`
	Rankable         bool     `json:"rankable"`
	SpendPriority    *float64 `json:"spend_priority,omitempty"`
	PercentRemaining int      `json:"percent_remaining,omitempty"`
	Runway           string   `json:"runway,omitempty"`
	QuotaSource      string   `json:"quota_source,omitempty"`
	Reason           string   `json:"reason"`
}

// Choice is a routing decision: the chosen harness/model/effort, the ordered reasons, and the baseline rows cited when a
// non-default harness was chosen. When a matched profile array cannot be resolved to one candidate (an approval-gated
// rule, a spendPriority tie within the epsilon, or no rankable candidate) Escalate is true with EscalateReason and every
// Candidate is listed; dispatch then stops with a question for the captain and never picks around it (DESIGN wave-4 item
// 10). Rule/Resolver/Confidence/SpendPriority record the provenance for evidence.route.
type Choice struct {
	Harness        string      `json:"harness"`
	Model          string      `json:"model"`
	Effort         string      `json:"effort,omitempty"`
	Reasons        []string    `json:"reasons"`
	CitedRows      []string    `json:"cited_rows,omitempty"`
	Escalate       bool        `json:"escalate,omitempty"`
	EscalateReason string      `json:"escalate_reason,omitempty"`
	Rule           string      `json:"rule,omitempty"`     // "rule=<n>" | "override" | "default" | ""
	Resolver       string      `json:"resolver,omitempty"` // leader | jev | none
	Confidence     *float64    `json:"confidence,omitempty"`
	SpendPriority  *float64    `json:"spend_priority,omitempty"`
	Candidates     []Candidate `json:"candidates,omitempty"`
}

// Decide resolves a story's harness/model in precedence order (DESIGN wave-4 item 10): a story pin (honored verbatim,
// but refused when a matched rule forbids it) -> the leader-matched rule's profile array (three gates + spendPriority
// ranking) -> the default profile array -> today's baseline ladder (card fit -> baseline -> policy default; ADR 0011
// stays, observe-only). The captain --harness/--model/--effort override sits above all of this and is applied by the
// caller (it skips routing entirely). A profile array that cannot resolve to one candidate returns Escalate.
func Decide(story Story, pol *workspace.Policy, cards map[string]harness.Capability, readings []quota.Reading, rows []BaselineRow) Choice {
	role := story.Role
	if role == "" {
		role = harness.RoleWorker
	}
	rule, ruleIdx, ruleOK := matchedRule(pol, story.Route)

	// Rung 1: a story that pins its harness. A pin is honored verbatim, EXCEPT when the story also carries a matched rule
	// whose profile array does not contain that harness - a pin the rule forbids is a contradiction, refused with the
	// rule text rather than silently overriding the posture.
	if h := story.Harness; h != "" && h != "auto" {
		if ruleOK {
			if prof, found := ruleProfileForHarness(rule, h); found {
				ch := Choice{Harness: h, Model: nonEmpty(story.Model, prof.Model), Effort: nonEmpty(story.Effort, prof.Effort), Rule: story.Route, Resolver: "leader"}
				ch.Reasons = append(ch.Reasons, fmt.Sprintf("story pins harness=%s, allowed by rule %d", h, ruleIdx+1))
				return ch
			}
			return escalateChoice(story.Route, "leader",
				fmt.Sprintf("story pins harness=%s but rule %d (%s) allows only %s", h, ruleIdx+1, ruleWhenExcerpt(rule), profileHarnesses(rule.Profiles)), nil)
		}
		ch := Choice{Harness: h, Model: story.Model, Effort: story.Effort}
		ch.Reasons = append(ch.Reasons, fmt.Sprintf("story frontmatter pins harness=%s", h))
		if story.Model != "" {
			ch.Reasons = append(ch.Reasons, fmt.Sprintf("story frontmatter pins model=%s", story.Model))
		}
		return ch
	}

	// Rung 2: a leader-matched rule's profile array is resolved through the three gates and the spendPriority ranking.
	if ruleOK {
		return resolveProfiles(rule.Profiles, story, cards, readings, pol, story.Route, "leader", rule.Approval, ruleWhenExcerpt(rule))
	}
	// Rung 3: the default profile array (an explicit `override` route, or no route with a default array configured).
	if len(pol.Routing.DefaultProfiles) > 0 {
		resolver := "none"
		if story.Route == "override" {
			resolver = "leader"
		}
		return resolveProfiles(pol.Routing.DefaultProfiles, story, cards, readings, pol, "default", resolver, "", "default_profiles")
	}

	// Rung 4+: today's baseline ladder (ADR 0011, observe-only). Unchanged from before the routing rules landed.
	var ch Choice
	ch.Effort = story.Effort
	rolePol := pol.Harness.Worker
	if role == harness.RoleLeader {
		rolePol = pol.Harness.Leader
	}
	def := rolePol.Default
	ch.Model = story.Model

	// Rung 2: keep only options whose capability card fits the role. A missing card or a card without the role is
	// dropped with a reason, so an option that has no adapter never wins by default.
	var fit []string
	for _, opt := range rolePol.Options {
		card, ok := cards[opt]
		if !ok {
			ch.Reasons = append(ch.Reasons, fmt.Sprintf("%s dropped: no capability card", opt))
			continue
		}
		if !roleFits(card, role) {
			ch.Reasons = append(ch.Reasons, fmt.Sprintf("%s dropped: card does not fit role %s", opt, role))
			continue
		}
		fit = append(fit, opt)
	}
	ch.Reasons = append(ch.Reasons, fmt.Sprintf("card-fit candidates: %s", listOrNone(fit)))

	// Rung: quota is observe-only on the ladder (ADR 0011). It never changes the pick here; it only records the reading so
	// the decision never looks like quota was consulted and found full. The rules path above is where quota actually gates.
	for _, h := range fit {
		if q := quota.Pick(readings, h, ""); !q.Known {
			reason := "unknown"
			if q.Reason != "" {
				reason = q.Reason
			}
			ch.Reasons = append(ch.Reasons, fmt.Sprintf("quota for %s unknown, skipped (%s)", h, reason))
		}
	}

	// Rung 4: baseline. Only a table with >= Bar measured rows unlocks a non-default choice, and the chosen harness
	// must cite the rows behind it.
	measured := Measured(rows)
	if len(measured) >= Bar {
		if pick, cited, ok := bestByBaseline(fit, def, measured); ok {
			ch.Harness = pick
			ch.CitedRows = cited
			ch.Reasons = append(ch.Reasons, fmt.Sprintf("baseline rows %d >= %d: chose %s over default %s (fewest mean leader fixes)", len(measured), Bar, pick, def))
			return ch
		}
		ch.Reasons = append(ch.Reasons, fmt.Sprintf("baseline rows %d >= %d but no candidate beats default %s: default kept", len(measured), Bar, def))
	} else {
		ch.Reasons = append(ch.Reasons, fmt.Sprintf("baseline rows %d < %d: default kept", len(measured), Bar))
	}

	// Rung 5: policy default.
	ch.Harness = def
	if !contains(fit, def) {
		ch.Reasons = append(ch.Reasons, fmt.Sprintf("warning: policy default %s is not a card-fit candidate", def))
	}
	ch.Reasons = append(ch.Reasons, fmt.Sprintf("policy default harness=%s", def))
	return ch
}

// bestByBaseline picks the card-fit candidate with the strictly lowest mean leader-fix count and returns it with the
// rows that back it, but only when it beats the default's mean. A candidate with no measured rows is not eligible.
//
// ponytail: naive mean over leader fixes, no variance test or significance. Enough to break a tie once >= Bar rows
// exist; replace with a proper comparison (variance, per-condition) when the real downstream baseline lands.
func bestByBaseline(fit []string, def string, measured []BaselineRow) (string, []string, bool) {
	type agg struct {
		sum, n int
		rows   []string
	}
	byHarness := map[string]*agg{}
	for _, r := range measured {
		if r.LeaderFixes < 0 || !contains(fit, r.Harness) {
			continue
		}
		a := byHarness[r.Harness]
		if a == nil {
			a = &agg{}
			byHarness[r.Harness] = a
		}
		a.sum += r.LeaderFixes
		a.n++
		a.rows = append(a.rows, r.Cite())
	}
	mean := func(h string) (float64, bool) {
		if a := byHarness[h]; a != nil && a.n > 0 {
			return float64(a.sum) / float64(a.n), true
		}
		return 0, false
	}
	defMean, defOK := mean(def)
	if !defOK {
		return "", nil, false // no default baseline to beat; keep default
	}
	// Deterministic order for a stable pick.
	names := make([]string, 0, len(byHarness))
	for h := range byHarness {
		names = append(names, h)
	}
	sort.Strings(names)
	best, bestMean, found := "", defMean, false
	for _, h := range names {
		if h == def {
			continue
		}
		if m, ok := mean(h); ok && m < bestMean {
			best, bestMean, found = h, m, true
		}
	}
	if !found {
		return "", nil, false
	}
	return best, byHarness[best].rows, true
}

// matchedRule resolves a story's `route:` frontmatter to a rule: "rule=<n>" (1-based) returns rules[n-1] when in range.
// "override", "", or an out-of-range/garbled value returns ok=false (no rule matched; the caller falls through). An
// out-of-range rule number is handled by the caller as an escalate at rung 2 only when it parsed a number; here a bad
// number simply does not match so the fall-through and the escalate stay in one place.
func matchedRule(pol *workspace.Policy, route string) (workspace.RoutingRule, int, bool) {
	route = strings.TrimSpace(route)
	if !strings.HasPrefix(route, "rule=") {
		return workspace.RoutingRule{}, -1, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(route, "rule=")))
	if err != nil || n < 1 || n > len(pol.Routing.Rules) {
		return workspace.RoutingRule{}, -1, false
	}
	return pol.Routing.Rules[n-1], n - 1, true
}

// resolveProfiles applies the three gates and the spendPriority ranking to a profile array (DESIGN wave-4 item 10). An
// approval-gated rule escalates before any gate. Every profile becomes a Candidate; the highest known spendPriority
// among eligible, rankable candidates wins, a tie within the policy epsilon escalates, and no rankable candidate
// escalates. It never invents a match and never picks around a gate failure.
func resolveProfiles(profiles []workspace.RoutingProfile, story Story, cards map[string]harness.Capability, readings []quota.Reading, pol *workspace.Policy, ruleLabel, resolver, approval, whenExcerpt string) Choice {
	if approval == "captain" {
		return escalateChoice(ruleLabel, resolver, fmt.Sprintf("rule %s requires the captain's explicit approval before dispatch", ruleLabel), candidatesFor(profiles, story, cards, readings, pol))
	}
	cands := candidatesFor(profiles, story, cards, readings, pol)
	ch := Choice{Rule: ruleLabel, Resolver: resolver, Candidates: cands}
	ch.Reasons = append(ch.Reasons, fmt.Sprintf("resolving %s (%s): %d profile(s)", ruleLabel, whenExcerpt, len(profiles)))

	var best *Candidate
	rankable := 0
	for i := range cands {
		c := &cands[i]
		if !c.Eligible || !c.Rankable || c.SpendPriority == nil {
			continue
		}
		rankable++
		if best == nil || *c.SpendPriority > *best.SpendPriority {
			best = c
		}
	}
	if best == nil {
		ch.Escalate = true
		ch.EscalateReason = "no rankable eligible candidate; every profile is ineligible or has no comparable spendPriority"
		ch.Reasons = append(ch.Reasons, ch.EscalateReason)
		return ch
	}
	// A tie within the epsilon among the top spendPriority values is a genuine tie: escalate rather than break it by array
	// order or harness name (DESIGN: no array-order bias).
	eps := pol.RoutingTieEpsilon()
	tied := 0
	for i := range cands {
		c := &cands[i]
		if c.Eligible && c.Rankable && c.SpendPriority != nil && abs(*c.SpendPriority-*best.SpendPriority) <= eps {
			tied++
		}
	}
	if tied > 1 {
		ch.Escalate = true
		ch.EscalateReason = fmt.Sprintf("genuine spendPriority tie within epsilon %g among %d candidates", eps, tied)
		ch.Reasons = append(ch.Reasons, ch.EscalateReason)
		return ch
	}
	ch.Harness = best.Harness
	ch.Model = best.Model
	ch.Effort = nonEmpty(best.Effort, story.Effort)
	ch.SpendPriority = best.SpendPriority
	ch.Reasons = append(ch.Reasons, fmt.Sprintf("chose %s spendPriority=%g (highest among %d rankable)", best.Harness, *best.SpendPriority, rankable))
	return ch
}

// candidatesFor evaluates every profile in an array into a Candidate through the three gates, in array order (order is
// recorded, never used to break a tie).
func candidatesFor(profiles []workspace.RoutingProfile, story Story, cards map[string]harness.Capability, readings []quota.Reading, pol *workspace.Policy) []Candidate {
	out := make([]Candidate, 0, len(profiles))
	for _, prof := range profiles {
		out = append(out, evaluateProfile(prof, story, cards, readings, pol))
	}
	return out
}

// evaluateProfile runs one profile through the three gates over the quota reading for its (harness, model): gate 1
// eligibility (credential attention or exhausted_now => ineligible), gate 2 the reasoning-class floor (effort below the
// profile floor => ineligible), gate 3 runway feasibility (projected_exhaustion below the min runway => ineligible),
// then rankability by spendPriority (missing/unmeasurable => eligible but unrankable, listed and never chosen).
func evaluateProfile(prof workspace.RoutingProfile, story Story, cards map[string]harness.Capability, readings []quota.Reading, pol *workspace.Policy) Candidate {
	q := quota.Pick(readings, prof.Harness, prof.Model)
	c := Candidate{
		Harness:          prof.Harness,
		Model:            prof.Model,
		Effort:           nonEmpty(prof.Effort, story.Effort),
		Provider:         nonEmpty(prof.Provider, prof.Harness),
		PercentRemaining: q.PercentRemaining,
		Runway:           q.Runway,
		QuotaSource:      q.Source,
	}
	// Gate 1: eligibility.
	if q.Attention != "" {
		c.Reason = "gate 1 (eligibility): " + q.Attention
		return c
	}
	if q.Runway == quota.RunwayExhaustedNow {
		c.Reason = "gate 1 (eligibility): runway exhausted_now"
		return c
	}
	// Gate 2: reasoning-class floor.
	if prof.Floor != "" {
		meets, ok := harness.EffortMeets(story.Effort, prof.Floor)
		if !ok {
			// story effort is empty or an unknown class: the floor cannot be evaluated, so it does not gate here (the effort
			// is resolved and validated by the caller); note the disclosed uncertainty.
			c.Reason = fmt.Sprintf("gate 2 (floor): effort %q not comparable to floor %q; floor not enforced", story.Effort, prof.Floor)
		} else if !meets {
			c.Reason = fmt.Sprintf("gate 2 (floor): effort %s below reasoning-class floor %s", story.Effort, prof.Floor)
			return c
		}
	}
	// Gate 3: runway feasibility (projected exhaustion below the min runway).
	if q.Runway == quota.RunwayProjected && q.UsableRunwaySeconds >= 0 && q.UsableRunwaySeconds < pol.RoutingMinRunwaySeconds() {
		c.Reason = fmt.Sprintf("gate 3 (runway): projected_exhaustion in %ds below min %ds", q.UsableRunwaySeconds, pol.RoutingMinRunwaySeconds())
		return c
	}
	// Past the gates: eligible. Rankable only with a known, comparable spendPriority.
	c.Eligible = true
	c.SpendPriority = q.SpendPriority
	switch {
	case q.Source == quota.SourceNone || !q.Known:
		c.Reason = "eligible, unrankable: quota not machine-readable (" + orReason(q.Reason, "no quota source") + ")"
	case q.SpendPriority == nil:
		c.Reason = "eligible, unrankable: spendPriority missing or unmeasurable"
	default:
		c.Rankable = true
		c.Reason = "eligible"
	}
	return c
}

// escalateChoice builds an escalate Choice carrying the reason and the candidates considered.
func escalateChoice(ruleLabel, resolver, reason string, cands []Candidate) Choice {
	return Choice{Escalate: true, EscalateReason: reason, Rule: ruleLabel, Resolver: resolver, Candidates: cands, Reasons: []string{reason}}
}

func ruleProfileForHarness(r workspace.RoutingRule, h string) (workspace.RoutingProfile, bool) {
	for _, p := range r.Profiles {
		if p.Harness == h {
			return p, true
		}
	}
	return workspace.RoutingProfile{}, false
}

func profileHarnesses(profiles []workspace.RoutingProfile) string {
	var hs []string
	for _, p := range profiles {
		hs = append(hs, p.Harness)
	}
	return listOrNone(hs)
}

func ruleWhenExcerpt(r workspace.RoutingRule) string {
	w := strings.TrimSpace(r.When)
	if len(w) > 60 {
		return w[:60]
	}
	return w
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func orReason(a, fallback string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return fallback
}

func roleFits(card harness.Capability, role harness.Role) bool {
	for _, r := range card.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}
