package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/orca"
	"github.com/nphattai/coxswain/internal/state"
)

// A watcher tick reads the worker pane through the real orca adapter. An `orca terminal read` that hangs (and leaves a
// descendant holding its stdout) must cost the tick at most the read bound, never the hang (story cox-refresh-routing-pin,
// scope add from PR #48; firstmate fm_exec_timed, tests/fm-timeout-lib.test.sh:114@a8572f6).
func TestTickReturnsWithinOrcaReadBound(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/sh\necho \"$@\" >> \"$0.log\"\ncase \"$1 $2\" in\n\"terminal read\") sleep 60 & sleep 60 ;;\n*) echo '{\"ok\":true,\"result\":{}}' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "orca"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(r, a time.Duration) { orca.ReadBound, orca.ActBound = r, a }(orca.ReadBound, orca.ActBound)
	orca.ReadBound, orca.ActBound = 300*time.Millisecond, time.Second

	epic := t.TempDir()
	must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
	sessions := filepath.Join(epic, state.ControlDir, "sessions")
	must(t, os.MkdirAll(sessions, 0o755))
	sess, _ := json.Marshal(backend.Session{Kind: "orca", ID: "ctx_1", Handle: "term_1", Story: "s"})
	must(t, os.WriteFile(filepath.Join(sessions, "s.json"), sess, 0o644))
	c := orca.New("run_1")
	c.Plane = "terminal"
	c.Epic = epic
	w := &Watcher{EpicDir: epic, Backend: c}

	done := make(chan struct{})
	start := time.Now()
	go func() { _, _ = w.Tick(); close(done) }()
	select {
	case <-done:
		calls, _ := os.ReadFile(filepath.Join(bin, "orca.log"))
		t.Logf("Tick returned after %s; orca calls:\n%s", time.Since(start).Round(time.Millisecond), calls)
	case <-time.After(15 * time.Second):
		t.Fatal("Tick did not return within 15s: an orca terminal read that hangs holds the watcher tick")
	}
}
