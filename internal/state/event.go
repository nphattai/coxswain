// Package state is the source of truth for story lifecycle: an append-only event log (coxswain.event.v1), a pure
// fold to a snapshot, and the resolver that merges the five observation sources into a fleet view (coxswain.fleet.v1).
// It performs its own file I/O for the log but depends on no backend, harness, or forge package (only the backend
// interface for the Liveness type it resolves over).
package state

// Schema is the event schema id every record carries. A reader dispatches on this and ignores records with an
// unknown schema rather than crashing (event.v1 versioning).
const Schema = "coxswain.event.v1"

// State is a story lifecycle state. The zero value is intentionally invalid; producers always set a real state.
type State string

const (
	Submitted       State = "submitted"
	Working         State = "working"
	InputRequired   State = "input_required"
	Parked          State = "parked"
	Completed       State = "completed"
	Failed          State = "failed"
	Canceled        State = "canceled"
	PendingExternal State = "pending_external"
)

// Actor is who caused a transition.
type Actor string

const (
	Captain   Actor = "captain"
	Leader    Actor = "leader"
	Worker    Actor = "worker"
	Watcher   Actor = "watcher"
	Migration Actor = "migration" // events synthesized from a v1 .run by cox migrate
)

// Non-lifecycle event types. These are additive to event.v1: they carry a Type instead of a From/To story transition
// and use the reserved story id EpicStory ("_epic"), so the fold ignores them (they are not a story's state).
const (
	DesignSigned   = "design_signed"    // captain signed DESIGN.md against a synthesis
	DesignAmended  = "design_amended"   // DESIGN.md changed after a signature, with a reason
	QuotaManualSet = "quota_manual_set" // leader set or cleared a captain-declared quota reading (M11)
	Merged         = "merged"           // cox ship merge merged a PR at its live head (item 8): evidence {pr, head, method, by}
	EpicClosed     = "epic_closed"      // cox epic close archived the epic (B-46): evidence {previous_status}
)

// EpicStory is the reserved story id for epic-scoped, non-lifecycle events (design_signed / design_amended).
const EpicStory = "_epic"

// Event is one append-only record, coxswain.event.v1. Most events are story lifecycle transitions (From -> To); an
// event with a non-empty Type is an epic-scoped fact (e.g. design_signed) that the fold skips. Evidence is a free-form
// correlation map (map[string]any) so a newer producer can add keys without a schema break.
type Event struct {
	Schema            string         `json:"schema"`
	TS                string         `json:"ts"`
	Type              string         `json:"type,omitempty"` // "" => a story lifecycle transition; else design_signed/design_amended
	Epic              string         `json:"epic"`
	Story             string         `json:"story"`
	Attempt           int            `json:"attempt"`
	Actor             Actor          `json:"actor"`
	From              State          `json:"from"`
	To                State          `json:"to"`
	Evidence          map[string]any `json:"evidence,omitempty"`
	ExternalConfirmed bool           `json:"external_confirmed"`
}
