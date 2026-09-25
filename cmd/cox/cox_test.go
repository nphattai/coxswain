package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// authorizeWorker is the single gate every launch path shares. It refuses an unsandboxed harness without authority,
// authorizes pi only with --allow-unsandboxed (installing its extension out-of-tree under the epic .cox), authorizes
// claude silently via its standing ack, and leaves codex (sandboxed) ungated.
func TestAuthorizeWorker(t *testing.T) {
	t.Setenv("COX_PLANE", "terminal") // pi is gated to the terminal plane
	epic := t.TempDir()
	// pi without the flag is refused at the gate.
	if _, _, _, err := authorizeWorker("pi", epic, "s1", false, true); err == nil || !strings.Contains(err.Error(), "unsandboxed") {
		t.Fatalf("pi without --allow-unsandboxed must be refused, got %v", err)
	}
	// pi with the flag: authorized (authority=flag), extension installed out-of-tree under the epic .cox.
	ext, _, auth, err := authorizeWorker("pi", epic, "s1", true, true)
	if err != nil {
		t.Fatalf("pi with flag: %v", err)
	}
	if auth != "flag" || ext == "" {
		t.Fatalf("pi authorized: ext=%q auth=%q, want non-empty ext + authority flag", ext, auth)
	}
	if !strings.HasPrefix(ext, filepath.Join(epic, ".cox", "pi-ext")) {
		t.Errorf("pi extension must be out-of-tree under the epic .cox, got %q", ext)
	}
	// pi with installExtension=false (bare baseline): authorized but NO extension is installed or passed.
	if ext4, _, _, err := authorizeWorker("pi", epic, "s2", true, false); err != nil || ext4 != "" {
		t.Fatalf("bare pi authorizeWorker = (ext=%q err=%v), want no extension", ext4, err)
	}
	// claude authorizes silently via the standing ack: no flag, no extension, authority standing-ack.
	if ext2, _, auth2, err := authorizeWorker("claude", epic, "s1", false, true); err != nil || ext2 != "" || auth2 != "standing-ack" {
		t.Fatalf("claude authorizeWorker = (ext=%q auth=%q err=%v), want (\"\", standing-ack, nil)", ext2, auth2, err)
	}
	// codex is sandboxed: no extension, empty authority, no error.
	if _, _, auth3, err := authorizeWorker("codex", epic, "s1", false, true); err != nil || auth3 != "" {
		t.Fatalf("codex authorizeWorker auth=%q err=%v, want empty authority no error", auth3, err)
	}
}

// pi is gated to the terminal plane: on the orchestration plane Orca's Spawn drops the adapter-owned argv, so
// authorizeWorker refuses a pi launch there (even with --allow-unsandboxed).
func TestAuthorizeWorkerPiRefusedOffTerminalPlane(t *testing.T) {
	t.Setenv("COX_PLANE", "orchestration")
	epic := t.TempDir()
	if _, _, _, err := authorizeWorker("pi", epic, "s1", true, true); err == nil || !strings.Contains(err.Error(), "terminal plane") {
		t.Fatalf("pi on the orchestration plane must be refused, got %v", err)
	}
	// claude is unaffected by the plane gate.
	if _, _, _, err := authorizeWorker("claude", epic, "s1", false, true); err != nil {
		t.Fatalf("claude must not be plane-gated, got %v", err)
	}
}

func TestEnvDuration(t *testing.T) {
	const name = "COX_TEST_DUR"

	// Unset -> default.
	t.Setenv(name, "")
	if got := envDuration(name, 7*time.Second); got != 7*time.Second {
		t.Fatalf("unset = %v, want default 7s", got)
	}
	// A valid duration (park-wait's "120s" shape) parses.
	t.Setenv(name, "120s")
	if got := envDuration(name, 0); got != 120*time.Second {
		t.Fatalf("120s = %v, want 2m", got)
	}
	// Unparsable -> default, never a panic.
	t.Setenv(name, "not-a-dur")
	if got := envDuration(name, 5*time.Minute); got != 5*time.Minute {
		t.Fatalf("garbage = %v, want default 5m", got)
	}
}

