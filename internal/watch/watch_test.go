package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
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
	w := &Watcher{EpicDir: epic, Backend: b, Leader: "term_leader", Now: fixedNow()}
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
	b := fake.New()
	b.FailNext("Probe", nil)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	w := &Watcher{
		EpicDir: epic, Backend: b,
		Sessions: map[string]backend.Session{"s": {ID: "ctx_1"}},
		StaleMin: 20 * time.Minute,
		Now:      func() time.Time { return now },
	}
	// Seed an old heartbeat.
	hb := filepath.Join(epic, ".cox", "watch", "hb", "ctx_1")
	if err := os.MkdirAll(filepath.Dir(hb), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hb, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-30 * time.Minute)
	if err := os.Chtimes(hb, old, old); err != nil {
		t.Fatal(err)
	}
	n, err := w.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 unknown_probe wake, got %d", n)
	}
	wakes, _ := wake.Drain(epic, true)
	if len(wakes) != 1 || wakes[0].Kind != wake.KindUnknownProbe {
		t.Fatalf("wake wrong: %+v", wakes)
	}
	if wakes[0].Evidence["error"] == nil {
		t.Fatal("unknown_probe must carry the probe error")
	}
	// Heartbeat must still exist (never conclude gone).
	if _, err := os.Stat(hb); err != nil {
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
func TestIdleNoDoneWake(t *testing.T) {
	setup := func(t *testing.T, composer string, doneAfterSteer bool, lastMsgAgo time.Duration) (*Watcher, string) {
		epic := t.TempDir()
		must(t, state.Append(epic, ev(epic, "s", 1, state.Submitted, state.Working)))
		now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
		b := fake.New()
		b.ComposerState = composer
		w := &Watcher{EpicDir: epic, Backend: b, Sessions: map[string]backend.Session{"s": {ID: "ctx_1", Handle: "term_1"}}, Now: func() time.Time { return now }}
		stamp := func(sub, key string, ago time.Duration) {
			dir := filepath.Join(epic, ".cox", "watch", sub)
			must(t, os.MkdirAll(dir, 0o755))
			p := filepath.Join(dir, key)
			must(t, os.WriteFile(p, nil, 0o644))
			ts := now.Add(-ago)
			must(t, os.Chtimes(p, ts, ts))
		}
		stamp("lastmsg", "ctx_1", lastMsgAgo)
		if _, err := inbox.Write(epic, "s", "do the follow-ups and re-verify", inbox.Steer, ""); err != nil {
			t.Fatal(err)
		}
		recs, _ := inbox.List(epic, "s")
		steerAt := now.Add(-8 * time.Minute)
		must(t, os.Chtimes(recs[0].Path, steerAt, steerAt))
		if doneAfterSteer {
			stamp("lastdone", "ctx_1", 6*time.Minute) // worker_done after the 8-min-old steer
		}
		return w, epic
	}

	t.Run("fires once per steer", func(t *testing.T) {
		w, epic := setup(t, "empty", false, 10*time.Minute)
		n, urgent, err := w.idleNoDonePass()
		if err != nil || n != 1 || !urgent {
			t.Fatalf("want 1 urgent wake, got n=%d urgent=%v err=%v", n, urgent, err)
		}
		wakes, _ := wake.Drain(epic, true)
		if len(wakes) != 1 || wakes[0].Kind != wake.KindIdleNoDone {
			t.Fatalf("wake wrong: %+v", wakes)
		}
		if n2, _, _ := w.idleNoDonePass(); n2 != 0 {
			t.Fatalf("idle_no_done repeated for the same steer: %d", n2)
		}
	})

	t.Run("a busy composer does not fire", func(t *testing.T) {
		w, _ := setup(t, "busy", false, 10*time.Minute)
		if n, _, _ := w.idleNoDonePass(); n != 0 {
			t.Fatalf("busy composer should not raise idle_no_done, got %d", n)
		}
	})

	t.Run("a recent message does not fire", func(t *testing.T) {
		w, _ := setup(t, "empty", false, 1*time.Minute)
		if n, _, _ := w.idleNoDonePass(); n != 0 {
			t.Fatalf("a worker that just messaged is not idle, got %d", n)
		}
	})

	t.Run("a worker_done after the steer clears it", func(t *testing.T) {
		w, _ := setup(t, "empty", true, 10*time.Minute)
		if n, _, _ := w.idleNoDonePass(); n != 0 {
			t.Fatalf("a worker_done after the steer should suppress idle_no_done, got %d", n)
		}
	})

	// ADR 0012 item 10: a worker_done WAKE (from `cox story report done`, no mail lastdone watch file) after the steer
	// clears idle_no_done just the same, because the wake queue is the plane-independent completion source.
	t.Run("a report-done wake after the steer clears it", func(t *testing.T) {
		w, epic := setup(t, "empty", false, 10*time.Minute)
		doneAt := time.Date(2026, 9, 15, 11, 54, 0, 0, time.UTC) // after the 8-min-old steer (11:52)
		must(t, func() error {
			_, err := wake.Append(epic, wake.Wake{Epic: "e", Story: "s", Kind: wake.KindWorkerDone, TS: doneAt.Format(time.RFC3339)})
			return err
		}())
		if n, _, _ := w.idleNoDonePass(); n != 0 {
			t.Fatalf("a report-done wake after the steer should suppress idle_no_done, got %d", n)
		}
	})
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

// F9: a "done:" status is a completion (touches lastdone), and a rejected second worker_done has its prefix stripped
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
	// It touched lastdone, so idle_no_done is cleared for that dispatch.
	if _, err := os.Stat(filepath.Join(epic, ".cox", "watch", "lastdone", "ctx_1")); err != nil {
		t.Errorf("done: status did not touch lastdone: %v", err)
	}
	// The rejected worker_done note has the prefix stripped.
	if k := byStory["ctx_2"]; k.Kind != wake.KindWorkerDone || strings.Contains(k.Note, "Rejected") || !strings.Contains(k.Note, "real summary here") {
		t.Fatalf("rejected worker_done note wrong: kind=%q note=%q", k.Kind, k.Note)
	}
}
