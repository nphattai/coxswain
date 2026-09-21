package main

import (
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/adapter/forge"
	forgefake "github.com/nphattai/coxswain/internal/adapter/forge/fake"
	"github.com/nphattai/coxswain/internal/state"
)

// The forge observer resolves a story's pr/checks/merged from the forge, tags source and observed_at, caches per head,
// resolves any retrieval error to three-state unknown, and honors --no-forge.
func TestForgeObserver(t *testing.T) {
	now := time.Now().UTC()
	snap := &state.StorySnap{ID: "s1", State: state.Working}

	// A PR with two green checks, not merged.
	green := &forgefake.Forge{F: forgefake.Fixture{
		PR:     forge.PR{Number: 12, Head: "abc"},
		Checks: []forge.Check{{Status: "completed", Conclusion: "success"}, {Status: "completed", Conclusion: "success"}},
		Merged: false,
	}}
	fo := &forgeObserver{now: now, cache: map[string]state.Observation{}, newForge: func(string) forge.Forge { return green }}
	obs := fo.observe(t.TempDir(), snap)
	if obs.Source != "forge" || obs.ObservedAt == "" {
		t.Fatalf("forge observation not tagged: %+v", obs)
	}
	v, ok := obs.Value.(forgeVal)
	if !ok {
		t.Fatalf("value is not a forgeVal: %T", obs.Value)
	}
	if v.PR == nil || *v.PR != 12 || v.Checks != "pass" || v.ChecksCount != 2 || v.Merged != "false" {
		t.Fatalf("forge value wrong: %+v", v)
	}

	// A retrieval error -> unknown, never a guessed pass; exit path stays clean (no panic).
	boom := &forgefake.Forge{F: forgefake.Fixture{Errors: map[string]string{"pr": "gh: not logged in"}}}
	fe := &forgeObserver{now: now, cache: map[string]state.Observation{}, newForge: func(string) forge.Forge { return boom }}
	if obs := fe.observe(t.TempDir(), snap); obs.Value != "unknown" || obs.Error == "" {
		t.Fatalf("error path must be unknown with a reason: %+v", obs)
	}

	// --no-forge (disabled) -> unknown without probing at all.
	fd := &forgeObserver{now: now, cache: map[string]state.Observation{}, disabled: true, newForge: func(string) forge.Forge {
		t.Fatal("disabled observer must not build a forge")
		return nil
	}}
	if obs := fd.observe(t.TempDir(), snap); obs.Value != "unknown" {
		t.Fatalf("disabled forge must be unknown: %+v", obs)
	}
}

// The forge probe is cached per head within one run, so gh is called once even when observe runs twice.
func TestForgeObserverCachesPerHead(t *testing.T) {
	calls := 0
	fo := &forgeObserver{now: time.Now().UTC(), cache: map[string]state.Observation{}, newForge: func(string) forge.Forge {
		calls++
		return &forgefake.Forge{F: forgefake.Fixture{PR: forge.PR{Number: 7}}}
	}}
	dir := t.TempDir()
	snap := &state.StorySnap{ID: "s1", State: state.Working}
	_ = fo.observe(dir, snap)
	_ = fo.observe(dir, snap)
	if calls != 1 {
		t.Fatalf("forge built %d times, want 1 (cached per head)", calls)
	}
}

