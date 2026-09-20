package pi

import (
	"strings"
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

// Telemetry for a worktree with no Pi session is Unknown (never 0 usage, F11). Deeper telemetry parsing cases live in
// telemetry_test.go.
func TestTelemetryUnknownWhenNoSession(t *testing.T) {
	ctx, err := (&Harness{Home: t.TempDir()}).Telemetry("/some/worktree")
	if err != nil {
		t.Fatalf("Telemetry err: %v", err)
	}
	if ctx.Known {
		t.Errorf("Telemetry Known = true, want false (no session); ctx=%+v", ctx)
	}
}

// A dispatched Pi worker's argv spells the provider/model as `--model <id>`, types `--thinking <level>` when a level is
// set, marks trust with `--approve` (Pi's per-run trust so a worker never stalls on the trust dialog), then the
// story-file prompt.
func TestLaunchArgsWorker(t *testing.T) {
	argv := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Worktree: "/wt", Model: "anthropic/claude-opus-4-8", Effort: "high",
		Brief: harness.Brief{StoryPath: "/epics/v2/stories/s.md"},
	})
	want := []string{
		"pi", "--model", "anthropic/claude-opus-4-8", "--thinking", "high", "--approve",
		"Your task is the story file /epics/v2/stories/s.md - read it in full and follow its Working rules exactly.",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("pi worker argv =\n  %v\nwant\n  %v", argv, want)
	}
}

// With no thinking level set, Pi types no --thinking (it uses its default), but still marks trust with --approve.
func TestLaunchArgsWorkerNoThinking(t *testing.T) {
	argv := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Worktree: "/wt", Model: "anthropic/claude-opus-4-8",
		Brief: harness.Brief{StoryPath: "/e/stories/s.md"},
	})
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--thinking") {
		t.Errorf("no effort must type no --thinking: %v", argv)
	}
	if !strings.Contains(joined, "--approve") {
		t.Errorf("pi worker must mark trust with --approve: %v", argv)
	}
}

func TestValidateThinking(t *testing.T) {
	for _, ok := range []string{"", "off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if err := ValidateThinking(ok); err != nil {
			t.Errorf("ValidateThinking(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"ultra", "none", "HIGH ", "verylow", "1"} {
		if err := ValidateThinking(bad); err == nil {
			t.Errorf("ValidateThinking(%q) = nil, want error", bad)
		}
	}
}

// Pi's trust is a launch flag (--approve), so PrepareWorktree pre-seeds nothing and never mutates user config.
func TestPrepareWorktreeNoOp(t *testing.T) {
	if err := New().PrepareWorktree("/any/worktree"); err != nil {
		t.Errorf("pi PrepareWorktree must be a no-op, got %v", err)
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
