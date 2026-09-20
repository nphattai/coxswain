package pi

import (
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// The Pi card must match DESIGN §1 exactly; docs/adapters/pi.md mirrors it and the card/doc parity test compares them.
func TestCard(t *testing.T) {
	got := New().Card()
	want := harness.Capability{
		Name:         "pi",
		Roles:        []harness.Role{harness.RoleLeader, harness.RoleWorker},
		Wake:         harness.WakePush,
		Checkpoint:   harness.CheckpointAuto,
		Doorbell:     true,
		Interrupt:    true,
		Telemetry:    true,
		Sandbox:      false,
		Instructions: "AGENTS.md + Agent Skills",
	}
	if got.Name != want.Name || got.Wake != want.Wake || got.Checkpoint != want.Checkpoint ||
		got.Doorbell != want.Doorbell || got.Interrupt != want.Interrupt || got.Telemetry != want.Telemetry ||
		got.Sandbox != want.Sandbox || got.Instructions != want.Instructions {
		t.Errorf("Pi card = %+v, want %+v", got, want)
	}
	if len(got.Roles) != 2 || got.Roles[0] != harness.RoleLeader || got.Roles[1] != harness.RoleWorker {
		t.Errorf("Pi roles = %v, want [leader worker]", got.Roles)
	}
}

// Telemetry is Unknown (never 0) until the JSONL parser lands; a stubbed reading must not fabricate zero usage.
func TestTelemetryUnknownStub(t *testing.T) {
	ctx, err := New().Telemetry("/some/worktree")
	if err != nil {
		t.Fatalf("Telemetry err: %v", err)
	}
	if ctx.Known {
		t.Errorf("Telemetry Known = true, want false (stub); ctx=%+v", ctx)
	}
}

func TestValidateModel(t *testing.T) {
	ok := []string{
		"anthropic/claude-opus-4-8",
		"openai/gpt-5.6-sol",
		"openrouter/anthropic/claude-opus-4-8", // provider + namespaced id is allowed
	}
	for _, m := range ok {
		if err := ValidateModel(m); err != nil {
			t.Errorf("ValidateModel(%q) = %v, want nil", m, err)
		}
	}
	bad := []string{
		"",                // empty
		"   ",             // blank
		"opus",            // provider-less
		"claude-opus-4-8", // provider-less
		"/opus",           // empty provider
		"anthropic/",      // empty id
	}
	for _, m := range bad {
		if err := ValidateModel(m); err == nil {
			t.Errorf("ValidateModel(%q) = nil, want error", m)
		}
	}
}
