package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

func TestMailPassAppendsWakeThenAcks(t *testing.T) {
	epic := t.TempDir()
	b := fake.New()
	mb := b.Mail().(*fake.Mailbox)
	mb.Delivery = "dlv"
	mb.Queue = []backend.Message{{
		ID: "relay_1", From: "dispatch:ctx_1", Type: "worker_done",
		Subject: "PR #12 up", Payload: `{"dispatchId":"ctx_1","outcome":"succeeded"}`,
	}}
	writeLeader(t, epic, "term_leader")
	w := &Watcher{EpicDir: epic, Backend: b, Now: fixedNow()}
	n, err := w.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 wake, got %d", n)
	}
	wakes, _ := wake.Drain(epic, true)
	if len(wakes) != 1 || wakes[0].Kind != wake.KindWorkerDone {
		t.Fatalf("wake wrong: %+v", wakes)
	}
	if len(mb.Acked) != 1 || mb.Acked[0] != "dlv" {
		t.Fatalf("delivery must be acked after the wake is written: %v", mb.Acked)
	}
	// Second tick must not re-append (seen tracking).
	n, _ = w.Tick()
	if n != 0 {
		t.Fatalf("duplicate wake on second tick: %d", n)
	}
	// Urgent wake -> leader doorbell via the terminal Send (not orchestration mail, which wakes no one).
	if !containsCall(b.Calls, "Send") {
		t.Fatalf("expected a leader terminal doorbell on an urgent wake, calls=%v", b.Calls)
	}
	if len(mb.Sent) != 0 {
		t.Fatalf("leader doorbell must not go through orchestration mail, mb.Sent=%v", mb.Sent)
	}
}

