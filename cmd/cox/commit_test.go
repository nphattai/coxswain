package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
)

// The happy path writes all three artifacts and never touches Stop.
func TestCommitDispatchPersistsAll(t *testing.T) {
	epic := t.TempDir()
	b := fake.New()
	sess := backend.Session{Kind: "fake", ID: "sess-1", Handle: "term-1"}
	if err := commitDispatch(b, epic, "e1", "s1", 1, state.Leader, sess, "/wt/s1", map[string]any{"arena_role": "adversary"}, state.Submitted); err != nil {
		t.Fatalf("commitDispatch: %v", err)
	}
	events, _, err := state.Load(epic)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v, want 1 working event", len(events), err)
	}
	ev := events[0]
	if ev.To != state.Working || ev.Evidence["dispatch"] != "sess-1" || ev.Evidence["arena_role"] != "adversary" {
		t.Fatalf("working event wrong: %+v", ev)
	}
	if _, err := os.Stat(filepath.Join(epic, ".cox", "sessions", "s1.json")); err != nil {
		t.Fatalf("session not written: %v", err)
	}
	if got := readTrimmed(filepath.Join(epic, ".cox", "wt", "s1")); got != "/wt/s1" {
		t.Fatalf("worktree file = %q, want /wt/s1", got)
	}
	for _, c := range b.Calls {
		if c == "Stop" {
			t.Fatal("Stop called on the happy path")
		}
	}
}

// When a persistence write fails and Stop confirms, the session is released and no pending_external is left behind.
func TestCommitDispatchRollbackConfirmedStop(t *testing.T) {
	epic := t.TempDir()
	// Make the session write fail: put a file where the sessions dir must be created.
	if err := os.MkdirAll(filepath.Join(epic, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, ".cox", "sessions"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := fake.New()
	b.StopConfirmed = true
	sess := backend.Session{Kind: "fake", ID: "sess-2"}
	err := commitDispatch(b, epic, "e1", "s1", 1, state.Leader, sess, "/wt/s1", nil, state.Submitted)
	if err == nil {
		t.Fatal("want error when session write fails")
	}
	if !calledStop(b) {
		t.Fatal("Stop not called on rollback")
	}
	events, _, _ := state.Load(epic)
	for _, ev := range events {
		if ev.To == state.PendingExternal {
			t.Fatalf("confirmed stop should not record pending_external: %+v", ev)
		}
	}
}

// When Stop does not confirm, ownership is retained: a pending_external event carrying the dispatch id is recorded.
func TestCommitDispatchRollbackUnconfirmedStopKeepsSession(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, ".cox", "sessions"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := fake.New()
	b.StopConfirmed = false
	sess := backend.Session{Kind: "fake", ID: "sess-3"}
	err := commitDispatch(b, epic, "e1", "s1", 1, state.Leader, sess, "/wt/s1", nil, state.Submitted)
	if err == nil || !strings.Contains(err.Error(), "pending_external") {
		t.Fatalf("want pending_external error, got %v", err)
	}
	events, _, _ := state.Load(epic)
	found := false
	for _, ev := range events {
		if ev.To == state.PendingExternal && !ev.ExternalConfirmed && ev.Evidence["dispatch"] == "sess-3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pending_external event with the session id: %+v", events)
	}
}

func calledStop(b *fake.Backend) bool {
	for _, c := range b.Calls {
		if c == "Stop" {
			return true
		}
	}
	return false
}
