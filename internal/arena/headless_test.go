package arena

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/arena/check"
	"github.com/nphattai/coxswain/internal/workspace"
)

// fakeHarness writes a fake `claude` on PATH that ignores stdin and prints a claude-style JSON object whose result holds
// the report block. It returns nothing; the caller sets PATH via t.Setenv.
func fakeHarness(t *testing.T, sha string) {
	t.Helper()
	bin := t.TempDir()
	report := "```report\n---\nrecommendation: adopt with the guard\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| the lock drops early | cox/foo.go:1@" + sha + " | 2 | epic-blocking | 90 | go test ./... | hold it |\n```"
	b, err := json.Marshal(map[string]string{"result": "Here is my review.\n\n" + report + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "resp.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat >/dev/null\ncat \"$(dirname \"$0\")/resp.json\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// headlessEpic builds an epic at <ws>/proj/epics/e1 whose "cox" alias points at a one-file git repo, and returns the
// workspace root, epic dir, and repo HEAD sha.
func headlessEpic(t *testing.T) (ws, epicDir, sha string) {
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
	os.WriteFile(filepath.Join(repo, "foo.go"), []byte("package t\n"), 0o644)
	git("add", ".")
	git("commit", "-qm", "init")
	shaOut, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	sha = strings.TrimSpace(string(shaOut))

	ws = t.TempDir()
	epicDir = filepath.Join(ws, "proj", "epics", "e1")
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Symlink(repo, filepath.Join(epicDir, "cox"))
	os.WriteFile(filepath.Join(epicDir, "repos"), []byte("cox "+repo+"\n"), 0o644)
	os.WriteFile(filepath.Join(epicDir, "DESIGN.md"), []byte("# e1\n\na small design with one repo\n"), 0o644)
	return ws, epicDir, sha
}

func TestRunHeadless(t *testing.T) {
	ws, epicDir, sha := headlessEpic(t)

	fakeHarness(t, sha)

	pol := &workspace.Policy{}
	pol.Harness.Leader.Options = []string{"claude", "codex"}
	pol.Harness.Arena.Adversary.Rule = "not-leader"
	pol.Harness.Arena.Adversary.Default = "codex"

	// Leader codex => adversary resolves to the not-leader harness claude (our fake). A one-repo design triggers lite,
	// so only the adversary runs.
	res, err := RunHeadless(Options{EpicDir: epicDir, WsRoot: ws, Policy: pol, Leader: "codex", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Headless) != 1 {
		t.Fatalf("want 1 headless role, got %d (%+v)", len(res.Headless), res.Headless)
	}
	r := res.Headless[0]
	if r.Err != nil {
		t.Fatalf("headless adversary failed: %v", r.Err)
	}
	if r.Harness != "claude" {
		t.Fatalf("adversary harness = %q, want claude", r.Harness)
	}
	if r.Sha != sha {
		t.Errorf("recorded sha = %q, want %q", r.Sha, sha)
	}
	// cox wrote the report from the JSON block.
	b, err := os.ReadFile(filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"))
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	if !strings.Contains(string(b), "the lock drops early") {
		t.Errorf("report missing the claim:\n%s", b)
	}
	// The written report passes arena check (v3, valid tier/severity/check, resolvable citation).
	_, errs, _, err := check.Report(epicDir, filepath.Join(epicDir, "reports", "arena", "round-1-adversary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Errorf("headless report should pass arena check, got: %v", errs)
	}
}

// captureHarness writes a fake harness binary of the given name that records its stdin and argv to files under a capture
// dir (returned) and prints a claude-style JSON object carrying the report block. It prepends the binary's dir to PATH.
func captureHarness(t *testing.T, name, sha string) (capDir string) {
	t.Helper()
	bin := t.TempDir()
	capDir = t.TempDir()
	report := "```report\n---\nrecommendation: adopt\n---\n" +
		"| claim | evidence | tier | severity | confidence | check | proposal |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| a real bug | cox/foo.go:1@" + sha + " | 2 | epic-blocking | 90 | go test ./... | fix |\n```"
	b, err := json.Marshal(map[string]string{"result": "review\n\n" + report + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capDir, "resp.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// The script saves stdin and argv into capDir (absolute paths, since the harness runs cwd=leader checkout).
	script := "#!/bin/sh\ncat > " + filepath.Join(capDir, "stdin.txt") + "\n" +
		"printf '%s\\n' \"$@\" > " + filepath.Join(capDir, "argv.txt") + "\n" +
		"cat " + filepath.Join(capDir, "resp.json") + "\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return capDir
}

// A headless role gets its whole prompt (pack + role template + directive) on stdin, never as an argv token, for both
// claude and codex - a prompt starting with `---` passed as an arg is parsed as a flag (M11 round 2, codex).
func TestRunHeadlessPromptOnStdin(t *testing.T) {
	for _, tc := range []struct{ harness, leader string }{
		{"claude", "codex"}, // leader codex => adversary (not-leader) is claude
		{"codex", "claude"}, // leader claude => adversary is codex
	} {
		t.Run(tc.harness, func(t *testing.T) {
			ws, epicDir, sha := headlessEpic(t)
			cap := captureHarness(t, tc.harness, sha)

			pol := &workspace.Policy{}
			pol.Harness.Leader.Options = []string{"claude", "codex"}
			pol.Harness.Arena.Adversary.Rule = "not-leader"
			pol.Harness.Arena.Adversary.Default = tc.harness

			res, err := RunHeadless(Options{EpicDir: epicDir, WsRoot: ws, Policy: pol, Leader: tc.leader, Round: 1})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Headless) != 1 || res.Headless[0].Err != nil {
				t.Fatalf("headless run: %+v", res.Headless)
			}
			if res.Headless[0].Harness != tc.harness {
				t.Fatalf("harness = %q, want %q", res.Headless[0].Harness, tc.harness)
			}
			stdin, err := os.ReadFile(filepath.Join(cap, "stdin.txt"))
			if err != nil {
				t.Fatalf("no stdin captured: %v", err)
			}
			// The prompt (with the ```report directive) arrived on stdin.
			if !strings.Contains(string(stdin), "fenced block that opens") {
				t.Errorf("prompt not delivered on stdin:\n%s", stdin)
			}
			argv, _ := os.ReadFile(filepath.Join(cap, "argv.txt"))
			// No argv token carries the prompt body (which starts with the blinded pack, then a role template).
			if strings.Contains(string(argv), "fenced block that opens") || strings.Contains(string(argv), "coxswain.arena.v3") {
				t.Errorf("prompt leaked into argv: %s", argv)
			}
		})
	}
}

// A re-run skips a role whose report for the round is already present, leaving the report untouched; --force re-runs it.
func TestRunHeadlessSkipsDoneRole(t *testing.T) {
	ws, epicDir, sha := headlessEpic(t)
	dir := filepath.Join(epicDir, "reports", "arena")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "round-1-adversary.md"), []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pol := &workspace.Policy{}
	pol.Harness.Leader.Options = []string{"claude", "codex"}
	pol.Harness.Arena.Adversary.Rule = "not-leader"
	pol.Harness.Arena.Adversary.Default = "codex"

	// A one-repo design is lite: only the adversary. Its report exists, so the re-run skips it and does not touch it.
	res, err := RunHeadless(Options{EpicDir: epicDir, WsRoot: ws, Policy: pol, Leader: "codex", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Headless) != 1 || !res.Headless[0].Skipped {
		t.Fatalf("want the adversary skipped, got %+v", res.Headless)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "round-1-adversary.md")); string(b) != "existing\n" {
		t.Errorf("skipped role report was overwritten: %q", b)
	}

	// --force re-runs it (now the fake harness writes a real report).
	fakeHarness(t, sha)
	res, err = RunHeadless(Options{EpicDir: epicDir, WsRoot: ws, Policy: pol, Leader: "codex", Round: 1, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Headless[0].Skipped || res.Headless[0].Err != nil {
		t.Fatalf("--force should re-run the role: %+v", res.Headless[0])
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "round-1-adversary.md")); string(b) == "existing\n" {
		t.Errorf("--force did not rewrite the report")
	}
}

// --role runs only the named role of the active set; an inactive role is refused.
func TestRunHeadlessRoleFilter(t *testing.T) {
	ws, epicDir, sha := headlessEpic(t)
	// A design that mentions money+auth triggers a full arena (adversary + reviewer + domain).
	os.WriteFile(filepath.Join(epicDir, "DESIGN.md"), []byte("# e1\n\nthis design touches money and auth\n"), 0o644)
	fakeHarness(t, sha) // reviewer runs the leader harness (claude here)

	pol := &workspace.Policy{}
	pol.Harness.Leader.Options = []string{"claude", "codex"}
	pol.Harness.Arena.Adversary.Rule = "not-leader"
	pol.Harness.Arena.Adversary.Default = "codex"

	res, err := RunHeadless(Options{EpicDir: epicDir, WsRoot: ws, Policy: pol, Leader: "claude", Round: 1, Role: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Headless) != 1 || string(res.Headless[0].Role) != "reviewer" {
		t.Fatalf("--role reviewer should run only the reviewer, got %+v", res.Headless)
	}

	// An inactive/unknown role is refused up front.
	if _, err := RunHeadless(Options{EpicDir: epicDir, WsRoot: ws, Policy: pol, Leader: "claude", Round: 1, Role: "nope"}); err == nil {
		t.Fatal("want an error for an unknown role")
	}
}

func TestExtractReport(t *testing.T) {
	// claude single-object JSON.
	claudeOut := `{"type":"result","result":"prose\n` + "```report\\nBODY\\n```" + `\nmore"}`
	got, err := extractReport([]byte(claudeOut))
	if err != nil || strings.TrimSpace(got) != "BODY" {
		t.Fatalf("claude extract = %q, err %v", got, err)
	}
	// codex JSONL: the block sits in one event's text field.
	codexOut := "{\"type\":\"item\"}\n" + `{"type":"message","text":"here\n` + "```report\\nBODY2\\n```" + `"}` + "\n"
	got, err = extractReport([]byte(codexOut))
	if err != nil || strings.TrimSpace(got) != "BODY2" {
		t.Fatalf("codex extract = %q, err %v", got, err)
	}
	// no block.
	if _, err := extractReport([]byte(`{"result":"no block here"}`)); err == nil {
		t.Fatal("want error when no report block")
	}
}
