package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// The template policy declares the parallel-same-repo condition but leaves it unenforced.
func TestTemplatePolicyParallelNotEnforced(t *testing.T) {
	pol, err := workspace.LoadPolicyFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if pol.WorkersPerRepo.AllowParallelWhen.Enforced {
		t.Error("parallel-same-repo must ship unenforced until a scheduler checks files_owned")
	}
	if pol.WorkersPerRepo.AllowParallelWhen.FilesOwned != "disjoint" {
		t.Errorf("files_owned condition = %q, want disjoint", pol.WorkersPerRepo.AllowParallelWhen.FilesOwned)
	}
}

// sameRepoWorking finds another working story sharing the repo alias, and ignores the dispatching story and non-working ones.
func TestSameRepoWorking(t *testing.T) {
	epic := t.TempDir()
	writeStory(t, epic, "a", "web")
	writeStory(t, epic, "b", "web")
	writeStory(t, epic, "c", "api")
	// a and c working, b submitted only.
	appendWorking(t, epic, "a")
	appendWorking(t, epic, "c")

	got := sameRepoWorking(epic, "b", "web")
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("sameRepoWorking = %v, want [a] (c is a different repo, b is not working)", got)
	}
	// Dispatching a itself while c (different repo) works: no same-repo conflict.
	if got := sameRepoWorking(epic, "a", "web"); len(got) != 0 {
		t.Fatalf("sameRepoWorking(a) = %v, want none", got)
	}
}

// story done records completed with the merge evidence, and refuses to complete a story that is not working/input_required.
func TestStoryDone(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "") // no live backend: exercise the pure state path
	epic := t.TempDir()
	appendWorking(t, epic, "s1")

	if rc := storyDone([]string{"s1", "--epic", epic, "--merge", "abc123"}); rc != 0 {
		t.Fatalf("storyDone rc=%d, want 0", rc)
	}
	events, _, _ := state.Load(epic)
	last := events[len(events)-1]
	if last.From != state.Working || last.To != state.Completed || last.Evidence["merge"] != "abc123" || last.Actor != state.Leader {
		t.Fatalf("completed event wrong: %+v", last)
	}
	// Completing an already-completed story is refused (only working/input_required can complete).
	if rc := storyDone([]string{"s1", "--epic", epic}); rc == 0 {
		t.Fatal("completing a completed story should fail")
	}
	// An unknown story is refused too.
	if rc := storyDone([]string{"ghost", "--epic", epic}); rc == 0 {
		t.Fatal("completing an unknown story should fail")
	}
}

// releaseStory with --close-worktree stops the worker terminal (ADR 0012: Stop closes the terminal) BEFORE removing the
// worktree, and clears the worktree record. The fake backend records call order (M14).
func TestReleaseStoryClosesTerminalBeforeWorktree(t *testing.T) {
	epic := t.TempDir()
	appendWorking(t, epic, "s1")
	if err := saveSession(epic, "s1", backend.Session{Kind: "orca-terminal", ID: "term_1", Handle: "term_1"}); err != nil {
		t.Fatal(err)
	}
	if err := saveWorktree(epic, "s1", "/wt/s1"); err != nil {
		t.Fatal(err)
	}
	events, _, _ := state.Load(epic)
	snap := state.Fold(events).Stories["s1"]

	b := fake.New()
	b.StopConfirmed = true
	if err := releaseStory(epic, "s1", snap, state.Completed, map[string]any{}, b, true); err != nil {
		t.Fatal(err)
	}
	stopAt, rmAt := -1, -1
	for i, c := range b.Calls {
		switch c {
		case "Stop":
			stopAt = i
		case "WorktreeRemove":
			rmAt = i
		}
	}
	if stopAt < 0 || rmAt < 0 || stopAt > rmAt {
		t.Fatalf("terminal must be stopped before worktree removal, calls=%v", b.Calls)
	}
	if readWorktree(epic, "s1") != "" {
		t.Error("worktree record not cleared after --close-worktree")
	}
}

