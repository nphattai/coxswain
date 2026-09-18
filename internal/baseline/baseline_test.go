package baseline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// LeakCheck refuses a before sha that already contains the solution branch, and allows an earlier one.
func TestLeakCheck(t *testing.T) {
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a"), []byte("1"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-qm", "base before solution")
	beforeSHA := gitT(t, repo, "rev-parse", "HEAD")

	// The solution lands on story/feat on top of before.
	gitT(t, repo, "switch", "-qc", "story/feat")
	os.WriteFile(filepath.Join(repo, "b"), []byte("2"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-qm", "the solution")
	solvedSHA := gitT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "switch", "-q", "main")

	// before = pre-solution: no leak.
	leak, _, err := LeakCheck(repo, "feat", beforeSHA)
	if err != nil {
		t.Fatal(err)
	}
	if leak {
		t.Fatal("pre-solution before must not be a leak")
	}
	// before = the solved sha: the solution is reachable -> leak.
	leak, detail, err := LeakCheck(repo, "feat", solvedSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !leak || !strings.Contains(detail, "story/feat") {
		t.Fatalf("solved before must leak, got leak=%v detail=%q", leak, detail)
	}
	// No solution branch at all: not a leak (the normal pre-solution case).
	if leak, _, err := LeakCheck(repo, "never-started", beforeSHA); err != nil || leak {
		t.Fatalf("missing solution branch must not leak, got leak=%v err=%v", leak, err)
	}
	// An unresolvable before sha is an error, not a silent pass.
	if _, _, err := LeakCheck(repo, "feat", "deadbeef"); err == nil {
		t.Fatal("an unresolvable before sha should error")
	}
}

// Record creates the dated file with a header on first write and appends one row per run.
func TestRecord(t *testing.T) {
	dir := t.TempDir()
	path, err := Record(dir, "2026-09-15", Row{Story: "m4", SHA: "abcdef0123456789", Harness: "claude", Condition: "bare", Result: "dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "claude-2026-09-15.md" {
		t.Fatalf("path = %s", path)
	}
	if _, err := Record(dir, "2026-09-15", Row{Story: "m4", SHA: "abcdef0123456789", Harness: "claude", Condition: "v2", Result: "unknown"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	for _, want := range []string{"n=1", "| story |", "| m4 | abcdef012345 | claude | bare | dry-run | - |", "| m4 | abcdef012345 | claude | v2 | unknown | - |"} {
		if !strings.Contains(got, want) {
			t.Errorf("baseline file missing %q:\n%s", want, got)
		}
	}
}
