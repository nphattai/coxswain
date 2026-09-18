package arena

import (
	"testing"

	"github.com/nphattai/coxswain/internal/state"
)

// A fresh role dispatches at the round number from submitted; a role left pending_external relaunches as a new attempt
// from its current state instead of appending a second submitted->working at the same attempt (A7).
func TestDispatchTransition(t *testing.T) {
	// No prior events: fresh dispatch at the round.
	if attempt, from := dispatchTransition(nil, 1); attempt != 1 || from != state.Submitted {
		t.Fatalf("fresh = (%d, %q), want (1, submitted)", attempt, from)
	}
	if attempt, from := dispatchTransition(nil, 2); attempt != 2 || from != state.Submitted {
		t.Fatalf("fresh round 2 = (%d, %q), want (2, submitted)", attempt, from)
	}

	// A role stuck pending_external at attempt 1 relaunches at attempt 2 from pending_external.
	stuck := &state.StorySnap{ID: "arena-adversary", State: state.PendingExternal, Attempt: 1}
	if attempt, from := dispatchTransition(stuck, 1); attempt != 2 || from != state.PendingExternal {
		t.Fatalf("relaunch = (%d, %q), want (2, pending_external)", attempt, from)
	}

	// A completed prior round redispatches as the next attempt from completed (a fresh round of the same role).
	done := &state.StorySnap{ID: "arena-adversary", State: state.Completed, Attempt: 1}
	if attempt, from := dispatchTransition(done, 2); attempt != 2 || from != state.Completed {
		t.Fatalf("next round = (%d, %q), want (2, completed)", attempt, from)
	}
}

// Run refuses a round past 3 before touching the backend or the pack (ADR 0013: at most 3 rounds).
func TestRunRefusesRound4(t *testing.T) {
	_, err := Run(nil, Options{Round: 4})
	if err == nil || err.Error() != "max 3 rounds (ADR 0013)" {
		t.Fatalf("round 4 should be refused, got: %v", err)
	}
}
