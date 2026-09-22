package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	if rc := storyDone([]string{"s1", "--epic", epic}); rc != 0 {
		t.Fatalf("storyDone rc=%d, want 0", rc)
	}
	events, _, _ := state.Load(epic)
	last := events[len(events)-1]
	if last.From != state.Working || last.To != state.Completed || last.Actor != state.Leader {
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

// Item 8c: `cox story done --merge <sha>` refuses a sha that is not landed on the branch the story's delivery mode
// requires (here local-only -> the repo's production branch). Base-behavior probe: on the base sha done records ANY
// --merge sha as evidence with no containment check, so the uncontained case below is (wrongly) accepted.
func TestStoryDoneMergeContainment(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	root := t.TempDir()
	if _, err := workspace.Init(root, nil); err != nil {
		t.Fatal(err)
	}
	// A git repo whose `production` branch holds one commit; a second commit on the default branch is NOT on production.
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "config", "user.email", "t@t")
	gitT(t, repo, "config", "user.name", "t")
	writeFileT(t, filepath.Join(repo, "a"), "1")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "one")
	onProd := gitOutT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "branch", "production")
	writeFileT(t, filepath.Join(repo, "b"), "2")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "two")
	offProd := gitOutT(t, repo, "rev-parse", "HEAD")
	if err := workspace.AddRepo(root, workspace.Repo{Alias: "app", Path: repo, Production: "production"}); err != nil {
		t.Fatal(err)
	}

	epic := filepath.Join(root, "proj", "epics", "slug")
	if err := os.MkdirAll(filepath.Join(epic, "stories"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStoryFM(t, epic, "s1", "---\nid: s1\nrepo: app\nmode: local-only\n---\nbody\n")
	if err := saveWorktree(epic, "s1", repo, 1); err != nil {
		t.Fatal(err)
	}

	// A sha not on production is refused.
	appendWorking(t, epic, "s1")
	if rc := storyDone([]string{"s1", "--epic", epic, "--merge", offProd}); rc == 0 {
		t.Fatal("done --merge with a sha not on production must be refused (local-only)")
	}
	// A sha on production is accepted and recorded as evidence.
	if rc := storyDone([]string{"s1", "--epic", epic, "--merge", onProd}); rc != 0 {
		t.Fatalf("done --merge with a sha on production must be accepted, rc=%d", rc)
	}
	events, _, _ := state.Load(epic)
	if last := events[len(events)-1]; last.To != state.Completed || last.Evidence["merge"] != onProd {
		t.Fatalf("completed event wrong: %+v", last)
	}
}

// releaseStory with --close-worktree stops the worker terminal (ADR 0012: Stop closes the terminal) BEFORE removing the
// worktree, and clears the worktree record. The fake backend records call order (M14).
func TestReleaseStoryClosesTerminalBeforeWorktree(t *testing.T) {
	epic := t.TempDir()
	appendWorking(t, epic, "s1")
	if err := saveSession(epic, "s1", backend.Session{Kind: "orca-terminal", ID: "term_1", Handle: "term_1"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := saveWorktree(epic, "s1", "/wt/s1", 1); err != nil {
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

// DESIGN wave-2 item 6: dispatch arms the harness-owned busy record and writes the worker busy hooks for a claude
// worker (its card reports its own state), and leaves codex unarmed unless policy harness.busy_verified vouches for a
// codex-hook writer. On the base sha the Claude card did not report busy state, so a claude dispatch never armed one.
func TestDispatchArmsClaudeNotCodexByDefault(t *testing.T) {
	epic := t.TempDir()
	wt := t.TempDir()

	// Claude: armed, record busy, worker hooks written.
	gen, err := armWorkerBusy(epic, "s1", "claude", wt, &workspace.Policy{})
	if err != nil {
		t.Fatalf("armWorkerBusy claude: %v", err)
	}
	if gen == "" {
		t.Fatal("claude must be armed at dispatch")
	}
	if _, err := os.Stat(filepath.Join(wt, ".claude", "settings.local.json")); err != nil {
		t.Fatalf("claude worker hooks not written: %v", err)
	}

	// Codex default (busy_verified off): not armed, no record.
	if gen, err := armWorkerBusy(epic, "s2", "codex", t.TempDir(), &workspace.Policy{}); err != nil || gen != "" {
		t.Fatalf("codex default arm = (%q, %v), want (\"\", nil): codex must not be armed until busy_verified", gen, err)
	}

	// Codex with busy_verified: armed.
	pol := &workspace.Policy{}
	pol.Harness.BusyVerified = true
	if gen, err := armWorkerBusy(epic, "s3", "codex", t.TempDir(), pol); err != nil || gen == "" {
		t.Fatalf("codex with busy_verified arm = (%q, %v), want a gen and no error", gen, err)
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

// Item 9: `cox story done` for a scout refuses without its report file and never requires --merge; with the report it
// completes. Base-behavior probe: on the base sha there is no kind, so a scout completes with no report (wrongly).
func TestScoutDoneRequiresReport(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "")
	epic := t.TempDir()
	writeStoryFM(t, epic, "sc", "---\nid: sc\nrepo: app\nkind: scout\n---\nbody\n")
	appendWorking(t, epic, "sc")

	// No report yet: refused.
	if rc := storyDone([]string{"sc", "--epic", epic}); rc == 0 {
		t.Fatal("scout done without a report must be refused")
	}
	// Write the report; now it completes and records the report as evidence (no --merge needed).
	if err := os.MkdirAll(filepath.Join(epic, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(epic, "reports", "sc.md"), "# scout report")
	if rc := storyDone([]string{"sc", "--epic", epic}); rc != 0 {
		t.Fatalf("scout done with a report rc=%d, want 0", rc)
	}
	events, _, _ := state.Load(epic)
	if last := events[len(events)-1]; last.To != state.Completed || last.Evidence["report"] == nil {
		t.Fatalf("scout completion must record the report: %+v", last)
	}
}

// Item 9: `cox story promote` flips a scout to a ship story, sets the mode, and appends the Superseding contract section;
// promoting a non-scout is refused. Base-behavior probe: on the base sha there is no `cox story promote` subcommand.
func TestStoryPromote(t *testing.T) {
	root := t.TempDir()
	if _, err := workspace.Init(root, nil); err != nil {
		t.Fatal(err)
	}
	epic := filepath.Join(root, "proj", "epics", "slug")
	if err := os.MkdirAll(filepath.Join(epic, "stories"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStoryFM(t, epic, "sc", "---\nid: sc\nrepo: app\nmode: direct-PR\nkind: scout\n---\n\n# sc\n")

	if rc := storyPromote([]string{"sc", "--epic", epic, "--mode", "no-mistakes"}); rc != 0 {
		t.Fatalf("promote rc=%d, want 0", rc)
	}
	b, err := os.ReadFile(filepath.Join(epic, "stories", "sc.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "kind: ship") {
		t.Errorf("promote must flip kind to ship:\n%s", text)
	}
	if !strings.Contains(text, "mode: no-mistakes") {
		t.Errorf("promote must set the mode:\n%s", text)
	}
	if !strings.Contains(text, "Superseding contract") || !strings.Contains(text, "Delivery contract: mode=no-mistakes") {
		t.Errorf("promote must append the superseding contract with the delivery line:\n%s", text)
	}
	if storyKind(epic, "sc") != "ship" {
		t.Errorf("promoted story kind = %q, want ship", storyKind(epic, "sc"))
	}
	// Promoting a story that is already a ship is refused.
	if rc := storyPromote([]string{"sc", "--epic", epic, "--mode", "direct-PR"}); rc == 0 {
		t.Fatal("promoting a non-scout must be refused")
	}
}

// writeStoryFM writes a story file with an explicit frontmatter block (for mode/kind tests).
func writeStoryFM(t *testing.T, epic, id, content string) {
	t.Helper()
	dir := filepath.Join(epic, "stories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
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

// Item 1: the line startWatcher prints when it reuses a live watcher names the story that watcher will pick up on its
// next tick (the watcher reloads its session set each tick), so a dispatch into a running epic is not silent.
func TestWatcherReuseLineNamesStory(t *testing.T) {
	line := watcherReuseLine(4242, "cox-wake-delivery-core")
	if !strings.Contains(line, "4242") || !strings.Contains(line, "cox-wake-delivery-core") || !strings.Contains(line, "next tick") {
		t.Fatalf("reuse line missing pid/story/next-tick: %q", line)
	}
}
