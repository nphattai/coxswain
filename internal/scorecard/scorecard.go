// Package scorecard reports effectiveness metrics per dispatch attempt, from data already on disk. It is the
// attempt-aware Go port of v1 bin/scorecard.sh: it never merges attempts (a story parked at attempt 1 and completed at
// attempt 2 keeps two rows), and every metric is three-state - a real value, a measured 0, or "unknown" (a nil pointer)
// when its source is absent. It reads events.jsonl (per-attempt time in each state, resumes), the inbox (steers), the
// wake queue (questions), and, when a UsageReader is wired, the harness session log (tokens in/out, cost). It writes
// nothing.
package scorecard

import (
	"sort"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// Schema is the scorecard document schema id.
const Schema = "coxswain.scorecard.v1"

// Card is the whole scorecard document.
type Card struct {
	Schema      string  `json:"schema"`
	Epic        string  `json:"epic"`
	GeneratedAt string  `json:"generated_at"`
	Stories     []Story `json:"stories"`
}

// Story is one dispatched story with a row per attempt.
type Story struct {
	ID       string    `json:"id"`
	Attempts []Attempt `json:"attempts"`
}

// Attempt is the per-attempt metric block. A nil pointer means "unknown" (the source could not be read) and must render
// as unknown, never 0; a non-nil zero is a real measurement. CIWallInclQueueS is named for exactly what it measures -
// wall clock of the PR's CI including any queue wait - and is unknown when the story has no PR or no timed checks.
type Attempt struct {
	Attempt          int      `json:"attempt"`
	WallWorkingS     *int     `json:"wall_working_s"`
	WallParkedS      *int     `json:"wall_parked_s"`
	Steers           *int     `json:"steers"`
	Questions        *int     `json:"questions"`
	Resumes          *int     `json:"resumes"`
	TokensIn         *int     `json:"tokens_in"`
	TokensOut        *int     `json:"tokens_out"`
	CostUSD          *float64 `json:"cost_usd"`
	CIWallInclQueueS *int     `json:"ci_wall_incl_queue_s"`
}

// Usage is one harness API call's billed usage at a point in time, for per-attempt attribution.
type Usage struct {
	At  time.Time
	In  int     // input-side tokens billed (fresh input + cache read + cache creation)
	Out int     // output tokens
	USD float64 // list-price cost of this call
}

// UsageReader returns the harness session usage for a story, or ok=false when none is readable (a codex reduced-mode
// worker, or no session log), in which case the token and cost metrics stay unknown rather than 0.
type UsageReader func(story string) (calls []Usage, ok bool)

// ChecksReader returns a story PR's CI check runs (with timestamps), or ok=false when there is no PR or the forge could
// not be read, in which case ci_wall_incl_queue_s stays unknown rather than 0.
type ChecksReader func(story string) (checks []forge.Check, ok bool)

// Build assembles the scorecard for an epic (or one story when storyFilter is set). usage and checks may be nil, in
// which case the token/cost metrics and ci_wall_incl_queue_s respectively are unknown for every attempt.
func Build(epicDir, storyFilter string, usage UsageReader, checks ChecksReader) (Card, error) {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return Card{}, err
	}
	now := time.Now().UTC()
	snap := state.Fold(events)
	card := Card{Schema: Schema, Epic: snap.Epic, GeneratedAt: now.Format(time.RFC3339)}
	for _, s := range snap.SortedStories() {
		if storyFilter != "" && s.ID != storyFilter {
			continue
		}
		card.Stories = append(card.Stories, buildStory(epicDir, s.ID, events, usage, checks, now))
	}
	return card, nil
}

// buildStory computes every attempt row for one story.
func buildStory(epicDir, story string, events []state.Event, usage UsageReader, checks ChecksReader, now time.Time) Story {
	evs := storyTransitions(events, story)

	// State durations and resumes per attempt from the transitions.
	working := map[int]int{}
	parked := map[int]int{}
	resumes := map[int]int{}
	attemptStart := map[int]time.Time{}
	var order []int // attempts in first-seen order
	for i, e := range evs {
		if _, seen := attemptStart[e.attempt]; !seen {
			attemptStart[e.attempt] = e.at
			order = append(order, e.attempt)
		}
		end := now
		if i+1 < len(evs) {
			end = evs[i+1].at
		}
		if d := int(end.Sub(e.at).Seconds()); d > 0 {
			switch e.to {
			case state.Working:
				working[e.attempt] += d
			case state.Parked:
				parked[e.attempt] += d
			}
		}
		if e.to == state.Working && verb(e.ev) == "relaunch" {
			resumes[e.attempt]++
		}
	}
	sort.Ints(order)

	// Attempt time windows: [start(N), start(next attempt) or now) - used to attribute steers, questions and session
	// usage to the attempt they happened in.
	windows := attemptWindows(order, attemptStart, now)

	steers := steersByAttempt(epicDir, story, windows)
	questions := questionsByAttempt(epicDir, story, windows)
	calls, haveUsage := readUsage(usage, story)

	st := Story{ID: story}
	for _, n := range order {
		a := Attempt{
			Attempt:          n,
			WallWorkingS:     ptr(working[n]),
			WallParkedS:      ptr(parked[n]),
			Steers:           ptr(steers[n]),
			Questions:        ptr(questions[n]),
			Resumes:          ptr(resumes[n]),
			CIWallInclQueueS: nil, // set below on the latest attempt when a PR with checks exists
		}
		if haveUsage {
			in, out, usd := usageInWindow(calls, windows[n])
			a.TokensIn, a.TokensOut, a.CostUSD = ptr(in), ptr(out), ptrF(usd)
		}
		st.Attempts = append(st.Attempts, a)
	}
	// ci_wall_incl_queue_s belongs to the story's current PR head, so it is attributed to the latest attempt (the one
	// that owns that head); it stays unknown when there is no PR or no timed checks.
	if checks != nil && len(st.Attempts) > 0 {
		if ck, ok := checks(story); ok {
			if wall, ok := ciWallInclQueue(ck); ok {
				st.Attempts[len(st.Attempts)-1].CIWallInclQueueS = ptr(wall)
			}
		}
	}
	return st
}

