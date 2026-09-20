// Package pi is the Pi (@earendil-works/pi-coding-agent) harness adapter. Pi targets push wake and automatic
// checkpoints through a project-local Coxswain extension (see docs/adapters/pi.md); those card values are release
// gates proven by the extension lifecycle tests and dogfood, not assumptions. Pi reads AGENTS.md and Agent Skills
// natively, runs with the user's permissions (no tool sandbox), and saves sessions as JSONL. It satisfies
// harness.Harness.
//
// This file holds the card, packaging, and the launch/telemetry entry points. The production launch argv
// (model/thinking/trust flags) lands with the adapter-owned launch seam; telemetry JSONL parsing lands in
// telemetry.go.
package pi

import (
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// Harness is the Pi adapter.
type Harness struct{}

// New returns a Pi adapter.
func New() *Harness { return &Harness{} }

// Card is the Pi capability contract, mirrored exactly by docs/adapters/pi.md. Sandbox is false: Pi runs with the
// user's permissions and provides no host-filesystem confinement, so an unsandboxed Pi worker dispatch requires
// explicit recorded authority at the card-notice gate (DESIGN §4). Wake=push and checkpoint=auto are delivered by the
// packaged Pi extension.
func (h *Harness) Card() harness.Capability {
	return harness.Capability{
		Name:         "pi",
		Roles:        []harness.Role{harness.RoleLeader, harness.RoleWorker},
		Wake:         harness.WakePush,
		Checkpoint:   harness.CheckpointAuto,
		Doorbell:     true,
		Interrupt:    true,
		Telemetry:    true,
		Sandbox:      false,
		Instructions: "AGENTS.md + Agent Skills",
	}
}

// Package renders instructions for Pi: Pi discovers AGENTS.md and Agent Skills natively from the worktree, so like
// Codex, Package only needs AGENTS.md present in dst (a no-op when absent). Skills are delivered out of band.
func (h *Harness) Package(role harness.Role, dst string) error {
	if _, err := os.Stat(filepath.Join(dst, "AGENTS.md")); err != nil {
		return nil // no job description to package; not an error
	}
	return nil
}

// LaunchArgs returns the argv to start Pi in wt. The full production argv (explicit provider/model, thinking level,
// trust/resource flags, and the packaged extension) is composed by the adapter-owned launch seam; this returns the
// worker's story-file prompt so the seam has a stable base. A relaunch asks the worker to inject its checkpoint first.
func (h *Harness) LaunchArgs(role harness.Role, wt string, b harness.Brief) []string {
	args := []string{"pi"}
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

// Telemetry reports Pi token/turn usage from its session JSONL. The real parser, bound to a captured session file,
// lands in telemetry.go; until then usage is Unknown, never 0 (F11).
func (h *Harness) Telemetry(session string) (harness.Context, error) {
	return harness.Context{Known: false}, nil
}
