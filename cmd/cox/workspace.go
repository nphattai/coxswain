package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdWorkspace implements `cox workspace init ...` and `cox workspace hooks ...`.
func cmdWorkspace(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox workspace init|hooks ...")
		return 2
	}
	switch args[0] {
	case "init":
		return cmdWorkspaceInit(args[1:])
	case "hooks":
		return cmdWorkspaceHooks(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: cox workspace init|hooks ...")
		return 2
	}
}

// cmdWorkspaceInit scaffolds cox/workspace.json and cox/policy.json (and cox/services/) under the workspace root,
// leaving any file that already exists untouched. --from-repos-md seeds workspace.json's repos[] from a v1 docs/repos.md.
func cmdWorkspaceInit(args []string) int {
	fs := flag.NewFlagSet("workspace init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "workspace root")
	fromReposMD := fs.String("from-repos-md", "", "seed repos[] from a v1 docs/repos.md")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var seed *workspace.Workspace
	if *fromReposMD != "" {
		ws, err := workspace.FromReposMD(*fromReposMD)
		if err != nil {
			return fail("%v", err)
		}
		seed = ws
	}
	created, err := workspace.Init(*root, seed)
	if err != nil {
		return fail("%v", err)
	}
	if len(created) == 0 {
		fmt.Println("workspace already initialized (nothing created)")
		return 0
	}
	for _, c := range created {
		fmt.Println("created", c)
	}
	return 0
}

// cmdWorkspaceHooks rewrites a clone's <root>/.claude/settings.json from v1 bin/hook-*.sh shims to v2 `cox hook <name>`
// commands (found on PATH, so it does not depend on $CLAUDE_PROJECT_DIR/bin), sets env.COX_EPIC and env.COX_STORY, and
// backs the original up to settings.json.v1 (never overwriting an existing backup). It keeps every other key and is
// idempotent: a second run finds no v1 shim and the same env, so it changes nothing.
func cmdWorkspaceHooks(args []string) int {
	fs := flag.NewFlagSet("workspace hooks", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "workspace clone root (holds .claude/settings.json)")
	epicDir := fs.String("epic", "", "epic directory to set as COX_EPIC")
	harnessName := fs.String("harness", "claude", "harness whose hooks to install: claude | codex")
	dryRun := fs.Bool("dry-run", false, "print the changes and write nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" || *epicDir == "" {
		return usageErr("cox workspace hooks --root <clone> --epic <dir> [--harness claude|codex] [--dry-run]")
	}
	if *harnessName == "codex" {
		return installCodexHooks(*root, *epicDir, *dryRun)
	}
	if *harnessName != "claude" {
		return usageErr("cox workspace hooks: unknown harness " + *harnessName + " (want claude | codex)")
	}
	path := filepath.Join(*root, ".claude", "settings.json")
	orig, err := os.ReadFile(path)
	if err != nil {
		return fail("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(orig, &doc); err != nil {
		return fail("parse %s: %v", path, err)
	}

	hooksChanged := rewriteHookCommands(doc["hooks"])
	envChanged := setEnv(doc, *epicDir)

	out, err := marshalSettings(doc)
	if err != nil {
		return fail("%v", err)
	}
	if bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(orig)) {
		fmt.Printf("hooks: %s already on cox hooks (no change)\n", path)
		return 0
	}
	if *dryRun {
		fmt.Printf("hooks (dry-run) would rewrite %s:\n", path)
		fmt.Printf("  hook commands changed: %v; env set: %v\n", hooksChanged, envChanged)
		fmt.Print(string(out))
		return 0
	}
	backup := path + ".v1"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := os.WriteFile(backup, orig, 0o644); err != nil {
			return fail("back up settings: %v", err)
		}
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fail("write settings: %v", err)
	}
	fmt.Printf("hooks: rewrote %s (backup %s)\n", path, backup)
	return 0
}

// installCodexHooks writes the codex leader hooks into a project-level <root>/.codex/hooks.json. Codex reads
// <repo>/.codex/hooks.json additively alongside ~/.codex/hooks.json (verified against codex-cli 0.154 on 2026-09-16,
// docs/adapters/codex.md), so the leader's hooks live in a cox-owned project file and the user's ~/.codex/hooks.json
// (Orca and agentkit entries) is never touched. The hook commands carry --harness codex so `cox hook` emits codex's
// block/continue JSON instead of Claude's exit-2, and each hook self-guards via .cox/leader (notLeaderTerminal), so a
// worker terminal that happens to sit in the same repo no-ops. Idempotent: a re-run drops the prior cox groups and
// writes the same four back, preserving any non-cox entries.
func installCodexHooks(root, epicDir string, dryRun bool) int {
	path := filepath.Join(root, ".codex", "hooks.json")
	doc := map[string]any{}
	orig, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(orig, &doc); err != nil {
			return fail("parse %s: %v", path, err)
		}
	case !os.IsNotExist(err):
		return fail("read %s: %v", path, err)
	}

	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	base := "cox hook %s --harness codex --epic " + epicDir
	for _, h := range []struct {
		event, command, matcher string
		timeout                 int
		async                   bool
	}{
		{event: "Stop", command: fmt.Sprintf(base, "stop-rewake"), timeout: 3600, async: true},
		{event: "UserPromptSubmit", command: fmt.Sprintf(base, "prompt-drain"), timeout: 20},
		{event: "SessionStart", command: fmt.Sprintf(base, "session-start") + " --story " + leaderStory, timeout: 20},
		{event: "PreCompact", command: fmt.Sprintf(base, "precompact") + " --story " + leaderStory, timeout: 20, matcher: "manual|auto"},
	} {
		hooks[h.event] = append(withoutCoxGroups(hooks[h.event]), codexHookGroup(h.command, h.matcher, h.timeout, h.async))
	}
	doc["hooks"] = hooks

	out, err := marshalSettings(doc)
	if err != nil {
		return fail("%v", err)
	}
	if bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(orig)) {
		fmt.Printf("hooks: %s already carries the cox codex hooks (no change)\n", path)
		return 0
	}
	if dryRun {
		fmt.Printf("hooks (dry-run) would write %s:\n%s", path, out)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fail("write %s: %v", path, err)
	}
	fmt.Printf("hooks: wrote %s (codex leader: Stop, UserPromptSubmit, SessionStart, PreCompact)\n", path)
	return 0
}

