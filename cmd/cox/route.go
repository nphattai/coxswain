package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/routing"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdRoute implements `cox route --story <id> --epic <dir> [--json]`: it prints the harness/model routing would pick
// for a story, with the reasons and any cited baseline rows. It never dispatches; it is the read-only view of the same
// Decide that `cox story dispatch` runs for a `harness: auto` story.
func cmdRoute(args []string) int {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	story := fs.String("story", "", "story id")
	brief := fs.String("brief", "", "resolve a rule match for a story brief file via the opt-in typed path (Jev)")
	candidates := fs.String("candidates", "", "comma-separated harness:model list; print the first quota-eligible one (or none, exit 1)")
	asJSON := fs.Bool("json", false, "emit the Choice as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *brief != "" {
		return cmdRouteBrief(*epicDir, *brief)
	}
	if *candidates != "" {
		return cmdRouteCandidates(*epicDir, *candidates)
	}
	if *epicDir == "" || *story == "" {
		return usageErr("cox route --story <id> --epic <dir> [--json] | --brief <file> --epic <dir> | --candidates h:m,... --epic <dir>")
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
	if choice.Escalate {
		fmt.Printf("route %s: ESCALATE - %s\n", *story, choice.EscalateReason)
	} else {
		fmt.Printf("route %s: harness=%s", *story, choice.Harness)
		if choice.Model != "" {
			fmt.Printf(" model=%s", choice.Model)
		}
		if choice.Effort != "" {
			fmt.Printf(" effort=%s", choice.Effort)
		}
		fmt.Println()
	}
	if choice.Rule != "" {
		fmt.Printf("  rule=%s resolver=%s\n", choice.Rule, orDashStr(choice.Resolver))
	}
	for _, r := range choice.Reasons {
		fmt.Printf("  - %s\n", r)
	}
	// Every candidate is accounted for with its gate results (DESIGN wave-4 item 10c), not only the choice.
	for _, c := range choice.Candidates {
		fmt.Printf("  candidate: %s:%s provider=%s remaining=%d%% spendPriority=%s runway=%s -> %s\n",
			c.Harness, orDashStr(c.Model), orDashStr(c.Provider), c.PercentRemaining, spStr(c.SpendPriority), orDashStr(c.Runway), c.Reason)
	}
	for _, c := range choice.CitedRows {
		fmt.Printf("  cited: %s\n", c)
	}
	// Observe-only quota (ADR 0011): print the reading for the chosen harness/model as information. On the baseline-ladder
	// path it never changed the Choice; the rules path already gated on it above.
	if choice.Harness != "" {
		q := quota.Pick(mergedQuotaReadings(*epicDir), choice.Harness, choice.Model)
		fmt.Printf("  quota: %s\n", quotaReadingLine(q))
	}
	return 0
}

// cmdRouteCandidates implements `cox route --candidates h:m,h:m --epic <dir>`: it prints the first quota-eligible
// candidate from the current reading (a candidate is eligible when its reading is known, its percent is above zero, no
// applicable runway is exhausted_now, and its credential needs no attention), or `none` and exit 1 when none is. It has
// no side effects: it reads quota and prints, never dispatches (DESIGN wave-4 item 10c, ported from fm-quota-choose.sh).
func cmdRouteCandidates(epicDir, list string) int {
	if epicDir == "" {
		return usageErr("cox route --candidates h:m,... --epic <dir>")
	}
	readings := mergedQuotaReadings(epicDir)
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		h, m, _ := strings.Cut(item, ":")
		h = strings.TrimSpace(h)
		q := quota.Pick(readings, h, modelAlias(strings.TrimSpace(m)))
		if q.Known && q.Attention == "" && q.Runway != quota.RunwayExhaustedNow && q.PercentRemaining > 0 {
			if m == "" {
				fmt.Println(h)
			} else {
				fmt.Printf("%s %s\n", h, strings.TrimSpace(m))
			}
			return 0
		}
	}
	fmt.Println("none")
	return 1
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
	rows, err := routing.ParseBaselines(baselinesDir(epicDir))
	if err != nil {
		return routing.Choice{}, fmt.Errorf("parse baselines: %w", err)
	}
	// Refuse dispatch on a routing profile whose harness has no card or whose effort the card rejects (the card-fit half
	// of routing validation; the structural half runs at policy.Load). Named field, never selected around.
	if err := pol.ValidateRoutingCards(cards); err != nil {
		return routing.Choice{}, err
	}
	in := routing.Story{
		ID: story, Harness: meta.Harness, Model: modelAlias(meta.Model), Role: harness.RoleWorker,
		Route: meta.Route, Effort: storyEffort(pol, meta), Kind: nonEmpty(meta.Kind, "ship"),
	}
	// The rules path gates over the live quota reading; the baseline ladder reads it observe-only (ADR 0011).
	return routing.Decide(in, pol, cards, mergedQuotaReadings(epicDir), rows), nil
}

// storyEffort resolves the reasoning-effort class a story routes at: its `effort:` frontmatter when set, else the policy
// default for its kind (routing.effort[kind], else the code default: scout xhigh, ship low, arena high). (DESIGN wave-4
// item 10.)
func storyEffort(pol *workspace.Policy, meta storyMeta) string {
	if e := strings.TrimSpace(meta.Effort); e != "" {
		return e
	}
	return pol.EffortForKind(nonEmpty(meta.Kind, "ship"))
}

