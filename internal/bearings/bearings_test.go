package bearings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/wake"
)

// A truncated digest names the stage that hit the bound, even though reaping that stage's subprocess lets the sealed
// digest goroutine run on through the later stages (regression: the banner once named "next-step").
func TestTruncationNamesTheStalledStage(t *testing.T) {
	for _, stage := range []string{"doctor", "fleet-state"} {
		d, err := Compose(Opts{Workspace: t.TempDir(), LeaderID: "me", Timeout: 500 * time.Millisecond,
			StageCmd: map[string][]string{stage: {"sleep", "30"}}})
		if err != nil || !d.Truncated {
			t.Fatalf("%s: truncated=%v err=%v", stage, d.Truncated, err)
		}
		if want := `stopped during the "` + stage + `" stage`; !strings.Contains(d.Text, want) {
			t.Errorf("banner does not name %q:\n%s", stage, d.Text)
		}
	}
}

func TestRunBoundedExitCodes(t *testing.T) {
	start := time.Now()
	if code, err := RunBounded(300*time.Millisecond, "sleep", "30"); err != nil || code != timeoutExit {
		t.Errorf("timed out run: code %d err %v, want 124", code, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the bound did not stop the run promptly")
	}
	if code, err := RunBounded(5*time.Second, "sh", "-c", "exit 3"); err != nil || code != 3 {
		t.Errorf("natural exit: code %d err %v, want 3", code, err)
	}
}

// The CLI path: the deferred stage runs in a detached worker, and the forge-checks section says so. It never starts on
// a read-only or re-emitted start.
func TestDetachedForgeChecks(t *testing.T) {
	ws := t.TempDir()
	calls := 0
	o := Opts{Workspace: ws, LeaderID: "me", Detach: func() error { calls++; return nil }}
	d, _ := Compose(o)
	if calls != 1 || !strings.Contains(d.Text, "IN PROGRESS - the deferred forge checks have not finished yet.") ||
		!strings.Contains(d.Text, "detached cox bearings deferred worker") {
		t.Errorf("detached start: calls=%d\n%s", calls, d.Text)
	}
	o.Detach = func() error { return errors.New("exec: no such file") }
	if d, _ = Compose(o); !strings.Contains(d.Text, "FORGE_CHECKS: the deferred forge worker could not start (exec: no such file)") {
		t.Errorf("a failed detach was not reported:\n%s", d.Text)
	}
	calls = 0
	o.Detach = func() error { calls++; return nil }
	o.Reemit, o.Source = true, "compact"
	Compose(o)
	o.Reemit, o.Source, o.LeaderID = false, "", "other" // "me" holds the lease and is live
	if d, _ = Compose(o); calls != 0 || !d.ReadOnly {
		t.Errorf("a re-emit or read-only start launched the worker: calls=%d read-only=%v", calls, d.ReadOnly)
	}
}

// A digest the bound cuts inside the lease stage never reports ownership it did not verify.
func TestReadOnlyUntilTheLeaseIsVerified(t *testing.T) {
	ws := t.TempDir()
	if ok, err := Acquire(ws, "holder", nil); !ok || err != nil {
		t.Fatal(ok, err)
	}
	d, _ := Compose(Opts{Workspace: ws, LeaderID: "me", Timeout: 300 * time.Millisecond,
		Live: func(string) bool { time.Sleep(2 * time.Second); return false }})
	if !d.Truncated || !d.ReadOnly {
		t.Errorf("truncated=%v read-only=%v, want a truncated read-only digest", d.Truncated, d.ReadOnly)
	}
}

// A failed forge result is published once, and only while the leader that started the stage still holds the lease.
func TestDeferredPublishesOnceAndOnlyForTheHolder(t *testing.T) {
	ws := t.TempDir()
	ep := filepath.Join(ws, "epics", "demo")
	if err := os.MkdirAll(ep, 0o755); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		all, _ := wake.Load(ep)
		n := 0
		for _, w := range all {
			if strings.HasPrefix(w.Note, "startup-forge:") {
				n++
			}
		}
		return n
	}
	o := Opts{Workspace: ws, LeaderID: "me", Forge: func() error { return errors.New("gh auth: offline") }}
	Acquire(ws, "me", nil)
	for i := 0; i < 2; i++ {
		if _, err := RunDeferred(o, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(); n != 1 {
		t.Errorf("two workers for one unacked failure queued %d wakes, want 1", n)
	}
	wake.AckThrough(ep, 99)
	Acquire(ws, "new-leader", func(string) bool { return false }) // a takeover
	RunDeferred(o, 5*time.Second)
	if n := count(); n != 1 {
		t.Errorf("a worker that outlived a takeover published: %d wakes", n)
	}
}

// Reinforcement matches an entry with or without its bullet; a reinforcement that names no entry is an exception.
func TestReinforceMatchingAndUnmatchedException(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, "cox", "notes", "learnings.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(HeaderPointer+"\n- exercised fact <!--a:2026-08-01-->\n"), 0o600)
	now, _ := time.Parse("2006-01-02", "2026-09-24")
	r, err := Curate(ws, now, []string{"exercised fact", "- no such entry"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "- exercised fact <!--a:2026-09-24-->") {
		t.Errorf("a bare reinforcement did not match its bulleted entry:\n%s", b)
	}
	if len(r.Exceptions) != 1 || !strings.Contains(r.Exceptions[0], "no such entry") || r.ResetSafe {
		t.Errorf("an unmatched reinforcement was not an exception: %+v", r)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("the rewrite changed the file mode to %v", fi.Mode().Perm())
	}
}

// The header pointer is corrected where it sits, never duplicated.
func TestHeaderPointerCorrectedInPlace(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, "cox", "notes", "captain.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("# Captain\n<!-- memory tiers: see the old place -->\n\n- pref\n"), 0o644)
	if _, err := Curate(ws, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "memory tiers:") != 1 || !strings.Contains(string(b), "# Captain\n"+HeaderPointer+"\n") {
		t.Errorf("pointer not corrected in place:\n%s", b)
	}
}
