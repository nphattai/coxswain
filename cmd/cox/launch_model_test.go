package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testPiModel is the workspace policy's harness.worker.models.pi in these tests, distinct from the template default so
// a launch that reads the policy is told apart from one that falls back to the template.
const testPiModel = "openai-codex/policy-pi-test"

// fakeOrcaScript answers the Orca CLI calls a worker launch makes: `terminal create` and `worktree create` succeed,
// `terminal send` (the typed launch line) is logged and fails so no launch-confirm wait follows, everything else fails.
// Every call is appended to the log, so a test reads the exact launch line - and its --model - the worker would run.
const fakeOrcaScript = `#!/bin/sh
echo "$@" >> '%LOG%'
name=$(echo "$@" | sed -n 's/.*--name \([^ ]*\).*/\1/p')
case "$1 $2" in
'terminal create') echo '{"ok":true,"result":{"terminal":{"handle":"h1"}}}' ;;
'worktree create') echo '{"ok":true,"result":{"worktree":{"path":"%WT%","branch":"'"$name"'"}}}' ;;
'terminal close') echo '{"ok":true,"result":{}}' ;;
*) echo '{"ok":false,"error":{"message":"fake orca"}}'; exit 1 ;;
esac
`

// launchFixture builds a workspace whose policy maps pi -> testPiModel, one epic with story s1 (frontmatter given), a
// git worktree recorded for s1, and a fake `orca` on PATH on the terminal plane. It returns the epic dir, the worktree,
// and the orca call log.
func launchFixture(t *testing.T, frontmatter string) (epic, wt, log string) {
	t.Helper()
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"schema":"coxswain.workspace.v1"}`)
	tpl, err := os.ReadFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	pol := strings.Replace(string(tpl), `"pi": "openai-codex/gpt-5.6-sol"`, `"pi": "`+testPiModel+`"`, 1)
	if pol == string(tpl) {
		t.Fatal("template policy no longer maps pi to openai-codex/gpt-5.6-sol; update the fixture")
	}
	mustWrite(t, filepath.Join(ws, "cox", "policy.json"), pol)
	epic = filepath.Join(ws, "epics", "e1")
	mustWrite(t, filepath.Join(epic, "stories", "s1.md"), "---\nid: s1\n"+frontmatter+"---\nbody\n")
	appendWorking(t, epic, "s1")

	wt = t.TempDir()
	gitInitRepo(t, wt)
	if out, err := exec.Command("git", "-C", wt, "checkout", "-q", "-b", "story/s1").CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v %s", err, out)
	}
	mustWrite(t, wtFilePath(epic, "s1"), wt)

	bin := t.TempDir()
	log = filepath.Join(bin, "orca.log")
	script := strings.NewReplacer("%LOG%", log, "%WT%", wt).Replace(fakeOrcaScript)
	mustWrite(t, filepath.Join(bin, "orca"), script)
	if err := os.Chmod(filepath.Join(bin, "orca"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORCA_RUN_ID", "run-fake")
	t.Setenv("COX_PLANE", "terminal")
	t.Setenv("ORCA_TERMINAL_HANDLE", "")
	return epic, wt, log
}

// launchedModel returns the --model of the launch line typed into the worker terminal, failing the test when no launch
// line reached Orca (the path refused before spawning).
func launchedModel(t *testing.T, log string) string {
	t.Helper()
	b, _ := os.ReadFile(log)
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "terminal send") {
			continue
		}
		i := strings.Index(line, "'--model' '")
		if i < 0 {
			return ""
		}
		rest := line[i+len("'--model' '"):]
		return rest[:strings.Index(rest, "'")]
	}
	t.Fatalf("no launch line reached orca; calls:\n%s", b)
	return ""
}

// Finding 4 / F-2: a same-harness `cox story resume` of a pi story with no pinned model launches with the policy's
// harness.worker.models.pi (on beedd57 it refused: "pi model is empty").
func TestResumeLaunchesPolicyPiModel(t *testing.T) {
	epic, _, log := launchFixture(t, "harness: pi\n")
	_ = storyControl("resume", []string{"s1", "--epic", epic, "--allow-unsandboxed"})
	if got := launchedModel(t, log); got != testPiModel {
		t.Fatalf("resume launched --model %q, want the policy pi model %q", got, testPiModel)
	}
}

// Resume keeps an explicit --model over the frontmatter and the policy default.
func TestResumeExplicitModelWins(t *testing.T) {
	epic, _, log := launchFixture(t, "harness: pi\nmodel: openai-codex/frontmatter-model\n")
	_ = storyControl("resume", []string{"s1", "--epic", epic, "--allow-unsandboxed", "--model", "openai-codex/flag-model"})
	if got := launchedModel(t, log); got != "openai-codex/flag-model" {
		t.Fatalf("resume launched --model %q, want the --model flag", got)
	}
}

// A reroute resume onto pi takes the pi policy default, never the old harness's frontmatter model.
func TestRerouteLaunchesPolicyPiModel(t *testing.T) {
	epic, _, log := launchFixture(t, "harness: claude\nmodel: claude-opus-4-8\n")
	_ = storyControl("resume", []string{"s1", "--epic", epic, "--harness", "pi", "--allow-unsandboxed"})
	if got := launchedModel(t, log); got != testPiModel {
		t.Fatalf("reroute launched --model %q, want the policy pi model %q", got, testPiModel)
	}
}

// `cox control <story> relaunch` of a pi story with no pinned model launches with the policy pi model.
func TestControlRelaunchLaunchesPolicyPiModel(t *testing.T) {
	epic, _, log := launchFixture(t, "harness: pi\n")
	_ = cmdControl([]string{"s1", "relaunch", "--note", "resume", "--epic", epic, "--allow-unsandboxed"})
	if got := launchedModel(t, log); got != testPiModel {
		t.Fatalf("relaunch launched --model %q, want the policy pi model %q", got, testPiModel)
	}
}

// `cox story dispatch` of a pi story with no pinned model launches with the policy pi model.
func TestDispatchLaunchesPolicyPiModel(t *testing.T) {
	epic, _, log := launchFixture(t, "harness: pi\nrepo: r\n")
	_ = cmdStory([]string{"dispatch", "s1", "--epic", epic, "--allow-unsandboxed"})
	if got := launchedModel(t, log); got != testPiModel {
		t.Fatalf("dispatch launched --model %q, want the policy pi model %q", got, testPiModel)
	}
}

// `cox baseline run` of a pi story with no pinned model launches with the policy pi model.
func TestBaselineLaunchesPolicyPiModel(t *testing.T) {
	epic, wt, log := launchFixture(t, "harness: pi\n")
	// The replay worktree sits on the baseline branch; no story/s1 branch may be reachable (baseline's leak guard).
	if out, err := exec.Command("git", "-C", wt, "branch", "-m", "story/s1", "baseline/s1-v2").CombinedOutput(); err != nil {
		t.Fatalf("git branch -m: %v %s", err, out)
	}
	head, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	_ = cmdBaseline([]string{"run", "--story", "s1", "--epic", epic, "--harness", "pi", "--condition", "v2",
		"--before", strings.TrimSpace(string(head)), "--repo", wt, "--allow-unsandboxed"})
	if got := launchedModel(t, log); got != testPiModel {
		t.Fatalf("baseline launched --model %q, want the policy pi model %q", got, testPiModel)
	}
}
