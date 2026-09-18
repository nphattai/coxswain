package control

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/watch"
)

func newCtl(epic string, b backend.Backend) *Controller {
	return &Controller{EpicDir: epic, Backend: b, ParkWait: 40 * time.Millisecond, PollInterval: 5 * time.Millisecond, Warn: &bytes.Buffer{}}
}

// seed a working story at the given attempt.
func seedWorking(t *testing.T, epic, story string, attempt int) {
	t.Helper()
	if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: attempt, Actor: state.Leader, From: state.Submitted, To: state.Working, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
}

func lastState(t *testing.T, epic, story string) *state.StorySnap {
	t.Helper()
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	return state.Fold(events).Stories[story]
}

func TestInterruptConfirmed(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	if err := newCtl(epic, b).Interrupt("s", backend.Session{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	s := lastState(t, epic, "s")
	if s.State != state.Working || s.PendingExternal {
		t.Fatalf("after confirmed interrupt want working, got %s (pending=%v)", s.State, s.PendingExternal)
	}
	if !contains(b.Calls, "Interrupt") || !contains(b.Calls, "Send") {
		t.Fatalf("interrupt must call Interrupt then Send doorbell: %v", b.Calls)
	}
}

// An undelivered interrupt is not a transition: the story stays working (so OpenStories still counts it and idle rearm
// stays armed), the error is surfaced, and the audit event records delivered:false.
func TestInterruptNotDeliveredStaysWorking(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	b.FailNext("Interrupt", nil)
	if err := newCtl(epic, b).Interrupt("s", backend.Session{ID: "x"}); err == nil {
		t.Fatal("expected an error when interrupt is not delivered")
	}
	s := lastState(t, epic, "s")
	if s.State != state.Working || s.PendingExternal {
		t.Fatalf("undelivered interrupt must stay working, got %s (pending=%v)", s.State, s.PendingExternal)
	}
	if d, _ := s.LastEvent.Evidence["delivered"].(bool); d {
		t.Fatalf("undelivered interrupt must record delivered:false, evidence=%v", s.LastEvent.Evidence)
	}
	// OpenStories keys on the same working/input_required state, so a failed interrupt must not drop the story.
	open, err := watch.OpenStories(epic)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0] != "s" {
		t.Fatalf("failed interrupt must keep story countable for rearm, OpenStories=%v", open)
	}
}

func TestRelaunchBumpsAttempt(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	sess, err := newCtl(epic, b).Relaunch("s", "/wt", "phase 2 half done", backend.Session{}, backend.HarnessSpec{Name: "claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Fatal("expected a session from relaunch")
	}
	s := lastState(t, epic, "s")
	if s.State != state.Working || s.Attempt != 2 {
		t.Fatalf("relaunch want working attempt 2, got %s attempt %d", s.State, s.Attempt)
	}
}

// Relaunch closes the previous attempt's terminal (Stop) before spawning the new one, and records the closed handle in
// the working event evidence (ADR 0012, M14).
func TestRelaunchClosesPriorTerminal(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	b.StopConfirmed = true
	prior := backend.Session{Kind: "orca-terminal", ID: "term_old", Handle: "term_old"}
	if _, err := newCtl(epic, b).Relaunch("s", "/wt", "resume", prior, backend.HarnessSpec{Name: "claude"}, nil); err != nil {
		t.Fatal(err)
	}
	stopAt, spawnAt := indexOf(b.Calls, "Stop"), indexOf(b.Calls, "Spawn")
	if stopAt < 0 || spawnAt < 0 || stopAt > spawnAt {
		t.Fatalf("prior terminal must be closed (Stop) before Spawn, calls=%v", b.Calls)
	}
	s := lastState(t, epic, "s")
	if s.LastEvent.Evidence["closed_terminal"] != "term_old" || s.LastEvent.Evidence["closed_confirmed"] != true {
		t.Fatalf("closed terminal not recorded in evidence: %+v", s.LastEvent.Evidence)
	}
}

// With no prior session (first relaunch), Relaunch closes nothing and records no closed_terminal.
func TestRelaunchNoPriorNoClose(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	if _, err := newCtl(epic, b).Relaunch("s", "/wt", "resume", backend.Session{}, backend.HarnessSpec{Name: "claude"}, nil); err != nil {
		t.Fatal(err)
	}
	if contains(b.Calls, "Stop") {
		t.Fatalf("no prior session must not call Stop, calls=%v", b.Calls)
	}
	if _, ok := lastState(t, epic, "s").LastEvent.Evidence["closed_terminal"]; ok {
		t.Fatal("no closed_terminal evidence expected without a prior session")
	}
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

// A reroute resume merges evidence.reroute onto the working event and still bumps the attempt (fake backend).
func TestRelaunchRerouteEvidence(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	reroute := map[string]any{"reroute": map[string]any{"from": "claude", "to": "codex", "reason": "quota low"}}
	if _, err := newCtl(epic, b).Relaunch("s", "/wt", "resume on codex", backend.Session{}, backend.HarnessSpec{Name: "codex"}, reroute); err != nil {
		t.Fatal(err)
	}
	s := lastState(t, epic, "s")
	if s.State != state.Working || s.Attempt != 2 {
		t.Fatalf("reroute want working attempt 2, got %s attempt %d", s.State, s.Attempt)
	}
	rr, ok := s.LastEvent.Evidence["reroute"].(map[string]any)
	if !ok || rr["to"] != "codex" || rr["from"] != "claude" {
		t.Fatalf("reroute evidence not recorded: %+v", s.LastEvent.Evidence)
	}
}

func TestRelaunchSpawnFailStaysPending(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	b.FailNext("Spawn", nil)
	_, err := newCtl(epic, b).Relaunch("s", "/wt", "note", backend.Session{}, backend.HarnessSpec{Name: "claude"}, nil)
	if err == nil {
		t.Fatal("expected spawn failure error")
	}
	s := lastState(t, epic, "s")
	if s.State != state.PendingExternal || s.Attempt != 2 {
		t.Fatalf("failed relaunch must stay pending_external at attempt 2, got %s attempt %d", s.State, s.Attempt)
	}
}

func TestParkRefusesWithoutCheckpoint(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	b := fake.New()
	err := newCtl(epic, b).Park("s", epic, backend.Session{ID: "x"})
	if err == nil || !strings.Contains(err.Error(), "refusing to park blind") {
		t.Fatalf("park must refuse without a checkpoint, got %v", err)
	}
	// A park steer must have been written asking for the checkpoint.
	recs, _ := listInbox(epic, "s")
	if len(recs) == 0 || !strings.Contains(recs, "PARK") {
		t.Fatalf("expected a PARK steer, inbox: %q", recs)
	}
	// Story must not be parked.
	if s := lastState(t, epic, "s"); s.State == state.Parked {
		t.Fatal("story should not be parked without a checkpoint")
	}
}

func TestParkConfirmedAndAbandon(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirmed bool
		wantNote  bool
	}{
		{"confirmed stop", true, false},
		{"abandon only", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wt, head := initGitRepo(t)
			epic := t.TempDir()
			seedWorking(t, epic, "s", 1)
			writeCheckpoint(t, epic, "s", 1, head)
			b := fake.New()
			b.StopConfirmed = tc.confirmed
			warn := &bytes.Buffer{}
			ctl := &Controller{EpicDir: epic, Backend: b, ParkWait: time.Second, PollInterval: 5 * time.Millisecond, Warn: warn}
			if err := ctl.Park("s", wt, backend.Session{ID: "x"}); err != nil {
				t.Fatalf("park: %v", err)
			}
			s := lastState(t, epic, "s")
			if s.State != state.Parked {
				t.Fatalf("want parked, got %s", s.State)
			}
			gotNote := strings.Contains(warn.String(), "abandon")
			if gotNote != tc.wantNote {
				t.Fatalf("abandon warning = %v, want %v (%q)", gotNote, tc.wantNote, warn.String())
			}
		})
	}
}

// The park bug: a worker wrote a checkpoint with the FULL head sha while Facts used --short, so checkpointMatches
// always failed and park waited forever. With prefix-tolerant HeadMatches and full-sha Facts, a checkpoint recording
// either the full or the short sha parks. (initGitRepo returns the short sha; fullHead returns the full one.)
func TestParkMatchesFullAndShortHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		full bool
	}{
		{"checkpoint full sha", true},
		{"checkpoint short sha", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wt, short := initGitRepo(t)
			head := short
			if tc.full {
				head = fullHead(t, wt)
			}
			epic := t.TempDir()
			seedWorking(t, epic, "s", 1)
			writeCheckpoint(t, epic, "s", 1, head)
			b := fake.New()
			b.StopConfirmed = true
			if err := newCtl(epic, b).Park("s", wt, backend.Session{ID: "x"}); err != nil {
				t.Fatalf("park with %s head should match, got %v", tc.name, err)
			}
			if s := lastState(t, epic, "s"); s.State != state.Parked {
				t.Fatalf("want parked, got %s", s.State)
			}
		})
	}
}

