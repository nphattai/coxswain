package watch

import (
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/workspace"
)

// leaderIsPush reports whether the epic's leader harness is woken through its own hooks (card wake: push - the Claude
// Stop asyncRewake exit 2, the Pi extension's agent_settled). Firstmate never types into such a primary: the hook's
// rewake is the only attended wake (docs/supervision-protocols/claude.md:6-8@a8572f6), so the terminal doorbell is for
// pull leaders only (B-73). The harness is policy harness.leader.default, "claude" when the policy names none (the
// template default, as cmd/cox/quota_probe.go reads it). An epic with no resolvable workspace policy is not known to be
// push, so it keeps the doorbell (the pre-B-73 behaviour). Read fresh each call, so a policy edit applies next ring.
func leaderIsPush(epicDir string) bool {
	wsRoot := workspaceRoot(epicDir)
	if wsRoot == "" {
		return false
	}
	abs, err := filepath.Abs(epicDir)
	if err != nil {
		return false
	}
	pol, err := workspace.Resolve(wsRoot, filepath.Dir(filepath.Dir(abs))) // <project>/epics/<epic>
	if err != nil {
		return false
	}
	name := pol.Harness.Leader.Default
	if name == "" {
		name = "claude"
	}
	return registry.Card(name).Wake == harness.WakePush
}

// workspaceRoot walks up from dir to the nearest ancestor holding cox/workspace.json, or "" when there is none.
func workspaceRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, workspace.ControlDir, "workspace.json")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}