// cmdRouteBrief implements `cox route --brief <file> --epic <dir>`: the opt-in typed match (DESIGN wave-4 item 10b). When
// TYPESAFE_API_KEY is present (environment wins over the workspace's gitignored .env) AND routing.rules is non-empty, it
// asks Jev for the one rule match, applies the confidence floor and the mechanical gates in code, and prints a TOON-style
// block. Off (no key) prints one stderr line and exits 0 with the leader path unchanged; every resolution outcome exits
// 0; a usage/config error (unreadable brief, no policy) exits 2. The key is used only as a request header, never printed.
func cmdRouteBrief(epicDir, briefPath string) int {
	if epicDir == "" || briefPath == "" {
		return usageErr("cox route --brief <file> --epic <dir>")
	}
	// Opt-in gate: read the key from the environment, else the workspace's gitignored .env (environment wins). Copy into a
	// local, never place it on argv, in a log, or in output.
	apiKey := typedAPIKey(epicDir)
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "route: typed resolution off (TYPESAFE_API_KEY absent)")
		return 0
	}
	briefBytes, err := os.ReadFile(briefPath)
	if err != nil {
		return fail("read brief %s: %v", briefPath, err)
	}
	pol := loadPolicyQuiet(epicDir)
	if pol == nil {
		return fail("cannot load policy for %s (need cox/policy.json above the epic)", epicDir)
	}
	cards := map[string]harness.Capability{}
	for _, name := range registry.Names() {
		h, _ := registry.Adapter(name)
		cards[name] = h.Card()
	}
	if err := pol.ValidateRoutingCards(cards); err != nil {
		return fail("%v", err)
	}
	meta := parseStoryMeta(briefBytes)
	story := routing.Story{
		Harness: meta.Harness, Model: modelAlias(meta.Model), Role: harness.RoleWorker,
		Route: meta.Route, Effort: storyEffort(pol, meta), Kind: nonEmpty(meta.Kind, "ship"),
	}
	// COX_TYPESAFE_BASE_URL redirects the endpoint to an httptest server in tests; empty uses the production base. The
	// real endpoint is never called from a test.
	cfg := routing.TypedConfig{BaseURL: strings.TrimSpace(os.Getenv("COX_TYPESAFE_BASE_URL"))}
	// The model sees only the story's task sections (firstmate 795e4b5), not the whole story file.
	res := routing.ResolveTyped(context.Background(), cfg, apiKey, projectName(epicDir), routing.TaskText(string(briefBytes), meta.Kind),
		pol, cards, mergedQuotaReadings(epicDir), story)
	printTypedResult(res)
	return 0
}

// typedAPIKey returns the typed-resolution key: the environment TYPESAFE_API_KEY, else a TYPESAFE_API_KEY= line in the
// workspace's gitignored .env (environment wins). It is read into a local and never logged.
func typedAPIKey(epicDir string) string {
	if k := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")); k != "" {
		return k
	}
	ws, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(ws, ".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "TYPESAFE_API_KEY="); ok {
			return strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	return ""
}

// projectName is the project label the typed request carries as state (never a secret): the epic slug.
func projectName(epicDir string) string {
	return filepath.Base(strings.TrimRight(epicDir, string(filepath.Separator)))
}

// printTypedResult renders a TypedResult as the TOON-style block: status, model/latency/confidence, one candidate line
// per profile with its evidence, and on `clear` a ready-to-dispatch `profile:` line. It never prints the API key.
func printTypedResult(res routing.TypedResult) {
	fmt.Println("route (typed):")
	fmt.Printf("  status: %s\n", res.Status)
	tokens := "-"
	if res.HasUsage {
		tokens = fmt.Sprintf("%d/%d", res.InputTokens, res.OutputTokens)
	}
	fmt.Printf("  model: %s   latency_ms: %d   tokens: %s\n", orDashStr(res.Model), res.LatencyMS, tokens)
	if res.Rule != "" {
		fmt.Printf("  rule: %s (%s)   confidence: %.3g\n", res.Rule, res.RuleWhen, res.Confidence)
	}
	if len(res.Probabilities) > 0 {
		fmt.Printf("  probabilities: %s\n", probLine(res.Probabilities))
	}
	if res.Fallback != "" {
		fmt.Printf("  fallback: %s\n", res.Fallback)
	}
	if res.Reason != "" {
		fmt.Printf("  reason: %s\n", res.Reason)
	}
	if res.Choice != nil {
		for _, c := range res.Choice.Candidates {
			fmt.Printf("  candidate: %s:%s  provider=%s  remaining=%d%%  spendPriority=%s  runway=%s  -> %s\n",
				c.Harness, orDashStr(c.Model), orDashStr(c.Provider), c.PercentRemaining, spStr(c.SpendPriority), orDashStr(c.Runway), c.Reason)
		}
		if res.Status == routing.TypedClear && res.Choice.Harness != "" {
			line := "  profile: --harness " + res.Choice.Harness
			if res.Choice.Model != "" {
				line += " --model " + res.Choice.Model
			}
			if res.Choice.Effort != "" {
				line += " --effort " + res.Choice.Effort
			}
			fmt.Println(line)
		}
	}
}

func probLine(p map[string]float64) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.3g", k, p[k]))
	}
	return strings.Join(parts, " ")
}

func spStr(p *float64) string {
	if p == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.4g", *p)
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