// story done refuses a busy worker (still running) unless --force; empty/pending/unknown composers never block.
func TestComposerBlocksDone(t *testing.T) {
	if !composerBlocksDone(backend.ComposerBusy, false) {
		t.Error("a busy composer must block completion")
	}
	if composerBlocksDone(backend.ComposerBusy, true) {
		t.Error("--force must override a busy composer")
	}
	for _, cs := range []string{backend.ComposerEmpty, backend.ComposerPending, backend.ComposerUnknown, ""} {
		if composerBlocksDone(cs, false) {
			t.Errorf("composer %q must not block completion (only busy does)", cs)
		}
	}
}

// story fail records failed with the reason, accepts working|input_required|parked, requires --reason, and never
// deletes the branch (there is no branch delete on the release path; a worktree record removal keeps the branch).
func TestStoryFail(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	epic := t.TempDir()
	appendWorking(t, epic, "s1")

	// Missing --reason is refused (evidence.reason is required).
	if rc := storyTerminate(state.Failed, []string{"s1", "--epic", epic}); rc == 0 {
		t.Fatal("story fail without --reason must be refused")
	}
	if rc := storyTerminate(state.Failed, []string{"s1", "--epic", epic, "--reason", "harness crashed"}); rc != 0 {
		t.Fatalf("storyTerminate(fail) rc=%d, want 0", rc)
	}
	events, _, _ := state.Load(epic)
	last := events[len(events)-1]
	if last.From != state.Working || last.To != state.Failed || last.Evidence["reason"] != "harness crashed" {
		t.Fatalf("failed event wrong: %+v", last)
	}
	// A terminal story cannot be failed again (only working|input_required|parked).
	if rc := storyTerminate(state.Failed, []string{"s1", "--epic", epic, "--reason", "x"}); rc == 0 {
		t.Fatal("failing an already-failed story should be refused")
	}
}

// story cancel accepts a parked story and records canceled with the reason.
func TestStoryCancelFromParked(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	epic := t.TempDir()
	appendWorking(t, epic, "s2")
	if err := state.Append(epic, state.Event{
		Epic: "e1", Story: "s2", Attempt: 1, Actor: state.Leader,
		From: state.Working, To: state.Parked, ExternalConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if rc := storyTerminate(state.Canceled, []string{"s2", "--epic", epic, "--reason", "scope dropped"}); rc != 0 {
		t.Fatalf("storyTerminate(cancel) rc=%d, want 0", rc)
	}
	events, _, _ := state.Load(epic)
	last := events[len(events)-1]
	if last.From != state.Parked || last.To != state.Canceled || last.Evidence["reason"] != "scope dropped" {
		t.Fatalf("canceled event wrong: %+v", last)
	}
}

// story dispatch refuses a harness with no adapter before touching a backend (capability enforcement, phase-07 item 4).
func TestStoryDispatchRefusesUnadapteredHarness(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	epic := t.TempDir()
	if rc := storyDispatch([]string{"s1", "--epic", epic, "--harness", "omp"}); rc != 1 {
		t.Fatalf("dispatch --harness omp rc=%d, want 1 (no adapter)", rc)
	}
}

// story dispatch refuses an unsandboxed harness (pi: sandbox false, no standing ack) before spawn unless
// --allow-unsandboxed authorizes it at the card-notice gate (Option C, AC3). The refusal happens before any backend.
func TestStoryDispatchRefusesUnsandboxedWithoutAuthority(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	epic := t.TempDir()
	if rc := storyDispatch([]string{"s1", "--epic", epic, "--harness", "pi", "--model", "anthropic/claude-opus-4-8"}); rc != 1 {
		t.Fatalf("dispatch --harness pi without --allow-unsandboxed rc=%d, want 1 (unsandboxed gate)", rc)
	}
}

func writeStory(t *testing.T, epic, id, repo string) {
	t.Helper()
	dir := filepath.Join(epic, "stories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: " + id + "\nrepo: " + repo + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendWorking(t *testing.T, epic, id string) {
	t.Helper()
	if err := state.Append(epic, state.Event{
		Epic: "e1", Story: id, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
}
