package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A pi baseline run validates the model before any spawn (or backend): a provider-less/empty pi model is refused with a
// bounded diagnostic, not recorded as an unknown run. On the old code baseline never called piPreSpawnValidate.
func TestBaselineRefusesBadPiModel(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	epic := t.TempDir()
	// A story with a bare (provider-less) pi model in frontmatter; no policy default exists for pi.
	dir := filepath.Join(epic, "stories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.md"), []byte("---\nid: s1\nmodel: opus\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := cmdBaseline([]string{"run", "--story", "s1", "--epic", epic, "--harness", "pi", "--condition", "bare", "--before", "HEAD"})
	if rc != 1 {
		t.Fatalf("baseline run with a provider-less pi model rc=%d, want 1 (pre-spawn validation)", rc)
	}
}