// ciWallInclQueue measures a PR's CI wall clock including queue wait: the earliest check StartedAt to the latest check
// CompletedAt. It is unknown (ok=false) when no check reports both a start and a completion (still queued/running), or
// when the span is negative (clock skew). Including queue means it starts at StartedAt, which for gh is the run's
// creation/queue time, not the first executing second.
func ciWallInclQueue(checks []forge.Check) (int, bool) {
	var first, last time.Time
	haveFirst, haveLast := false, false
	for _, c := range checks {
		if t, err := time.Parse(time.RFC3339, c.StartedAt); err == nil {
			if !haveFirst || t.Before(first) {
				first, haveFirst = t, true
			}
		}
		if t, err := time.Parse(time.RFC3339, c.CompletedAt); err == nil {
			if !haveLast || t.After(last) {
				last, haveLast = t, true
			}
		}
	}
	if !haveFirst || !haveLast {
		return 0, false
	}
	secs := int(last.Sub(first).Seconds())
	if secs < 0 {
		return 0, false
	}
	return secs, true
}

// transition is a parsed story lifecycle event with its time.
type transition struct {
	at      time.Time
	attempt int
	to      state.State
	ev      state.Event
}

// storyTransitions returns the story's lifecycle events (Type == "") in chronological (file) order, each with a parsed
// timestamp. Events whose timestamp does not parse are skipped rather than crashing the whole card.
func storyTransitions(events []state.Event, story string) []transition {
	var out []transition
	for _, e := range events {
		if e.Story != story || e.Type != "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.TS)
		if err != nil {
			continue
		}
		out = append(out, transition{at: at.UTC(), attempt: e.Attempt, to: e.To, ev: e})
	}
	return out
}

// window is a half-open time interval [Start, End) an attempt occupied.
type window struct {
	Start time.Time
	End   time.Time
}

// attemptWindows maps each attempt to [its start, the next attempt's start or now).
func attemptWindows(order []int, start map[int]time.Time, now time.Time) map[int]window {
	w := map[int]window{}
	for i, n := range order {
		end := now
		if i+1 < len(order) {
			end = start[order[i+1]]
		}
		w[n] = window{Start: start[n], End: end}
	}
	return w
}

func (w window) contains(t time.Time) bool {
	return !t.Before(w.Start) && t.Before(w.End)
}

// steersByAttempt counts the story's steer inbox records (handled and unhandled) whose timestamp falls in each attempt
// window. FYIs are not steers and are not counted.
func steersByAttempt(epicDir, story string, windows map[int]window) map[int]int {
	counts := map[int]int{}
	recs, err := inbox.All(epicDir, story)
	if err != nil {
		return counts
	}
	for _, r := range recs {
		if r.Urgency != inbox.Steer {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.At)
		if err != nil {
			continue
		}
		counts[attemptOf(windows, at.UTC())]++
	}
	return counts
}

// questionsByAttempt counts the story's question wakes per attempt window.
func questionsByAttempt(epicDir, story string, windows map[int]window) map[int]int {
	counts := map[int]int{}
	wakes, err := wake.Load(epicDir)
	if err != nil {
		return counts
	}
	for _, wk := range wakes {
		if wk.Story != story || wk.Kind != wake.KindQuestion {
			continue
		}
		at, err := time.Parse(time.RFC3339, wk.TS)
		if err != nil {
			continue
		}
		counts[attemptOf(windows, at.UTC())]++
	}
	return counts
}

// usageInWindow sums the tokens and cost of the session calls that fall in one attempt window.
func usageInWindow(calls []Usage, w window) (in, out int, usd float64) {
	for _, c := range calls {
		if w.contains(c.At) {
			in += c.In
			out += c.Out
			usd += c.USD
		}
	}
	return in, out, usd
}

func readUsage(usage UsageReader, story string) ([]Usage, bool) {
	if usage == nil {
		return nil, false
	}
	return usage(story)
}

// attemptOf returns the attempt whose window contains t, or the earliest attempt when t predates every window (a steer
// that arrived before the first recorded event still belongs to the first attempt), or the latest when it is after all.
func attemptOf(windows map[int]window, t time.Time) int {
	best, bestStart := 0, time.Time{}
	first := true
	for n, w := range windows {
		if w.contains(t) {
			return n
		}
		if first || w.Start.Before(bestStart) {
			best, bestStart, first = n, w.Start, false
		}
	}
	// Not inside any window: fall to the nearest by choosing the latest window whose start is <= t, else the earliest.
	chosen, chosenStart, have := best, time.Time{}, false
	for n, w := range windows {
		if !w.Start.After(t) && (!have || w.Start.After(chosenStart)) {
			chosen, chosenStart, have = n, w.Start, true
		}
	}
	return chosen
}

// verb returns the evidence "verb" of an event ("relaunch" for a resume), or "".
func verb(e state.Event) string {
	if v, ok := e.Evidence["verb"].(string); ok {
		return v
	}
	return ""
}

func ptr(n int) *int          { return &n }
func ptrF(f float64) *float64 { return &f }
