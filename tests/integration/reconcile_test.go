package integration

import (
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/reconcile"
	"github.com/nphattai/coxswain/internal/state"
)

// A story left in pending_external by a spawn that succeeded then failed to persist is reconciled from the probe alone:
// an alive session becomes working, a gone (settled) session becomes failed, an unknown probe is kept, and a dry-run
// writes nothing. This is the M7 A3 success criterion (spawn ok then error -> working|failed by probe).
func TestReconcilePendingExternal(t *testing.T) {
	cases := []struct {
		name     string
		intended state.State
		live     backend.Liveness
		apply    bool
		wantTo   state.State // "" => kept in pending_external
	}{
		{"alive adopts working", state.Working, backend.Alive, true, state.Working},
		{"gone fails the dispatch", state.Working, backend.Settled, true, state.Failed},
		{"unknown probe is kept", state.Working, backend.Unknown, true, state.PendingExternal},
		{"dry-run writes nothing", state.Working, backend.Alive, false, state.PendingExternal},
		{"park confirmed on gone", state.Parked, backend.Settled, true, state.Parked},
		{"park kept while alive", state.Parked, backend.Alive, true, state.PendingExternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			epic := t.TempDir()
			seedPending(t, epic, "s1", tc.intended)
			b := fake.New()
			b.Liveness = tc.live
			sessions := map[string]backend.Session{"s1": {Kind: "fake", ID: "sess-1"}}

			decs, err := reconcile.Run(epic, b, sessions, tc.apply)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if len(decs) != 1 {
				t.Fatalf("got %d decisions, want 1", len(decs))
			}

			events, _, _ := state.Load(epic)
			got := state.Fold(events).Stories["s1"].State
			if got != tc.wantTo {
				t.Fatalf("resulting state = %q, want %q (dec=%+v)", got, tc.wantTo, decs[0])
			}
			// Confirmable-but-not-applied only when dry-run, or when the probe could not confirm.
			if tc.apply && got != state.PendingExternal && !decs[0].Applied {
				t.Fatalf("apply reached %q but decision not marked applied: %+v", got, decs[0])
			}
			if !tc.apply && decs[0].Applied {
				t.Fatal("dry-run must not mark a decision applied")
			}
		})
	}
}

// A pending_external story with no saved session cannot be probed, so it is always kept (never inferred gone, F08).
func TestReconcileNoSessionKeeps(t *testing.T) {
	epic := t.TempDir()
	seedPending(t, epic, "s1", state.Working)
	b := fake.New()
	b.Liveness = backend.Settled // even a "gone-looking" backend must not matter without a session to probe
	decs, err := reconcile.Run(epic, b, map[string]backend.Session{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if decs[0].Confirmable() || decs[0].Applied {
		t.Fatalf("no-session story must be kept: %+v", decs[0])
	}
	events, _, _ := state.Load(epic)
	if got := state.Fold(events).Stories["s1"].State; got != state.PendingExternal {
		t.Fatalf("state = %q, want pending_external", got)
	}
}

// seedPending records submitted->working (confirmed) then working->pending_external{intended_to} (unconfirmed), the
// exact shape a crash between the two appends of a side effect leaves behind.
func seedPending(t *testing.T, epic, story string, intended state.State) {
	t.Helper()
	if err := state.Append(epic, state.Event{
		Epic: "e", Story: story, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.Append(epic, state.Event{
		Epic: "e", Story: story, Attempt: 1, Actor: state.Leader,
		From: state.Working, To: state.PendingExternal,
		Evidence: map[string]any{"intended_to": string(intended), "dispatch": "sess-1"}, ExternalConfirmed: false,
	}); err != nil {
		t.Fatal(err)
	}
}