// codexHookGroup builds one hooks.json matcher-group with a single command hook, as map[string]any so it merges and
// re-marshals stably alongside any existing entries. matcher/async/timeout are omitted when zero-valued.
func codexHookGroup(command, matcher string, timeout int, async bool) map[string]any {
	hook := map[string]any{"type": "command", "command": command}
	if timeout > 0 {
		hook["timeout"] = timeout
	}
	if async {
		hook["async"] = true
	}
	group := map[string]any{"hooks": []any{hook}}
	if matcher != "" {
		group["matcher"] = matcher
	}
	return group
}

// withoutCoxGroups returns the event's existing matcher-groups with every cox-installed group (a command containing
// "cox hook ") dropped, so a re-run replaces cox's own groups without disturbing anyone else's. A non-array or absent
// value yields an empty slice.
func withoutCoxGroups(v any) []any {
	arr, _ := v.([]any)
	kept := make([]any, 0, len(arr))
	for _, item := range arr {
		if b, err := json.Marshal(item); err == nil && bytes.Contains(b, []byte("cox hook ")) {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

// v1HookRe matches a v1 hook shim command, capturing the hook name between hook- and .sh.
var v1HookRe = regexp.MustCompile(`hook-([a-z0-9-]+)\.sh`)

// v1HookAlias maps a v1 hook basename to its cox hook subcommand where the names differ.
var v1HookAlias = map[string]string{"session-compact": "session-start"}

// rewriteHookCommands walks the parsed hooks structure and replaces every v1 bin/hook-*.sh command with the matching
// `cox hook <name>` (via PATH). It returns whether it changed anything.
func rewriteHookCommands(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "command" {
				if s, ok := val.(string); ok {
					if m := v1HookRe.FindStringSubmatch(s); m != nil {
						name := m[1]
						if alias, ok := v1HookAlias[name]; ok {
							name = alias
						}
						cmd := "cox hook " + name
						if s != cmd {
							t[k] = cmd
							changed = true
						}
					}
				}
				continue
			}
			if rewriteHookCommands(val) {
				changed = true
			}
		}
	case []any:
		for _, item := range t {
			if rewriteHookCommands(item) {
				changed = true
			}
		}
	}
	return changed
}

// setEnv ensures doc["env"].COX_EPIC and COX_STORY are set, keeping any other env keys. Returns whether it changed.
func setEnv(doc map[string]any, epicDir string) bool {
	envMap, _ := doc["env"].(map[string]any)
	if envMap == nil {
		envMap = map[string]any{}
	}
	changed := false
	want := map[string]string{"COX_EPIC": epicDir, "COX_STORY": leaderStory}
	for k, v := range want {
		if cur, _ := envMap[k].(string); cur != v {
			envMap[k] = v
			changed = true
		}
	}
	doc["env"] = envMap
	return changed
}

// marshalSettings pretty-prints the settings doc with two-space indent and a trailing newline, matching Claude Code's
// on-disk shape. Map keys are emitted sorted, so a re-run of an already-migrated file produces byte-identical output.
func marshalSettings(doc map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	return append(b, '\n'), nil
}
