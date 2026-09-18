package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// epicWithRepo builds an epic dir whose "cox" alias points at a git repo containing a passing go module, and returns the
// epic dir and the repo HEAD sha. Claims cite that sha so verify can build a detached worktree from it.
func epicWithRepo(t *testing.T) (epicDir, sha string) {
	t.Helper()
	repo := t.TempDir()
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/t\n\ngo 1.21\n")
	write("internal/a.go", "package a\n\n// needle marks a line grep can find at the repo root\nvar needle = 1\n")
	write("foo.go", "package t\n\nfunc Add(a, b int) int { return a + b }\n")
	write("foo_test.go", "package t\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")

	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
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

func writeReport(t *testing.T, epicDir, name, body string) {
	t.Helper()
	dir := filepath.Join(epicDir, "reports", "arena")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRun(t *testing.T) {
	epicDir, sha := epicWithRepo(t)
	ev := "cox/foo.go:1@" + sha
	body := "---\nrecommendation: x\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n|---|---|---|---|---|---|---|\n" +
		"| the module builds and tests pass | " + ev + " | 2 | epic-blocking | 90 | go test ./... | keep |\n" +
		"| a string that is not there | " + ev + " | 3 | significant | 60 | grep zzz-absent foo.go | fix |\n" +
		"| reaches the network | " + ev + " | 3 | significant | 50 | curl http://example.com | drop |\n"
	writeReport(t, epicDir, "round-1-adversary.md", body)

	r, err := Run(epicDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Results) != 3 {
		t.Fatalf("want 3 results, got %d: %+v", len(r.Results), r.Results)
	}
	got := map[string]string{}
	for _, res := range r.Results {
		got[res.Status] = res.Check
	}
	if r.Results[0].Status != Pass {
		t.Errorf("go test check: status = %q (%s), want pass", r.Results[0].Status, r.Results[0].Detail)
	}
	if r.Results[1].Status != Fail {
		t.Errorf("grep check: status = %q (%s), want fail", r.Results[1].Status, r.Results[1].Detail)
	}
	if r.Results[2].Status != Unknown || !strings.Contains(r.Results[2].Detail, "allowlist") {
		t.Errorf("curl check: status = %q (%s), want unknown/allowlist", r.Results[2].Status, r.Results[2].Detail)
	}

	// The JSON file is written.
	if _, err := os.Stat(filepath.Join(epicDir, "reports", "arena", "verify-round-1.json")); err != nil {
		t.Errorf("verify-round-1.json not written: %v", err)
	}
}

// An assertion check compares the cited line at its sha to the expected text.
func TestVerifyAssertion(t *testing.T) {
	epicDir, sha := epicWithRepo(t)
	pass := "cox/go.mod:1@" + sha + ` == "module example.com/t"`
	fail := "cox/go.mod:1@" + sha + ` == "module wrong"`
	body := "| claim | evidence | tier | severity | confidence | check | proposal |\n|---|---|---|---|---|---|---|\n" +
		"| module path is right | cox/go.mod:1@" + sha + " | 3 | significant | 70 | " + pass + " | keep |\n" +
		"| module path is wrong | cox/go.mod:1@" + sha + " | 3 | significant | 70 | " + fail + " | keep |\n"
	writeReport(t, epicDir, "round-1-adversary.md", body)

	r, err := Run(epicDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(r.Results))
	}
	if r.Results[0].Status != Pass {
		t.Errorf("assertion pass: %q (%s)", r.Results[0].Status, r.Results[0].Detail)
	}
	if r.Results[1].Status != Fail {
		t.Errorf("assertion fail: %q (%s)", r.Results[1].Status, r.Results[1].Detail)
	}
}

// A command check whose path carries the claim's own `<alias>/` prefix runs at the repo root: verify strips the prefix
// so `grep -n needle cox/internal/a.go` finds the line (first live v3 run, 2026-09-16). Without the strip the path would
// be cox/internal/a.go inside a worktree that has no cox/ dir, and grep would fail.
func TestVerifyStripsAliasPrefix(t *testing.T) {
	epicDir, sha := epicWithRepo(t)
	ev := "cox/internal/a.go:4@" + sha
	body := "---\nrecommendation: x\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n|---|---|---|---|---|---|---|\n" +
		"| the needle is present | " + ev + " | 3 | significant | 70 | grep -n needle cox/internal/a.go | keep |\n"
	writeReport(t, epicDir, "round-1-adversary.md", body)

	r, err := Run(epicDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(r.Results))
	}
	if r.Results[0].Status != Pass {
		t.Fatalf("alias-prefixed check: status = %q (%s), want pass", r.Results[0].Status, r.Results[0].Detail)
	}
}

func TestShellFields(t *testing.T) {
	got, err := shellFields(`grep -n "foo bar" file.go`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grep", "-n", "foo bar", "file.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("shellFields = %v, want %v", got, want)
	}
	// A `;` is a literal argument, not a command separator (no shell).
	got, _ = shellFields("go test ; rm -rf /")
	if got[0] != "go" || got[2] != ";" {
		t.Errorf("shellFields should keep ; as a literal arg, got %v", got)
	}
	// allowed() gates on the first tokens, so the `;` form is refused (go's subcommand is "test", but the trailing
	// literal args are harmless; the point is no shell runs them).
	if !allowed(got) {
		t.Errorf("go test ... should be allowed, got %v", got)
	}
}
