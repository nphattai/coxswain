package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A live cox-watch whose epic sits under a known root is fine; one whose epic dir is gone, and one outside every root,
// are orphans (B-37: dogfood watchers that outlived their temp worktrees).
func TestOrphanWatchers(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "ws", "epics", "e1")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "epic") // exists, but not under root
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	procs := []WatchProc{
		{Pid: 100, Epic: live},    // under a known root, still exists -> not an orphan
		{Pid: 200, Epic: outside}, // exists but outside every root -> orphan
		{Pid: 300, Epic: filepath.Join(root, "ws", "epics", "gone")}, // under a root but no longer exists -> orphan
	}
	issues := orphanWatchers([]string{root}, procs)
	if len(issues) != 2 {
		t.Fatalf("expected 2 orphans, got %d: %v", len(issues), issues)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "orphan watcher pid 200") || !strings.Contains(joined, "not under any known workspace") {
		t.Errorf("out-of-root watcher must be named: %q", joined)
	}
	if !strings.Contains(joined, "orphan watcher pid 300") || !strings.Contains(joined, "no longer exists") {
		t.Errorf("gone-dir watcher must be named: %q", joined)
	}
	if strings.Contains(joined, "pid 100") {
		t.Errorf("a live in-workspace watcher must not be flagged: %q", joined)
	}
}

// parseWatchProcs pulls the pid and --epic dir out of ps output and ignores non-cox-watch lines.
func TestParseWatchProcs(t *testing.T) {
	ps := strings.Join([]string{
		"  100 /Users/x/go/bin/cox watch --epic /Users/x/Work/ws/epics/e1",
		"  200 cox watch --epic=/tmp/dogfood/epic --replace",
		"  300 /usr/bin/vim --epic notes.txt", // not cox watch -> ignored
		"  400 cox status",                    // cox but not watch -> ignored
	}, "\n")
	got := parseWatchProcs(ps)
	if len(got) != 2 {
		t.Fatalf("expected 2 cox-watch procs, got %d: %+v", len(got), got)
	}
	if got[0].Pid != 100 || got[0].Epic != "/Users/x/Work/ws/epics/e1" {
		t.Errorf("proc 0 = %+v", got[0])
	}
	if got[1].Pid != 200 || got[1].Epic != "/tmp/dogfood/epic" {
		t.Errorf("proc 1 = %+v (--epic= form not parsed?)", got[1])
	}
}