// forgeSummary shortens the human line: a found PR reads as PR/checks/merged, a head with no PR reads "no-pr", gh
// missing or logged out reads "unknown: gh unavailable", any other error keeps its first line. The full error stays in
// the JSON observation, so this only affects the table (M8 A0).
func TestForgeSummaryClassifies(t *testing.T) {
	now := time.Now().UTC()
	snap := &state.StorySnap{ID: "s1", State: state.Working}
	observe := func(f *forgefake.Forge) state.Observation {
		fo := &forgeObserver{now: now, cache: map[string]state.Observation{}, newForge: func(string) forge.Forge { return f }}
		return fo.observe(t.TempDir(), snap)
	}
	cases := []struct {
		name string
		f    forgefake.Fixture
		want string
	}{
		{"pr-found", forgefake.Fixture{PR: forge.PR{Number: 12}, Checks: []forge.Check{{Status: "completed", Conclusion: "success"}}}, "PR #12 checks=pass merged=false"},
		{"no-pr", forgefake.Fixture{Errors: map[string]string{"pr": `gh pr view story/m4: exit status 1: no pull requests found for branch "story/m4"`}}, "no-pr"},
		{"logged-out", forgefake.Fixture{Errors: map[string]string{"pr": "gh pr view story/m4: exit status 4: not logged into any GitHub hosts"}}, "unknown: gh unavailable"},
		{"other", forgefake.Fixture{Errors: map[string]string{"pr": "gh pr view story/m4: exit status 1: HTTP 502"}}, "unknown: gh pr view story/m4: exit status 1: HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := observe(&forgefake.Forge{F: tc.f})
			if got := forgeSummary(obs); got != tc.want {
				t.Fatalf("forgeSummary = %q, want %q", got, tc.want)
			}
			// The JSON keeps the full error, never the shortened human form.
			if tc.f.Errors != nil && obs.Error != tc.f.Errors["pr"] {
				t.Fatalf("obs.Error = %q, want full %q", obs.Error, tc.f.Errors["pr"])
			}
		})
	}
}

func TestProbeLiveness(t *testing.T) {
	epic := t.TempDir()

	// No backend -> Unknown (M1 behavior preserved).
	if got := probeLiveness(nil, epic, "s"); got != backend.Unknown {
		t.Fatalf("nil backend = %v, want Unknown", got)
	}

	b := fake.New()
	b.Liveness = backend.Alive

	// Backend but no saved session -> Unknown.
	if got := probeLiveness(b, epic, "s"); got != backend.Unknown {
		t.Fatalf("no session = %v, want Unknown", got)
	}

	// Saved session -> the backend's real liveness.
	if err := saveSession(epic, "s", backend.Session{Kind: "fake", ID: "ctx_1"}, 1); err != nil {
		t.Fatal(err)
	}
	if got := probeLiveness(b, epic, "s"); got != backend.Alive {
		t.Fatalf("saved session = %v, want Alive", got)
	}

	// A probe error stays Unknown, never inferred gone (F08).
	b.FailNext("Probe", nil)
	if got := probeLiveness(b, epic, "s"); got != backend.Unknown {
		t.Fatalf("probe error = %v, want Unknown", got)
	}
}

func TestProbeComposer(t *testing.T) {
	epic := t.TempDir()
	working := &state.StorySnap{ID: "s", State: state.Working}
	if err := saveSession(epic, "s", backend.Session{Kind: "fake", ID: "ctx_1"}, 1); err != nil {
		t.Fatal(err)
	}

	b := fake.New()
	b.ComposerState = backend.ComposerEmpty

	// No backend -> unknown (never inferred idle).
	if got := probeComposer(nil, epic, working); got != backend.ComposerUnknown {
		t.Fatalf("nil backend = %q, want unknown", got)
	}
	// A non-working story is not probed (composer only matters while a worker holds the terminal).
	done := &state.StorySnap{ID: "s", State: state.Completed}
	if got := probeComposer(b, epic, done); got != backend.ComposerUnknown {
		t.Fatalf("completed story = %q, want unknown", got)
	}
	// A working story with a session returns the backend's real composer state.
	if got := probeComposer(b, epic, working); got != backend.ComposerEmpty {
		t.Fatalf("working story = %q, want empty", got)
	}
	// A probe error stays unknown, never inferred idle.
	b.FailNext("Composer", nil)
	if got := probeComposer(b, epic, working); got != backend.ComposerUnknown {
		t.Fatalf("composer error = %q, want unknown", got)
	}
	// A working story with no saved session stays unknown.
	if got := probeComposer(b, epic, &state.StorySnap{ID: "other", State: state.Working}); got != backend.ComposerUnknown {
		t.Fatalf("no session = %q, want unknown", got)
	}
}
