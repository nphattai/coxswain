package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/workspace"
)

func writeExec(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// SingleOnPATH is the which -a check: one is pass, none is fail, two is fail.
func TestSingleOnPATH(t *testing.T) {
	d1 := t.TempDir()
	writeExec(t, d1, "coxbin")
	t.Setenv("PATH", d1)
	if c := SingleOnPATH("coxbin"); c.Status != StatusPass {
		t.Errorf("one on PATH = %v, want pass", c)
	}
	if c := SingleOnPATH("missingbin"); c.Status != StatusFail {
		t.Errorf("none on PATH = %v, want fail", c)
	}
	d2 := t.TempDir()
	writeExec(t, d2, "coxbin")
	t.Setenv("PATH", d1+string(os.PathListSeparator)+d2)
	if c := SingleOnPATH("coxbin"); c.Status != StatusFail || c.Fix == "" {
		t.Errorf("two on PATH = %v, want fail with a fix hint", c)
	}
}

// Present passes for a binary on PATH and fails (with a fix) otherwise; Reachable passes on exit 0 and is unknown on a
// non-zero probe.
func TestPresentAndReachable(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, dir, "orcafake")
	t.Setenv("PATH", dir)
	if c := Present("orcafake", "install it"); c.Status != StatusPass {
		t.Errorf("present = %v, want pass", c)
	}
	if c := Present("nope", "install it"); c.Status != StatusFail || c.Fix == "" {
		t.Errorf("missing = %v, want fail with fix", c)
	}
	if c := Reachable("orcafake", nil, 5*time.Second); c.Status != StatusPass {
		t.Errorf("reachable exit0 = %v, want pass", c)
	}
	writeExec(t, dir, "orcabad")
	if err := os.WriteFile(filepath.Join(dir, "orcabad"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if c := Reachable("orcabad", nil, 5*time.Second); c.Status != StatusUnknown {
		t.Errorf("unreachable = %v, want unknown", c)
	}
}

// HarnessBinaries: the default harness must be present (fail if missing); a non-default option is info if missing; a
// non-adaptered option is info.
func TestHarnessBinaries(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, dir, "hpresent")
	t.Setenv("PATH", dir)
	adaptered := func(n string) bool { return n == "hpresent" || n == "hmissing" }

	pol := &workspace.Policy{}
	pol.Harness.Leader.Default = "hpresent"
	pol.Harness.Worker.Default = "hpresent"
	pol.Harness.Leader.Options = []string{"hpresent"}
	pol.Harness.Worker.Options = []string{"hpresent", "hmissing", "noad"}
	byName := map[string]Check{}
	for _, c := range HarnessBinaries(pol, adaptered) {
		byName[c.Name] = c
	}
	if byName["harness hpresent"].Status != StatusPass {
		t.Errorf("present default = %v, want pass", byName["harness hpresent"])
	}
	if byName["harness hmissing"].Status != StatusInfo {
		t.Errorf("missing non-default adaptered = %v, want info", byName["harness hmissing"])
	}
	if byName["harness noad"].Status != StatusInfo {
		t.Errorf("non-adaptered = %v, want info", byName["harness noad"])
	}

	// A missing DEFAULT harness fails with a fix hint.
	pol2 := &workspace.Policy{}
	pol2.Harness.Leader.Default = "hmissing"
	pol2.Harness.Worker.Default = "hmissing"
	pol2.Harness.Leader.Options = []string{"hmissing"}
	pol2.Harness.Worker.Options = []string{"hmissing"}
	got := HarnessBinaries(pol2, adaptered)
	if len(got) != 1 || got[0].Status != StatusFail || got[0].Fix == "" {
		t.Errorf("missing default harness = %v, want a single fail with fix", got)
	}
}

// Roots merges the defaults, $COX_ROOTS and explicit --root values, de-duplicated.
func TestRoots(t *testing.T) {
	t.Setenv("COX_ROOTS", "/a"+string(os.PathListSeparator)+"/b")
	got := Roots([]string{"/b", "/c"})
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r] {
			t.Errorf("Roots has a duplicate %q: %v", r, got)
		}
		seen[r] = true
	}
	for _, want := range []string{"/a", "/b", "/c"} {
		if !seen[want] {
			t.Errorf("Roots missing %q: %v", want, got)
		}
	}
}

