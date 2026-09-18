package state

import (
	"errors"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// The resolver never transitions a story from a probe: for every (event state) x (probe outcome), the resolved state
// equals the event-log state, and the liveness observation reflects the probe. A probe error or Unknown resolves to
// liveness "unknown" and still keeps the event state (F08: never infer gone, never clear ownership).
func TestResolveStoryTable(t *testing.T) {
	now := time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC)
	eventStates := []State{Submitted, Working, InputRequired, Parked, Completed, Failed, Canceled, PendingExternal}

	type probeCase struct {
		name     string
		liveness backend.Liveness
		err      error
		want     string
	}
	probes := []probeCase{
		{"alive", backend.Alive, nil, "alive"},
		{"settled", backend.Settled, nil, "settled"},
		{"unknown", backend.Unknown, nil, "unknown"},
		{"error", backend.Alive, errors.New("probe boom"), "unknown"}, // error wins over any liveness value
	}

	for _, es := range eventStates {
		for _, p := range probes {
			snap := &StorySnap{ID: "s", State: es, Attempt: 2}
			out := ResolveStory(snap, Inputs{Liveness: p.liveness, ProbeErr: p.err, ProbeAt: now}, now)
			if out.State != es {
				t.Errorf("[%s/%s] state = %q, want %q (no transition from probe)", es, p.name, out.State, es)
			}
			if out.Attempt != 2 {
				t.Errorf("[%s/%s] attempt = %d, want 2", es, p.name, out.Attempt)
			}
			liv := out.Observations["liveness"]
			if liv.Value != p.want {
				t.Errorf("[%s/%s] liveness = %v, want %q", es, p.name, liv.Value, p.want)
			}
			if liv.Source != "backend" {
				t.Errorf("[%s/%s] liveness source = %q, want backend", es, p.name, liv.Source)
			}
			if liv.ObservedAt == "" {
				t.Errorf("[%s/%s] liveness observed_at missing", es, p.name)
			}
		}
	}
}

// A probe error is preserved as the liveness observation's error text (not collapsed to a bare "unknown"), and a
// successful probe leaves error empty.
func TestResolveStoryKeepsProbeError(t *testing.T) {
	now := time.Now().UTC()
	snap := &StorySnap{ID: "s", State: Working, Attempt: 1}

	out := ResolveStory(snap, Inputs{Liveness: backend.Alive, ProbeErr: errors.New("worker-read: exit 1")}, now)
	liv := out.Observations["liveness"]
	if liv.Value != "unknown" || liv.Error != "worker-read: exit 1" {
		t.Fatalf("probe error should be kept, got value=%v error=%q", liv.Value, liv.Error)
	}

	ok := ResolveStory(snap, Inputs{Liveness: backend.Alive, ProbeErr: nil}, now)
	if e := ok.Observations["liveness"].Error; e != "" {
		t.Fatalf("successful probe should have empty error, got %q", e)
	}
}

// Optional observations appear only when supplied; attempt is floored at 1 for fleet.v1.
func TestResolveStoryOptionalObservations(t *testing.T) {
	now := time.Now().UTC()
	snap := &StorySnap{ID: "s", State: Working, Attempt: 0}
	git := &Observation{Value: map[string]any{"head": "abc"}, Source: "git", ObservedAt: now.Format(time.RFC3339)}
	out := ResolveStory(snap, Inputs{Liveness: backend.Unknown, Git: git}, now)

	if out.Attempt != 1 {
		t.Errorf("attempt = %d, want floored to 1", out.Attempt)
	}
	if _, ok := out.Observations["git"]; !ok {
		t.Error("git observation should be present")
	}
	if _, ok := out.Observations["forge"]; ok {
		t.Error("forge observation should be absent when not supplied")
	}
}
