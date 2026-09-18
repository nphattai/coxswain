package arena

import (
	"os"
	"path/filepath"
	"testing"
)

// Collect copies a report a role wrote in its worktree, never clobbers one already in the epic dir, and skips a role
// with no recorded worktree.
func TestCollect(t *testing.T) {
	epic := t.TempDir()
	mustDir(t, filepath.Join(epic, ".cox", "wt"))
	mustDir(t, filepath.Join(epic, "reports", "arena"))

	// adversary: report lives only in its worktree -> collected.
	advWt := t.TempDir()
	mustDir(t, filepath.Join(advWt, "reports", "arena"))
	writeFile(t, filepath.Join(advWt, "reports", "arena", "round-1-adversary.md"), "adv report")
	writeFile(t, filepath.Join(epic, ".cox", "wt", "arena-adversary"), advWt)

	// reviewer: report already in the epic dir -> not clobbered even though the worktree has a different one.
	revWt := t.TempDir()
	mustDir(t, filepath.Join(revWt, "reports", "arena"))
	writeFile(t, filepath.Join(revWt, "reports", "arena", "round-1-reviewer.md"), "worktree version")
	writeFile(t, filepath.Join(epic, "reports", "arena", "round-1-reviewer.md"), "epic version")
	writeFile(t, filepath.Join(epic, ".cox", "wt", "arena-reviewer"), revWt)

	// domain: no worktree recorded -> skipped.

	copied, err := Collect(epic, 1)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(copied) != 1 {
		t.Fatalf("copied %v, want exactly the adversary report", copied)
	}
	if got := readFile(t, filepath.Join(epic, "reports", "arena", "round-1-adversary.md")); got != "adv report" {
		t.Errorf("adversary report = %q, want copied from worktree", got)
	}
	if got := readFile(t, filepath.Join(epic, "reports", "arena", "round-1-reviewer.md")); got != "epic version" {
		t.Errorf("reviewer report = %q, want the epic version untouched", got)
	}
}

func mustDir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
