package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The leader wake hooks no longer bind to an epic via COX_EPIC (DESIGN §3): they walk up to the workspace and act on
// every active epic. The grep is scoped to cmd/cox/workspace.go, hooks/, templates/, and the leader docs. cmd/cox/hook.go
// is intentionally excluded: its precompact/session-start path keeps the COX_EPIC/COX_STORY worker-plane env fallback so
// a terminal-plane worker's checkpoint hooks still resolve their epic (captain ruling, inbox 001 + review round 2).
func TestNoCOXEPICInLeaderHookPath(t *testing.T) {
	var files []string
	files = append(files,
		"workspace.go",
		"../../docs/adapters/claude.md",
		"../../docs/adapters/codex.md",
		"../../docs/reference/cli.md",
	)
	for _, dir := range []string{"../../hooks", "../../templates"} {
		_ = fs.WalkDir(os.DirFS(dir), ".", func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, filepath.Join(dir, p))
			}
			return nil
		})
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if bytes.Contains(b, []byte("COX_EPIC")) {
			t.Errorf("%s still references COX_EPIC; the leader hook path must be epic-less", f)
		}
	}
}
