// Package routing chooses the harness (and model) for a story. It is a review_when default, never a hard rule (ADR
// 0011): below the baseline bar it may only filter the policy options by capability-card fit and fall back to the
// policy default, and it always reports the reasons and the baseline rows it cited. Only a baseline table of at least
// 3 stories x 2 harnesses x 2 conditions (12 measured rows) unlocks choosing a non-default harness.
package routing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/workspace"
)

// Bar is the number of measured baseline rows required before routing may choose a non-default harness: 3 stories x 2
// harnesses x 2 conditions.
const Bar = 12

// Story is the routing input drawn from a story's frontmatter. Harness "" or "auto" means "route me"; any other value
// pins the harness and short-circuits routing. Role defaults to worker (dispatch routes workers).
type Story struct {
	ID      string
	Harness string
	Model   string
	Role    harness.Role
}

// Choice is a routing decision: the chosen harness and model, the ordered reasons, and the baseline rows cited when a
// non-default harness was chosen (empty otherwise).
type Choice struct {
	Harness   string   `json:"harness"`
	Model     string   `json:"model"`
	Reasons   []string `json:"reasons"`
	CitedRows []string `json:"cited_rows"`
}

// Decide runs the routing ladder in order: story frontmatter pin -> policy options filtered by card fit -> quota
// (unknown is skipped with a reason) -> baseline (only >= Bar measured rows may beat the default) -> policy default.
// It never returns an empty harness: with no card-fit candidate it still returns the policy default and flags it.
func Decide(story Story, pol *workspace.Policy, cards map[string]harness.Capability, quotas map[string]quota.Reading, rows []BaselineRow) Choice {
	role := story.Role
	if role == "" {
		role = harness.RoleWorker
	}
	var ch Choice

	// Rung 1: a story that pins its harness is honored verbatim; routing does not second-guess a fixed frontmatter.
	if h := story.Harness; h != "" && h != "auto" {
		ch.Harness = h
		ch.Model = story.Model
		ch.Reasons = append(ch.Reasons, fmt.Sprintf("story frontmatter pins harness=%s", h))
		if story.Model != "" {
			ch.Reasons = append(ch.Reasons, fmt.Sprintf("story frontmatter pins model=%s", story.Model))
		}
		return ch
	}

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

	// Rung 3: quota. No harness has a machine-readable quota today, so every reading is unknown and skipped; the reason
	// records it so the decision never looks like quota was consulted and found full.
	for _, h := range fit {
		if q, ok := quotas[h]; !ok || !q.Known {
			reason := "unknown"
			if ok && q.Reason != "" {
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
