package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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
// gpt-5.6-sol, claude gets claude-opus-4-8, and a harness with no map entry resolves to "" (LaunchLine omits --model).
// The opus alias is claude-only.
func TestResolveWorkerModelPerHarness(t *testing.T) {
	pol, err := workspace.LoadPolicyFile("../../templates/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveWorkerModel(pol, "codex", ""); got != "gpt-5.6-sol" {
		t.Errorf("codex default = %q, want gpt-5.6-sol", got)
	}
	if got := resolveWorkerModel(pol, "claude", ""); got != "claude-opus-4-8" {
		t.Errorf("claude default = %q, want claude-opus-4-8", got)
	}
	if got := resolveWorkerModel(pol, "omp", ""); got != "" {
		t.Errorf("unmapped harness = %q, want \"\" (no --model)", got)
	}
	// The opus alias applies only to claude.
	if got := resolveWorkerModel(pol, "claude", "opus"); got != "claude-opus-4-8" {
		t.Errorf("claude opus alias = %q, want claude-opus-4-8", got)
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
