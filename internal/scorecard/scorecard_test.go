package scorecard

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

var t0 = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

func ts(min int) string { return t0.Add(time.Duration(min) * time.Minute).Format(time.RFC3339) }

func appendEv(t *testing.T, epic string, attempt int, from, to state.State, min int, ev map[string]any) {
	t.Helper()
	if err := state.Append(epic, state.Event{
		Epic: "e1", Story: "s1", Attempt: attempt, Actor: state.Leader,
		From: from, To: to, TS: ts(min), Evidence: ev, ExternalConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// A story parked at attempt 1 and completed at attempt 2 keeps two separate rows: the resume is never merged into one,
// and each attempt's wall time in working/parked is measured independently.
func TestBuildKeepsAttemptsSeparate(t *testing.T) {
	epic := t.TempDir()
	// attempt 1: dispatched, then parked.
	appendEv(t, epic, 1, state.Submitted, state.Working, 0, nil)
	appendEv(t, epic, 1, state.Working, state.Parked, 10, nil)
	// attempt 2: relaunched, then completed.
	appendEv(t, epic, 2, state.Parked, state.PendingExternal, 30, map[string]any{"verb": "relaunch"})
	appendEv(t, epic, 2, state.PendingExternal, state.Working, 31, map[string]any{"verb": "relaunch", "dispatch": "ctx_2"})
	appendEv(t, epic, 2, state.Working, state.Completed, 41, nil)

	card, err := Build(epic, "", nil, nil) // no usage/checks reader -> tokens/cost and ci unknown
	if err != nil {
		t.Fatal(err)
	}
	if len(card.Stories) != 1 || len(card.Stories[0].Attempts) != 2 {
		t.Fatalf("want 1 story with 2 attempts, got %+v", card.Stories)
	}
	a1, a2 := card.Stories[0].Attempts[0], card.Stories[0].Attempts[1]

	if a1.Attempt != 1 || a2.Attempt != 2 {
		t.Fatalf("attempt numbers = %d,%d want 1,2", a1.Attempt, a2.Attempt)
	}
	// attempt 1: 10m working, then 20m parked (until the attempt-2 event); no resume.
	wantInt(t, "a1.working", a1.WallWorkingS, 600)
	wantInt(t, "a1.parked", a1.WallParkedS, 1200)
	wantInt(t, "a1.resumes", a1.Resumes, 0)
	// attempt 2: 10m working (relaunch to completed), no parked time, one resume.
	wantInt(t, "a2.working", a2.WallWorkingS, 600)
	wantInt(t, "a2.parked", a2.WallParkedS, 0)
	wantInt(t, "a2.resumes", a2.Resumes, 1)

	// tokens/cost and ci are unknown (nil) because no usage reader and no forge CI - never a spurious 0.
	for _, p := range []*int{a1.TokensIn, a1.TokensOut, a1.CIWallInclQueueS, a2.TokensIn, a2.TokensOut} {
		if p != nil {
			t.Fatalf("unmeasured metric should be unknown (nil), got %d", *p)
		}
	}
	if a1.CostUSD != nil || a2.CostUSD != nil {
		t.Fatal("cost should be unknown without a usage reader")
	}
}

// Steers, questions and session usage are attributed to the attempt whose time window they fall in.
func TestBuildAttributesToAttempt(t *testing.T) {
	epic := t.TempDir()
	appendEv(t, epic, 1, state.Submitted, state.Working, 0, nil)
	appendEv(t, epic, 1, state.Working, state.Parked, 10, nil)
	appendEv(t, epic, 2, state.Parked, state.Working, 30, map[string]any{"verb": "relaunch"})
	appendEv(t, epic, 2, state.Working, state.Completed, 40, nil)

	// A steer in attempt 1's window and a manual inbox record; a question wake in attempt 2's window.
	writeInbox(t, epic, "s1", 1, "steer", ts(5))
	writeInbox(t, epic, "s1", 2, "fyi", ts(6)) // an fyi is not a steer
	if _, err := wake.Append(epic, wake.Wake{Epic: "e1", Story: "s1", Kind: wake.KindQuestion, TS: ts(35)}); err != nil {
		t.Fatal(err)
	}

	// A fake usage reader: one call in each window.
	usage := func(story string) ([]Usage, bool) {
		return []Usage{
			{At: t0.Add(5 * time.Minute), In: 100, Out: 10, USD: 0.5},
			{At: t0.Add(35 * time.Minute), In: 200, Out: 20, USD: 1.0},
		}, true
	}

	card, err := Build(epic, "s1", usage, nil)
	if err != nil {
		t.Fatal(err)
	}
	a1, a2 := card.Stories[0].Attempts[0], card.Stories[0].Attempts[1]

	wantInt(t, "a1.steers", a1.Steers, 1)
	wantInt(t, "a2.steers", a2.Steers, 0)
	wantInt(t, "a1.questions", a1.Questions, 0)
	wantInt(t, "a2.questions", a2.Questions, 1)
	wantInt(t, "a1.tokens_in", a1.TokensIn, 100)
	wantInt(t, "a1.tokens_out", a1.TokensOut, 10)
	wantInt(t, "a2.tokens_in", a2.TokensIn, 200)
	wantInt(t, "a2.tokens_out", a2.TokensOut, 20)
	if a2.CostUSD == nil || *a2.CostUSD != 1.0 {
		t.Fatalf("a2.cost = %v, want 1.0", a2.CostUSD)
	}
}

// ci_wall_incl_queue_s is measured from the earliest check start to the latest check completion (including queue) and
// attributed to the latest attempt; a story with no checks stays unknown, never 0.
func TestBuildCIWallInclQueue(t *testing.T) {
	epic := t.TempDir()
	appendEv(t, epic, 1, state.Submitted, state.Working, 0, nil)
	appendEv(t, epic, 1, state.Working, state.Completed, 20, nil)

	// Two checks: the first queued at t0, ran to t0+8m; the second started later but finished last at t0+12m.
	checks := func(story string) ([]forge.Check, bool) {
		return []forge.Check{
			{Name: "build", Status: "completed", Conclusion: "success", StartedAt: ts(0), CompletedAt: ts(8)},
			{Name: "test", Status: "completed", Conclusion: "success", StartedAt: ts(3), CompletedAt: ts(12)},
		}, true
	}
	card, err := Build(epic, "s1", nil, checks)
	if err != nil {
		t.Fatal(err)
	}
	a := card.Stories[0].Attempts[0]
	// earliest start t0, latest completion t0+12m -> 720s including queue.
	wantInt(t, "ci_wall", a.CIWallInclQueueS, 720)

	// No PR / no checks -> unknown, never 0.
	none := func(story string) ([]forge.Check, bool) { return nil, false }
	card2, _ := Build(epic, "s1", nil, none)
	if card2.Stories[0].Attempts[0].CIWallInclQueueS != nil {
		t.Fatal("ci_wall must be unknown (nil) with no checks")
	}
}

func wantInt(t *testing.T, name string, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is unknown, want %d", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", name, *got, want)
	}
}

func writeInbox(t *testing.T, epic, story string, seq int, urgency, at string) {
	t.Helper()
	dir := filepath.Join(epic, "inbox", story)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "schema=coxswain.inbox.v1\nat=" + at + "\nurgency=" + urgency + "\n--\nfix it\n"
	if err := os.WriteFile(filepath.Join(dir, filepathName(seq)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func filepathName(seq int) string {
	return string(rune('0'+seq/100)) + string(rune('0'+(seq/10)%10)) + string(rune('0'+seq%10)) + ".msg"
}
