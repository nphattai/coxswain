package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/supervision"
	"github.com/nphattai/coxswain/internal/wake"
)

// checkRig is an epic with one registered check whose body is script, and a watcher on a fake clock.
func checkRig(t *testing.T, script string) (*Watcher, string, *time.Time) {
	t.Helper()
	epic := t.TempDir()
	control := filepath.Join(epic, state.ControlDir)
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(control, "probe.check.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := supervision.Register(control, "probe"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	w := &Watcher{EpicDir: epic, Backend: fake.New(), CheckInterval: time.Minute, Now: func() time.Time { return now }}
	return w, path, &now
}

func checkWakes(t *testing.T, epic string) []wake.Wake {
	t.Helper()
	ws, err := wake.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	var out []wake.Wake
	for _, w := range ws {
		if w.Kind == wake.KindCheck {
			out = append(out, w)
		}
	}
	return out
}

func TestCheckPassRunsInWatcherEnvOnItsCadence(t *testing.T) {
	t.Setenv("COX_TEST_CHECK_ENV", "kept")
	w, path, now := checkRig(t, "#!/usr/bin/env bash\nprintf 'fired %s\\n\\n' \"$COX_TEST_CHECK_ENV\"\necho noise >&2\n")
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	ws := checkWakes(t, w.EpicDir)
	if len(ws) != 1 || ws[0].Note != "check: "+path+": fired kept" || !wake.IsUrgent(ws[0].Kind) {
		t.Fatalf("want one urgent check wake with the trimmed stdout in the watcher env, got %+v", ws)
	}
	*now = now.Add(30 * time.Second)
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if n := len(checkWakes(t, w.EpicDir)); n != 1 {
		t.Fatalf("the check ran again inside its interval: %d wakes", n)
	}
	*now = now.Add(time.Minute)
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if n := len(checkWakes(t, w.EpicDir)); n != 2 {
		t.Fatalf("the check did not run again after its interval: %d wakes", n)
	}
}

func TestCheckPassRejectsDriftedBytesWithoutRunning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	w, path, _ := checkRig(t, "#!/usr/bin/env bash\necho ok\n")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\ntouch "+marker+"\necho drifted\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	ws := checkWakes(t, w.EpicDir)
	if len(ws) != 1 || ws[0].Note != "check: rejected unauthenticated state checks: "+path {
		t.Fatalf("want one rejection naming the drifted check, got %+v", ws)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a check whose bytes drifted from its registration was run")
	}
}

func TestCheckPassTimeoutKillsTheCheckGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	w, _, _ := checkRig(t, "#!/usr/bin/env bash\necho partial\n(sleep 1; touch "+marker+") &\nsleep 30\n")
	w.CheckTimeout = 300 * time.Millisecond
	start := time.Now()
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("a hung check held the tick for %v", d)
	}
	if ws := checkWakes(t, w.EpicDir); len(ws) != 1 || !strings.HasSuffix(ws[0].Note, ": partial") {
		t.Fatalf("the output captured before the timeout was not reported: %+v", ws)
	}
	time.Sleep(1500 * time.Millisecond) // past the background child's own deadline: it must already be dead
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a timed-out check's process group survived")
	}
}

// A check that exits leaving a background child still reports its output at once, and the child is killed
// (fm run_check_capture captures to a file; fm_active_check_stop kills the group).
func TestCheckPassBackgroundChildNeitherSwallowsOutputNorSurvives(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	w, path, _ := checkRig(t, "#!/usr/bin/env bash\n(sleep 1; touch "+marker+") &\necho merged\n")
	start := time.Now()
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("the tick waited %v on the check's background child", d)
	}
	if ws := checkWakes(t, w.EpicDir); len(ws) != 1 || ws[0].Note != "check: "+path+": merged" {
		t.Fatalf("the output of a check that left a background child was lost: %+v", ws)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the check's background child survived the run")
	}
}

// A watcher that stops takes its running check down with it (fm watcher_cleanup kills FM_ACTIVE_CHECK_PGID).
func TestCheckPassStopCancelsTheRunningCheck(t *testing.T) {
	w, _, _ := checkRig(t, "#!/usr/bin/env bash\nsleep 30\n")
	stop := make(chan struct{})
	w.stop = stop
	done := make(chan struct{})
	go func() { _, _ = w.Tick(); close(done) }()
	time.Sleep(200 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stopping watcher left its check running")
	}
}

func TestCheckPassFailedEnqueueRerunsNextTick(t *testing.T) {
	w, _, _ := checkRig(t, "#!/usr/bin/env bash\necho fired\n")
	queue := filepath.Join(w.EpicDir, state.ControlDir, "wake.jsonl")
	if err := os.Mkdir(queue, 0o755); err != nil { // the enqueue fails
		t.Fatal(err)
	}
	if _, err := w.Tick(); err == nil {
		t.Fatal("a failed check enqueue was not an error")
	}
	if err := os.Remove(queue); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if n := len(checkWakes(t, w.EpicDir)); n != 1 {
		t.Fatalf("the check lost to a failed enqueue did not run again: %d wakes", n)
	}
}
