package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGH puts a gh script first on PATH.
func fakeGH(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A hung gh is bounded (the watcher's CI read runs through here each tick, cox-refresh-cli review H1): the call returns
// a timed-out error at the bound instead of hanging its caller.
func TestRunGHIsBounded(t *testing.T) {
	fakeGH(t, "sleep 30")
	prev := ghBound
	ghBound = 500 * time.Millisecond
	t.Cleanup(func() { ghBound = prev })
	began := time.Now()
	_, err := runGH(t.TempDir(), "pr", "view", "story/s1")
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("a hung gh held the caller %s", took)
	}
	if err == nil || !strings.Contains(err.Error(), "timed out after 500ms") {
		t.Fatalf("err = %v, want a timed-out error", err)
	}
}

// A failing gh keeps its exit status and first stderr line, the reason cox state classifies (M8 A0).
func TestRunGHKeepsTheStderrReason(t *testing.T) {
	fakeGH(t, "echo 'no pull requests found for branch \"story/s1\"' >&2\necho second >&2\nexit 1")
	_, err := runGH(t.TempDir(), "pr", "view", "story/s1")
	if err == nil || err.Error() != `gh pr view story/s1: exit status 1: no pull requests found for branch "story/s1"` {
		t.Fatalf("err = %v", err)
	}
}
