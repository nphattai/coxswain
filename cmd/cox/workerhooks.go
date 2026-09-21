package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
// Stop -> idle, SessionEnd -> retire. Every command ends with `|| true` so a refused Apply (a stale gen after a re-arm,
// or a source the harness stops trusting) never breaks the harness lifecycle, and calls ${COX_BIN:-cox} so a worker
// runs the SAME cox that launched it (the driver Orca pins on the worker PATH may predate `cox busy`). It merges into any
// existing settings like writeLeaderHooks does (idempotent: a re-dispatch replaces cox's own busy groups, keeps the
// rest). A harness with no worker-hook target is a no-op.
func writeWorkerBusyHooks(harnessName, wtPath, epicDir, story string) error {
	path, source := workerHookTarget(harnessName, wtPath)
	if path == "" {
		return nil // this harness reports busy some other way (e.g. pi's extension); no settings file to write
	}
	return mergeWorkerHookFile(path, busyHookGroups(epicDir, story, source))
}

// workerHookTarget maps a harness to its worker settings file inside the worktree and the trusted source token its
// worker hooks report as. An empty path means the harness has no settings-file hook target (its state comes from an
// extension, not a settings hook).
func workerHookTarget(harnessName, wtPath string) (path, source string) {
	switch harnessName {
	case "claude":
		return filepath.Join(wtPath, ".claude", "settings.json"), "claude-hook"
	case "codex":
		return filepath.Join(wtPath, ".codex", "hooks.json"), "codex-hook"
	default:
		return "", ""
	}
}

// busyHookGroups builds the three per-event busy hook groups (Claude Code settings shape: event -> [{hooks:[{type,command}]}]).
// The epic and story are fixed at dispatch and rendered literally; the gen is $COX_BUSY_GEN from the launch env.
func busyHookGroups(epicDir, story, source string) map[string][]any {
	apply := func(state, event string) string {
		return fmt.Sprintf(`${COX_BIN:-cox} busy apply %s --epic %q --story %q --gen "$COX_BUSY_GEN" --source %s --event %s || true`,
			state, epicDir, story, source, event)
	}
	retire := fmt.Sprintf(`${COX_BIN:-cox} busy retire --epic %q --story %q --gen "$COX_BUSY_GEN" || true`, epicDir, story)
	group := func(cmd string) []any {
		return []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}}
	}
	return map[string][]any{
		"UserPromptSubmit": group(apply("busy", "prompt")),
		"Stop":             group(apply("idle", "stop")),
		"SessionEnd":       group(retire),
	}
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