func TestParkStopErrorKeepsOwnership(t *testing.T) {
	wt, head := initGitRepo(t)
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	writeCheckpoint(t, epic, "s", 1, head)
	b := fake.New()
	b.FailNext("Stop", nil)
	err := newCtl(epic, b).Park("s", wt, backend.Session{ID: "x"})
	if err == nil {
		t.Fatal("expected stop error")
	}
	if s := lastState(t, epic, "s"); s.State != state.PendingExternal {
		t.Fatalf("stop error must keep pending_external, got %s", s.State)
	}
}

// Stop can error even though the worker has actually settled (e.g. Orca closed the terminal itself). A Settled probe
// overrides the Stop error and parks with a note; Alive or Unknown keeps pending_external (ownership held).
func TestParkStopErrorProbeSettled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		live    backend.Liveness
		wantErr bool
	}{
		{"stop error + settled -> parked", backend.Settled, false},
		{"stop error + alive -> fail", backend.Alive, true},
		{"stop error + unknown -> fail", backend.Unknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wt, head := initGitRepo(t)
			epic := t.TempDir()
			seedWorking(t, epic, "s", 1)
			writeCheckpoint(t, epic, "s", 1, head)
			b := fake.New()
			b.FailNext("Stop", nil)
			b.Liveness = tc.live
			err := newCtl(epic, b).Park("s", wt, backend.Session{ID: "x"})
			s := lastState(t, epic, "s")
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected stop error to keep ownership")
				}
				if s.State != state.PendingExternal {
					t.Fatalf("want pending_external, got %s", s.State)
				}
				return
			}
			if err != nil {
				t.Fatalf("Settled probe should park despite the Stop error: %v", err)
			}
			if s.State != state.Parked {
				t.Fatalf("want parked, got %s", s.State)
			}
			if note, _ := s.LastEvent.Evidence["note"].(string); !strings.Contains(note, "stop errored but probe settled") {
				t.Fatalf("expected a stop-errored note, got %q", note)
			}
		})
	}
}

