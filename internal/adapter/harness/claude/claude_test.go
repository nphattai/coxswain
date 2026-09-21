package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// Concurrent PrepareWorktree calls for different worktrees must all persist their trust entries: the process lock +
// unique temp file prevent a lost update. On the old code (fixed .cox.tmp, no lock) parallel dispatches drop entries.
func TestPrepareWorktreeConcurrent(t *testing.T) {
	home := t.TempDir()
	h := &Harness{Home: home}
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- h.PrepareWorktree(filepath.Join(home, "wt", strconv.Itoa(i)))
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent PrepareWorktree error: %v", e)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("config corrupted by concurrent writes: %v", err)
	}
	projects, _ := root["projects"].(map[string]any)
	for i := 0; i < n; i++ {
		abs, _ := filepath.Abs(filepath.Join(home, "wt", strconv.Itoa(i)))
		e, ok := projects[abs].(map[string]any)
		if !ok || e["hasTrustDialogAccepted"] != true {
			t.Errorf("lost trust entry for worktree %d (concurrent write dropped it)", i)
		}
	}
}

// PrepareWorktree merges projects[<abspath>].hasTrustDialogAccepted=true into ~/.claude.json, creating the file when
// absent and preserving every other project entry and top-level key.
func TestPrepareWorktreeMergesTrust(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude.json")
	// Pre-existing config with an unrelated project and a top-level key that must survive the merge.
	seed := `{"numStartups":7,"projects":{"/other":{"hasTrustDialogAccepted":true,"allowedTools":["Bash"]}}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Harness{Home: home}
	wt := filepath.Join(home, "worktrees", "story-x")
	if err := h.PrepareWorktree(wt); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	var root map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if root["numStartups"].(float64) != 7 {
		t.Errorf("top-level key not preserved: %v", root["numStartups"])
	}
	projects := root["projects"].(map[string]any)
	if other, ok := projects["/other"].(map[string]any); !ok || other["hasTrustDialogAccepted"] != true {
		t.Errorf("unrelated project entry not preserved: %v", projects["/other"])
	}
	entry, ok := projects[wt].(map[string]any)
	if !ok || entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("worktree trust not set: %v", projects[wt])
	}
}

// PrepareWorktree creates ~/.claude.json when it does not exist yet, with just this worktree trusted.
func TestPrepareWorktreeCreatesFile(t *testing.T) {
	home := t.TempDir()
	h := &Harness{Home: home}
	wt := filepath.Join(home, "wt")
	if err := h.PrepareWorktree(wt); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	entry := root["projects"].(map[string]any)[wt].(map[string]any)
	if entry["hasTrustDialogAccepted"] != true {
		t.Errorf("worktree trust not set in new file: %v", entry)
	}
}

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
