package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nphattai/coxswain/templates"
)

// TemplateWorkerModel reads the embedded template's per-harness worker default.
func TestTemplateWorkerModel(t *testing.T) {
	if got := TemplateWorkerModel("codex"); got != "gpt-5.6-sol" {
		t.Errorf("codex template model = %q, want gpt-5.6-sol", got)
	}
	if got := TemplateWorkerModel("claude"); got != "claude-opus-5-5" {
		t.Errorf("claude template model = %q, want claude-opus-5-5", got)
	}
	if got := TemplateWorkerModel("pi"); got != "openai-codex/gpt-5.6-sol" {
		t.Errorf("pi template model = %q, want openai-codex/gpt-5.6-sol", got)
	}
	if got := TemplateWorkerModel("nope"); got != "" {
		t.Errorf("unmapped harness must be empty, got %q", got)
	}
}

// PolicyDrift lists keys the template declares but a stale workspace policy lacks (here the whole harness.worker.models
// map), and reports nothing when the workspace policy matches the template.
func TestPolicyDrift(t *testing.T) {
	dir := t.TempDir()
	// A workspace policy whose harness.worker has options/default but no models map (the M10c drift).
	stale := `{
      "harness": { "worker": { "options": ["claude"], "default": "claude" } }
    }`
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	drift, err := PolicyDrift(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range drift {
		if k == "harness.worker.models" {
			found = true
		}
	}
	if !found {
		t.Fatalf("drift must include harness.worker.models, got %v", drift)
	}

	// The template compared against itself has no drift.
	tmpl, err := templates.File("policy.json")
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, "full.json")
	if err := os.WriteFile(full, tmpl, 0o644); err != nil {
		t.Fatal(err)
	}
	if d, err := PolicyDrift(full); err != nil || len(d) != 0 {
		t.Fatalf("template vs itself must have no drift, got %v (err %v)", d, err)
	}
}
