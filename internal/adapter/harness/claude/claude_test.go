package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

func TestTelemetryFromSessionLog(t *testing.T) {
	home := t.TempDir()
	wt := "/fake/worktrees/story-x"
	dir := logDir(home, wt)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two assistant lines with usage; the last one wins for tokens, both count as turns.
	log := `{"message":{"usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":5}}}
{"type":"user"}
{"message":{"usage":{"input_tokens":20,"cache_read_input_tokens":300,"cache_creation_input_tokens":7}}}
`
	if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Harness{Home: home}
	ctx, err := h.Telemetry(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.Known {
		t.Fatal("expected Known telemetry")
	}
	if ctx.Tokens != 327 { // 20+300+7
		t.Fatalf("tokens = %d, want 327", ctx.Tokens)
	}
	if ctx.Turns != 2 {
		t.Fatalf("turns = %d, want 2", ctx.Turns)
	}
}

// F11: no session log must resolve to Unknown, not 0 tokens.
func TestTelemetryNoLogIsUnknownNotZero(t *testing.T) {
	h := &Harness{Home: t.TempDir()}
	ctx, err := h.Telemetry("/no/such/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Known {
		t.Fatal("expected Unknown when there is no log")
	}
	if ctx.Tokens != 0 {
		t.Fatal("Unknown telemetry should carry Known=false; callers render 'unknown', never 0k")
	}
}

func TestCardIsPush(t *testing.T) {
	c := New().Card()
	if c.Wake != harness.WakePush || c.Checkpoint != harness.CheckpointAuto || !c.Telemetry {
		t.Fatalf("claude card wrong: %+v", c)
	}
}

func TestLaunchArgsRelaunchInjectsCheckpoint(t *testing.T) {
	args := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Worktree: "/wt",
		Brief: harness.Brief{StoryPath: "stories/x.md", InjectCheckpoint: true, Note: "phase 2 half done"},
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "cox checkpoint inject") || !strings.Contains(joined, "phase 2 half done") {
		t.Fatalf("relaunch args missing checkpoint/note: %q", joined)
	}
}

// A dispatched claude worker's argv is exactly `claude --model <id> <policy flags> <prompt>` - the model spelled
// --model, the flags in order, then the story-file prompt. Claude has no writable-root/network sandbox flags.
func TestLaunchArgsWorkerGolden(t *testing.T) {
	argv := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Worktree: "/wt", Model: "claude-opus-4-8",
		Flags: []string{"--permission-mode", "bypassPermissions"},
		Brief: harness.Brief{StoryPath: "/epics/v2/stories/m10.md"},
	})
	want := []string{
		"claude", "--model", "claude-opus-4-8", "--permission-mode", "bypassPermissions",
		"Your task is the story file /epics/v2/stories/m10.md - read it in full and follow its Working rules exactly.",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("claude worker argv =\n  %v\nwant\n  %v", argv, want)
	}
	// No sandbox roots/network for claude.
	if strings.Contains(strings.Join(argv, " "), "--add-dir") {
		t.Fatalf("claude must not type --add-dir: %v", argv)
	}
}