// Scenario 2: a crash after the side effect but before the confirming append leaves pending_external{intended_to}.
// Reconcile finishes it by probing, and never re-issues the side effect.
func TestReconcileParkDoesNotRepeatStop(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	// Simulate the first append of a park that then crashed.
	if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: "s", Attempt: 1, Actor: state.Leader, From: state.Working, To: state.PendingExternal, Evidence: map[string]any{"intended_to": "parked", "verb": "park"}, ExternalConfirmed: false}); err != nil {
		t.Fatal(err)
	}
	b := fake.New()
	b.Liveness = backend.Settled // the worker actually did stop
	if err := newCtl(epic, b).Reconcile("s", backend.Session{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	if s := lastState(t, epic, "s"); s.State != state.Parked {
		t.Fatalf("reconcile should complete to parked, got %s", s.State)
	}
	if contains(b.Calls, "Stop") {
		t.Fatalf("reconcile must not re-issue Stop: %v", b.Calls)
	}
	if !contains(b.Calls, "Probe") {
		t.Fatalf("reconcile should probe: %v", b.Calls)
	}
}

func TestReconcileNothingPending(t *testing.T) {
	epic := t.TempDir()
	seedWorking(t, epic, "s", 1)
	if err := newCtl(epic, fake.New()).Reconcile("s", backend.Session{ID: "x"}); err != nil {
		t.Fatalf("reconcile on a non-pending story should be a no-op: %v", err)
	}
}

// --- helpers ---

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func listInbox(epic, story string) (string, error) {
	dir := filepath.Join(epic, "inbox", story)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".msg") {
			raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			b.Write(raw)
		}
	}
	return b.String(), nil
}

func writeCheckpoint(t *testing.T, epic, story string, attempt int, head string) {
	t.Helper()
	dir := filepath.Join(epic, "handoffs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nschema: coxswain.checkpoint.v1\nstory: " + story + "\nattempt: " + strconv.Itoa(attempt) +
		"\nhead: " + head + "\nbase: origin/epic/e@000\nwritten_at: 2026-09-15T00:00:00Z\nreason: park\n---\n## Next action\ngo\n"
	if err := os.WriteFile(filepath.Join(dir, story+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initGitRepo creates a git worktree with one commit and returns its path and short HEAD.
func initGitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

// fullHead returns the full HEAD sha of a repo.
func fullHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
