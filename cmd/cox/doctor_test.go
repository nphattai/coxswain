package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/doctor"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// A workspace discovered via --root/epic whose epic has a dead watcher and an active story yields a watcher issue, so
// doctor's exit reflects it (PR#3 review finding 5). An alive watcher, or no open story, yields none.
func TestWatcherIssuesForWorkspaces(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, controlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(watchPidPath(epic), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if watcherInfo(epic).Alive {
		t.Skip("pid 999999 happens to be alive on this host")
	}
	seedEvent(t, epic, state.Submitted, state.Working) // an open story

	dead := []doctor.WorkspaceReport{{Epics: []doctor.EpicReport{{Path: epic, WatcherAlive: false}}}}
	if len(watcherIssuesForWorkspaces(dead)) == 0 {
		t.Error("a dead watcher with an active story in a discovered workspace must raise an issue")
	}
	// An alive watcher raises nothing.
	alive := []doctor.WorkspaceReport{{Epics: []doctor.EpicReport{{Path: epic, WatcherAlive: true}}}}
	if len(watcherIssuesForWorkspaces(alive)) != 0 {
		t.Error("an alive watcher must raise no issue")
	}
}

// A path-backed repo whose checkout is missing or is not a git checkout makes doctor fail (finding 7): the issue is
// surfaced and folds into the exit code.
func TestWorkspaceRepoIssues(t *testing.T) {
	reps := []doctor.WorkspaceReport{{Root: "/ws", RepoIssues: []string{`repo "api" path /gone does not exist`}}}
	got := workspaceRepoIssues(reps)
	if len(got) != 1 || !strings.Contains(got[0], "/ws") || !strings.Contains(got[0], "api") {
		t.Fatalf("workspaceRepoIssues = %v, want the repo issue prefixed with the workspace root", got)
	}
	if len(workspaceRepoIssues([]doctor.WorkspaceReport{{Root: "/ok"}})) != 0 {
		t.Error("a workspace with no repo issues must contribute nothing")
	}
}

// doctor renders one capability card per implemented harness, each tagged adapter=yes, with the card's real fields.
func TestDoctorHarnessCards(t *testing.T) {
	cards := harnessCards(false)
	if len(cards) != 2 {
		t.Fatalf("got %d harness cards, want 2 (claude, codex)", len(cards))
	}
	byName := map[string]harnessCard{}
	for _, c := range cards {
		if !c.Adapter {
			t.Errorf("%s: adapter must be true (it is in the registry)", c.Name)
		}
		byName[c.Name] = c
	}
	if byName["claude"].Wake != "push" || byName["claude"].Checkpoint != "auto" || !byName["claude"].Telemetry {
		t.Errorf("claude card wrong: %+v", byName["claude"])
	}
	if byName["codex"].Wake != "pull" || byName["codex"].Checkpoint != "manual" || byName["codex"].Telemetry {
		t.Errorf("codex card wrong: %+v", byName["codex"])
	}
}

// When the cox codex leader hooks are installed, doctor shows codex wake=push (the Stop hook delivers wakes), not the
// card default of pull.
func TestDoctorCodexWakePushWhenHooksInstalled(t *testing.T) {
	cardWake := func(cards []harnessCard) string {
		for _, c := range cards {
			if c.Name == "codex" {
				return c.Wake
			}
		}
		return ""
	}
	if w := cardWake(harnessCards(false)); w != "pull" {
		t.Fatalf("no hooks: codex wake = %q, want pull", w)
	}
	if w := cardWake(harnessCards(true)); w != "push" {
		t.Fatalf("hooks installed: codex wake = %q, want push", w)
	}

	// codexCoxHooksInstalled detects the stop-rewake command in root/.codex/hooks.json.
	root := t.TempDir()
	if codexCoxHooksInstalled(root) {
		t.Error("no file: must report not installed")
	}
	mustWrite(t, filepath.Join(root, ".codex", "hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"command":"cox hook stop-rewake --harness codex --epic /e"}]}]}}`)
	if !codexCoxHooksInstalled(root) {
		t.Error("stop-rewake command present: must report installed")
	}
}

// The policy-options column marks each declared harness adapter yes|no. The seed template lists only adaptered
// harnesses (claude, codex) so a minimal install passes doctor clean (arena round 1, adversary claim 1, accepted); a
// non-adaptered option, when a user adds one, still resolves as adapter=false.
func TestDoctorPolicyOptions(t *testing.T) {
	pol, err := workspace.LoadPolicyFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, o := range policyOptionsFrom(pol) {
		got[o.Name] = o.Adapter
	}
	for name, want := range map[string]bool{"claude": true, "codex": true} {
		if got[name] != want {
			t.Errorf("option %q adapter=%v, want %v", name, got[name], want)
		}
	}
	if _, ok := got["omp"]; ok {
		t.Errorf("seed template must list only adaptered harnesses in worker.options, got %v", got)
	}
	// A user-added non-adaptered option still resolves as adapter=false.
	pol.Harness.Worker.Options = append(pol.Harness.Worker.Options, "omp")
	adapters := map[string]bool{}
	for _, o := range policyOptionsFrom(pol) {
		adapters[o.Name] = o.Adapter
	}
	if adapters["omp"] {
		t.Errorf("omp must resolve adapter=false")
	}
}

// watcherInfo reports a live pidfile as alive and reads the last-tick file; a stale pid is present but dead. When the
// watcher is not alive and a story is still working, watcherIssue fires (the dogfood gap: a dead watcher nobody noticed).
func TestWatcherInfoAndIssue(t *testing.T) {
	epic := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	// No pidfile: not present, and no issue even with a working story.
	if wi := watcherInfo(epic); wi.Present {
		t.Error("no pidfile must be Present=false")
	}

	// A live pid (this process) plus a recent tick: alive, tick age read.
	writeCoxFile(epic, "watch.pid", "")
	if err := os.WriteFile(watchPidPath(epic), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	tickDir := filepath.Join(epic, controlDir, "watch")
	os.MkdirAll(tickDir, 0o755)
	os.WriteFile(filepath.Join(tickDir, "lasttick"), []byte(now.Add(-30*time.Second).Format(time.RFC3339)), 0o644)
	wi := watcherInfo(epic)
	if !wi.Present || !wi.Alive || !wi.HasTick {
		t.Fatalf("live watcher wrong: %+v", wi)
	}
	if line := watcherLine(wi, now); !strings.Contains(line, "alive") || !strings.Contains(line, "30s ago") {
		t.Errorf("watcher line wrong: %q", line)
	}
	// Alive + working story => no issue.
	if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: "s", Attempt: 1, Actor: state.Leader, From: state.Submitted, To: state.Working, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	if iss := watcherIssue(epic, wi); iss != "" {
		t.Errorf("alive watcher must raise no issue, got %q", iss)
	}

	// A dead pid with a story still working => issue.
	os.WriteFile(watchPidPath(epic), []byte("999999"), 0o644)
	dead := watcherInfo(epic)
	if dead.Alive {
		t.Skip("pid 999999 happens to be alive on this host")
	}
	if iss := watcherIssue(epic, dead); iss == "" || !strings.Contains(iss, "not alive") {
		t.Fatalf("dead watcher with a working story must raise an issue, got %q", iss)
	}
}

// resolveWorkerModel falls back to the template default for a harness the workspace policy does not map, instead of
// returning "" (which would launch with no --model).
func TestResolveWorkerModelTemplateFallback(t *testing.T) {
	// An empty policy maps no codex model; resolveWorkerModel borrows the template default gpt-5.6-sol.
	if got := resolveWorkerModel(&workspace.Policy{}, "codex", ""); got != "gpt-5.6-sol" {
		t.Errorf("codex fallback = %q, want gpt-5.6-sol", got)
	}
	// An explicit model still wins.
	if got := resolveWorkerModel(&workspace.Policy{}, "codex", "gpt-x"); got != "gpt-x" {
		t.Errorf("explicit model = %q, want gpt-x", got)
	}
}
