package main

import (
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

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
