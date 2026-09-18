package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/routing"
)

// cmdRoute implements `cox route --story <id> --epic <dir> [--json]`: it prints the harness/model routing would pick
// for a story, with the reasons and any cited baseline rows. It never dispatches; it is the read-only view of the same
// Decide that `cox story dispatch` runs for a `harness: auto` story.
func cmdRoute(args []string) int {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	story := fs.String("story", "", "story id")
	asJSON := fs.Bool("json", false, "emit the Choice as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" {
		return usageErr("cox route --story <id> --epic <dir> [--json]")
	}
	choice, err := routeStory(*epicDir, *story)
	if err != nil {
		return fail("%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(choice); err != nil {
			return fail("%v", err)
		}
		return 0
	}
	fmt.Printf("route %s: harness=%s", *story, choice.Harness)
	if choice.Model != "" {
		fmt.Printf(" model=%s", choice.Model)
	}
	fmt.Println()
	for _, r := range choice.Reasons {
		fmt.Printf("  - %s\n", r)
	}
	for _, c := range choice.CitedRows {
		fmt.Printf("  cited: %s\n", c)
	}
	// Observe-only quota (ADR 0011): print the reading for the chosen harness/model as information. It never changed the
	// Choice above; routing does not pick a non-default harness on quota until the baseline bar is met.
	q := quota.Pick(mergedQuotaReadings(*epicDir), choice.Harness, choice.Model)
	fmt.Printf("  quota: %s\n", quotaReadingLine(q))
	return 0
}

// quotaReadingLine renders a Reading as a one-line info string for cox route and doctor: percent+runway+resets when
// known, else "unknown (<reason>)". Source is always shown so provenance is visible.
func quotaReadingLine(q quota.Reading) string {
	scope := q.Harness
	if q.Model != "" {
		scope += "/" + q.Model
	}
	if !q.Known {
		return fmt.Sprintf("%s unknown [%s] (%s)", scope, q.Source, q.Reason)
	}
	line := fmt.Sprintf("%s %d%% runway=%s [%s]", scope, q.PercentRemaining, q.Runway, q.Source)
	if q.ResetsAt != "" {
		line += " resets=" + q.ResetsAt
	}
	return line
}

// routeStory builds the routing inputs for a story (frontmatter, policy, capability cards, quota, baseline rows) and
// returns Decide's Choice. It is the single path both `cox route` and `cox story dispatch` use, so the printed decision
// and the dispatched one never diverge. A policy that cannot be loaded is a hard error (routing needs the default).
func routeStory(epicDir, story string) (routing.Choice, error) {
	pol := loadPolicyQuiet(epicDir)
	if pol == nil {
		return routing.Choice{}, fmt.Errorf("cannot load policy for %s (need cox/policy.json above the epic)", epicDir)
	}
	meta := readStoryMeta(epicDir, story)
	cards := map[string]harness.Capability{}
	for _, name := range registry.Names() {
		h, _ := registry.Adapter(name)
		cards[name] = h.Card()
	}
	cardList := make([]harness.Capability, 0, len(cards))
	for _, name := range registry.Names() {
		cardList = append(cardList, cards[name])
	}
	rows, err := routing.ParseBaselines(baselinesDir(epicDir))
	if err != nil {
		return routing.Choice{}, fmt.Errorf("parse baselines: %w", err)
	}
	in := routing.Story{ID: story, Harness: meta.Harness, Model: modelAlias(meta.Model), Role: harness.RoleWorker}
	return routing.Decide(in, pol, cards, quota.Readings(cardList), rows), nil
}

// baselinesDir returns <workspace>/docs/baselines, or "" when no workspace root is found (routing then sees no rows and
// keeps the default).
func baselinesDir(epicDir string) string {
	ws, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return ""
	}
	return filepath.Join(ws, "docs", "baselines")
}
