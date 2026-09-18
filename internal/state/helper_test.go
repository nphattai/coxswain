package state

import (
	"fmt"
	"math/rand"
)

// genEvents produces n deterministic pseudo-random v1 events across a handful of stories, seeded so the fixture is
// reproducible. Attempts never drop below 1 and every state is a valid enum member.
func genEvents(n int, seed int64) []Event {
	r := rand.New(rand.NewSource(seed))
	states := []State{Submitted, Working, InputRequired, Parked, Completed, Failed, Canceled, PendingExternal}
	actors := []Actor{Captain, Leader, Worker, Watcher}
	events := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		story := fmt.Sprintf("story-%d", r.Intn(12))
		to := states[r.Intn(len(states))]
		events = append(events, Event{
			Schema:            Schema,
			TS:                fmt.Sprintf("2026-09-15T00:%02d:%02dZ", i/60%60, i%60),
			Epic:              "fixture",
			Story:             story,
			Attempt:           1 + r.Intn(3),
			Actor:             actors[r.Intn(len(actors))],
			From:              states[r.Intn(len(states))],
			To:                to,
			ExternalConfirmed: r.Intn(2) == 0,
		})
	}
	return events
}
