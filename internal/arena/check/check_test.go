package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// epicRepo makes an epic dir whose "cox" alias points at a git repo with internal/foo.go (3 lines), returning the epic
// dir and the HEAD sha.
func epicRepo(t *testing.T) (epicDir, sha string) {
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
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(string(out))

	epicDir = t.TempDir()
	if err := os.Symlink(repo, filepath.Join(epicDir, "cox")); err != nil {
		t.Fatal(err)
	}
	return epicDir, sha
}

func writeReport(t *testing.T, epicDir, name, body string) string {
	t.Helper()
	p := filepath.Join(epicDir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReportClean(t *testing.T) {
	epicDir, sha := epicRepo(t)
	body := "# adversary\n\n| claim | evidence | severity | proposal |\n|---|---|---|---|\n" +
		"| drops a used column | cox/internal/foo.go:2@" + sha + " | epic-blocking | keep it |\n" +
		"| minor nit | cox/internal/foo.go:1@" + sha + "; cox/internal/foo.go:3@" + sha + " | minor | tidy |\n"
	report := writeReport(t, epicDir, "round-1-adversary.md", body)

	counts, errs, _, err := Report(epicDir, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("clean report should have no errors, got: %v", errs)
	}
	if counts["epic-blocking"] != 1 || counts["minor"] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestReportBadCitations(t *testing.T) {
	epicDir, sha := epicRepo(t)
	body := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n" +
		"| wrong file | cox/internal/missing.go:1@" + sha + " | minor | x |\n" +
		"| wrong line | cox/internal/foo.go:99@" + sha + " | minor | x |\n" +
		"| wrong sha | cox/internal/foo.go:1@deadbeef | minor | x |\n" +
		"| bad severity | cox/internal/foo.go:1@" + sha + " | huge | x |\n" +
		"| unpinned | cox/internal/foo.go:1 | minor | x |\n"
	report := writeReport(t, epicDir, "round-1-adversary.md", body)

	_, errs, _, err := Report(epicDir, report)
	if err != nil {
		t.Fatal(err)
	}
	// One error per bad row (five rows, five distinct failures).
	if len(errs) != 5 {
		t.Fatalf("want 5 errors, got %d: %v", len(errs), errs)
	}
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"missing.go", "line 99", "deadbeef", "invalid severity", "not pinned"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors missing %q:\n%s", want, joined)
		}
	}
	// Each error names its source line (rows are on file lines 3-7).
	for _, want := range []string{"line 3", "line 4", "line 5", "line 6", "line 7"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors missing source %q:\n%s", want, joined)
		}
	}
}

// A v3 report with tier 2-3 and a check passes; the tier/confidence/check rules do not fire on valid rows.
func TestReportV3Clean(t *testing.T) {
	epicDir, sha := epicRepo(t)
	body := "---\nrecommendation: adopt\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n|---|---|---|---|---|---|---|\n" +
		"| a real bug | cox/internal/foo.go:2@" + sha + " | 2 | epic-blocking | 80 | go test ./... | fix |\n" +
		"| a nit | cox/internal/foo.go:1@" + sha + " | 3 | minor | 40 | | tidy |\n"
	report := writeReport(t, epicDir, "round-1-adversary.md", body)
	counts, errs, warnings, err := Report(epicDir, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("clean v3 report should have no errors, got: %v", errs)
	}
	if len(warnings) != 0 {
		t.Fatalf("v3 report should have no warnings, got: %v", warnings)
	}
	if counts["epic-blocking"] != 1 || counts["minor"] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

// A v3 report with a tier-5 epic-blocking claim, an out-of-range tier, an out-of-range confidence, and an epic-blocking
// claim missing its check is rejected, one error per violation.
func TestReportV3Violations(t *testing.T) {
	epicDir, sha := epicRepo(t)
	body := "---\nrecommendation: x\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n|---|---|---|---|---|---|---|\n" +
		"| inference as blocker | cox/internal/foo.go:1@" + sha + " | 5 | epic-blocking | 90 | go test ./... | x |\n" +
		"| bad tier | cox/internal/foo.go:1@" + sha + " | 9 | minor | 50 | | x |\n" +
		"| bad confidence | cox/internal/foo.go:1@" + sha + " | 3 | minor | 150 | | x |\n" +
		"| no check | cox/internal/foo.go:1@" + sha + " | 2 | significant | 60 | | x |\n"
	report := writeReport(t, epicDir, "round-1-adversary.md", body)
	_, errs, _, err := Report(epicDir, report)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"tier-5", "invalid tier \"9\"", "invalid confidence \"150\"", "significant claim needs a check"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors missing %q:\n%s", want, joined)
		}
	}
}

// A citation missing its `<alias>/` prefix gets a format hint naming the known aliases (not the raw "unknown repo
// alias"), and a command check carrying an alias prefix warns (not fails), since verify strips it at the repo root.
func TestReportFormatHints(t *testing.T) {
	epicDir, sha := epicRepo(t)
	repo, _ := os.Readlink(filepath.Join(epicDir, "cox"))
	// A repos file makes cox and lav the known aliases; the hint lists them.
	if err := os.WriteFile(filepath.Join(epicDir, "repos"), []byte("cox "+repo+"\nlav "+repo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "---\nrecommendation: x\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n|---|---|---|---|---|---|---|\n" +
		"| missing alias | internal/foo.go:2@" + sha + " | 3 | significant | 70 | grep -n one cox/internal/foo.go | fix |\n"
	report := writeReport(t, epicDir, "round-1-adversary.md", body)
	_, errs, warnings, err := Report(epicDir, report)
	if err != nil {
		t.Fatal(err)
	}
	joinedErr := strings.Join(errs, "\n")
	if !strings.Contains(joinedErr, "needs `<alias>/`") || !strings.Contains(joinedErr, "cox, lav") {
		t.Errorf("want an alias-format hint naming known aliases, got: %v", errs)
	}
	joinedWarn := strings.Join(warnings, "\n")
	if !strings.Contains(joinedWarn, "repo root") || !strings.Contains(joinedWarn, "alias prefix stripped") {
		t.Errorf("want a check-prefix warning, got: %v", warnings)
	}
}

// A v2 report (no frontmatter, 4 columns) still passes with a "v2 report" warning; the v3 rules do not fire on it.
func TestReportV2Warning(t *testing.T) {
	epicDir, sha := epicRepo(t)
	body := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n" +
		"| an old blocker | cox/internal/foo.go:2@" + sha + " | epic-blocking | fix |\n"
	report := writeReport(t, epicDir, "round-1-adversary.md", body)
	_, errs, warnings, err := Report(epicDir, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("v2 report should pass (no v3 rules), got errors: %v", errs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "v2 report") {
		t.Fatalf("want a single v2-report warning, got: %v", warnings)
	}
}
