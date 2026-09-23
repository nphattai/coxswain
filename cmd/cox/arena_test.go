package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
)

// arena close removes every tracked arena role worktree (branches kept) and deletes its .cox/wt record, leaving a
// non-arena story's worktree record untouched.
func TestCloseArenaWorktrees(t *testing.T) {
	epic := t.TempDir()
	if err := saveWorktree(epic, "arena-adversary", "/wt/arena-adversary", 1); err != nil {
		t.Fatal(err)
	}
	if err := saveWorktree(epic, "arena-reviewer", "/wt/arena-reviewer", 1); err != nil {
		t.Fatal(err)
	}
	// A regular story worktree record must not be touched by arena close.
	if err := saveWorktree(epic, "m6", "/wt/story-m6", 1); err != nil {
		t.Fatal(err)
	}

	b := fake.New()
	closed, err := closeArenaWorktrees(b, epic)
	if err != nil {
		t.Fatal(err)
	}
	if closed != 2 {
		t.Fatalf("closed = %d, want 2 arena worktrees", closed)
	}
	removes := 0
	for _, c := range b.Calls {
		if c == "WorktreeRemove" {
			removes++
		}
	}
	if removes != 2 {
		t.Fatalf("WorktreeRemove calls = %d, want 2", removes)
	}
	// Arena records gone, the regular story record kept.
	if _, err := os.Stat(filepath.Join(epic, ".cox", "wt", "arena-adversary")); !os.IsNotExist(err) {
		t.Fatal("arena-adversary record should be removed")
	}
	if got := readWorktree(epic, "m6"); got != "/wt/story-m6" {
		t.Fatalf("regular story record = %q, want kept", got)
	}
	if openArenaWorktrees(epic) {
		t.Fatal("no arena worktrees should remain open after close")
	}
}

// A WorktreeRemove failure keeps that record (for retry) and does not strand the others.
func TestCloseArenaWorktreesSkipsFailures(t *testing.T) {
	epic := t.TempDir()
	if err := saveWorktree(epic, "arena-adversary", "/wt/a", 1); err != nil {
		t.Fatal(err)
	}
	b := fake.New()
	b.FailNext("WorktreeRemove", nil)
	closed, err := closeArenaWorktrees(b, epic)
	if err != nil {
		t.Fatal(err)
	}
	if closed != 0 {
		t.Fatalf("closed = %d, want 0 (the one remove failed)", closed)
	}
	// The record is kept so the failed removal can be retried.
	if got := readWorktree(epic, "arena-adversary"); got != "/wt/a" {
		t.Fatalf("failed remove should keep the record, got %q", got)
	}
}

// pathRecorder is the fake backend with the worktree paths WorktreeRemove was handed.
type pathRecorder struct {
	*fake.Backend
	removed []string
}

func (p *pathRecorder) WorktreeRemove(wt backend.Worktree) error {
	p.removed = append(p.removed, wt.Path)
	return p.Backend.WorktreeRemove(wt)
}

// arena close hands the backend the decoded path of the JSON worktree record dispatch writes, never the raw JSON text
// (F-13), and still accepts a legacy plain-path record.
func TestCloseArenaWorktreesDecodesRecord(t *testing.T) {
	epic := t.TempDir()
	if err := saveWorktree(epic, "arena-adversary", "/wt/arena-adversary", 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, ".cox", "wt", "arena-reviewer"), []byte("/wt/arena-reviewer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &pathRecorder{Backend: fake.New()}
	if _, err := closeArenaWorktrees(b, epic); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.removed, ","); got != "/wt/arena-adversary,/wt/arena-reviewer" {
		t.Fatalf("WorktreeRemove paths = %q, want the decoded paths", got)
	}
}

// A malformed arena worktree record is reported and kept, never deleted as if it were empty.
func TestCloseArenaWorktreesKeepsMalformedRecord(t *testing.T) {
	epic := t.TempDir()
	rec := filepath.Join(epic, ".cox", "wt", "arena-domain")
	if err := os.MkdirAll(filepath.Dir(rec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(`{"path":`), 0o644); err != nil {
		t.Fatal(err)
	}
	closed, err := closeArenaWorktrees(fake.New(), epic)
	if err != nil || closed != 0 {
		t.Fatalf("closed=%d err=%v, want 0 and no error", closed, err)
	}
	if _, err := os.Stat(rec); err != nil {
		t.Fatalf("malformed record must be kept: %v", err)
	}
}
