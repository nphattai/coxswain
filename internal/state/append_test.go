package state

import (
	"fmt"
	"sync"
	"testing"
)

// 100 concurrent Appends to the same log must all land, with no lost or torn lines: Load parses every one without a
// corrupt-JSON error and returns exactly 100 events.
func TestAppendConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := Event{
				Epic: "e", Story: fmt.Sprintf("s-%03d", i), Attempt: 1,
				Actor: Worker, From: Submitted, To: Working,
			}
			if err := Append(dir, ev); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append failed: %v", err)
	}

	events, warnings, err := Load(dir)
	if err != nil {
		t.Fatalf("load after concurrent append: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d (lost or torn writes)", len(events), n)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		if seen[ev.Story] {
			t.Fatalf("duplicate story %q", ev.Story)
		}
		seen[ev.Story] = true
	}
}

// Append refuses an unconfirmed pending_external event without evidence.intended_to: reconcile would have no target to
// finish the transition toward after a crash between the two appends. A pending_external carrying intended_to is
// accepted; a non-pending transition is never subject to the rule.
func TestAppendRejectsPendingWithoutIntendedTo(t *testing.T) {
	dir := t.TempDir()
	// Missing intended_to -> rejected, and nothing is written.
	if err := Append(dir, Event{Epic: "e", Story: "s", Attempt: 1, Actor: Leader, From: Working, To: PendingExternal}); err == nil {
		t.Fatal("pending_external without intended_to must be rejected")
	}
	if events, _, _ := Load(dir); len(events) != 0 {
		t.Fatalf("a rejected event must not be written; got %d events", len(events))
	}
	// An invalid intended_to is rejected too.
	if err := Append(dir, Event{Epic: "e", Story: "s", Attempt: 1, Actor: Leader, From: Working, To: PendingExternal,
		Evidence: map[string]any{"intended_to": "nonsense"}}); err == nil {
		t.Fatal("pending_external with an invalid intended_to must be rejected")
	}
	// With a valid intended_to it is accepted.
	if err := Append(dir, Event{Epic: "e", Story: "s", Attempt: 1, Actor: Leader, From: Working, To: PendingExternal,
		Evidence: map[string]any{"intended_to": string(Parked)}}); err != nil {
		t.Fatalf("pending_external with intended_to must be accepted: %v", err)
	}
}

// Append defaults schema and ts so a caller only fills transition fields.
func TestAppendDefaultsSchemaAndTS(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, Event{Epic: "e", Story: "s", Attempt: 1, Actor: Leader, From: Submitted, To: Working}); err != nil {
		t.Fatal(err)
	}
	events, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events", len(events))
	}
	if events[0].Schema != Schema {
		t.Errorf("schema = %q, want %q", events[0].Schema, Schema)
	}
	if events[0].TS == "" {
		t.Error("ts was not defaulted")
	}
}
