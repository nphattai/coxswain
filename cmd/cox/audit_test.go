package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs f with os.Stdout redirected to a pipe and returns what it wrote.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// Item 9 (B-05): cox audit pr for a scout skips the forge entirely (no gh call, so no `gh pr view` error) and prints
// kind=scout report=<path|missing>. Base-behavior probe: on the base sha audit always probes the forge, so a scout with
// no PR reports a gh error / unknown rather than a scout line.
func TestAuditScoutSkipsForge(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, "stories"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, "stories", "sc.md"), []byte("---\nid: sc\nrepo: app\nkind: scout\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var rc int
	out := captureStdout(t, func() { rc = cmdAudit([]string{"pr", "sc", "--epic", epic}) })
	if rc != 0 {
		t.Fatalf("scout audit rc=%d, want 0 (forge skipped)", rc)
	}
	if !strings.Contains(out, "kind=scout") || !strings.Contains(out, "report=missing") {
		t.Errorf("scout audit line = %q", out)
	}

	// With the report present, it names the path.
	if err := os.MkdirAll(filepath.Join(epic, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, "reports", "sc.md"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { rc = cmdAudit([]string{"pr", "sc", "--epic", epic}) })
	if rc != 0 || !strings.Contains(out, "report="+filepath.Join(epic, "reports", "sc.md")) {
		t.Errorf("scout audit with report: rc=%d out=%q", rc, out)
	}
}
