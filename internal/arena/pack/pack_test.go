package pack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildEpic makes an epic dir whose "cox" alias points at a one-file git repo, with a DESIGN.md and one scout report.
// It returns the epic dir and the in-repo file's line count so tests can cite a good and a bad line.
func buildEpic(t *testing.T, design, scout string) (epicDir string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "foo.go"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")

	// Epic dir: <ws>/<project>/epics/<slug>.
	ws := t.TempDir()
	epicDir = filepath.Join(ws, "proj", "epics", "e1")
	for _, sub := range []string{"reports/scout"} {
		if err := os.MkdirAll(filepath.Join(epicDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(repo, filepath.Join(epicDir, "cox")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epicDir, "repos"), []byte("cox "+repo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epicDir, "DESIGN.md"), []byte(design), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epicDir, "reports", "scout", "cox.md"), []byte(scout), 0o644); err != nil {
		t.Fatal(err)
	}
	return epicDir
}

func TestBuildBlindsAndStampsSha(t *testing.T) {
	design := `# e1 design

## Discussion
captain: ship it fast
leader: I used claude to draft this, see commit abadcafe1234567

## Contract
The store reads cox/internal/foo.go:2 for the value.
`
	scout := "cox scout\nendpoint at cox/internal/foo.go:1\n"
	epicDir := buildEpic(t, design, scout)

	out, err := Build(epicDir, filepath.Dir(filepath.Dir(filepath.Dir(epicDir))), 1)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	// Blinded: discussion section, attribution lines, model name and commit link are gone.
	for _, banned := range []string{"## Discussion", "captain:", "leader:", "claude", "abadcafe1234567"} {
		if strings.Contains(got, banned) {
			t.Errorf("pack still contains %q:\n%s", banned, got)
		}
	}
	// Kept: the citation (needed) and a source content-sha header labelled so it is not mistaken for a git sha.
	if !strings.Contains(got, "cox/internal/foo.go:2") {
		t.Errorf("pack dropped a citation:\n%s", got)
	}
	if !strings.Contains(got, "DESIGN.md content:") {
		t.Errorf("pack has no per-source content sha header:\n%s", got)
	}
	// The pack prints each repo's git HEAD so a role cites a real commit, not the content hash.
	head := gitHead(t, filepath.Join(epicDir, "cox"))
	if !strings.Contains(got, "cox git HEAD "+head) {
		t.Errorf("pack missing repo git HEAD %s:\n%s", head, got)
	}
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestBuildRejectsBrokenCitation(t *testing.T) {
	design := "# e1\n\nreads cox/internal/foo.go:99 (past EOF)\n"
	epicDir := buildEpic(t, design, "cox scout\n")

	_, err := Build(epicDir, "", 1)
	if err == nil {
		t.Fatal("expected broken-citation error, got nil")
	}
	if !strings.Contains(err.Error(), "foo.go:99") {
		t.Errorf("error should name the broken citation, got: %v", err)
	}
	// No pack written when a citation is broken.
	if _, statErr := os.Stat(filepath.Join(epicDir, "reports", "arena", "context-pack.md")); !os.IsNotExist(statErr) {
		t.Errorf("pack should not exist after a broken citation")
	}
}

func TestBuildRound2Opposition(t *testing.T) {
	epicDir := buildEpic(t, "# e1\n\nreads cox/internal/foo.go:1\n", "cox scout\n")
	// Seed a prior-round synthesis with one verified claim and a provisional verdict.
	synth := "| role | claim | evidence | tier | severity | verified | verdict |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| adversary | the lock is dropped early | cox/internal/foo.go:1@abc1234 | 2 | epic-blocking | pass | unresolved |\n"
	if err := os.WriteFile(filepath.Join(epicDir, "reports", "arena", "synthesis-round-1.md"), []byte(synth), 0o644); err != nil {
		// reports/arena may not exist yet
		if err := os.MkdirAll(filepath.Join(epicDir, "reports", "arena"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(epicDir, "reports", "arena", "synthesis-round-1.md"), []byte(synth), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := Build(epicDir, "", 2)
	if err != nil {
		t.Fatalf("Build round 2: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"## Round 1 claims (verified)", "## Answer these", "the lock is dropped early", "which evidence would change the conclusion"} {
		if !strings.Contains(got, want) {
			t.Errorf("round-2 pack missing %q:\n%s", want, got)
		}
	}
}

func TestBuildRound2NeedsPriorSynthesis(t *testing.T) {
	epicDir := buildEpic(t, "# e1\n\nreads cox/internal/foo.go:1\n", "cox scout\n")
	_, err := Build(epicDir, "", 2)
	if err == nil || !strings.Contains(err.Error(), "synthesis-round-1") {
		t.Fatalf("round 2 without a prior synthesis should error naming it, got: %v", err)
	}
}
