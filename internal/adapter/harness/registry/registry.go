// Package registry maps a harness name to its adapter and enforces the capability card at dispatch time. It is the one
// place that knows which harnesses have adapters (claude, codex, pi); a name that appears in a policy's options without
// an adapter (omp, opencode) resolves to no adapter, so dispatch and arena refuse it with a clear message instead of
// silently falling back to claude. It imports the concrete adapters, so only wiring code (cmd/cox, internal/arena)
// imports it, never the core.
package registry

import (
	"fmt"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/claude"
	"github.com/nphattai/coxswain/internal/adapter/harness/codex"
	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
)

// Default returns the default adapter (claude), for a control path whose harness was already validated at dispatch.
func Default() harness.Harness { return claude.New() }

// Names returns the implemented harness names in stable order, so doctor can render one card row per adapter.
func Names() []string { return []string{"claude", "codex", "pi"} }

// Adapter returns the adapter for a harness name and whether one exists. Only claude, codex, and pi are implemented.
func Adapter(name string) (harness.Harness, bool) {
	switch name {
	case "claude":
		return claude.New(), true
	case "codex":
		return codex.New(), true
	case "pi":
		return pi.New(), true
	default:
		return nil, false
	}
}

// Card returns the named adapter's capability card, or the zero card when the name has no adapter. The zero card's
// booleans are all false, so a caller reading e.g. BusyRecord on an unknown name safely gets the conservative answer.
func Card(name string) harness.Capability {
	if h, ok := Adapter(name); ok {
		return h.Card()
	}
	return harness.Capability{}
}

// LaunchArgs composes the full production argv for a launch via the named adapter. It is the single production entry
// point for adapter-owned argv (the launch seam): wiring code (cmd/cox, internal/arena) calls it and threads the
// resulting []string into the backend spawn spec as data, so the backend never imports the harness layer (ADR 0002).
// An unadaptered name errors rather than launching a bare command.
func LaunchArgs(name string, l harness.Launch) ([]string, error) {
	h, ok := Adapter(name)
	if !ok {
		return nil, fmt.Errorf("harness %q has no adapter; cannot compose launch argv", name)
	}
	return h.LaunchArgs(l), nil
}

// PrepareWorktree marks the worker's fresh worktree trusted via the named adapter's own trust mechanism before spawn,
// so a dispatched worker never stalls on an interactive workspace-trust dialog (steer 002 / DESIGN obs #2). Wiring code
// (cmd/cox, internal/arena) calls it right before Spawn. An unadaptered name errors.
func PrepareWorktree(name, wt string) error {
	h, ok := Adapter(name)
	if !ok {
		return fmt.Errorf("harness %q has no adapter; cannot prepare worktree trust", name)
	}
	return h.PrepareWorktree(wt)
}

// Notices is the single card-notice gate (the one place a capability card is inspected at dispatch). It validates a
// harness against its card for a role and returns the reduced-mode notices to print plus the unsandboxed authority
// source recorded in dispatch evidence. It errors when the harness has no adapter, its card does not list the role, or
// an unsandboxed dispatch is unauthorized.
//
// A pull wake or a manual checkpoint is a notice, not a refusal: the dispatch proceeds and the notice tells the leader
// the worker must run `cox wake wait` / write its own checkpoints.
//
// Unsandboxed gate (generic, config-driven, no harness-name branch): a harness with `Sandbox == false` runs with no
// host-filesystem confinement, so its dispatch requires recorded authority from EITHER a standing card acknowledgment
// (Capability.UnsandboxedAck, e.g. claude) OR the per-dispatch allowUnsandboxed flag (`cox story dispatch
// --allow-unsandboxed`). With neither, dispatch fails before spawn. The standing ack authorizes silently (authority
// "standing-ack", no new notice, so an already-accepted harness's dispatch is unchanged); the explicit flag authorizes
// with a prominent reduced-mode warning (authority "flag"). authority is "" for a sandboxed harness.
func Notices(name string, role harness.Role, allowUnsandboxed bool) (notices []string, authority string, err error) {
	h, ok := Adapter(name)
	if !ok {
		return nil, "", fmt.Errorf("harness %q has no adapter (implemented: %s); refusing to dispatch", name, strings.Join(Names(), ", "))
	}
	card := h.Card()
	if !roleAllowed(card, role) {
		return nil, "", fmt.Errorf("harness %q capability card does not allow role %q", name, role)
	}
	if card.Wake == harness.WakePull {
		notices = append(notices, "reduced mode: pull wake, worker must run cox wake wait")
	}
	if card.Checkpoint == harness.CheckpointManual {
		notices = append(notices, "reduced mode: manual checkpoint, worker must write cox checkpoint facts at each phase boundary")
	}
	if !card.Sandbox {
		switch {
		case card.UnsandboxedAck:
			authority = "standing-ack"
		case allowUnsandboxed:
			authority = "flag"
			notices = append(notices, fmt.Sprintf(
				"reduced mode: UNSANDBOXED - harness %q has no host-filesystem confinement (authorized by --allow-unsandboxed); git-worktree isolation only, this is not a sandbox", name))
		default:
			return nil, "", fmt.Errorf(
				"harness %q runs unsandboxed (sandbox: false) with no host-filesystem confinement; pass --allow-unsandboxed to cox story dispatch to authorize it (this authorizes, it is not a sandbox)", name)
		}
	}
	return notices, authority, nil
}

func roleAllowed(card harness.Capability, role harness.Role) bool {
	for _, r := range card.Roles {
		if r == role {
			return true
		}
	}
	return false
}
