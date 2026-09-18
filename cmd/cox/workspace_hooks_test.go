package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v1Settings mirrors a downstream clone's .claude/settings.json (paths hidden): two hooks via $CLAUDE_PROJECT_DIR/bin, and
// an unrelated key that must survive.
const v1Settings = `{
  "permissions": { "allow": ["Bash(git status)"] },
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/bin/hook-prompt-drain.sh", "timeout": 20 } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/bin/hook-stop-rewake.sh", "asyncRewake": true, "timeout": 3600 } ] }
    ]
  }
}
`

func TestWorkspaceHooksRewritesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, settings, v1Settings)
	epic := "/epics/v2"

	if code := cmdWorkspaceHooks([]string{"--root", root, "--epic", epic}); code != 0 {
		t.Fatalf("first run exit %d", code)
	}

	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	// v1 shims are gone, replaced by cox hook commands via PATH.
	if strings.Contains(got, "bin/hook-") || strings.Contains(got, "CLAUDE_PROJECT_DIR") {
		t.Errorf("v1 hook shims survived:\n%s", got)
	}
	for _, want := range []string{"cox hook prompt-drain", "cox hook stop-rewake"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// env set, other keys kept, hook options preserved.
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	envMap, _ := doc["env"].(map[string]any)
	if envMap["COX_EPIC"] != epic || envMap["COX_STORY"] != leaderStory {
		t.Errorf("env not set: %+v", envMap)
	}
	if _, ok := doc["permissions"]; !ok {
		t.Error("unrelated permissions key was dropped")
	}
	if !strings.Contains(got, "asyncRewake") || !strings.Contains(got, "3600") {
		t.Error("Stop hook options (asyncRewake/timeout) were dropped")
	}
	// Backup is the exact original.
	if bak, err := os.ReadFile(settings + ".v1"); err != nil || string(bak) != v1Settings {
		t.Errorf("backup mismatch: err=%v", err)
	}

	// Idempotent: a second run changes nothing on disk and does not touch the backup.
	if code := cmdWorkspaceHooks([]string{"--root", root, "--epic", epic}); code != 0 {
		t.Fatalf("second run exit %d", code)
	}
	b2, _ := os.ReadFile(settings)
	if string(b2) != got {
		t.Errorf("second run changed the file:\n--- first ---\n%s\n--- second ---\n%s", got, b2)
	}
}

// The codex path writes a project-level .codex/hooks.json with the four leader hooks (never touching ~/.codex),
// carries --harness codex on each command, and is idempotent: a re-run leaves the file byte-identical and preserves a
// pre-existing non-cox entry.
func TestWorkspaceHooksCodexInstallsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	epic := "/Users/x/epics/v2"
	// A pre-existing non-cox project entry must survive the merge.
	pre := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"echo other"}]}]}}`
	mustWrite(t, filepath.Join(root, ".codex", "hooks.json"), pre)

	if code := cmdWorkspaceHooks([]string{"--root", root, "--epic", epic, "--harness", "codex"}); code != 0 {
		t.Fatalf("codex install exit %d", code)
	}
	path := filepath.Join(root, ".codex", "hooks.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse: %v\n%s", err, got)
	}
	for event, wantCmd := range map[string]string{
		"Stop":             "cox hook stop-rewake --harness codex --epic " + epic,
		"UserPromptSubmit": "cox hook prompt-drain --harness codex --epic " + epic,
		"SessionStart":     "cox hook session-start --harness codex --epic " + epic + " --story " + leaderStory,
		"PreCompact":       "cox hook precompact --harness codex --epic " + epic + " --story " + leaderStory,
	} {
		groups, ok := doc.Hooks[event]
		if !ok || len(groups) == 0 || len(groups[0].Hooks) == 0 {
			t.Fatalf("event %s missing:\n%s", event, got)
		}
		if groups[0].Hooks[0].Command != wantCmd {
			t.Errorf("event %s command = %q, want %q", event, groups[0].Hooks[0].Command, wantCmd)
		}
	}
	if doc.Hooks["Stop"][0].Hooks[0].Async != true || doc.Hooks["Stop"][0].Hooks[0].Timeout != 3600 {
		t.Errorf("Stop hook must be async with a long timeout: %+v", doc.Hooks["Stop"][0].Hooks[0])
	}
	if doc.Hooks["PreCompact"][0].Matcher != "manual|auto" {
		t.Errorf("PreCompact matcher = %q, want manual|auto", doc.Hooks["PreCompact"][0].Matcher)
	}
	if len(doc.Hooks["PostToolUse"]) != 1 || doc.Hooks["PostToolUse"][0].Hooks[0].Command != "echo other" {
		t.Errorf("pre-existing non-cox entry was not preserved:\n%s", got)
	}
	// User-level file is never touched (we only ever wrote under root/.codex).
	if strings.Contains(got, "orchestration") {
		t.Errorf("unexpected user-level content leaked into project hooks:\n%s", got)
	}

	// Idempotent: a second run is byte-identical.
	if code := cmdWorkspaceHooks([]string{"--root", root, "--epic", epic, "--harness", "codex"}); code != 0 {
		t.Fatalf("second codex install exit %d", code)
	}
	if b2, _ := os.ReadFile(path); string(b2) != got {
		t.Errorf("second run changed the file:\n--- first ---\n%s\n--- second ---\n%s", got, string(b2))
	}
}

// A dry-run writes nothing and creates no backup.
func TestWorkspaceHooksDryRun(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, settings, v1Settings)
	if code := cmdWorkspaceHooks([]string{"--root", root, "--epic", "/epics/v2", "--dry-run"}); code != 0 {
		t.Fatalf("dry-run exit %d", code)
	}
	if b, _ := os.ReadFile(settings); string(b) != v1Settings {
		t.Error("dry-run modified settings.json")
	}
	if _, err := os.Stat(settings + ".v1"); !os.IsNotExist(err) {
		t.Error("dry-run wrote a backup")
	}
}
