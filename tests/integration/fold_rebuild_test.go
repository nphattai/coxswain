// Package integration exercises the M1 core across package boundaries with the fake backend, no Orca required.
package integration

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/nphattai/coxswain/internal/state"
)

// The snapshot is a cache: deleting it and refolding the log must reproduce it exactly. On a 1,000-event seeded
// fixture, the in-memory fold equals the fold of the log loaded back from disk.
func TestFoldRebuildEqualsLog(t *testing.T) {
	dir := t.TempDir()
	events := genEvents(1000, 42)

	for _, ev := range events {
		if err := state.Append(dir, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	inMemory := state.Fold(events)

	loaded, warnings, err := state.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	rebuilt := state.Fold(loaded)

	if !reflect.DeepEqual(inMemory, rebuilt) {
		t.Fatal("snapshot rebuilt from the log does not equal the in-memory fold")
	}
}

func genEvents(n int, seed int64) []state.Event {
	r := rand.New(rand.NewSource(seed))
	states := []state.State{
		state.Submitted, state.Working, state.InputRequired, state.Parked,
		state.Completed, state.Failed, state.Canceled, state.PendingExternal,
	}
	actors := []state.Actor{state.Captain, state.Leader, state.Worker, state.Watcher}
	events := make([]state.Event, 0, n)
	for i := 0; i < n; i++ {
		ev := state.Event{
			Schema:            state.Schema,
			TS:                fmt.Sprintf("2026-09-15T%02d:%02d:%02dZ", i/3600%24, i/60%60, i%60),
			Epic:              "fixture",
			Story:             fmt.Sprintf("story-%d", r.Intn(12)),
			Attempt:           1 + r.Intn(3),
			Actor:             actors[r.Intn(len(actors))],
			From:              states[r.Intn(len(states))],
			To:                states[r.Intn(len(states))],
			ExternalConfirmed: r.Intn(2) == 0,
		}
		// An unconfirmed pending_external must carry evidence.intended_to (Append enforces it), so a valid fixture names one.
		if ev.To == state.PendingExternal && !ev.ExternalConfirmed {
			ev.Evidence = map[string]any{"intended_to": string(state.Working)}
		}
		events = append(events, ev)
	}
	return events
}