// resolveWorkerModel resolves the launch --model per harness from the template policy: codex with no story model gets
// gpt-5.6-sol, claude gets claude-opus-5-5, and a harness with no map entry resolves to "" (LaunchLine omits --model).
// The opus alias is claude-only.
func TestResolveWorkerModelPerHarness(t *testing.T) {
	pol, err := workspace.LoadPolicyFile("../../templates/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveWorkerModel(pol, "codex", ""); got != "gpt-5.6-sol" {
		t.Errorf("codex default = %q, want gpt-5.6-sol", got)
	}
	if got := resolveWorkerModel(pol, "claude", ""); got != "claude-opus-5-5" {
		t.Errorf("claude default = %q, want claude-opus-5-5", got)
	}
	// pi resolves its provider/model default from the template (item 6, B-47); a pinned model still wins.
	if got := resolveWorkerModel(pol, "pi", ""); got != "openai-codex/gpt-5.6-sol" {
		t.Errorf("pi default = %q, want openai-codex/gpt-5.6-sol", got)
	}
	if got := resolveWorkerModel(pol, "pi", "anthropic/claude-opus-4-8"); got != "anthropic/claude-opus-4-8" {
		t.Errorf("pi pinned = %q, want anthropic/claude-opus-4-8", got)
	}
	if got := resolveWorkerModel(pol, "omp", ""); got != "" {
		t.Errorf("unmapped harness = %q, want \"\" (no --model)", got)
	}
	// The opus alias applies only to claude.
	if got := resolveWorkerModel(pol, "claude", "opus"); got != "claude-opus-5-5" {
		t.Errorf("claude opus alias = %q, want claude-opus-5-5", got)
	}
	if got := resolveWorkerModel(pol, "codex", "opus"); got != "opus" {
		t.Errorf("codex must not alias opus = %q, want opus", got)
	}
}

// modelHarnessMismatch flags a cross-vendor model (claude-* at codex, gpt-*/o-series at claude) and passes a matching
// or unrecognised one. The guard only applies to the claude/codex pair.
func TestModelHarnessMismatch(t *testing.T) {
	bad := []struct{ harness, model string }{
		{"codex", "claude-opus-4-8"},
		{"claude", "gpt-5.6-sol"},
		{"claude", "o3-mini"},
		{"claude", "GPT-5"}, // case-insensitive
	}
	for _, c := range bad {
		if mism, _ := modelHarnessMismatch(c.harness, c.model); !mism {
			t.Errorf("expected mismatch for harness=%s model=%s", c.harness, c.model)
		}
	}
	ok := []struct{ harness, model string }{
		{"claude", "claude-opus-4-8"},
		{"codex", "gpt-5.6-sol"},
		{"codex", "o3-mini"},
		{"claude", ""},           // no model, nothing to flag
		{"claude", "opus"},       // bare alias is not a vendor id
		{"omp", "claude-x"},      // guard only covers claude/codex
		{"claude", "some-model"}, // unrecognised vendor
	}
	for _, c := range ok {
		if mism, _ := modelHarnessMismatch(c.harness, c.model); mism {
			t.Errorf("unexpected mismatch for harness=%s model=%s", c.harness, c.model)
		}
	}
}

// piPreSpawnValidate rejects a bare/empty/provider-less pi model and an unsupported thinking level before spawn, and
// applies no check to non-pi harnesses (or to a valid pi model/thinking).
func TestPiPreSpawnValidate(t *testing.T) {
	// Non-pi harnesses are never checked here (modelHarnessMismatch handles claude/codex).
	if err := piPreSpawnValidate("claude", "", ""); err != nil {
		t.Errorf("non-pi harness must not be checked: %v", err)
	}
	// Valid pi model + thinking passes.
	if err := piPreSpawnValidate("pi", "anthropic/claude-opus-4-8", "high"); err != nil {
		t.Errorf("valid pi model/thinking must pass: %v", err)
	}
	// Empty thinking passes (pi default).
	if err := piPreSpawnValidate("pi", "anthropic/claude-opus-4-8", ""); err != nil {
		t.Errorf("empty thinking must pass: %v", err)
	}
	// Bad models are rejected before spawn.
	for _, model := range []string{"", "opus", "/opus", "anthropic/"} {
		if err := piPreSpawnValidate("pi", model, ""); err == nil {
			t.Errorf("pi model %q must be rejected pre-spawn", model)
		}
	}
	// Unsupported thinking is rejected.
	if err := piPreSpawnValidate("pi", "anthropic/claude-opus-4-8", "ultra"); err == nil {
		t.Errorf("unsupported pi thinking must be rejected pre-spawn")
	}
}