// FindWorkspaces recognises a v2 workspace by cox/workspace.json at a root and one level below, plus explicit dirs.
func TestFindWorkspaces(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "myws")
	if err := os.MkdirAll(filepath.Join(ws, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "cox", "workspace.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := FindWorkspaces([]string{root}, nil)
	if len(got) != 1 || got[0] != ws {
		t.Fatalf("FindWorkspaces = %v, want [%s]", got, ws)
	}
	// An explicit dir (the workspace of --epic/cwd) is added even when not under a root.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "cox", "workspace.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = FindWorkspaces([]string{root}, []string{other})
	if len(got) != 2 {
		t.Fatalf("FindWorkspaces with explicit = %v, want 2", got)
	}
}

// InspectWorkspace validates the registry, lists epics with Status: and watcher liveness, and flags a repo checkout
// that carries a stray cox/policy.json.
func TestInspectWorkspace(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	if _, err := workspace.Init(root, &workspace.Workspace{Repos: []workspace.Repo{{Alias: "app", Path: repo, Production: "main"}}}); err != nil {
		t.Fatal(err)
	}
	// An epic with a Status: line and a dead watcher.
	epicDir := filepath.Join(root, "proj", "epics", "e1")
	if err := os.MkdirAll(filepath.Join(epicDir, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epicDir, "DESIGN.md"), []byte("# e1\n\nStatus: active (signed)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epicDir, ".cox", "watch.pid"), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray policy.json inside the repo checkout.
	if err := os.MkdirAll(filepath.Join(repo, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "cox", "policy.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := InspectWorkspace(root)
	if !rep.Valid {
		t.Fatalf("valid workspace reported invalid: %s", rep.Error)
	}
	if len(rep.Epics) != 1 || rep.Epics[0].Slug != "e1" || rep.Epics[0].Status == "" {
		t.Fatalf("epic listing wrong: %+v", rep.Epics)
	}
	if rep.Epics[0].WatcherAlive && rep.Epics[0].WatcherPid == 999999 {
		t.Skip("pid 999999 happens to be alive on this host")
	}
	if rep.Epics[0].WatcherAlive {
		t.Errorf("dead watcher reported alive")
	}
	found := false
	for _, a := range rep.PolicyInRepo {
		if a == "app" {
			found = true
		}
	}
	if !found {
		t.Errorf("stray cox/policy.json in repo checkout not flagged: %v", rep.PolicyInRepo)
	}
}

// An invalid workspace.json is reported Valid=false with a field-named error.
func TestInspectWorkspaceInvalid(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A repo with no production branch fails validation.
	if err := os.WriteFile(filepath.Join(root, "cox", "workspace.json"), []byte(`{"repos":[{"alias":"a","name":"org/x"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := InspectWorkspace(root)
	if rep.Valid || rep.Error == "" {
		t.Fatalf("invalid workspace must report Valid=false with an error, got %+v", rep)
	}
}

// hooksComplete requires all four cox hook commands, not just one (PR#3 review finding 6).
func TestHooksComplete(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.json")
	if err := os.WriteFile(full, []byte(`{"hooks":{
      "UserPromptSubmit":[{"hooks":[{"command":"cox hook prompt-drain"}]}],
      "Stop":[{"hooks":[{"command":"cox hook stop-rewake"}]}],
      "PreCompact":[{"hooks":[{"command":"cox hook precompact"}]}],
      "SessionStart":[{"hooks":[{"command":"cox hook session-start"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hooksComplete(full) {
		t.Error("all four hook commands present must be complete")
	}
	partial := filepath.Join(dir, "partial.json")
	if err := os.WriteFile(partial, []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":"cox hook prompt-drain"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if hooksComplete(partial) {
		t.Error("only prompt-drain present must be incomplete (Stop/PreCompact/SessionStart missing)")
	}
	if hooksComplete(filepath.Join(dir, "absent.json")) {
		t.Error("an absent settings file is incomplete")
	}
}

// pidAlive detects this process as alive and a bogus pid as dead.
func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("this process must be alive")
	}
	if pidAlive(0) {
		t.Error("pid 0 must not be alive")
	}
}
