package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/workspace"
)

// armWorkerBusy arms the busy-state record for a harness that reports its own idle/busy and writes that harness's worker
// busy hooks into the worktree, returning the incarnation gen to thread into the launch env (COX_BUSY_GEN). It returns
// "" (no arming) for a harness that does not report its own state, so such a harness is never stranded "busy" in a record
// nothing clears. It is the one place dispatch, resume, and relaunch share, so the arm decision and the hook write can
// never diverge between them.
func armWorkerBusy(epicDir, story, harnessName, wtPath string, pol *workspace.Policy) (string, error) {
	if !busyRecordEnabled(harnessName, pol) {
		return "", nil
	}
	card := registry.Card(harnessName)
	gen, err := busy.Arm(epicDir, story, harnessName, card.BusySources)
	if err != nil {
		return "", err
	}
	if err := writeWorkerBusyHooks(harnessName, wtPath, epicDir, story); err != nil {
		return "", err
	}
	return gen, nil
}

// busyRecordEnabled reports whether dispatch arms the busy record for this harness: a harness whose card says it reports
// its own state (claude, pi), or codex only when policy harness.busy_verified vouches for a wired codex-hook writer
// (default false, so codex is not armed until a captain flips it). It never branches on a harness name in a consumer -
// the card and the one policy flag decide.
func busyRecordEnabled(harnessName string, pol *workspace.Policy) bool {
	if registry.Card(harnessName).BusyRecord {
		return true
	}
	return harnessName == "codex" && pol.BusyVerified()
}

// writeWorkerBusyHooks writes the worker-side busy-state hooks into the story worktree's harness settings, so a
// dispatched worker reports its own idle/busy through the busy record (DESIGN wave-2 item 6). The hooks Apply against
// the gen armed at dispatch, threaded to the worker as $COX_BUSY_GEN in the launch env: UserPromptSubmit -> busy,
// Stop, StopFailure (Claude's API-error turn end) and SessionEnd -> idle, as firstmate's fm-spawn wires them, so an
// abnormal turn end can never strand the record busy. Every command ends with `|| true` so a refused Apply (a stale gen after a re-arm,
// or a source the harness stops trusting) never breaks the harness lifecycle, and calls ${COX_BIN:-cox} so a worker
// runs the SAME cox that launched it (the driver Orca pins on the worker PATH may predate `cox busy`). It merges into any
// existing settings like writeLeaderHooks does (idempotent: a re-dispatch replaces cox's own busy groups, keeps the
// rest). A harness with no worker-hook target is a no-op.
func writeWorkerBusyHooks(harnessName, wtPath, epicDir, story string) error {
	path, source := workerHookTarget(harnessName, wtPath)
	if path == "" {
		return nil // this harness reports busy some other way (e.g. pi's extension); no settings file to write
	}
	if err := mergeWorkerHookFile(path, busyHookGroups(harnessName, epicDir, story, source)); err != nil {
		return err
	}
	// The hook file is cox runtime, not the worker's deliverable. In THIS repo the path is gitignored, but a product repo
	// that tracks or does not ignore it would show a dirty worktree on every dispatch (the file would land in the story PR,
	// and close's landed() would keep the worktree as dirty). So when the path is not already ignored, exclude it locally
	// via the repo's shared info/exclude (never committed), keeping the worktree clean without touching .gitignore.
	excludeFromGitIfNeeded(wtPath, path)
	return nil
}