// currentAttempt bumps to N+1 only after a terminal canceled|failed state (a re-dispatch is a fresh attempt), and
// leaves working/input_required untouched (a live report keeps writing for the current attempt).
func TestCurrentAttemptAfterCancel(t *testing.T) {
	epic := t.TempDir()
	// Fixture: submitted -> working (attempt 1) -> canceled (attempt 1).
	appendEvent := func(from, to state.State, attempt int) {
		if err := state.Append(epic, state.Event{
			Epic: "e1", Story: "s1", Attempt: attempt, Actor: state.Leader,
			From: from, To: to, ExternalConfirmed: true, Evidence: map[string]any{"reason": "x"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(state.Submitted, state.Working, 1)
	// Working: no bump, still attempt 1.
	if got := currentAttempt(epic, "s1"); got != 1 {
		t.Fatalf("working attempt = %d, want 1", got)
	}
	appendEvent(state.Working, state.Canceled, 1)
	// Canceled: a re-dispatch is attempt 2.
	if got := currentAttempt(epic, "s1"); got != 2 {
		t.Fatalf("after cancel attempt = %d, want 2", got)
	}
	// Failed behaves the same.
	epic2 := t.TempDir()
	if err := state.Append(epic2, state.Event{
		Epic: "e1", Story: "s1", Attempt: 3, Actor: state.Leader,
		From: state.Working, To: state.Failed, ExternalConfirmed: true, Evidence: map[string]any{"reason": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := currentAttempt(epic2, "s1"); got != 4 {
		t.Fatalf("after fail attempt = %d, want 4", got)
	}
	// No events: attempt 1.
	if got := currentAttempt(t.TempDir(), "ghost"); got != 1 {
		t.Fatalf("no-events attempt = %d, want 1", got)
	}
}

// DESIGN wave-2 item 7: the session record is versioned by attempt and written by rename. After a relaunch bumps the
// attempt to 2, a late write carrying the old attempt 1 (a straggler from the prior incarnation) is dropped, so it
// never clobbers the newer session. On the base sha saveSession took no attempt and the last writer always won.
func TestSaveSessionDropsLowerAttempt(t *testing.T) {
	epic := t.TempDir()
	if err := saveSession(epic, "s", backend.Session{Kind: "orca", ID: "attempt2"}, 2); err != nil {
		t.Fatal(err)
	}
	// A late write from attempt 1 must be dropped (no error, but the file is unchanged).
	if err := saveSession(epic, "s", backend.Session{Kind: "orca", ID: "attempt1"}, 1); err != nil {
		t.Fatal(err)
	}
	sess, err := loadSession(epic, "s")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "attempt2" {
		t.Fatalf("session ID = %q, want attempt2 (the late attempt-1 write must be dropped)", sess.ID)
	}
	if a, _ := readSessionAttempt(epic, "s"); a != 2 {
		t.Fatalf("stamped attempt = %d, want 2", a)
	}
	// A same-or-higher attempt still overwrites (a status re-save during the live attempt, or the next relaunch).
	if err := saveSession(epic, "s", backend.Session{Kind: "orca", ID: "attempt3"}, 3); err != nil {
		t.Fatal(err)
	}
	if sess, _ := loadSession(epic, "s"); sess.ID != "attempt3" {
		t.Fatalf("session ID = %q, want attempt3 (a higher attempt overwrites)", sess.ID)
	}
}

// The worktree record is versioned the same way: a stale attempt-1 write after a relaunch to attempt 2 is dropped.
func TestSaveWorktreeDropsLowerAttempt(t *testing.T) {
	epic := t.TempDir()
	if err := saveWorktree(epic, "s", "/wt/attempt2", 2); err != nil {
		t.Fatal(err)
	}
	if err := saveWorktree(epic, "s", "/wt/attempt1", 1); err != nil {
		t.Fatal(err)
	}
	if got := readWorktree(epic, "s"); got != "/wt/attempt2" {
		t.Fatalf("worktree = %q, want /wt/attempt2 (late attempt-1 write dropped)", got)
	}
}

// DESIGN wave-2 item 7: the leader record is written as JSON and read back by the single reader, which also accepts a
// legacy plain-handle file. On the base sha the record was plain text only.
func TestLeaderJSONAndLegacyRead(t *testing.T) {
	epic := t.TempDir()
	if err := state.WriteLeader(epic, "term_9"); err != nil {
		t.Fatal(err)
	}
	if got := readLeader(epic); got != "term_9" {
		t.Fatalf("readLeader after WriteLeader = %q, want term_9", got)
	}
	// The written form is JSON carrying handle + pid + ts.
	b, err := os.ReadFile(filepath.Join(epic, ".cox", "leader"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "{") || !strings.Contains(string(b), `"handle":"term_9"`) {
		t.Fatalf("leader file is not the JSON record: %s", b)
	}
	rec, ok := state.ReadLeaderRecord(epic)
	if !ok || rec.PID == 0 || rec.TS == "" {
		t.Fatalf("leader record missing pid/ts: %+v ok=%v", rec, ok)
	}
	// A legacy plain-handle file is still read.
	if err := os.WriteFile(filepath.Join(epic, ".cox", "leader"), []byte("term_legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readLeader(epic); got != "term_legacy" {
		t.Fatalf("readLeader of a legacy plain handle = %q, want term_legacy", got)
	}
}

// B-52: `--model opus` follows the policy's claude worker model (captain 2026-09-23 moved claude workers to
// claude-opus-5-5 in policy while the alias stayed hardcoded to 4.8). A policy whose claude default is not an Opus id
// cannot redefine "opus", so the alias then falls back to the built-in default; codex never aliases.
func TestOpusAliasFollowsPolicy(t *testing.T) {
	pol := &workspace.Policy{}
	pol.Harness.Worker.Models = map[string]string{"claude": "claude-opus-5-5"}
	if got := resolveWorkerModel(pol, "claude", "opus"); got != "claude-opus-5-5" {
		t.Errorf("opus with policy claude-opus-5-5 = %q, want claude-opus-5-5", got)
	}
	pol.Harness.Worker.Models["claude"] = "claude-opus-9"
	if got := resolveWorkerModel(pol, "claude", "opus"); got != "claude-opus-9" {
		t.Errorf("opus with policy claude-opus-9 = %q, want claude-opus-9", got)
	}
	pol.Harness.Worker.Models["claude"] = "claude-sonnet-5"
	if got := resolveWorkerModel(pol, "claude", "opus"); got != workspace.DefaultWorkerModel {
		t.Errorf("opus with a non-Opus policy default = %q, want %s", got, workspace.DefaultWorkerModel)
	}
	if got := resolveWorkerModel(pol, "codex", "opus"); got != "opus" {
		t.Errorf("codex opus = %q, want opus untouched", got)
	}
	if got := modelAlias("opus"); got != workspace.DefaultWorkerModel {
		t.Errorf("policy-less alias = %q, want %s", got, workspace.DefaultWorkerModel)
	}
	if workspace.DefaultWorkerModel != "claude-opus-5-5" || workspace.TemplateWorkerModel("claude") != "claude-opus-5-5" {
		t.Errorf("claude worker default = %q / template %q, want claude-opus-5-5 (B-52)", workspace.DefaultWorkerModel, workspace.TemplateWorkerModel("claude"))
	}
}
