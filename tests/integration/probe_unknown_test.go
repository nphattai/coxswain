package integration

import (
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
)

// End to end: a failed probe resolves to liveness "unknown" and does NOT transition the story. The event-log state is
// preserved and ownership is not cleared (F08, P5).
func TestProbeFailureKeepsStateUnknown(t *testing.T) {
	b := fake.New()
	b.FailNext("Probe", nil)

	live, err := b.Probe(backend.Session{ID: "s"})
	if err == nil {
		t.Fatal("expected probe error")
	}

	snap := &state.StorySnap{ID: "s", State: state.Working, Attempt: 1}
	now := time.Now().UTC()
	out := state.ResolveStory(snap, state.Inputs{Liveness: live, ProbeErr: err, ProbeAt: now}, now)

	if out.State != state.Working {
		t.Errorf("state = %q, want working (no transition on probe failure)", out.State)
	}
	if got := out.Observations["liveness"].Value; got != "unknown" {
		t.Errorf("liveness = %v, want unknown", got)
	}
}
