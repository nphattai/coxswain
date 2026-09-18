// Package codex is the Codex harness adapter: no hooks, so wake is pull (the leader drains each turn and blocks on
// `cox wake wait` when idle), checkpoints are manual (the worker writes them at phase boundaries), and telemetry is
// unknown. It satisfies harness.Harness. See docs/adapters/codex.md.
package codex

import (
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// Harness is the Codex adapter.
type Harness struct{}

// New returns a Codex adapter.
func New() *Harness { return &Harness{} }

func (h *Harness) Card() harness.Capability {
	return harness.Capability{
		Name:         "codex",
		Roles:        []harness.Role{harness.RoleLeader, harness.RoleWorker},
		Wake:         harness.WakePull,
		Checkpoint:   harness.CheckpointManual,
		Doorbell:     true,
		Interrupt:    true,
		Telemetry:    false,
		Sandbox:      true,
		Instructions: "AGENTS.md + markdown skills",
	}
}

// Package renders instructions for Codex: it reads AGENTS.md natively, so Package only ensures AGENTS.md exists in dst
// (it is a no-op when absent). Skills are delivered as Markdown out of band.
func (h *Harness) Package(role harness.Role, dst string) error {
	if _, err := os.Stat(filepath.Join(dst, "AGENTS.md")); err != nil {
		return nil // no job description to package; not an error
	}
	return nil
}

// LaunchArgs returns the argv to start Codex in wt. Wake is pull, so the leader's idle turn ends on `cox wake wait`;
// that discipline lives in AGENTS.md, not in a launch flag. A relaunch asks the worker to inject the checkpoint first
// via the brief's opening line (Codex has no SessionStart hook to do it automatically).
func (h *Harness) LaunchArgs(role harness.Role, wt string, b harness.Brief) []string {
	args := []string{"codex"}
	if role == harness.RoleWorker && b.StoryPath != "" {
		prompt := "Your task is the story file " + b.StoryPath + " - read it in full and follow its Working rules exactly."
		if b.InjectCheckpoint {
			prompt = "Read your checkpoint with `cox checkpoint inject` first, then continue from Next action. " + prompt
		}
		if b.Note != "" {
			prompt += " Progress note from your previous attempt: " + b.Note
		}
		args = append(args, prompt)
	}
	return args
}

// Telemetry is always Unknown for Codex: it exposes no session log to read (F11: unknown, never 0).
func (h *Harness) Telemetry(session string) (harness.Context, error) {
	return harness.Context{Known: false}, nil
}
