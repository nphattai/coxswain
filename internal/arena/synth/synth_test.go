package synth

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	if err := os.WriteFile(filepath.Join(repo, "foo.go"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")
	out, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	sha = strings.TrimSpace(string(out))

	epicDir = filepath.Join(t.TempDir(), "e1")
	if err := os.MkdirAll(filepath.Join(epicDir, "reports", "arena"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, filepath.Join(epicDir, "cox")); err != nil {
		t.Fatal(err)
	}
	return epicDir, sha
}

func TestBuildGathersClaims(t *testing.T) {
	epicDir, sha := epicRepo(t)
	adv := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n| flaw | cox/foo.go:2@" + sha + " | epic-blocking | fix |\n"
	rev := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n| gap | cox/foo.go:1@" + sha + " | minor | note |\n"
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"), []byte(adv), 0o644)
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "round-1-reviewer.md"), []byte(rev), 0o644)

	out, err := Build(epicDir, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	got := string(b)
	for _, want := range []string{"| adversary | flaw |", "| reviewer | gap |", "verdict", "Round 2 needed?"} {
		if !strings.Contains(got, want) {
			t.Errorf("synthesis missing %q:\n%s", want, got)
		}
	}
	// F4: no seeded round2 frontmatter field (it could go stale and lie); the check computes it live instead.
	if strings.Contains(got, "round2:") {
		t.Errorf("synthesis must not carry a round2 field:\n%s", got)
	}
}

func TestRound2(t *testing.T) {
	header := "| role | claim | evidence | severity | verdict | reason | change | agrees |\n|---|---|---|---|---|---|---|---|\n"
	// An epic-blocking claim left unresolved opens round 2.
	need, reasons := Round2(header + "| adversary | boom | e | epic-blocking | unresolved | r | c | |\n")
	if !need || len(reasons) != 1 || !strings.Contains(reasons[0], "adversary: boom") {
		t.Fatalf("unresolved epic-blocking should need round 2: need=%v reasons=%v", need, reasons)
	}
	// Accepted epic-blocking, or an unresolved minor claim, do not.
	if need, _ := Round2(header + "| adversary | boom | e | epic-blocking | accepted | r | c | |\n"); need {
		t.Error("accepted epic-blocking should not need round 2")
	}
	if need, _ := Round2(header + "| reviewer | nit | e | minor | unresolved | r | c | |\n"); need {
		t.Error("unresolved minor should not need round 2")
	}
	// A blank verdict is "not adjudicated", handled by the sign blank-verdict gate, not round 2.
	if need, _ := Round2(header + "| adversary | boom | e | epic-blocking |  | r | c | |\n"); need {
		t.Error("blank verdict should not need round 2")
	}
}

func TestBuildRefusesOverwriteOfAdjudication(t *testing.T) {
	epicDir, sha := epicRepo(t)
	adv := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n| flaw | cox/foo.go:2@" + sha + " | epic-blocking | fix |\n"
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"), []byte(adv), 0o644)
	out := filepath.Join(epicDir, "reports", "arena", "synthesis.md")

	// A synthesis the leader has already adjudicated (a verdict filled).
	adjudicated := "| role | claim | evidence | severity | verdict | reason | DESIGN.md change | captain agrees |\n" +
		"|---|---|---|---|---|---|---|---|\n" +
		"| adversary | flaw | cox/foo.go:2@" + sha + " | epic-blocking | accepted | real | guard | |\n"
	os.WriteFile(out, []byte(adjudicated), 0o644)

	if _, err := Build(epicDir, 1, false); err == nil || !strings.Contains(err.Error(), "adjudication") {
		t.Fatalf("rebuild should refuse over adjudication, got %v", err)
	}
	if _, err := Build(epicDir, 1, true); err != nil {
		t.Fatalf("--force should overwrite: %v", err)
	}
	// HasAdjudication is false for a freshly synthesized (blank) table.
	if b, _ := os.ReadFile(out); HasAdjudication(string(b)) {
		t.Error("a force-rebuilt blank synthesis should not read as adjudicated")
	}
}

// Per-round synthesis: round 2 writes its own file and never touches round 1's adjudication; synthesis.md follows the
// latest round; and the --force guard applies per file (A8).
func TestBuildPerRoundKeepsPriorAdjudication(t *testing.T) {
	epicDir, sha := epicRepo(t)
	dir := filepath.Join(epicDir, "reports", "arena")
	report := func(round int, role string) {
		body := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n| c-" + role + " | cox/foo.go:1@" + sha + " | minor | note |\n"
		os.WriteFile(filepath.Join(dir, "round-"+itoa(round)+"-"+role+".md"), []byte(body), 0o644)
	}

	// Round 1 -> synthesis-round-1.md, synthesis.md -> it.
	report(1, "adversary")
	out1, err := Build(epicDir, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(out1) != "synthesis-round-1.md" {
		t.Fatalf("round 1 wrote %q, want synthesis-round-1.md", filepath.Base(out1))
	}
	if got := readlink(t, filepath.Join(dir, "synthesis.md")); got != "synthesis-round-1.md" {
		t.Fatalf("synthesis.md -> %q, want synthesis-round-1.md", got)
	}

	// The leader adjudicates round 1 directly in its file.
	adjudicated := "| role | claim | evidence | severity | verdict | reason | DESIGN.md change | captain agrees |\n" +
		"|---|---|---|---|---|---|---|---|\n" +
		"| adversary | c-adversary | cox/foo.go:1@" + sha + " | minor | accepted | real | none | y |\n"
	os.WriteFile(out1, []byte(adjudicated), 0o644)

	// Round 2 writes its own file; round 1's adjudication is untouched; synthesis.md now follows round 2.
	report(2, "adversary")
	out2, err := Build(epicDir, 2, false)
	if err != nil {
		t.Fatalf("round 2 build should succeed without clobbering round 1: %v", err)
	}
	if filepath.Base(out2) != "synthesis-round-2.md" {
		t.Fatalf("round 2 wrote %q, want synthesis-round-2.md", filepath.Base(out2))
	}
	if b, _ := os.ReadFile(out1); !HasAdjudication(string(b)) {
		t.Fatal("round 1 adjudication was lost when round 2 was synthesized")
	}
	if got := readlink(t, filepath.Join(dir, "synthesis.md")); got != "synthesis-round-2.md" {
		t.Fatalf("synthesis.md -> %q, want synthesis-round-2.md (latest)", got)
	}

	// The --force guard is per file: adjudicating round 2 makes a round-2 rebuild refuse, round 1 stays independent.
	os.WriteFile(out2, []byte(adjudicated), 0o644)
	if _, err := Build(epicDir, 2, false); err == nil || !strings.Contains(err.Error(), "adjudication") {
		t.Fatalf("round 2 rebuild should refuse over its own adjudication, got %v", err)
	}
}

func itoa(n int) string { return string(rune('0' + n)) }

func readlink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("synthesis.md is not a symlink: %v", err)
	}
	return target
}

