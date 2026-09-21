package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cox workspace hooks --harness pi installs the project-local extension and never installs claude/codex hooks. The
// effective-card resolver then verifies it (push/auto) and downgrades to pull/manual when it is missing or tampered.
func TestWorkspaceHooksPiInstallsExtensionAndResolves(t *testing.T) {
	root := t.TempDir()
	epic := "/Users/x/epics/v2"
	if code := cmdWorkspaceHooks([]string{"--root", root, "--epic", epic, "--harness", "pi"}); code != 0 {
		t.Fatalf("pi install exit %d", code)
	}
	// The pi extension is installed project-local; no claude/codex hook files are created.
	if _, err := os.Stat(filepath.Join(root, ".pi", "extensions", "cox-pi.ts")); err != nil {
		t.Errorf("pi extension not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("pi install must not write claude settings")
	}
	if _, err := os.Stat(filepath.Join(root, ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Errorf("pi install must not write codex hooks")
	}

	// resolvePiExtension installs out-of-tree and verifies (push/auto): entry set, no downgrade notice, and NOT inside
	// the story worktree (finding #6: the worktree must stay clean).
	installDir := filepath.Join(t.TempDir(), "pi-ext")
	entry, notices := resolvePiExtension(installDir, epic)
	if entry == "" || len(notices) != 0 {
		t.Fatalf("verified extension should yield an entry and no downgrade notice, got entry=%q notices=%v", entry, notices)
	}
	if !strings.HasPrefix(entry, installDir) {
		t.Errorf("extension entry %q must live under the out-of-tree install dir %q", entry, installDir)
	}

	// When the extension cannot be installed (here: the install path is under a regular file, so mkdir fails), the
	// effective card downgrades to pull/manual through the notice path and the entry is left empty (never inferred from
	// the static push/auto card).
	fileAsRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileAsRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entryBad, noticesBad := resolvePiExtension(filepath.Join(fileAsRoot, "wt"), epic)
	if entryBad != "" {
		t.Errorf("install failure must leave the extension unset (reduced mode), got %q", entryBad)
	}
	if len(noticesBad) == 0 || !strings.Contains(noticesBad[0], "reduced mode") || !strings.Contains(noticesBad[0], "pull/manual") {
		t.Errorf("install failure must emit a pull/manual downgrade notice, got %v", noticesBad)
	}
}

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

// An upgraded v1 workspace whose settings still carry bin/hook-*.sh shims has them replaced, not doubled, so prompt-drain
// and stop-rewake never run twice.
func TestWorkspaceHooksReplacesLegacyV1Shims(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, settings, `{
  "hooks": {
    "UserPromptSubmit": [ { "hooks": [ { "type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/bin/hook-prompt-drain.sh", "timeout": 20 } ] } ],
    "Stop": [ { "hooks": [ { "type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/bin/hook-stop-rewake.sh", "asyncRewake": true, "timeout": 3600 } ] } ]
  }
}`)
	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "claude"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(settings)
	got := string(b)
	if strings.Contains(got, "bin/hook-") || strings.Contains(got, "CLAUDE_PROJECT_DIR") {
		t.Errorf("legacy v1 hook shims survived:\n%s", got)
	}
	var doc struct {
		Hooks map[string][]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	// Exactly one group per event: the cox one, not the cox one plus the surviving v1 shim.
	if len(doc.Hooks["UserPromptSubmit"]) != 1 || len(doc.Hooks["Stop"]) != 1 {
		t.Errorf("v1 shim was not replaced (duplicate groups): %s", got)
	}
	if !strings.Contains(got, "cox hook prompt-drain") {
		t.Errorf("new cox hooks not written:\n%s", got)
	}
}

// A user hook whose script name merely looks shim-like (./scripts/hook-format.sh) is NOT a cox v1 shim and must survive
// (PR#3 review round 3, finding 2).
func TestWorkspaceHooksKeepsUserShimLookalike(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, settings, `{
  "hooks": {
    "PostToolUse": [ { "hooks": [ { "type": "command", "command": "./scripts/hook-format.sh" } ] } ]
  }
}`)
	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "claude"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	got, _ := os.ReadFile(settings)
	if !strings.Contains(string(got), "./scripts/hook-format.sh") {
		t.Errorf("a user hook that merely looks shim-like was dropped:\n%s", got)
	}
}

// A user command sharing a matcher group with a cox command survives: only the cox entry is stripped and re-added, the
// user's hook is kept (PR#3 review round 2, finding 3).
func TestWorkspaceHooksKeepsUserHookSharingACoxGroup(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, settings, `{
  "hooks": {
    "UserPromptSubmit": [ { "hooks": [
      { "type": "command", "command": "cox hook prompt-drain" },
      { "type": "command", "command": "echo my-own-hook" }
    ] } ]
  }
}`)
	if code := cmdWorkspaceHooks([]string{"--root", root, "--harness", "claude"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	got, _ := os.ReadFile(settings)
	if !strings.Contains(string(got), "echo my-own-hook") {
		t.Errorf("user hook sharing a cox group was dropped:\n%s", got)
	}
	if !strings.Contains(string(got), "cox hook prompt-drain") {
		t.Errorf("cox hook not present after re-run:\n%s", got)
	}
	// Exactly one cox prompt-drain command (the old one stripped, one re-added), not two.
	if n := strings.Count(string(got), "cox hook prompt-drain"); n != 1 {
		t.Errorf("expected one cox hook prompt-drain, got %d:\n%s", n, got)
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
