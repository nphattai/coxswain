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
		Name:           "pi",
		Roles:          []harness.Role{harness.RoleLeader, harness.RoleWorker},
		Wake:           harness.WakePush,
		Checkpoint:     harness.CheckpointAuto,
		Doorbell:       true,
		Interrupt:      true,
		Telemetry:      true,
		Sandbox:        false,
		UnsandboxedAck: false, // Pi has no standing ack: an unsandboxed Pi worker dispatch requires --allow-unsandboxed every time until it passes its support gates
		Instructions:   "AGENTS.md + Agent Skills",
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

// LaunchArgs returns the argv to start Pi: `pi --model <provider/model> [--thinking <level>] --approve <policy flags>
// <prompt>`. The model is spelled `--model` (verified against pi 0.85.1 --help) and never inferred from the harness
// name; the thinking level is typed only when set (else Pi uses its default). `--approve` is Pi's per-run trust marking
// (verified: `--approve, -a  Trust project-local files for this run`): a dispatched worker cannot answer the
// interactive trust dialog, so cox trusts the cox-created worktree at launch, which also loads the packaged extension
// and AGENTS.md/skills. This is Pi's harness-specific trust mechanism (ADR 0002); cox never mutates user-level Pi
// config. The packaged extension `-e` is added when extension packaging lands. A relaunch asks the worker to inject its
// checkpoint first.
func (h *Harness) LaunchArgs(l harness.Launch) []string {
	args := []string{"pi"}
	if l.Model != "" {
		args = append(args, "--model", l.Model)
	}
	if l.Effort != "" {
		args = append(args, "--thinking", l.Effort)
	}
	args = append(args, "--approve")
	for _, f := range l.Flags {
		if f != "" {
			args = append(args, f)
		}
	}
	if prompt := harness.WorkerPrompt(l.Role, l.Brief); prompt != "" {
		args = append(args, prompt)
	}
	return args
}

// PrepareWorktree is a no-op for Pi: Pi's per-directory trust is a per-run launch flag (--approve, emitted by
// LaunchArgs), not a persisted registry, so there is nothing to pre-seed and cox never mutates user-level Pi config.
func (h *Harness) PrepareWorktree(wt string) error { return nil }

// Telemetry reports Pi token/turn usage from its session JSONL. The real parser, bound to a captured session file,
// lands in telemetry.go; until then usage is Unknown, never 0 (F11).
func (h *Harness) Telemetry(session string) (harness.Context, error) {
	return harness.Context{Known: false}, nil
}
