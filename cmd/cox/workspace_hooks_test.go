package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cox workspace hooks --harness claude creates .claude/settings.json when absent, writes the four hook groups as
// `cox hook <name>` commands with no epic binding, and is idempotent.
func TestWorkspaceHooksClaudeCreatesWhenAbsentAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")

	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "claude"}); code != 0 {
		t.Fatalf("first run exit %d", code)
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("settings not created: %v", err)
	}
	got := string(b)
	for _, want := range []string{"cox hook prompt-drain", "cox hook stop-rewake", "cox hook precompact", "cox hook session-start"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "COX_EPIC") {
		t.Errorf("claude hooks must carry no epic binding:\n%s", got)
	}
	// The group shapes come from hooks/hooks.json: Stop keeps its long async timeout, SessionStart its matcher.
	if !strings.Contains(got, "3600") || !strings.Contains(got, "compact|resume") {
		t.Errorf("group shapes not carried from hooks.json:\n%s", got)
	}

	// Idempotent: a second run is byte-identical.
	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "claude"}); code != 0 {
		t.Fatalf("second run exit %d", code)
	}
	if b2, _ := os.ReadFile(settings); string(b2) != got {
		t.Errorf("second run changed the file:\n--- first ---\n%s\n--- second ---\n%s", got, string(b2))
	}
}

// A pre-existing user entry (permissions, or a non-cox hook group) survives the merge.
func TestWorkspaceHooksMergesUserEntries(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, settings, `{
  "permissions": { "allow": ["Bash(git status)"] },
  "hooks": { "PostToolUse": [ { "hooks": [ { "type": "command", "command": "echo user" } ] } ] }
}`)
	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "claude"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var doc map[string]any
	b, _ := os.ReadFile(settings)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["permissions"]; !ok {
		t.Error("unrelated permissions key was dropped")
	}
	if !strings.Contains(string(b), "echo user") {
		t.Errorf("pre-existing non-cox hook was dropped:\n%s", b)
	}
	if !strings.Contains(string(b), "cox hook prompt-drain") {
		t.Errorf("cox hooks not merged in:\n%s", b)
	}
}

// The codex path writes a project-level .codex/hooks.json with the four leader hooks carrying --harness codex (and the
// codex async key), no epic binding, preserving a pre-existing non-cox entry, and is idempotent.
func TestWorkspaceHooksCodexCreatesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	pre := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"echo other"}]}]}}`
	mustWrite(t, filepath.Join(root, ".codex", "hooks.json"), pre)

	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "codex"}); code != 0 {
		t.Fatalf("codex install exit %d", code)
	}
	path := filepath.Join(root, ".codex", "hooks.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"cox hook prompt-drain --harness codex",
		"cox hook stop-rewake --harness codex",
		"cox hook precompact --harness codex",
		"cox hook session-start --harness codex",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "COX_EPIC") {
		t.Errorf("codex hooks must carry no epic binding:\n%s", got)
	}
	if !strings.Contains(got, `"async"`) {
		t.Errorf("codex Stop hook must use the async key:\n%s", got)
	}
	if !strings.Contains(got, "echo other") {
		t.Errorf("pre-existing non-cox entry was not preserved:\n%s", got)
	}

	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "codex"}); code != 0 {
		t.Fatalf("second codex install exit %d", code)
	}
	if b2, _ := os.ReadFile(path); string(b2) != got {
		t.Errorf("second run changed the file")
	}
}
