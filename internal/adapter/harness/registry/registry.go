// Package registry maps a harness name to its adapter and enforces the capability card at dispatch time. It is the one
// place that knows which harnesses have adapters (claude, codex); a name that appears in a policy's options without an
// adapter (omp, opencode) resolves to no adapter, so dispatch and arena refuse it with a clear message instead of
// silently falling back to claude. It imports the concrete adapters, so only wiring code (cmd/cox, internal/arena)
// imports it, never the core.
package registry

import (
	"fmt"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/claude"
	"github.com/nphattai/coxswain/internal/adapter/harness/codex"
)

// Default returns the default adapter (claude), for a control path whose harness was already validated at dispatch.
func Default() harness.Harness { return claude.New() }

// Names returns the implemented harness names in stable order, so doctor can render one card row per adapter.
func Names() []string { return []string{"claude", "codex"} }

// Adapter returns the adapter for a harness name and whether one exists. Only claude and codex are implemented.
func Adapter(name string) (harness.Harness, bool) {
	switch name {
	case "claude":
		return claude.New(), true
	case "codex":
		return codex.New(), true
	default:
		return nil, false
	}
}

// Notices validates a harness against its capability card for a role and returns the reduced-mode notices to print. It
// returns an error when the harness has no adapter or its card does not list the role. A pull wake or a manual
// checkpoint is a notice, not a refusal: the dispatch proceeds (phase-07 item 4), and the notice tells the leader the
// worker must run `cox wake wait` / write its own checkpoints.
func Notices(name string, role harness.Role) (notices []string, err error) {
	h, ok := Adapter(name)
	if !ok {
		return nil, fmt.Errorf("harness %q has no adapter (implemented: claude, codex); refusing to dispatch", name)
	}
	card := h.Card()
	if !roleAllowed(card, role) {
		return nil, fmt.Errorf("harness %q capability card does not allow role %q", name, role)
	}
	if card.Wake == harness.WakePull {
		notices = append(notices, "reduced mode: pull wake, worker must run cox wake wait")
	}
	if card.Checkpoint == harness.CheckpointManual {
		notices = append(notices, "reduced mode: manual checkpoint, worker must write cox checkpoint facts at each phase boundary")
	}
	return notices, nil
}

func roleAllowed(card harness.Capability, role harness.Role) bool {
	for _, r := range card.Roles {
		if r == role {
			return true
		}
	}
	return false
}
