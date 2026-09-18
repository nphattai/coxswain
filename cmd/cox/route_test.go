package main

import (
	"os"
	"path/filepath"
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

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