// synth assigns each claim a stable id (<role>-<round>-<n>) and records the synthesis content sha for the answer guard.
func TestBuildAssignsIdsAndSha(t *testing.T) {
	epicDir, sha := epicRepo(t)
	adv := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n" +
		"| first | cox/foo.go:1@" + sha + " | minor | a |\n| second | cox/foo.go:2@" + sha + " | minor | b |\n"
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"), []byte(adv), 0o644)

	out, err := Build(epicDir, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	got := string(b)
	for _, want := range []string{"| adversary-1-1 | adversary | first |", "| adversary-1-2 | adversary | second |"} {
		if !strings.Contains(got, want) {
			t.Errorf("synthesis missing assigned id row %q:\n%s", want, got)
		}
	}
	if recorded := ReadSha(epicDir, 1); recorded == "" || recorded != ContentSha(b) {
		t.Errorf("recorded sha %q != content sha %q", recorded, ContentSha(b))
	}
}

// cox arena answer fills the captain-agrees cell by id, refuses on a sha mismatch, and refuses an unknown id.
func TestAnswer(t *testing.T) {
	epicDir, sha := epicRepo(t)
	adv := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n| the flaw | cox/foo.go:2@" + sha + " | epic-blocking | fix |\n"
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"), []byte(adv), 0o644)
	if _, err := Build(epicDir, 1, false); err != nil {
		t.Fatal(err)
	}

	round, err := Answer(epicDir, "adversary-1-1", "yes", "tai")
	if err != nil {
		t.Fatalf("answer failed: %v", err)
	}
	if round != 1 {
		t.Errorf("round = %d, want 1", round)
	}
	b, _ := os.ReadFile(filepath.Join(epicDir, "reports", "arena", "synthesis.md"))
	if !strings.Contains(string(b), "yes (by tai)") {
		t.Errorf("captain-agrees cell not written:\n%s", b)
	}
	// A second answer to the same synthesis still matches (the sha was re-recorded).
	if _, err := Answer(epicDir, "adversary-1-1", "no", ""); err != nil {
		t.Fatalf("second answer should match the re-recorded sha: %v", err)
	}

	// An unknown id is refused.
	if _, err := Answer(epicDir, "adversary-1-9", "yes", ""); err == nil {
		t.Fatal("want an error for an unknown id")
	}

	// Tampering with the synthesis after synth trips the sha guard.
	link := filepath.Join(epicDir, "reports", "arena", "synthesis.md")
	target, _ := os.Readlink(link)
	roundFile := filepath.Join(filepath.Dir(link), target)
	cur, _ := os.ReadFile(roundFile)
	os.WriteFile(roundFile, append(cur, []byte("\n<!-- hand edit -->\n")...), 0o644)
	if _, err := Answer(epicDir, "adversary-1-1", "yes", ""); err == nil || !strings.Contains(err.Error(), "sha") {
		t.Fatalf("want a sha-mismatch refusal, got %v", err)
	}
}

func TestBuildRefusesBrokenReport(t *testing.T) {
	epicDir, _ := epicRepo(t)
	bad := "| claim | evidence | severity | proposal |\n|---|---|---|---|\n| flaw | cox/foo.go:99@deadbeef | epic-blocking | fix |\n"
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"), []byte(bad), 0o644)

	if _, err := Build(epicDir, 1, false); err == nil {
		t.Fatal("expected synthesis to refuse a broken report")
	}
	if _, err := os.Stat(filepath.Join(epicDir, "reports", "arena", "synthesis.md")); !os.IsNotExist(err) {
		t.Error("synthesis should not be written when a report is broken")
	}
}

// synth auto-fills the tier column from the v3 report and the verified column from verify-round-N.json.
func TestBuildFillsTierAndVerified(t *testing.T) {
	epicDir, sha := epicRepo(t)
	adv := "---\nrecommendation: x\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n|---|---|---|---|---|---|---|\n" +
		"| the flaw | cox/foo.go:2@" + sha + " | 2 | epic-blocking | 90 | go test ./... | fix |\n"
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"), []byte(adv), 0o644)
	verify := `{"round":1,"results":[{"role":"adversary","claim":"the flaw","check":"go test ./...","status":"pass","detail":"exit 0"}]}`
	os.WriteFile(filepath.Join(epicDir, "reports", "arena", "verify-round-1.json"), []byte(verify), 0o644)

	out, err := Build(epicDir, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	got := string(b)
	// The claim row carries tier 2 and verified pass in the right columns.
	if !strings.Contains(got, "| adversary | the flaw | cox/foo.go:2@"+sha+" | 2 | epic-blocking | pass |") {
		t.Errorf("synthesis row missing auto-filled tier/verified:\n%s", got)
	}
	// The six leader sections are present.
	for _, want := range []string{"## Adopted decision", "## Decisive evidence", "## Rejected alternatives", "## Preserved locked decisions", "## Remaining uncertainty", "## Verification gates"} {
		if !strings.Contains(got, want) {
			t.Errorf("synthesis missing section %q", want)
		}
	}
}
