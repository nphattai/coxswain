package state

import (
	"reflect"
	"testing"
)

func TestFoldLastEventWins(t *testing.T) {
	events := []Event{
		{Schema: Schema, Epic: "e", Story: "a", Attempt: 1, From: Submitted, To: Working},
		{Schema: Schema, Epic: "e", Story: "a", Attempt: 1, From: Working, To: InputRequired},
		{Schema: Schema, Epic: "e", Story: "b", Attempt: 2, From: Working, To: Completed},
	}
	snap := Fold(events)
	if snap.Epic != "e" {
		t.Errorf("epic = %q", snap.Epic)
	}
	if snap.Stories["a"].State != InputRequired {
		t.Errorf("a state = %q, want input_required", snap.Stories["a"].State)
	}
	if snap.Stories["b"].Attempt != 2 {
		t.Errorf("b attempt = %d, want 2", snap.Stories["b"].Attempt)
	}
}

// A transition awaiting external confirmation retains ownership: PendingExternal is true only when the last event
// landed on pending_external without external_confirmed.
func TestFoldPendingExternalOwnership(t *testing.T) {
	// Unconfirmed external effect: ownership retained.
	snap := Fold([]Event{
		{Schema: Schema, Story: "a", Attempt: 1, From: Working, To: PendingExternal, ExternalConfirmed: false},
	})
	if !snap.Stories["a"].PendingExternal {
		t.Error("expected PendingExternal true while external unconfirmed")
	}
	// Confirmed follow-up clears it.
	snap = Fold([]Event{
		{Schema: Schema, Story: "a", Attempt: 1, From: Working, To: PendingExternal, ExternalConfirmed: false},
		{Schema: Schema, Story: "a", Attempt: 1, From: PendingExternal, To: Parked, ExternalConfirmed: true},
	})
	if snap.Stories["a"].PendingExternal {
		t.Error("expected PendingExternal false after confirmation")
	}
	if snap.Stories["a"].State != Parked {
		t.Errorf("state = %q, want parked", snap.Stories["a"].State)
	}
}

// Fold ignores records whose schema is not the v1 id (forward compatibility).
func TestFoldSkipsUnknownSchema(t *testing.T) {
	snap := Fold([]Event{
		{Schema: "coxswain.event.v2", Story: "a", To: Completed},
		{Schema: Schema, Story: "b", Attempt: 1, To: Working},
	})
	if _, ok := snap.Stories["a"]; ok {
		t.Error("unknown-schema record should be skipped")
	}
	if snap.Stories["b"] == nil {
		t.Error("v1 record should be folded")
	}
}

// Fold is pure: folding the same events twice yields identical snapshots.
func TestFoldDeterministic(t *testing.T) {
	events := genEvents(200, 7)
	if !reflect.DeepEqual(Fold(events), Fold(events)) {
		t.Fatal("Fold is not deterministic")
	}
}
