// Package reconcile finishes stories left in pending_external by a crash between the two appends of a side effect. For
// each such story it reads evidence.intended_to and probes the story's saved session, then decides - purely, from the
// probe - whether the intended transition can be confirmed. It never re-issues the side effect (Stop/Spawn) and never
// writes on an unknown probe (F08, P5): an unconfirmable story is kept in pending_external with the reason. This is the
// fleet-wide counterpart to control.Reconcile's single-story finish; cmd/cox wires the backend and sessions, and the
// watcher runs one pass every N ticks with apply.
package reconcile

import (
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/state"
)

// Decision is the reconciler's plan for one pending_external story. To is the confirmable transition target, or "" when
// the probe could not confirm the intended transition (the story is kept). Applied is true only when Run wrote it.
type Decision struct {
	Story      string           `json:"story"`
	IntendedTo state.State      `json:"intended_to"`
	Liveness   backend.Liveness `json:"-"`
	LivenessS  string           `json:"liveness"`
	To         state.State      `json:"to,omitempty"`
	Applied    bool             `json:"applied"`
	Reason     string           `json:"reason"`
}

// Confirmable reports whether the probe confirmed a transition the reconciler can write.
func (d Decision) Confirmable() bool { return d.To != "" }

// plan decides the transition target purely from the intended state and the probe result. It returns ("", reason) when
// nothing can be confirmed, so the story is kept in pending_external. Rules (phase-07 table):
//   - intended working: alive -> adopt as working; gone (settled) -> the dispatch never established, so failed; unknown -> keep.
//   - intended parked|completed|canceled|failed: gone -> confirm the intended state; alive -> keep (the effect did not
//     take); unknown -> keep.
func plan(intended state.State, live backend.Liveness, hasSession bool) (to state.State, reason string) {
	if !hasSession {
		return "", "no saved session to probe; left pending_external"
	}
	switch intended {
	case state.Working:
		switch live {
		case backend.Alive:
			return state.Working, "session alive: adopted as working"
		case backend.Settled:
			return state.Failed, "session gone: dispatch never established"
		default:
			return "", "probe unknown; left pending_external"
		}
	case state.Parked, state.Completed, state.Canceled, state.Failed:
		switch live {
		case backend.Settled:
			return intended, "session gone: " + string(intended) + " confirmed"
		case backend.Alive:
			return "", "session still alive; cannot confirm " + string(intended) + ", left pending_external"
		default:
			return "", "probe unknown; left pending_external"
		}
	default:
		return "", "invalid or missing intended_to; left pending_external"
	}
}

// Run plans (and, with apply, writes) the reconciliation of every pending_external story in the epic. It probes each
// story's saved session and decides purely from the probe; with apply it appends the confirmed transition
// (external_confirmed, actor watcher) only for the confirmable cases and leaves the rest untouched. It never removes a
// branch and never re-issues a side effect. A nil backend or a story with no saved session yields unknown -> keep.
//
// ponytail: probes each pending story's own session directly (Backend.Probe); a fleet-wide worker-list reconciliation
// is not needed while every pending_external event carries its dispatch. Add worker-list only if orphan sessions with
// no story event appear.
func Run(epicDir string, b backend.Backend, sessions map[string]backend.Session, apply bool) ([]Decision, error) {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return nil, err
	}
	snap := state.Fold(events)
	var out []Decision
	for _, s := range snap.SortedStories() {
		if s.State != state.PendingExternal || !s.PendingExternal {
			continue
		}
		intended, _ := s.LastEvent.Evidence["intended_to"].(string)
		sess, hasSession := sessions[s.ID]
		live := backend.Unknown
		if hasSession && b != nil {
			if l, perr := b.Probe(sess); perr == nil {
				live = l
			}
		}
		d := Decision{Story: s.ID, IntendedTo: state.State(intended), Liveness: live, LivenessS: live.String()}
		d.To, d.Reason = plan(state.State(intended), live, hasSession && b != nil)
		if apply && d.Confirmable() {
			ev := state.Event{
				Epic: filepath.Base(epicDir), Story: s.ID, Attempt: s.Attempt, Actor: state.Watcher,
				From: state.PendingExternal, To: d.To,
				Evidence: map[string]any{"note": "reconciled: " + d.Reason}, ExternalConfirmed: true,
			}
			if d.To == state.Failed || d.To == state.Canceled {
				ev.Evidence["reason"] = "reconciled: " + d.Reason
			}
			if err := state.Append(epicDir, ev); err != nil {
				return out, err
			}
			d.Applied = true
		}
		out = append(out, d)
	}
	return out, nil
}
