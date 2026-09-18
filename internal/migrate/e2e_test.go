package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/env"
	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/state"
)

const fixturesDir = "../../tests/fixtures/migrate"

// copyFixtureEpic lays the named .run fixture and the working-story handoff into a fresh temp epic.
func copyFixtureEpic(t *testing.T, runFile string) string {
	t.Helper()
	epic := filepath.Join(t.TempDir(), "epic")
	if err := os.MkdirAll(filepath.Join(epic, "handoffs"), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := os.ReadFile(filepath.Join(fixturesDir, runFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, ".run"), run, 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := os.ReadFile(filepath.Join(fixturesDir, "handoff-story-working.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, "handoffs", "story-working.md"), h, 0o644); err != nil {
		t.Fatal(err)
	}
	return epic
}

// TestE2EFixtureMigrate runs the full flow on the real (anonymized) polish fixture: dry-run reads 3 stories, apply with
// a dead working dispatch lands it in pending_external, handoff is wrapped, env files are written, .run is renamed, and
// a second apply is refused.
func TestE2EFixtureMigrate(t *testing.T) {
	epic := copyFixtureEpic(t, "polish.run")
	handoffOrig, _ := os.ReadFile(filepath.Join(epic, "handoffs", "story-working.md"))

	// Dry-run: Read reflects the fixture, writes nothing.
	p, err := Read(epic)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Stories) != 3 {
		t.Fatalf("want 3 started stories, got %d: %+v", len(p.Stories), p.Stories)
	}
	if _, err := os.Stat(filepath.Join(epic, ".cox")); !os.IsNotExist(err) {
		t.Error("dry-run (Read) must not create .cox")
	}

	// The working dispatch is dead per the fake backend: the story must not keep a session.
	p.ApplyLiveness([]backend.Worker{{Dispatch: "ctx_working", State: "failed"}})
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}

	// cox state folds to the right count and per-story states.
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	snap := state.Fold(events)
	if len(snap.Stories) != 3 {
		t.Fatalf("fold has %d stories, want 3", len(snap.Stories))
	}
	want := map[string]state.State{
		"story-working": state.PendingExternal,
		"story-parked":  state.Parked,
		"story-done":    state.Completed,
	}
	for id, w := range want {
		if s := snap.Stories[id]; s == nil || s.State != w {
			t.Errorf("story %s folded to %v, want %v", id, s, w)
		}
	}
	if !snap.Stories["story-working"].PendingExternal {
		t.Error("story-working should be an unconfirmed pending_external for cox reconcile")
	}

	// No session for the dead working dispatch.
	if _, err := os.Stat(filepath.Join(epic, ".cox", "sessions", "story-working.json")); !os.IsNotExist(err) {
		t.Error("dead working dispatch must not get a session file")
	}

	// Handoff wrapped: frontmatter added, body byte-preserved, backup holds the original, Inject accepts it.
	wrapped, _ := os.ReadFile(checkpoint.Path(epic, "story-working"))
	if !strings.HasPrefix(string(wrapped), "---\nschema: "+checkpoint.Schema) {
		t.Errorf("handoff not wrapped with checkpoint frontmatter:\n%s", wrapped)
	}
	if !strings.HasSuffix(string(wrapped), string(handoffOrig)) {
		t.Error("wrapped handoff body is not byte-equal to the original")
	}
	if b, _ := os.ReadFile(checkpoint.Path(epic, "story-working") + ".v1"); string(b) != string(handoffOrig) {
		t.Error("handoff backup .v1 mismatch")
	}
	if _, err := checkpoint.Inject(epic, "story-working", 1, ""); err != nil {
		t.Errorf("Inject rejected wrapped fixture handoff: %v", err)
	}

	// Env allocation files written; last-value-wins keeps story-working's re-set env, drops story-done's emptied env.
	allocs, err := env.LoadMigratedAllocs(epic)
	if err != nil {
		t.Fatal(err)
	}
	byStory := map[string]env.MigratedAlloc{}
	for _, a := range allocs {
		byStory[a.Story] = a
	}
	if a := byStory["story-working"]; a.Port != 3800 || a.EnvFile != "/WS/epic/.env.story-working" || a.State != env.StateMigratedUnverified {
		t.Errorf("story-working alloc wrong: %+v", a)
	}
	if a, ok := byStory["story-done"]; ok && a.EnvFile != "" {
		t.Errorf("story-done env should be dropped (emptied in v1), got %q", a.EnvFile)
	}

	// .run renamed away.
	if _, err := os.Stat(filepath.Join(epic, ".run")); !os.IsNotExist(err) {
		t.Error(".run should be renamed to .run.migrated")
	}
	if _, err := os.Stat(filepath.Join(epic, ".run.migrated")); err != nil {
		t.Errorf(".run.migrated missing: %v", err)
	}

	// A second apply is refused (idempotency guard).
	if err := Apply(p); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second apply should refuse, got %v", err)
	}
}

// TestE2EEpayFixtureFolds checks the second fixture (all stories completed) folds cleanly with no live sessions.
func TestE2EEpayFixtureFolds(t *testing.T) {
	epic := filepath.Join(t.TempDir(), "epay")
	if err := os.MkdirAll(epic, 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := os.ReadFile(filepath.Join(fixturesDir, "epay.run"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, ".run"), run, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Read(epic)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	events, _, _ := state.Load(epic)
	snap := state.Fold(events)
	for _, id := range []string{"epay-cashio", "epay-services"} {
		if s := snap.Stories[id]; s == nil || s.State != state.Completed {
			t.Errorf("%s should be completed, got %+v", id, s)
		}
	}
	// No session files: both dispatches were cleared on done.
	if entries, _ := os.ReadDir(filepath.Join(epic, ".cox", "sessions")); len(entries) != 0 {
		t.Errorf("completed stories should have no sessions, got %d", len(entries))
	}
}
