package integration

import (
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/worktree"
)

// A backend create failure stops the dispatch: Ensure returns an error and no path, never a fallback (F02).
func TestWorktreeEnsureStopsOnCreateFailure(t *testing.T) {
	b := fake.New()
	b.FailNext("WorktreeCreate", nil)
	wt, err := worktree.Ensure(b, "repo", "story/x", "origin/epic/e")
	if err == nil {
		t.Fatal("expected an error when create fails")
	}
	if wt.Path != "" {
		t.Fatalf("no path must be returned on failure, got %q", wt.Path)
	}
	if strings.Contains(err.Error(), "fallback") {
		t.Fatal("Ensure must not mention any fallback path")
	}
}
