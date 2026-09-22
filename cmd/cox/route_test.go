package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// routeStory resolves policy + cards + baseline for a story and returns the Choice; a below-bar baseline keeps the
// policy default and records the reason, and an auto story is routed rather than pinned.
func TestRouteStoryBelowBarKeepsDefault(t *testing.T) {
	ws := t.TempDir()
	// Minimal workspace: workspace.json marks the root, policy.json is the shipped template, one measured baseline row.
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"schema":"coxswain.workspace.v1"}`)
	tpl, err := os.ReadFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ws, "cox", "policy.json"), string(tpl))
	mustWrite(t, filepath.Join(ws, "docs", "baselines", "claude-2026-09-15.md"),
		"| story | before sha | harness | condition | test result | leader fixes |\n|---|---|---|---|---|---|\n| s1 | abc | claude | bare | pass | 0 |\n")

	epic := filepath.Join(ws, "proj", "epics", "e1")
	mustWrite(t, filepath.Join(epic, "stories", "s.md"), "---\nid: s\nharness: auto\n---\nbody\n")

	ch, err := routeStory(epic, "s")
	if err != nil {
		t.Fatalf("routeStory: %v", err)
	}
	if ch.Harness != "claude" {
		t.Fatalf("routed harness = %q, want claude (default kept)", ch.Harness)
	}
	found := false
	for _, r := range ch.Reasons {
		if r == "baseline rows 1 < 12: default kept" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing below-bar reason in %v", ch.Reasons)
	}
	// routeEvidence wraps the Choice under evidence.route for the working event.
	ev := routeEvidence(&ch)
	if ev["route"] == nil {
		t.Fatalf("routeEvidence missing route key: %v", ev)
	}
	if routeEvidence(nil) != nil {
		t.Fatal("routeEvidence(nil) must be nil (a fixed dispatch carries no route evidence)")
	}
}

// allTightQuotaAxiJSON is a schema-5 stub where both providers read exhausted_now, so every candidate fails gate 1 and a
// rule array escalates ("no rankable eligible candidate").
const allTightQuotaAxiJSON = `{"generatedAt":"2026-09-22T00:00:00Z","schemaVersion":5,"providers":[` +
	`{"provider":"claude","windows":[],"state":{"status":"fresh","stale":false},"quotaSemantics":{"status":"known","effectiveAvailability":[` +
	`{"scope":"all_models","status":"known","effectivePercentRemaining":2,"boundedBy":["w"],"limitingWindowIds":["w"],"runway":{"status":"exhausted_now"},"selection":{"status":"known","spendPriority":0.1}}]}},` +
	`{"provider":"codex","windows":[],"state":{"status":"fresh","stale":false},"quotaSemantics":{"status":"known","effectiveAvailability":[` +
	`{"scope":"all_models","status":"known","effectivePercentRemaining":1,"boundedBy":["w"],"limitingWindowIds":["w"],"runway":{"status":"exhausted_now"},"selection":{"status":"known","spendPriority":0.3}}]}}` +
	`]}`

// ruledWorkspace writes a workspace with one routing rule ([claude, codex]) and a hermetic quota-axi stub (claude
// spendPriority 0.2, codex 0.7), plus a routed story with the given frontmatter body, and returns the epic dir.
func ruledWorkspace(t *testing.T, storyFrontmatter string) string {
	return ruledWorkspaceQuota(t, stubQuotaAxiJSON, storyFrontmatter)
}

// ruledWorkspaceQuota is ruledWorkspace with a caller-chosen quota-axi stub payload.
func ruledWorkspaceQuota(t *testing.T, quotaJSON, storyFrontmatter string) string {
	t.Helper()
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"schema":"coxswain.workspace.v1"}`)
	stub := filepath.Join(ws, "quota-axi-stub")
	mustWrite(t, stub, "#!/bin/sh\ncat <<'JSON'\n"+quotaJSON+"\nJSON\n")
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	tpl, err := os.ReadFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	pol := strings.Replace(string(tpl), `"default": "policy",`,
		`"default": "policy", "rules": [{"when":"backend API work","profiles":[{"harness":"claude"},{"harness":"codex"}]}],`, 1)
	pol = strings.Replace(pol, `"binary": "",`, `"binary": "`+stub+`",`, 1)
	mustWrite(t, filepath.Join(ws, "cox", "policy.json"), pol)
	epic := filepath.Join(ws, "proj", "epics", "e1")
	mustWrite(t, filepath.Join(epic, "stories", "s.md"), storyFrontmatter)
	return epic
}

// cox route --story prints every candidate with its gate results (not only the choice) and the chosen harness; the
// rule's ranked pick is codex (spendPriority 0.7 > 0.2).
func TestRouteStoryPrintsEveryCandidate(t *testing.T) {
	epic := ruledWorkspace(t, "---\nid: s\nharness: auto\nroute: rule=1\nkind: ship\n---\nbody\n")
	var rc int
	out := captureStdout(t, func() { rc = cmdRoute([]string{"--story", "s", "--epic", epic}) })
	if rc != 0 {
		t.Fatalf("route --story exit = %d", rc)
	}
	if !strings.Contains(out, "harness=codex") {
		t.Fatalf("rule should rank codex highest:\n%s", out)
	}
	if !strings.Contains(out, "candidate: claude:") || !strings.Contains(out, "candidate: codex:") {
		t.Fatalf("every candidate must be printed with gate results:\n%s", out)
	}
	if !strings.Contains(out, "rule=rule=1") {
		t.Fatalf("the matched rule must be named:\n%s", out)
	}
}

// cox route --candidates prints the first quota-eligible candidate in order; a list with no eligible candidate prints
// none and exits 1.
func TestRouteCandidatesFirstEligible(t *testing.T) {
	epic := ruledWorkspace(t, "---\nid: s\nharness: auto\n---\nbody\n")
	var rc int
	out := captureStdout(t, func() { rc = cmdRoute([]string{"--candidates", "claude:opus,codex:gpt-5.6-sol", "--epic", epic}) })
	if rc != 0 {
		t.Fatalf("first-eligible candidates exit = %d, want 0", rc)
	}
	if strings.TrimSpace(out) != "claude opus" {
		t.Fatalf("first eligible should be claude opus, got %q", out)
	}
	// A harness with no quota row is not eligible; with only such candidates the result is none, exit 1.
	out2 := captureStdout(t, func() { rc = cmdRoute([]string{"--candidates", "opencode:x", "--epic", epic}) })
	if rc != 1 || strings.TrimSpace(out2) != "none" {
		t.Fatalf("no eligible candidate must print none and exit 1, got %q exit %d", out2, rc)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