// unwireWorkerBusy retires the prior incarnation's worker busy hooks from the worktree before a relaunch arms the
// replacement (firstmate fm_control_harness_wiring_paths + fm-spawn.sh "could not retire <harness> wiring"), so a
// harness switch leaves no hook applying against a retired gen. Firstmate owns the whole settings file and deletes it;
// cox merges into it, so only cox's busy entries go, and the file only when nothing else is left in it.
func unwireWorkerBusy(harnessName, wtPath string) error {
	path, _ := workerHookTarget(harnessName, wtPath)
	if path == "" {
		return nil
	}
	orig, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(orig, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	hooksMap, _ := doc["hooks"].(map[string]any)
	for event, v := range hooksMap {
		if kept := withoutBusyGroups(v); len(kept) > 0 {
			hooksMap[event] = kept
		} else {
			delete(hooksMap, event)
		}
	}
	if len(hooksMap) == 0 {
		delete(doc, "hooks")
	}
	if len(doc) == 0 {
		return os.Remove(path)
	}
	out, err := marshalSettings(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// workerHookTarget maps a harness to its worker settings file inside the worktree and the trusted source token its
// worker hooks report as. Claude uses settings.local.json - Claude Code merges local settings and honours hooks there,
// and the ".local" file is the per-checkout, not-committed settings slot, so cox's runtime hooks never touch the repo's
// tracked .claude/settings.json. An empty path means the harness reports busy some other way (e.g. pi's extension).
func workerHookTarget(harnessName, wtPath string) (path, source string) {
	switch harnessName {
	case "claude":
		return filepath.Join(wtPath, ".claude", "settings.local.json"), "claude-hook"
	case "codex":
		return filepath.Join(wtPath, ".codex", "hooks.json"), "codex-hook"
	default:
		return "", ""
	}
}

// excludeFromGitIfNeeded keeps the worker hook file out of the worktree's git status. When the file is not already
// ignored, it appends the worktree-relative path to the repo's shared info/exclude (under the git common dir, so it
// covers every worktree and is never committed). It is idempotent and a no-op outside a git repo (a temp test dir).
func excludeFromGitIfNeeded(wtPath, absPath string) {
	rel, err := filepath.Rel(wtPath, absPath)
	if err != nil {
		return
	}
	// check-ignore exits 0 when the path is already ignored: nothing to add.
	if exec.Command("git", "-C", wtPath, "check-ignore", "-q", rel).Run() == nil {
		return
	}
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return // not a git repo: nothing to exclude
	}
	excludePath := filepath.Join(strings.TrimSpace(string(out)), "info", "exclude")
	if data, err := os.ReadFile(excludePath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == rel {
				return // already excluded
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, rel)
}

// busyHookGroups builds the per-event busy hook groups (Claude Code settings shape: event -> [{hooks:[{type,command}]}]).
// The epic and story are fixed at dispatch and rendered literally; the gen is $COX_BUSY_GEN from the launch env. Every
// closing event applies idle (firstmate fm-busy-adapter-wiring claude_hooks_semantic_lifecycle): SessionEnd is a turn
// end like Stop, not a retire, and Claude's StopFailure closes an API-error turn that fires no Stop.
func busyHookGroups(harnessName, epicDir, story, source string) map[string][]any {
	apply := func(state, event string) string {
		return fmt.Sprintf(`${COX_BIN:-cox} busy apply %s --epic %q --story %q --gen "$COX_BUSY_GEN" --source %s --event %s || true`,
			state, epicDir, story, source, event)
	}
	group := func(cmd string) []any {
		return []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}}
	}
	groups := map[string][]any{
		"UserPromptSubmit": group(apply("busy", "prompt")),
		"Stop":             group(apply("idle", "stop")),
		"SessionEnd":       group(apply("idle", "session-end")),
	}
	if harnessName == "claude" {
		groups["StopFailure"] = group(apply("idle", "stop-failure"))
	}
	return groups
}

// mergeWorkerHookFile reads the settings file (creating it when absent), strips cox's own busy hook entries from each
// event (so a re-dispatch replaces them without duplicating), appends the new groups, and writes byte-stable JSON. Any
// non-cox user entry is preserved. It reuses marshalSettings so the on-disk shape matches the leader settings writer.
func mergeWorkerHookFile(path string, groups map[string][]any) error {
	doc := map[string]any{}
	orig, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(orig, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read %s: %w", path, err)
	}
	hooksMap, _ := doc["hooks"].(map[string]any)
	if hooksMap == nil {
		hooksMap = map[string]any{}
	}
	for event, gs := range groups {
		hooksMap[event] = append(withoutBusyGroups(hooksMap[event]), gs...)
	}
	doc["hooks"] = hooksMap

	out, err := marshalSettings(doc)
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(orig)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// withoutBusyGroups strips cox's own `cox busy` hook entries from an event's matcher-groups (filtering at the hook-entry
// level, dropping a group left empty), so a re-dispatch replaces them without disturbing any user entry. Mirrors
// withoutCoxGroups but keyed on the busy command.
func withoutBusyGroups(v any) []any {
	arr, _ := v.([]any)
	kept := make([]any, 0, len(arr))
	for _, item := range arr {
		g, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		hooks, hadHooks := g["hooks"].([]any)
		if !hadHooks {
			kept = append(kept, item)
			continue
		}
		keptHooks := make([]any, 0, len(hooks))
		for _, h := range hooks {
			if isBusyHook(h) {
				continue // a cox busy hook entry
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) == 0 {
			continue
		}
		g["hooks"] = keptHooks
		kept = append(kept, g)
	}
	return kept
}

// isBusyHook reports whether a hook entry is one of cox's own busy commands (so a re-dispatch replaces it).
func isBusyHook(h any) bool {
	b, err := json.Marshal(h)
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte("busy apply")) || bytes.Contains(b, []byte("busy retire"))
}
