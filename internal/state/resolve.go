package state

import (
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// FleetSchema is the schema id of the resolver's output.
const FleetSchema = "coxswain.fleet.v1"

// Observation is one tagged signal, exactly a coxswain.fleet.v1 observation. Value may be a string ("unknown"),
// an object, or any JSON value. Source and ObservedAt make a stale or failed probe visible instead of silently
// treating it as truth. Error carries the probe's failure text when the observation is "unknown" because the probe
// errored, so the reason is not lost; it is omitted when the probe succeeded.
type Observation struct {
	Value      any    `json:"value"`
	Source     string `json:"source"` // event | backend | hook | git | forge
	ObservedAt string `json:"observed_at"`
	Error      string `json:"error,omitempty"`
}

// StoryState is one story object in the fleet view.
type StoryState struct {
	ID           string                 `json:"id"`
	State        State                  `json:"state"`
	Attempt      int                    `json:"attempt"`
	Observations map[string]Observation `json:"observations"`
}

// Fleet is the whole coxswain.fleet.v1 document emitted by `cox state --json`.
type Fleet struct {
	Schema      string       `json:"schema"`
	GeneratedAt string       `json:"generated_at"`
	Epic        string       `json:"epic"`
	Stories     []StoryState `json:"stories"`
}

// Inputs are the observed signals the resolver merges onto the event-log state for one story. Liveness/ProbeErr/
// ProbeAt come from backend.Probe; Semantic, Git and Forge are optional pre-built observations gathered by the
// caller (the core wires the concrete adapters; this package stays free of them). A nil optional observation is
// simply omitted from the output.
type Inputs struct {
	Liveness backend.Liveness
	ProbeErr error
	ProbeAt  time.Time

	Semantic *Observation
	Git      *Observation
	Forge    *Observation
	Composer *Observation
}

// ResolveStory merges the observations onto the folded story state and returns its fleet-view object. The event log
// is authoritative for `state` and `attempt`: the resolver never transitions a story from a probe. A probe that
// errored or is Unknown surfaces as liveness "unknown" (source backend); it never becomes "gone" and never clears
// ownership (F08, P5). observed_at is the probe time so staleness is visible; a zero time falls back to now.
func ResolveStory(snap *StorySnap, in Inputs, now time.Time) StoryState {
	st := StoryState{
		ID:           snap.ID,
		State:        snap.State,
		Attempt:      snap.Attempt,
		Observations: map[string]Observation{},
	}
	if st.Attempt < 1 {
		st.Attempt = 1 // fleet.v1 requires attempt >= 1
	}

	liveness := "unknown"
	errText := ""
	if in.ProbeErr == nil {
		liveness = in.Liveness.String() // alive | settled | unknown
	} else {
		errText = in.ProbeErr.Error() // keep the failure reason instead of collapsing it to bare "unknown"
	}
	st.Observations["liveness"] = Observation{
		Value:      liveness,
		Source:     "backend",
		ObservedAt: stamp(in.ProbeAt, now),
		Error:      errText,
	}
	if in.Semantic != nil {
		st.Observations["semantic"] = *in.Semantic
	}
	if in.Git != nil {
		st.Observations["git"] = *in.Git
	}
	if in.Forge != nil {
		st.Observations["forge"] = *in.Forge
	}
	if in.Composer != nil {
		st.Observations["composer"] = *in.Composer
	}
	return st
}

// NewFleet assembles the top-level fleet document at time `now`.
func NewFleet(epic string, stories []StoryState, now time.Time) Fleet {
	return Fleet{
		Schema:      FleetSchema,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Epic:        epic,
		Stories:     stories,
	}
}

func stamp(t, now time.Time) string {
	if t.IsZero() {
		t = now
	}
	return t.UTC().Format(time.RFC3339)
}