// writeSession writes <epic>/.cox/sessions/<story>.json so Tick's per-tick reload picks it up (item 1).
func writeSession(t *testing.T, epic, story string, sess backend.Session) {
	t.Helper()
	dir := filepath.Join(epic, state.ControlDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, story+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeLeader writes <epic>/.cox/leader.
func writeLeader(t *testing.T, epic, handle string) {
	t.Helper()
	dir := filepath.Join(epic, state.ControlDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "leader"), []byte(handle), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The leader handle is read fresh from .cox/leader every tick, not cached at start, so a re-bind after a leader restart
// is honoured on the next ring (finding 4). With no leader file, an urgent wake rings nobody.
func TestLeaderHandleReadFresh(t *testing.T) {
	epic := t.TempDir()
	w := &Watcher{EpicDir: epic, Backend: fake.New(), Now: fixedNow()}
	if got := w.leaderHandle(); got != "" {
		t.Errorf("no leader file must read as empty, got %q", got)
	}
	writeLeader(t, epic, "term_a")
	if got := w.leaderHandle(); got != "term_a" {
		t.Errorf("leader read = %q, want term_a", got)
	}
	writeLeader(t, epic, "term_b") // a restart re-binds the file
	if got := w.leaderHandle(); got != "term_b" {
		t.Errorf("leader must be re-read per tick, got %q, want term_b", got)
	}

	// No leader file: an urgent wake must not ring anyone.
	epic2 := t.TempDir()
	b := fake.New()
	mb := b.Mail().(*fake.Mailbox)
	mb.Delivery = "d"
	mb.Queue = []backend.Message{{ID: "r", From: "dispatch:c", Type: "worker_done", Subject: "up", Payload: `{"dispatchId":"c","outcome":"succeeded"}`}}
	w2 := &Watcher{EpicDir: epic2, Backend: b, Now: fixedNow()}
	if _, err := w2.Tick(); err != nil {
		t.Fatal(err)
	}
	if containsCall(b.Calls, "Send") {
		t.Errorf("with no leader file, an urgent wake must ring no doorbell: %v", b.Calls)
	}
}

// A failed leader doorbell is logged (handle + error) to .cox/watch/log instead of vanishing to /dev/null (finding 4).
func TestLeaderDoorbellFailureLogged(t *testing.T) {
	epic := t.TempDir()
	writeLeader(t, epic, "term_dead")
	b := fake.New()
	mb := b.Mail().(*fake.Mailbox)
	mb.Delivery = "d"
	mb.Queue = []backend.Message{{ID: "r", From: "dispatch:c", Type: "worker_done", Subject: "up", Payload: `{"dispatchId":"c","outcome":"succeeded"}`}}
	b.FailNext("Send", nil) // the leader doorbell fails
	w := &Watcher{EpicDir: epic, Backend: b, Now: fixedNow()}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(epic, state.ControlDir, "watch", "log"))
	if err != nil {
		t.Fatalf("watch/log not written on a failed leader doorbell: %v", err)
	}
	if !strings.Contains(string(log), "term_dead") || !strings.Contains(string(log), "leader-doorbell") {
		t.Errorf("log line must name the handle and the failure: %q", log)
	}
}

// F8(a): a status wake carries the body, not just the subject (which reads like a phase label).
func TestStatusWakeNoteIncludesBody(t *testing.T) {
	epic := t.TempDir()
	b := fake.New()
	mb := b.Mail().(*fake.Mailbox)
	mb.Delivery = "dlv"
	mb.Queue = []backend.Message{{
		ID: "relay_9", From: "dispatch:ctx_1", Type: "status",
		Subject: "followups-F1-F7", Body: "done: all seven follow-ups landed, part E re-run green",
		Payload: `{"dispatchId":"ctx_1"}`,
	}}
	w := &Watcher{EpicDir: epic, Backend: b, Now: fixedNow()}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	wakes, _ := wake.Drain(epic, true)
	if len(wakes) != 1 || wakes[0].Kind != wake.KindStatus {
		t.Fatalf("want one status wake, got %+v", wakes)
	}
	note := wakes[0].Note
	if !strings.Contains(note, "followups-F1-F7 | ") || !strings.Contains(note, "all seven follow-ups landed") {
		t.Fatalf("status note must carry subject + body, got %q", note)
	}
	// unit: body leads when subject is empty.
	if got := wakeNote(wake.KindStatus, "", "just the body"); got != "just the body" {
		t.Errorf("empty-subject note = %q, want body only", got)
	}
}

// A long status body is capped at 200 chars in the note but kept whole in Full, so `cox wake drain --full` can show it.
func TestWakeFullKeepsUntruncatedBody(t *testing.T) {
	longBody := strings.Repeat("x", 500)
	note := wakeNote(wake.KindStatus, "phase3", longBody)
	full := wakeFull(wake.KindStatus, "phase3", longBody)

	if len(note) >= len(full) {
		t.Fatalf("note (%d) should be shorter than full (%d)", len(note), len(full))
	}
	if !strings.Contains(full, longBody) {
		t.Fatal("full must contain the whole untruncated body")
	}
	if strings.Contains(note, longBody) {
		t.Fatal("note must be capped, not carry the whole 500-char body")
	}
	// A short status wake needs no separate Full: the two are identical, so mailPass stores nothing extra.
	if wakeNote(wake.KindStatus, "s", "short") != wakeFull(wake.KindStatus, "s", "short") {
		t.Fatal("short status: note and full should match so no duplicate is stored")
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

func TestHeartbeatIsNotAWake(t *testing.T) {
	epic := t.TempDir()
	b := fake.New()
	mb := b.Mail().(*fake.Mailbox)
	mb.Queue = []backend.Message{{
		ID: "relay_hb", From: "dispatch:ctx_1", Type: "heartbeat",
		Payload: `{"dispatchId":"ctx_1","phase":"implementing"}`,
	}}
	w := &Watcher{EpicDir: epic, Backend: b, Now: fixedNow()}
	n, err := w.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("heartbeat should not create a wake, got %d", n)
	}
	// It should have recorded a heartbeat file for the dispatch.
	if _, err := os.Stat(filepath.Join(epic, ".cox", "watch", "hb", "ctx_1")); err != nil {
		t.Fatalf("heartbeat not tracked: %v", err)
	}
}

// F08: a failed probe raises unknown_probe and keeps the heartbeat; it is never concluded "gone".
func TestStaleProbeErrorKeepsHeartbeat(t *testing.T) {
	epic := t.TempDir()
	must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
	b := fake.New()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	writeSession(t, epic, "s", backend.Session{ID: "ctx_1"}) // Tick reloads sessions from disk (item 1)
	w := &Watcher{EpicDir: epic, Backend: b, Now: func() time.Time { return now }}
	if _, err := w.Tick(); err != nil { // the quiet clock starts
		t.Fatal(err)
	}
	now = now.Add(time.Minute) // quiet past DefaultStaleQuiet: first sight of the stale worker
	b.FailNext("Probe", nil)
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	wakes, _ := wake.Drain(epic, true)
	if len(wakes) != 1 || wakes[0].Kind != wake.KindUnknownProbe {
		t.Fatalf("wake wrong: %+v", wakes)
	}
	if wakes[0].Evidence["error"] == nil {
		t.Fatal("unknown_probe must carry the probe error")
	}
	// The quiet clock must still exist (never conclude gone).
	if _, err := os.Stat(filepath.Join(epic, ".cox", "watch", "hb", "ctx_1")); err != nil {
		t.Fatalf("heartbeat was deleted after a failed probe: %v", err)
	}
}

// Parked stories must not count toward idle rearm (the parked-story rearm bug).
func TestOpenStoriesExcludesParked(t *testing.T) {
	epic := t.TempDir()
	must(t, state.Append(epic, ev(epic, "a", 1, state.Submitted, state.Working)))
	must(t, state.Append(epic, ev(epic, "b", 1, state.Working, state.Parked)))
	must(t, state.Append(epic, ev(epic, "c", 1, state.Working, state.InputRequired)))
	open, err := OpenStories(epic)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 {
		t.Fatalf("expected 2 open (working, input_required), got %v", open)
	}
	for _, id := range open {
		if id == "b" {
			t.Fatal("parked story b must not be counted for rearm")
		}
	}
}

// The inbox ladder advances only when the doorbell actually rang: a busy composer (or no session) must not bump the
// ring count, else it escalates to STUCK after ringMax ticks though nothing was delivered.
func TestInboxLadderBumpsOnlyWhenRung(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rang      bool
		wantCount int
	}{
		{"rung bumps", true, 1},
		{"busy composer does not bump", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			epic := t.TempDir()
			if _, err := inbox.Write(epic, "s", "please look", inbox.Steer, ""); err != nil {
				t.Fatal(err)
			}
			recs, err := inbox.List(epic, "s")
			if err != nil || len(recs) != 1 {
				t.Fatalf("seed steer: %v (recs=%d)", err, len(recs))
			}
			now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
			old := now.Add(-5 * time.Minute) // past InboxGrace
			if err := os.Chtimes(recs[0].Path, old, old); err != nil {
				t.Fatal(err)
			}
			b := fake.New()
			b.SendRang = tc.rang
			w := &Watcher{
				EpicDir:  epic,
				Backend:  b,
				Sessions: map[string]backend.Session{"s": {ID: "ctx_1"}},
				Now:      func() time.Time { return now },
			}
			if _, _, err := w.inboxLadder(); err != nil {
				t.Fatal(err)
			}
			dir := inbox.Dir(epic, "s")
			rs, err := inbox.Ring(dir, "001.msg")
			if err != nil {
				t.Fatal(err)
			}
			if rs.Count != tc.wantCount {
				t.Fatalf("ring count = %d, want %d (rang=%v)", rs.Count, tc.wantCount, tc.rang)
			}
		})
	}
}

// A permission-blocked pane (Send returns ErrAgentPromptBlocked) is skipped:permission: the ladder still bumps so an
// undeliverable steer escalates to stuck after ringMax, instead of deferring silently forever (M14).
func TestInboxLadderPermissionBlockedEscalates(t *testing.T) {
	epic := t.TempDir()
	if _, err := inbox.Write(epic, "s", "please look", inbox.Steer, ""); err != nil {
		t.Fatal(err)
	}
	recs, err := inbox.List(epic, "s")
	if err != nil || len(recs) != 1 {
		t.Fatalf("seed steer: %v (recs=%d)", err, len(recs))
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	old := now.Add(-5 * time.Minute)
	if err := os.Chtimes(recs[0].Path, old, old); err != nil {
		t.Fatal(err)
	}
	b := fake.New()
	w := &Watcher{
		EpicDir:      epic,
		Backend:      b,
		Sessions:     map[string]backend.Session{"s": {ID: "ctx_1", Handle: "term_1"}},
		InboxRingMax: 1, // escalate after one bump
		Now:          func() time.Time { return now },
	}
	dir := inbox.Dir(epic, "s")

	// Pass 1: Send is refused with ErrAgentPromptBlocked, but the ladder bumps (skipped:permission).
	b.FailNext("Send", backend.ErrAgentPromptBlocked)
	if _, _, err := w.inboxLadder(); err != nil {
		t.Fatal(err)
	}
	rs, err := inbox.Ring(dir, "001.msg")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Count != 1 {
		t.Fatalf("permission-blocked must bump the ladder, ring count = %d, want 1", rs.Count)
	}

	// Pass 2 (past grace): Count >= ringMax escalates to exactly one stuck wake.
	now = now.Add(5 * time.Minute)
	b.FailNext("Send", backend.ErrAgentPromptBlocked)
	if _, _, err := w.inboxLadder(); err != nil {
		t.Fatal(err)
	}
	wakes, _ := wake.Drain(epic, true)
	if len(wakes) != 1 || wakes[0].Kind != wake.KindStuck {
		t.Fatalf("permission-blocked steer must escalate to a stuck wake, got %+v", wakes)
	}
}

// markTick writes a parseable RFC3339 timestamp to <epic>/.cox/watch/lasttick so doctor/state can read the last-tick age.
func TestMarkTick(t *testing.T) {
	epic := t.TempDir()
	// A live epic always has its .cox control tree; markTick writes the beacon into it but never resurrects it (a
	// vanished .cox means the epic was torn down and the watcher is about to evict).
	must(t, os.MkdirAll(filepath.Join(epic, state.ControlDir), 0o755))
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	w := &Watcher{EpicDir: epic, Now: func() time.Time { return now }}
	w.markTick()
	b, err := os.ReadFile(filepath.Join(w.watchDir(), "lasttick"))
	if err != nil {
		t.Fatalf("lasttick not written: %v", err)
	}
	if got, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err != nil || !got.Equal(now) {
		t.Fatalf("lasttick = %q (parse err %v), want %s", b, err, now.Format(time.RFC3339))
	}
}

// --- helpers ---

func fixedNow() func() time.Time {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

func ev(epic, story string, attempt int, from, to state.State) state.Event {
	return state.Event{Epic: filepath.Base(epic), Story: story, Attempt: attempt, Actor: state.Leader, From: from, To: to, ExternalConfirmed: true}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// F8(c): a working story that went idle (composer empty, last message stale) with an unanswered steer and no
// worker_done since raises exactly one urgent idle_no_done wake, once per steer.
// Turn-end triage (supersedes the steer-gated idle pass, firstmate fm-watch-triage.test.sh:774): a working story whose
// harness record turned idle with no report since the turn began raises one urgent idle_no_done - no steer needed -
// once per turn end; a report in the same turn covers it; a still-busy worker is absorbed.
func TestTurnEndWithoutReportSurfaces(t *testing.T) {
	setup := func(t *testing.T) (*Watcher, string, string) {
		epic := t.TempDir()
		must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
		writeSession(t, epic, "s", backend.Session{ID: "ctx_1", Handle: "term_1"})
		gen, err := busy.Arm(epic, "s", "claude", []string{"dispatch", "claude-hook", "recovery"})
		must(t, err)
		return &Watcher{EpicDir: epic, Backend: fake.New()}, epic, gen
	}

	t.Run("fires once per turn end without a steer", func(t *testing.T) {
		w, epic, gen := setup(t)
		must(t, busy.Apply(epic, "s", busy.Idle, gen, "claude-hook", "Stop"))
		if _, err := w.Tick(); err != nil {
			t.Fatal(err)
		}
		wakes, _ := wake.Drain(epic, true)
		if len(wakes) != 1 || wakes[0].Kind != wake.KindIdleNoDone {
			t.Fatalf("want one idle_no_done, got %+v", wakes)
		}
		if _, err := w.Tick(); err != nil {
			t.Fatal(err)
		}
		if again, _ := wake.Drain(epic, true); len(again) != 1 {
			t.Fatalf("the same turn end surfaced twice: %+v", again)
		}
	})

	t.Run("a report in the same turn covers it", func(t *testing.T) {
		w, epic, gen := setup(t)
		_, err := wake.Append(epic, wake.Wake{Epic: "e", Story: "s", Kind: wake.KindWorkerDone, Note: "shipped"})
		must(t, err)
		must(t, busy.Apply(epic, "s", busy.Idle, gen, "claude-hook", "Stop"))
		if _, err := w.Tick(); err != nil {
			t.Fatal(err)
		}
		for _, wk := range mustDrain(t, epic) {
			if wk.Kind == wake.KindIdleNoDone {
				t.Fatalf("a reported turn end raised idle_no_done: %+v", wk)
			}
		}
	})

	t.Run("a busy worker is absorbed", func(t *testing.T) {
		w, epic, _ := setup(t)
		if _, err := w.Tick(); err != nil {
			t.Fatal(err)
		}
		if got := mustDrain(t, epic); len(got) != 0 {
			t.Fatalf("a busy worker raised %+v", got)
		}
	})
}

func mustDrain(t *testing.T, epic string) []wake.Wake {
	t.Helper()
	ws, err := wake.Drain(epic, true)
	must(t, err)
	return ws
}

// M10b: a worker blocked on a local prompt (Composer "blocked") for longer than BlockedWait raises one urgent stuck
// wake, at most once per waiting interval, and re-arms after the worker unblocks.
func TestBlockedPassStuckWake(t *testing.T) {
	epic := t.TempDir()
	must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	b := fake.New()
	b.ComposerState = backend.ComposerBlocked
	w := &Watcher{EpicDir: epic, Backend: b, Sessions: map[string]backend.Session{"s": {ID: "ctx_1", Handle: "term_1"}}, Now: func() time.Time { return now }}

	// First sight records the interval start, no wake yet.
	if n, urg, err := w.blockedPass(); err != nil || n != 0 || urg {
		t.Fatalf("first sight should record only, got n=%d urg=%v err=%v", n, urg, err)
	}
	// Still within the 2m window: no wake.
	now = now.Add(1 * time.Minute)
	if n, _, _ := w.blockedPass(); n != 0 {
		t.Fatalf("within window should not fire, got %d", n)
	}
	// Past the window (3m blocked): one urgent stuck wake with the fixed note.
	now = now.Add(2 * time.Minute)
	n, urg, err := w.blockedPass()
	if err != nil || n != 1 || !urg {
		t.Fatalf("want 1 urgent stuck wake, got n=%d urg=%v err=%v", n, urg, err)
	}
	wakes, _ := wake.Drain(epic, true)
	if len(wakes) != 1 || wakes[0].Kind != wake.KindStuck || !strings.Contains(wakes[0].Note, "waiting on a local prompt") {
		t.Fatalf("wake wrong: %+v", wakes)
	}
	// Same interval: does not repeat.
	now = now.Add(5 * time.Minute)
	if n, _, _ := w.blockedPass(); n != 0 {
		t.Fatalf("stuck wake repeated in the same interval: %d", n)
	}
	// The worker unblocks, then blocks again: a new interval re-arms the wake.
	b.ComposerState = backend.ComposerBusy
	if n, _, _ := w.blockedPass(); n != 0 {
		t.Fatalf("unblocked should not fire: %d", n)
	}
	b.ComposerState = backend.ComposerBlocked
	now = now.Add(1 * time.Minute)
	if n, _, _ := w.blockedPass(); n != 0 { // first sight of the new interval
		t.Fatalf("new interval first sight should not fire: %d", n)
	}
	now = now.Add(3 * time.Minute)
	if n, _, _ := w.blockedPass(); n != 1 {
		t.Fatalf("new interval should fire again after the window: %d", n)
	}
}

// Item 2: when blockedPass raises the stuck wake it captures the worker terminal's screen (Backend.Screen) and carries
// the visible prompt block in the note, the Full text, and the evidence, plus the remedy, so the leader answers from the
// hook output without opening the terminal. A backend that cannot read the screen still gets the wake, minus the dialog.
func TestBlockedPassStuckWakeCarriesDialog(t *testing.T) {
	epic := t.TempDir()
	must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	b := fake.New()
	b.ComposerState = backend.ComposerBlocked
	b.ScreenRows = []string{
		"", "some earlier output", "",
		"Ship to production now?",
		"  1. Yes, deploy",
		"  2. No, hold",
		"❯ ",
	}
	w := &Watcher{EpicDir: epic, Backend: b, Sessions: map[string]backend.Session{"s": {ID: "ctx_1", Handle: "term_1"}}, BlockedWait: time.Minute, Now: func() time.Time { return now }}

	if _, _, err := w.blockedPass(); err != nil { // first sight records the interval
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if n, urg, err := w.blockedPass(); err != nil || n != 1 || !urg {
		t.Fatalf("want one urgent stuck wake, got n=%d urg=%v err=%v", n, urg, err)
	}
	wakes, _ := wake.Drain(epic, true)
	if len(wakes) != 1 {
		t.Fatalf("want one wake, got %+v", wakes)
	}
	wk := wakes[0]
	if !strings.Contains(wk.Note, "Ship to production now?") {
		t.Errorf("note must carry the captured question: %q", wk.Note)
	}
	if !strings.Contains(wk.Note, "Remedy:") {
		t.Errorf("note must name the remedy: %q", wk.Note)
	}
	if prompt, _ := wk.Evidence["prompt"].(string); !strings.Contains(prompt, "1. Yes, deploy") || !strings.Contains(prompt, "2. No, hold") {
		t.Errorf("evidence.prompt must carry the numbered options: %q", prompt)
	}
	if !strings.Contains(wk.Full, "visible prompt:") || !strings.Contains(wk.Full, "Ship to production now?") {
		t.Errorf("Full must carry the untruncated dialog: %q", wk.Full)
	}
	if !containsCall(b.Calls, "Screen") {
		t.Errorf("blockedPass must read the terminal screen, calls=%v", b.Calls)
	}
}

// Item 1: a story whose session file is written AFTER the watcher's first tick is picked up on the next tick, so a
// story dispatched into a running watcher is covered (before, Sessions was cached at start and the new story was
// invisible). blockedPass is the proof: with the second session absent it cannot track the second story; once the file
// exists, the next tick reloads it and blockedPass records the block for it.
func TestTickReloadsSessionsForStoryDispatchedAfterStart(t *testing.T) {
	epic := t.TempDir()
	must(t, state.Append(epic, ev(epic, "s1", 1, state.Submitted, state.Working)))
	must(t, state.Append(epic, ev(epic, "s2", 1, state.Submitted, state.Working)))
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	b := fake.New()
	b.ComposerState = backend.ComposerBlocked
	writeSession(t, epic, "s1", backend.Session{ID: "ctx_1", Handle: "term_1"})
	w := &Watcher{EpicDir: epic, Backend: b, BlockedWait: time.Minute, Now: func() time.Time { return now }}

	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Sessions["s2"]; ok {
		t.Fatal("s2 has no session file yet; it must not be tracked")
	}
	if !w.blockedSince("s2").IsZero() {
		t.Fatal("s2 must not be blocked-tracked before its session exists")
	}

	// s2 is dispatched after the watcher started: its session file appears now.
	writeSession(t, epic, "s2", backend.Session{ID: "ctx_2", Handle: "term_2"})
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Sessions["s2"]; !ok {
		t.Fatal("a session written after the watcher started must be reloaded on the next tick (item 1)")
	}
	if w.blockedSince("s2").IsZero() {
		t.Fatal("the reloaded s2 must be blocked-tracked on the next tick")
	}
}

// F9: a "done:" status is a completion, and a rejected second worker_done has its prefix stripped
// from the note.
func TestDoneStatusAndRejectedWorkerDoneNote(t *testing.T) {
	epic := t.TempDir()
	b := fake.New()
	mb := b.Mail().(*fake.Mailbox)
	mb.Delivery = "dlv"
	mb.Queue = []backend.Message{
		{ID: "m_done", From: "dispatch:ctx_1", Type: "status", Subject: "done: F9/F10 landed, part E green", Payload: `{"dispatchId":"ctx_1"}`},
		{ID: "m_rej", From: "dispatch:ctx_2", Type: "worker_done", Subject: "Rejected worker_done: real summary here", Payload: `{"dispatchId":"ctx_2"}`},
	}
	w := &Watcher{EpicDir: epic, Backend: b, Now: fixedNow()}
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	wakes, _ := wake.Drain(epic, true)
	byStory := map[string]wake.Wake{}
	for _, k := range wakes {
		byStory[k.Story] = k
	}
	// The done: status classified as a completion.
	if k := byStory["ctx_1"]; k.Kind != wake.KindWorkerDone {
		t.Fatalf("done: status kind = %q, want worker_done", k.Kind)
	}
	// It is the story's last status line, which the stale path reads as terminal (turn-end triage replaced lastdone).
	if line, _ := w.statusLine("ctx_1"); !captainRelevant(line) {
		t.Errorf("done: status not recorded as a terminal status line: %q", line)
	}
	// The rejected worker_done note has the prefix stripped.
	if k := byStory["ctx_2"]; k.Kind != wake.KindWorkerDone || strings.Contains(k.Note, "Rejected") || !strings.Contains(k.Note, "real summary here") {
		t.Fatalf("rejected worker_done note wrong: kind=%q note=%q", k.Kind, k.Note)
	}
}

// --- Harness-owned busy state consult order (DESIGN wave-3 item 3) ---
// The idle/blocked passes read the busy record FIRST, so a Pi worker whose TUI the backend classifier cannot recognize
// (fake ComposerState "unknown") is still seen idle/busy. FAIL_TO_PASS: on the old code the pass used only the backend
// composer, so "unknown" never fired idle_no_done and "empty" always could.
// The harness record decides the turn end, not the backend's UI classifier: a pi-ext idle record surfaces even when the
// backend composer is unknown, and a busy record absorbs even when the composer reads empty.
func TestTurnEndConsultsBusyRecordFirst(t *testing.T) {
	setup := func(t *testing.T, backendComposer, recordState string) (*Watcher, string) {
		epic := t.TempDir()
		must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
		gen, err := busy.Arm(epic, "s", "pi", []string{"pi-ext", "dispatch", "interrupt", "recovery"})
		must(t, err)
		must(t, busy.Apply(epic, "s", recordState, gen, "pi-ext", "e"))
		writeSession(t, epic, "s", backend.Session{ID: "ctx_1", Handle: "term_1"})
		b := fake.New()
		b.ComposerState = backendComposer
		return &Watcher{EpicDir: epic, Backend: b}, epic
	}

	t.Run("harness idle fires even when the backend composer is unknown", func(t *testing.T) {
		w, epic := setup(t, backend.ComposerUnknown, busy.Idle)
		if _, err := w.Tick(); err != nil {
			t.Fatal(err)
		}
		if got := mustDrain(t, epic); len(got) != 1 || got[0].Kind != wake.KindIdleNoDone {
			t.Fatalf("want one idle_no_done, got %+v", got)
		}
	})

	t.Run("harness busy skips even when the backend composer is empty", func(t *testing.T) {
		w, epic := setup(t, backend.ComposerEmpty, busy.Busy)
		if _, err := w.Tick(); err != nil {
			t.Fatal(err)
		}
		if got := mustDrain(t, epic); len(got) != 0 {
			t.Fatalf("harness busy must not surface, got %+v", got)
		}
	})
}

// --- item 2: watcher self-eviction (B-37) ---

// evictReason returns "" while the epic's .cox tree, epic dir, and own binary are present; it names the reason when any
// disappears (or the epic is closed), so the Run loop stands down instead of polling a dead root forever.
func TestEvictReason(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, state.ControlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{EpicDir: epic, Now: fixedNow()}
	if r := w.evictReason(); r != "" {
		t.Fatalf("healthy epic must not evict, got %q", r)
	}
	// .cox removed -> evict.
	if err := os.RemoveAll(filepath.Join(epic, state.ControlDir)); err != nil {
		t.Fatal(err)
	}
	if r := w.evictReason(); !strings.Contains(r, "control tree") {
		t.Fatalf("missing .cox must evict, got %q", r)
	}
	// .cox back, but the epic is closed (.cox.closed present) -> evict.
	if err := os.MkdirAll(filepath.Join(epic, state.ControlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, state.ControlDir+".closed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := w.evictReason(); !strings.Contains(r, "closed") {
		t.Fatalf("closed epic must evict, got %q", r)
	}
	// Binary gone: point the resolver at a path that does not exist.
	os.Remove(filepath.Join(epic, state.ControlDir+".closed"))
	orig := watcherExecutable
	watcherExecutable = func() (string, error) { return filepath.Join(epic, "no-such-cox"), nil }
	defer func() { watcherExecutable = orig }()
	if r := w.evictReason(); !strings.Contains(r, "binary") {
		t.Fatalf("missing binary must evict, got %q", r)
	}
}

// The Run loop exits within one tick once the .cox control tree is renamed away, and releases so the process can end.
func TestRunEvictsWhenControlTreeGone(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, state.ControlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{EpicDir: epic, Backend: fake.New(), Now: fixedNow()}
	done := make(chan struct{})
	go func() { w.Run(make(chan struct{}), 5*time.Millisecond); close(done) }()
	// Let it tick at least once, then rename .cox away.
	time.Sleep(20 * time.Millisecond)
	if err := os.Rename(filepath.Join(epic, state.ControlDir), filepath.Join(epic, "gone")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	// The loop stands down one tick after the rename (markTick no longer resurrects .cox); a generous ceiling absorbs
	// CI scheduler jitter under -race without measuring the tick.
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit within one tick after .cox was renamed away")
	}
}

// --- item 3: leader reachability (B-33 nudge storm; doorbell-failure alarm) ---

// seedUrgentWake appends one urgent wake so an unacked backlog stands for the nudge path.
func seedUrgentWake(t *testing.T, epic string) {
	t.Helper()
	if _, err := wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: "s", Kind: wake.KindStuck, Note: "x"}); err != nil {
		t.Fatal(err)
	}
}

// B-33: a re-nudge for an unacked backlog whose max gen is unchanged is sent at most once per NudgeWindow; a window
// that has elapsed re-nudges, and a grown backlog always nudges.
func TestNudgeRateLimitedForUnchangedBacklog(t *testing.T) {
	epic := t.TempDir()
	writeLeader(t, epic, "term_leader")
	seedUrgentWake(t, epic)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	b := fake.New()
	b.SendRang = true
	w := &Watcher{EpicDir: epic, Backend: b, NudgeWindow: time.Minute, Now: func() time.Time { return now }}

	w.nudgeLeader()
	w.nudgeLeader() // same backlog, same window: suppressed
	if got := countCalls(b.Calls, "Send"); got != 1 {
		t.Fatalf("unchanged backlog within the window must nudge once, got %d", got)
	}
	// Window elapses -> re-nudge.
	now = now.Add(2 * time.Minute)
	w.nudgeLeader()
	if got := countCalls(b.Calls, "Send"); got != 2 {
		t.Fatalf("re-nudge after the window must fire, got %d Send calls", got)
	}
}

// item 3: three consecutive doorbell failures raise exactly one _leader stuck wake and fire the alarm channel once per
// window via an injected runner; further failures within the window neither re-raise nor re-alarm.
func TestDoorbellFailuresRaiseStuckAndAlarm(t *testing.T) {
	epic := t.TempDir()
	writeLeader(t, epic, "term_dead")
	seedUrgentWake(t, epic)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	var alarms []string
	b := fake.New()
	w := &Watcher{
		EpicDir: epic, Backend: b, NudgeWindow: time.Minute, AlarmWindow: 30 * time.Minute,
		AlarmChannel: "command:true",
		AlarmRun:     func(channel, summary string) error { alarms = append(alarms, channel+"|"+summary); return nil },
		Now:          func() time.Time { return now },
	}
	// Four failing nudges. A failed doorbell never records the nudge, so each tick re-nudges the same backlog.
	for i := 0; i < 4; i++ {
		b.FailNext("Send", nil)
		w.nudgeLeader()
	}
	leaderWakes := 0
	wakes, _ := wake.Load(epic)
	for _, wk := range wakes {
		if wk.Story == "_leader" && wk.Kind == wake.KindStuck {
			leaderWakes++
		}
	}
	if leaderWakes != 1 {
		t.Fatalf("three failures must raise exactly one _leader stuck wake, got %d", leaderWakes)
	}
	if len(alarms) != 1 {
		t.Fatalf("alarm must fire once per window, got %d (%v)", len(alarms), alarms)
	}
	if !strings.Contains(alarms[0], "command:true") || !strings.Contains(alarms[0], "term_dead") {
		t.Fatalf("alarm must carry the channel and the handle, got %q", alarms[0])
	}
	if DoorbellFailMax(epic) < DoorbellFailAlarm {
		t.Fatalf("doorbell-fail counter must be >= %d, got %d", DoorbellFailAlarm, DoorbellFailMax(epic))
	}
	// A delivered doorbell resets the streak.
	b.SendRang = true
	now = now.Add(time.Hour) // past the nudge window so the re-nudge fires and succeeds
	w.nudgeLeader()
	if DoorbellFailMax(epic) != 0 {
		t.Fatalf("a delivered doorbell must reset the failure counter, got %d", DoorbellFailMax(epic))
	}
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// writeBusyRecord writes a busy-state record directly (bypassing Apply) with a controlled last-event time, so a test can
// place the record's timestamp exactly relative to the injected clock.
func writeBusyRecord(t *testing.T, epic, story string, at time.Time) {
	t.Helper()
	rec := busy.Record{
		Schema: busy.Schema, State: busy.Busy, Gen: "gtest.deadbeef", Seq: 1, TS: at.Unix(),
		Source: "pi-ext", Event: "agent_start", Harness: "pi",
		Sources: []string{"pi-ext", "dispatch", "interrupt", "recovery"},
	}
	b, err := json.Marshal(rec)
	must(t, err)
	must(t, os.MkdirAll(filepath.Dir(busy.Path(epic, story)), 0o755))
	must(t, os.WriteFile(busy.Path(epic, story), b, 0o600))
	must(t, os.WriteFile(busy.GenPath(epic, story), []byte(rec.Gen+"\n"), 0o600)) // the armed-gen sidecar Arm writes
}

// DESIGN wave-2 item 6d: a working story whose busy record has said busy longer than BusyTurnMax, with no fresh busy
// event and no checkpoint, raises exactly one routine (non-urgent) status wake per window - a nudge, never an interrupt.
// On the base sha there is no busyTurnMaxPass, so a silently-busy worker was never surfaced.
func TestBusyTurnBoundStartsTheWedgeTimer(t *testing.T) {
	epic := t.TempDir()
	must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
	writeSession(t, epic, "s", backend.Session{ID: "ctx_1", Handle: "term_1"})
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	w := &Watcher{EpicDir: epic, Backend: fake.New(), Now: func() time.Time { return now }}

	// A record busy since only 30m ago is within the 60m default: no wake.
	writeBusyRecord(t, epic, "s", now.Add(-30*time.Minute))
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := mustDrain(t, epic); len(got) != 0 {
		t.Fatalf("recent busy must not wake, got %+v", got)
	}

	// Busy since 2h ago with no checkpoint: one routine status note, and the wedge timer starts.
	writeBusyRecord(t, epic, "s", now.Add(-2*time.Hour))
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	got := mustDrain(t, epic)
	if len(got) != 1 || got[0].Kind != wake.KindStatus {
		t.Fatalf("want one status note at the bound, got %+v", got)
	}

	// Inside the wedge threshold: nothing more.
	now = now.Add(time.Minute)
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	if again := mustDrain(t, epic); len(again) != 1 {
		t.Fatalf("inside the threshold must not repeat, got %+v", again)
	}

	// Past StaleMin: a possible-wedge escalation (never an interrupt).
	now = now.Add(DefaultStaleMin)
	if _, err := w.Tick(); err != nil {
		t.Fatal(err)
	}
	last := mustDrain(t, epic)
	if len(last) != 2 || last[1].Kind != wake.KindStale || !strings.Contains(last[1].Note, "possible wedge, escalation 1") {
		t.Fatalf("want a possible-wedge stale wake, got %+v", last)
	}
}

// markTick must never resurrect a vanished control tree: if .cox is gone (epic torn down mid-tick) it writes nothing, so
// the loop's self-eviction is not defeated by the watcher recreating .cox/watch every tick (the -race flake root cause).
func TestMarkTickDoesNotResurrectControlTree(t *testing.T) {
	epic := t.TempDir() // no .cox
	w := &Watcher{EpicDir: epic, Now: fixedNow()}
	w.markTick()
	if _, err := os.Stat(filepath.Join(epic, state.ControlDir)); !os.IsNotExist(err) {
		t.Fatalf(".cox was resurrected by markTick (err=%v); it must stay gone so the watcher evicts", err)
	}
}

// firstmate docs/wedge-alarm.md: one directive per line, every non-off channel fires, an absent config is auto, and
// auto resolves to osascript on macOS only.
func TestAlarmChannels(t *testing.T) {
	saved := alarmGOOS
	defer func() { alarmGOOS = saved }()
	alarmGOOS = "darwin"
	for spec, want := range map[string]string{
		"":                                     "osascript",
		"auto":                                 "osascript",
		"default":                              "osascript",
		"off":                                  "",
		"# comment\n\nosascript\ncommand:true": "osascript|command:true",
		"off\ncommand:true":                    "command:true",
	} {
		if got := strings.Join(alarmChannels(spec), "|"); got != want {
			t.Errorf("alarmChannels(%q) = %q, want %q", spec, got, want)
		}
	}
	alarmGOOS = "linux"
	if got := alarmChannels("auto"); len(got) != 0 {
		t.Errorf("auto on linux resolved to %v; firstmate has no built-in channel there", got)
	}
}

// A timed-out notifier's whole process group is killed, not just its shell.
func TestRunAlarmChannelKillsTheProcessGroup(t *testing.T) {
	saved := alarmTimeout
	defer func() { alarmTimeout = saved }()
	alarmTimeout = 300 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	err := runAlarmChannel("command:sleep 30 & echo $! > "+pidFile+"; wait", "summary")
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("want a timeout error inside the bound, got %v after %s", err, time.Since(start))
	}
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid > 0 && syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the notifier's child %d outlived the timeout", pid)
	}
}
