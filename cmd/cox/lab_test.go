package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nphattai/coxswain/internal/state"
)

// labWorkspace builds a minimal workspace (workspace.json + template policy.json) with one epic and returns the epic dir.
func labWorkspace(t *testing.T) (ws, epic string) {
	t.Helper()
	ws = t.TempDir()
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"schema":"coxswain.workspace.v1"}`)
	tpl, err := os.ReadFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ws, "cox", "policy.json"), string(tpl))
	epic = filepath.Join(ws, "epics", "e1")
	mustWrite(t, filepath.Join(epic, "stories", "s1.md"), "---\nid: s1\n---\nbody\n")
	appendWorking(t, epic, "s1")
	return ws, epic
}

// lab new + assign records the variant in the experiment file and as evidence.lab on the epic event log.
func TestLabAssignWritesEvidence(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	_, epic := labWorkspace(t)

	if rc := labNew([]string{"speed", "--rule", "arena.trigger", "--metric", "cost_usd", "--epic", epic}); rc != 0 {
		t.Fatalf("lab new rc=%d", rc)
	}
	if rc := labAssign([]string{"speed", "--story", "s1", "--epic", epic}); rc != 0 {
		t.Fatalf("lab assign rc=%d", rc)
	}
	events, _, _ := state.Load(epic)
	last := events[len(events)-1]
	labEv, ok := last.Evidence["lab"].(map[string]any)
	if !ok {
		t.Fatalf("last event has no evidence.lab: %+v", last)
	}
	if labEv["name"] != "speed" || labEv["variant"] != "on" || labEv["rule"] != "arena.trigger" {
		t.Fatalf("evidence.lab wrong: %+v", labEv)
	}
}

// lab retire writes a draft proposal and never touches policy.json.
func TestLabRetireLeavesPolicyUnchanged(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	ws, epic := labWorkspace(t)
	if rc := labNew([]string{"speed", "--rule", "arena.trigger", "--metric", "cost_usd", "--epic", epic}); rc != 0 {
		t.Fatalf("lab new rc=%d", rc)
	}
	policyPath := filepath.Join(ws, "cox", "policy.json")
	before, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}

	if rc := labRetire([]string{"speed", "--epic", epic, "--no-forge"}); rc != 0 {
		t.Fatalf("lab retire rc=%d", rc)
	}
	draft := filepath.Join(ws, "docs", "decisions", "draft-lab-speed.md")
	if _, err := os.Stat(draft); err != nil {
		t.Fatalf("draft not written: %v", err)
	}
	after, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("lab retire must not change policy.json")
	}
}
