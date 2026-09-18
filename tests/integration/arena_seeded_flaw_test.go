package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	arenacheck "github.com/nphattai/coxswain/internal/arena/check"
	"github.com/nphattai/coxswain/internal/arena/synth"
	"github.com/nphattai/coxswain/internal/epic"
)

// The seeded-flaw fixture: an adversary report where claim (a) correctly catches the dropped-column flaw and claim (b)
// cites a real line but reasons wrongly. Both citations are real, so `arena check` passes both - machine-checking proves
// the citation exists, not that the reasoning is right. The synthesis then leaves every verdict blank for the leader to
// accept (a) and reject (b), and `design --sign` refuses while any verdict is blank.
func TestArenaSeededFlawChecksCleanAndBlocksSignUntilAdjudicated(t *testing.T) {
	epicDir := t.TempDir()

	// A real repo with the two cited files, committed so the citations pin to a real sha.
	repo := filepath.Join(t.TempDir(), "billing-repo")
	write(t, filepath.Join(repo, "app", "billing.go"),
		"package billing\n\n// Price prices an invoice from the account tier.\nfunc Price(tier string) int { // reads accounts.tier\n\treturn len(tier)\n}\n")
	write(t, filepath.Join(repo, "db", "migrate.sql"),
		"CREATE TABLE tiers (id INT PRIMARY KEY, name TEXT);\nALTER TABLE accounts DROP COLUMN tier;\n")
	sha := commitRepo(t, repo)

	// The epic's alias symlink cite.RepoDir resolves: <epic>/billing -> the repo.
	if err := os.Symlink(repo, filepath.Join(epicDir, "billing")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(epicDir, "repos"), "billing billing-repo\n")

	// The adversary report: (a) true, epic-blocking; (b) real citation, wrong inference, significant.
	report := "# Arena adversary - round 1\n\n" +
		"| claim | evidence | severity | proposal |\n" +
		"|---|---|---|---|\n" +
		"| Dropping accounts.tier breaks billing which still reads it | billing/app/billing.go:4@" + sha + " | epic-blocking | keep the column until billing reads tier_id |\n" +
		"| Backfill loses data with no unique index on tiers.name | billing/db/migrate.sql:2@" + sha + " | significant | add a unique index |\n"
	reportPath := filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md")
	write(t, reportPath, report)

	// check: both citations resolve at the sha; nothing broken.
	counts, errs, _, err := arenacheck.Report(epicDir, reportPath)
	if err != nil {
		t.Fatalf("check.Report: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("check should pass both real citations, got: %v", errs)
	}
	if counts["epic-blocking"] != 1 || counts["significant"] != 1 {
		t.Fatalf("counts = %v, want one epic-blocking and one significant", counts)
	}

	// synth: both claims land in the synthesis with blank verdicts for the leader to adjudicate.
	synthPath, err := synth.Build(epicDir, 1, false)
	if err != nil {
		t.Fatalf("synth.Build: %v", err)
	}
	sb, _ := os.ReadFile(synthPath)
	if n := strings.Count(string(sb), "billing/"); n != 2 {
		t.Fatalf("synthesis should carry both claims (2 citations), got %d", n)
	}

	// sign refuses while verdicts are blank - the arena is not a ritual, adjudication is required before a signature.
	if err := epic.Sign(epicDir, ""); err == nil {
		t.Fatal("Sign must refuse a synthesis with blank verdicts")
	} else if !strings.Contains(err.Error(), "verdict") {
		t.Fatalf("Sign error should name the blank verdicts, got: %v", err)
	}
}

// A citation to a line past the end of the file is rejected by check, naming the report line.
func TestArenaCheckRejectsOutOfRangeCitation(t *testing.T) {
	epicDir := t.TempDir()
	repo := filepath.Join(t.TempDir(), "r")
	write(t, filepath.Join(repo, "f.go"), "package r\n") // one line
	sha := commitRepo(t, repo)
	if err := os.Symlink(repo, filepath.Join(epicDir, "r")); err != nil {
		t.Fatal(err)
	}
	report := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n" +
		"| bogus | r/f.go:99@" + sha + " | minor | x |\n"
	reportPath := filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md")
	write(t, reportPath, report)
	_, errs, _, err := arenacheck.Report(epicDir, reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), "out of range") {
		t.Fatalf("want an out-of-range error, got: %v", errs)
	}
}

// commitRepo git-inits repo, commits everything, and returns the HEAD sha.
func commitRepo(t *testing.T, repo string) string {
	t.Helper()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "test")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "seed")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
