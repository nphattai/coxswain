package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/workspace"
)

// readSettingsHooks reads the hooks map from a harness settings file for assertions.
func readSettingsHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	h, _ := doc["hooks"].(map[string]any)
	return h
}

// hookCommand returns the single command string an event's first hook group carries.
func hookCommand(t *testing.T, hooks map[string]any, event string) string {
	t.Helper()
	groups, ok := hooks[event].([]any)
	if !ok || len(groups) == 0 {
		t.Fatalf("event %q missing from settings hooks", event)
	}
	g := groups[0].(map[string]any)
	inner := g["hooks"].([]any)
	return inner[0].(map[string]any)["command"].(string)
}

// armWorkerBusy on a Claude story arms the busy record and writes the three worker busy hooks into the worktree's
// .claude/settings.json, each running `cox busy apply|retire` with the gen from $COX_BUSY_GEN and the claude-hook source.
// On the base sha there is no armWorkerBusy/worker-hook writer and the Claude card does not report busy state, so a
// dispatched Claude worker never wrote a busy record - this is the behavior that changed.
func TestArmWorkerBusyClaudeWritesHooks(t *testing.T) {
	epic := t.TempDir()
	wt := t.TempDir()
	gen, err := armWorkerBusy(epic, "s1", "claude", wt, &workspace.Policy{})
	if err != nil {
		t.Fatalf("armWorkerBusy: %v", err)
	}
	if gen == "" {
		t.Fatal("claude worker must be armed (gen returned), got empty")
	}
	if got := busy.Read(epic, "s1"); got != busy.Busy {
		t.Fatalf("busy record after arm = %q, want busy", got)
	}
	hooks := readSettingsHooks(t, filepath.Join(wt, ".claude", "settings.json"))
	prompt := hookCommand(t, hooks, "UserPromptSubmit")
	for _, want := range []string{"busy apply busy", "--source claude-hook", "--event prompt", "$COX_BUSY_GEN", "|| true"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("UserPromptSubmit command %q missing %q", prompt, want)
		}
	}
	if stop := hookCommand(t, hooks, "Stop"); !strings.Contains(stop, "busy apply idle") || !strings.Contains(stop, "--event stop") {
		t.Errorf("Stop command = %q, want busy apply idle --event stop", stop)
	}
	if end := hookCommand(t, hooks, "SessionEnd"); !strings.Contains(end, "busy retire") || !strings.Contains(end, "$COX_BUSY_GEN") {
		t.Errorf("SessionEnd command = %q, want busy retire with the env gen", end)
	}
}

// A re-run replaces cox's own busy groups without duplicating them and preserves an unrelated user hook.
func TestWorkerBusyHooksIdempotentAndPreservesUserHooks(t *testing.T) {
	wt := t.TempDir()
	dir := filepath.Join(wt, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a user hook on UserPromptSubmit that must survive.
	seed := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := writeWorkerBusyHooks("claude", wt, "/epic", "s1"); err != nil {
			t.Fatalf("write #%d: %v", i, err)
		}
	}
	hooks := readSettingsHooks(t, filepath.Join(dir, "settings.json"))
	groups := hooks["UserPromptSubmit"].([]any)
	busyCount, userKept := 0, false
	for _, g := range groups {
		cmds := g.(map[string]any)["hooks"].([]any)
		cmd := cmds[0].(map[string]any)["command"].(string)
		if strings.Contains(cmd, "busy apply") {
			busyCount++
		}
		if strings.Contains(cmd, "echo mine") {
			userKept = true
		}
	}
	if busyCount != 1 {
		t.Errorf("busy groups after two writes = %d, want 1 (idempotent, not duplicated)", busyCount)
	}
	if !userKept {
		t.Error("the user's own UserPromptSubmit hook was dropped")
	}
}

// Codex gets its worker busy hooks in .codex/hooks.json once armed (busy_verified). The default-off gating is asserted
// in story_test.go (TestDispatchArmsClaudeNotCodexByDefault); here we only check the codex hook target.
func TestWorkerBusyHooksCodexTarget(t *testing.T) {
	wt := t.TempDir()
	if err := writeWorkerBusyHooks("codex", wt, "/epic", "s1"); err != nil {
		t.Fatalf("write codex worker hooks: %v", err)
	}
	hooks := readSettingsHooks(t, filepath.Join(wt, ".codex", "hooks.json"))
	if cmd := hookCommand(t, hooks, "UserPromptSubmit"); !strings.Contains(cmd, "--source codex-hook") {
		t.Errorf("codex UserPromptSubmit command = %q, want --source codex-hook", cmd)
	}
}
