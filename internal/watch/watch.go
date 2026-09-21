// Package watch is the zero-token watcher: it reads the backend mailbox without consuming it, classifies new mail into
// the wake queue, runs the inbox re-ring ladder, and probes silent workers, all without typing into any terminal or
// spending a leader turn. It never acks a delivery before the wake is durably written (the 2026-09-15 lesson), never
// concludes "gone" from a failed probe (F08), and counts idle rearm only over stories the resolver reports as
// working or input_required (the parked-story rearm bug).
package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/reconcile"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// Defaults for the watcher windows (ported from v1 watch.sh: STALE_MIN 20m, RUNAWAY_MIN 30m, INBOX_GRACE 90s,
// INBOX_RING_MAX 3).
const (
	DefaultStaleMin     = 20 * time.Minute
	DefaultRunawayMin   = 30 * time.Minute
	DefaultInboxGrace   = 90 * time.Second
	DefaultInboxRingMax = 3
	// DefaultIdleNoDoneWait is how long a working story's last message must be silent before an empty composer with an
	// unanswered steer is treated as "finished but never sent worker_done".
	DefaultIdleNoDoneWait = 5 * time.Minute
	// DefaultReconcileEvery is how many ticks between reconcile passes (policy override, phase-07). At the 5s poll that
	// is one pass every ~50s; a story stuck in pending_external by a crash is finished within a window, not left forever.
	DefaultReconcileEvery = 10
	// DefaultBlockedWait is how long a worker's agent must be continuously blocked on a local prompt (approval or input
	// it cannot answer itself) before the watcher raises a stuck wake for the leader (M10b, ADR 0012).
	DefaultBlockedWait = 2 * time.Minute
)

// Watcher runs one epic's watch loop. Sessions maps a story to its worker session (for re-ring and interrupt); Tick
// reloads it from <epic>/.cox/sessions/ on every pass, so a story dispatched AFTER the watcher started is covered by
// blockedPass, the doorbell ladder, liveness and runaway on the next tick instead of being invisible until a restart
// (item 1). The leader terminal handle for the pull-path doorbell is read FRESH from <epic>/.cox/leader every tick
// (never cached), so a leader harness restart that re-binds the file is picked up on the next ring instead of ringing a
// dead handle (finding 4). Now is injectable for tests.
type Watcher struct {
	EpicDir        string
	Backend        backend.Backend
	Sessions       map[string]backend.Session
	StaleMin       time.Duration
	RunawayMin     time.Duration
	InboxGrace     time.Duration
	InboxRingMax   int
	IdleNoDoneWait time.Duration
	BlockedWait    time.Duration // how long a worker may be blocked on a local prompt before a stuck wake; 0 => default
	ReconcileEvery int           // ticks between reconcile passes; 0 => DefaultReconcileEvery
	Quota          QuotaProbe    // quota source + targets + thresholds; nil disables the quota pass
	Now            func() time.Time

	tickCount int // ticks since start, for pacing the reconcile pass
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watcher) staleMin() time.Duration   { return orDur(w.StaleMin, DefaultStaleMin) }
func (w *Watcher) runawayMin() time.Duration { return orDur(w.RunawayMin, DefaultRunawayMin) }
func (w *Watcher) inboxGrace() time.Duration { return orDur(w.InboxGrace, DefaultInboxGrace) }
func (w *Watcher) ringMax() int {
	if w.InboxRingMax > 0 {
		return w.InboxRingMax
	}
	return DefaultInboxRingMax
}

func orDur(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// Tick runs one watch pass: mail classification, inbox ladder, and stale probing. It returns the number of wakes it
// appended (0 when nothing was actionable). It never blocks; Run wraps it in a poll loop.
func (w *Watcher) Tick() (int, error) {
	w.tickCount++
	w.Sessions = LoadSessions(w.EpicDir) // pick up any story dispatched since the last tick (item 1)
	dispatchStory, err := w.dispatchStoryMap()
	if err != nil {
		return 0, err
	}
	urgent := false
	appended := 0

	n, urg, err := w.mailPass(dispatchStory)
	if err != nil {
		return appended, err
	}
	appended += n
	urgent = urgent || urg

	n, urg, err = w.inboxLadder()
	if err != nil {
		return appended, err
	}
	appended += n
	urgent = urgent || urg

	n, err = w.stalePass()
	if err != nil {
		return appended, err
	}
	appended += n

	n, urg, err = w.idleNoDonePass()
	if err != nil {
		return appended, err
	}
	appended += n
	urgent = urgent || urg

	n, urg, err = w.blockedPass()
	if err != nil {
		return appended, err
	}
	appended += n
	urgent = urgent || urg

	if n, err := w.reconcilePass(); err != nil {
		return appended, err
	} else {
		appended += n
	}

	n, urg, err = w.quotaPass()
	if err != nil {
		return appended, err
	}
	appended += n
	urgent = urgent || urg

	if urgent {
		if handle := w.leaderHandle(); handle != "" {
			// Pull-path doorbell: knock on the leader terminal to drain (mail only lands on the leader's own next check, so
			// it wakes no one). Fixed text (brief G). The handle is read fresh so a re-bound .cox/leader is honoured, and a
			// failed doorbell is logged (with the handle and error) instead of vanishing to /dev/null (finding 4).
			if _, err := w.Backend.Send(backend.Session{Kind: "orca", Handle: handle}, "Wake waiting: run `cox wake drain`"); err != nil {
				w.logLeaderDoorbellFailure(handle, err)
			}
		}
	}
	return appended, nil
}

// leaderHandle reads the current leader terminal handle from <epic>/.cox/leader, fresh each tick, or "" when unset.
func (w *Watcher) leaderHandle() string {
	b, err := os.ReadFile(filepath.Join(w.EpicDir, state.ControlDir, "leader"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// logLeaderDoorbellFailure appends one "<ts> <handle> <err>" line to <epic>/.cox/watch/log so a leader doorbell that
// never reached its terminal (e.g. a stale handle after a restart) is visible instead of discarded. Best-effort.
func (w *Watcher) logLeaderDoorbellFailure(handle string, cause error) {
	dir := w.watchDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s leader-doorbell %s %v\n", w.now().UTC().Format(time.RFC3339), handle, cause)
}

// Run polls Tick every poll interval until the context-like stop channel is closed. Polling is mandatory (no fsnotify;
// macOS symlink risk). It logs Tick errors to stderr and keeps going, since a transient backend error must not kill
// the watcher.
func (w *Watcher) Run(stop <-chan struct{}, poll time.Duration) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	for {
		if _, err := w.Tick(); err != nil {
			fmt.Fprintln(os.Stderr, "watch:", err)
		}
		w.markTick()
		select {
		case <-stop:
			return
		case <-time.After(poll):
		}
	}
}

// markTick records the wall-clock time of the last completed watch pass to <epic>/.cox/watch/lasttick, so `cox doctor`
// and `cox state` can report the watcher's last-tick age and flag a watcher that died while a story is still working
// (M14, dogfood: the watcher stopped writing and nobody noticed - no watcher-alive line anywhere). Best-effort: a write
// failure never disturbs the loop.
func (w *Watcher) markTick() {
	dir := w.watchDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "lasttick"), []byte(w.now().UTC().Format(time.RFC3339)), 0o644)
}

// mailPass reads the mailbox without consuming, appends a wake for each new actionable message, tracks heartbeats for
// phase/liveness, and only after the wakes are written acks the delivery (never before: the 2026-09-15 lesson).
func (w *Watcher) mailPass(dispatchStory map[string]string) (int, bool, error) {
	msgs, deliveryID, err := w.Backend.Mail().Check()
	if err != nil {
		return 0, false, fmt.Errorf("mailbox check: %w", err)
	}
	seen, err := w.loadSeen()
	if err != nil {
		return 0, false, err
	}
	appended := 0
	urgent := false
	for _, m := range msgs {
		if m.ID != "" && seen[m.ID] {
			continue
		}
		disp, phase := payloadFields(m.Payload)
		if disp == "" {
			disp = strings.TrimPrefix(m.From, "dispatch:")
		}
		story := dispatchStory[disp]
		if story == "" {
			story = disp // fall back to the dispatch id as the story key; a wake needs a non-empty story
		}
		// Track the arrival of any message so idle_no_done can tell a worker that went quiet from one still messaging.
		w.touchWatchFile("lastmsg", disp)
		kind := wake.Classify(m)
		if kind == wake.KindHeartbeat {
			w.touchHeartbeat(disp, phase)
			w.markSeen(m.ID)
			continue
		}
		if kind == wake.KindWorkerDone {
			w.forgetHeartbeat(disp)            // a finished worker must not be reported STALE forever (v1)
			w.touchWatchFile("lastdone", disp) // idle_no_done clears once a worker_done arrives
		}
		note := wakeNote(kind, m.Subject, m.Body)
		wk := wake.Wake{
			Epic: filepath.Base(w.EpicDir), Story: story, Kind: kind,
			Note:     note,
			Evidence: map[string]any{"dispatch": disp, "msg": m.ID},
		}
		if full := wakeFull(kind, m.Subject, m.Body); full != note {
			wk.Full = full // keep the untruncated text for `cox wake drain --full`
		}
		gen, err := wake.Append(w.EpicDir, wk)
		if err != nil {
			return appended, urgent, err
		}
		_ = gen
		appended++
		if wake.IsUrgent(kind) {
			urgent = true
		}
		w.markSeen(m.ID)
	}
	// Only now, after every wake is durably written, ack the delivery (if the backend gave one).
	if deliveryID != "" {
		if err := w.Backend.Mail().Ack(deliveryID); err != nil {
			return appended, urgent, fmt.Errorf("ack delivery: %w", err)
		}
	}
	return appended, urgent, nil
}

// inboxLadder rings unhandled steers older than InboxGrace and escalates to a stuck wake after InboxRingMax rings; a
// steer unread past RunawayMin on a live (busy) worker is interrupted once per window and raises a runaway wake.
func (w *Watcher) inboxLadder() (int, bool, error) {
	base := filepath.Join(w.EpicDir, "inbox")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read inbox: %w", err)
	}
	now := w.now()
	appended := 0
	urgent := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		story := e.Name()
		recs, err := inbox.List(w.EpicDir, story)
		if err != nil {
			return appended, urgent, err
		}
		dir := inbox.Dir(w.EpicDir, story)
		for _, rec := range recs {
			info, err := os.Stat(rec.Path)
			if err != nil {
				continue
			}
			age := now.Sub(info.ModTime())
			if age < w.inboxGrace() {
				continue
			}
			key := fmt.Sprintf("%03d.msg", rec.Seq)
			rs, err := inbox.Ring(dir, key)
			if err != nil {
				return appended, urgent, err
			}
			// Respect the grace between rings.
			if rs.LastTS != 0 && now.Sub(time.Unix(rs.LastTS, 0)) < w.inboxGrace() {
				continue
			}
			if rs.Count >= w.ringMax() {
				if !rs.Escalated {
					if _, err := wake.Append(w.EpicDir, wake.Wake{
						Epic: filepath.Base(w.EpicDir), Story: story, Kind: wake.KindStuck,
						Note: fmt.Sprintf("inbox %s/%s unacknowledged after %d rings", story, key, rs.Count),
					}); err != nil {
						return appended, urgent, err
					}
					appended++
					urgent = true
					if err := inbox.MarkEscalated(dir, key, now.Unix()); err != nil {
						return appended, urgent, err
					}
				}
				continue
			}
			// Ring the worker doorbell; only a real ring advances the ladder. A story with no session, or one whose
			// composer was too busy to knock, delivered nothing - counting it would escalate to STUCK after ringMax
			// ticks though the worker was never actually nudged.
			rang := false
			permBlocked := false
			if sess, ok := w.Sessions[story]; ok {
				r, err := w.Backend.Send(sess, inbox.Doorbell(dir))
				switch {
				case errors.Is(err, backend.ErrAgentPromptBlocked):
					permBlocked = true // skipped:permission - the pane is waiting on a local prompt Orca refuses to type over
				case err != nil:
					fmt.Fprintf(os.Stderr, "watch: doorbell %s/%s failed: %v\n", story, key, err)
				}
				rang = r
			}
			switch {
			case rang, permBlocked:
				// A real ring OR a permission-blocked pane both bump the ladder: an undeliverable steer to a worker stuck on
				// a permission prompt must still escalate to stuck after ringMax, not defer silently forever (M14).
				if permBlocked {
					fmt.Fprintf(os.Stderr, "watch: %s/%s orca refused typed prompt (permission pending); ladder bumped toward stuck\n", story, key)
				}
				if _, err := inbox.BumpRing(dir, key, now.Unix()); err != nil {
					return appended, urgent, err
				}
			default:
				fmt.Fprintf(os.Stderr, "watch: %s/%s not rung (no session or busy composer); ladder not bumped\n", story, key)
			}
			// Runaway: unread past RunawayMin on a live worker -> interrupt once per window.
			if rec.Urgency != inbox.FYI && age > w.runawayMin() {
				lastInt, _ := inbox.LastInterrupt(dir)
				if lastInt == 0 || now.Sub(time.Unix(lastInt, 0)) > w.runawayMin() {
					if sess, ok := w.Sessions[story]; ok {
						if live, err := w.Backend.Probe(sess); err == nil && live == backend.Alive {
							_ = w.Backend.Interrupt(sess)
							_, _ = w.Backend.Send(sess, inbox.Doorbell(dir))
							if err := inbox.MarkInterrupt(dir, now.Unix()); err != nil {
								return appended, urgent, err
							}
							if _, err := wake.Append(w.EpicDir, wake.Wake{
								Epic: filepath.Base(w.EpicDir), Story: story, Kind: wake.KindRunaway,
								Note: fmt.Sprintf("%s busy %dm with %s unread - interrupted", story, int(age.Minutes()), key),
							}); err != nil {
								return appended, urgent, err
							}
							appended++
							urgent = true
						}
					}
				}
			}
		}
	}
	return appended, urgent, nil
}

// stalePass probes any tracked worker whose heartbeat is older than StaleMin. A failed or Unknown probe raises an
// unknown_probe wake and keeps the heartbeat (F08: never conclude gone); a Settled probe forgets it; an Alive worker
// is just quiet. The heartbeat mtime is bumped after a probe so a stale worker is probed at most once per window.
func (w *Watcher) stalePass() (int, error) {
	hbDir := filepath.Join(w.EpicDir, state.ControlDir, "watch", "hb")
	entries, err := os.ReadDir(hbDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read heartbeat dir: %w", err)
	}
	now := w.now()
	appended := 0
	for _, e := range entries {
		disp := e.Name()
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < w.staleMin() {
			continue
		}
		sess, ok := w.Sessions[dispatchStoryReverse(w.Sessions, disp)]
		if !ok {
			// No session to probe with; keep the heartbeat, do not conclude gone.
			w.bumpHeartbeat(disp)
			continue
		}
		live, probeErr := w.Backend.Probe(sess)
		if probeErr != nil || live == backend.Unknown {
			note := "liveness probe failed"
			ev := map[string]any{"dispatch": disp}
			if probeErr != nil {
				note = probeErr.Error()
				ev["error"] = probeErr.Error()
			}
			if _, err := wake.Append(w.EpicDir, wake.Wake{
				Epic: filepath.Base(w.EpicDir), Story: storyForDisp(w.Sessions, disp), Kind: wake.KindUnknownProbe,
				Note: note, Evidence: ev,
			}); err != nil {
				return appended, err
			}
			appended++
		}
		w.bumpHeartbeat(disp) // one probe per window; never delete the heartbeat (F08)
	}
	return appended, nil
}

// idleNoDonePass raises one urgent idle_no_done wake per unanswered steer when a working story looks finished but never
// sent a worker_done: its composer is empty, its last mailbox message is older than IdleNoDoneWait, and its newest steer
// is more recent than any worker_done. This catches a re-run that ended with only a status (F8), which would otherwise
// leave the story idle with the leader waiting. It fires at most once per steer (keyed by the steer's timestamp).
func (w *Watcher) idleNoDonePass() (int, bool, error) {
	events, _, err := state.Load(w.EpicDir)
	if err != nil {
		return 0, false, err
	}
	snap := state.Fold(events)
	now := w.now()
	appended := 0
	urgent := false
	for _, s := range snap.SortedStories() {
		if s.State != state.Working {
			continue
		}
		sess, ok := w.Sessions[s.ID]
		if !ok {
			continue
		}
		disp := sess.ID
		lastMsg := w.watchFileMtime("lastmsg", disp)
		if lastMsg.IsZero() || now.Sub(lastMsg) < w.idleNoDoneWait() {
			continue // never messaged, or still messaging recently
		}
		steerTS := newestSteer(w.EpicDir, s.ID)
		if steerTS.IsZero() {
			continue // no steer awaiting completion
		}
		// A worker_done clears idle_no_done regardless of how it arrived: on the terminal plane it is a wake appended by
		// `cox story report done` (no mail), so read the newest worker_done wake for the story, not just the mail-driven
		// lastdone watch file (F8c, ADR 0012 item 10). Take whichever source is later.
		lastDone := laterTime(w.watchFileMtime("lastdone", disp), newestWorkerDoneWake(w.EpicDir, s.ID))
		if !lastDone.IsZero() && !lastDone.Before(steerTS) {
			continue // a worker_done already arrived at or after the steer
		}
		if w.firedIdleNoDone(s.ID) == steerTS.Unix() {
			continue // already raised for this steer
		}
		if w.composerState(s.ID, sess) != backend.ComposerEmpty {
			continue // still mid-turn, typing, or the backend cannot tell
		}
		if _, err := wake.Append(w.EpicDir, wake.Wake{
			Epic: filepath.Base(w.EpicDir), Story: s.ID, Kind: wake.KindIdleNoDone,
			Note: fmt.Sprintf("%s idle (composer empty, last message %dm ago) with an unanswered steer and no worker_done since it - it may have ended with a status instead of worker_done",
				s.ID, int(now.Sub(lastMsg).Minutes())),
			Evidence: map[string]any{"dispatch": disp},
		}); err != nil {
			return appended, urgent, err
		}
		appended++
		urgent = true
		w.recordIdleNoDone(s.ID, steerTS)
	}
	return appended, urgent, nil
}

// composerState resolves a worker's composer verdict harness-first (DESIGN wave-3 item 3): the harness-owned busy record
// wins (idle -> empty, busy -> busy), so a Pi worker whose TUI the backend classifier does not recognize is still seen
// idle/busy; only when the harness reports no state does it fall back to the backend's own Composer. Same order the
// backend ring path uses, so the idle/blocked passes and the doorbell never disagree.
func (w *Watcher) composerState(story string, sess backend.Session) string {
	if cs, ok := backend.BusyComposer(w.EpicDir, story); ok {
		return cs
	}
	cs, _ := w.Backend.Composer(sess)
	return cs
}

// blockedPass raises one urgent stuck wake when a working story's worker has been continuously blocked on a local prompt
// (an approval or input request it cannot answer itself) for longer than BlockedWait. Composer reports "blocked" from
// the backend's structured agent state (Orca agents[] state "waiting"); the block start is stamped on first sight and
// cleared the moment the worker is no longer blocked, so the wake fires at most once per waiting interval and re-arms for
// the next one. A blocked worker is Alive, not gone (M10b, ADR 0012), so this pass, not liveness, is what surfaces it.
func (w *Watcher) blockedPass() (int, bool, error) {
	events, _, err := state.Load(w.EpicDir)
	if err != nil {
		return 0, false, err
	}
	snap := state.Fold(events)
	now := w.now()
	appended := 0
	urgent := false
	for _, s := range snap.SortedStories() {
		if s.State != state.Working {
			continue
		}
		sess, ok := w.Sessions[s.ID]
		if !ok {
			continue
		}
		if w.composerState(s.ID, sess) != backend.ComposerBlocked {
			w.clearBlocked(s.ID) // no longer waiting: the interval ends, so a fresh block re-arms the wake
			continue
		}
		since := w.blockedSince(s.ID)
		if since.IsZero() {
			w.recordBlocked(s.ID, now) // first sight of this waiting interval
			continue
		}
		if now.Sub(since) < w.blockedWait() {
			continue // waiting, but not long enough yet
		}
		if w.blockedFired(s.ID) {
			continue // already raised for this interval
		}
		// Capture the worker terminal's screen so the leader answers the prompt from the hook output, not by opening the
		// terminal (item 2). A failed read just omits the dialog: the stuck wake still fires. The rows ARE the screen, so
		// no env/secret is captured.
		note := blockedNote
		ev := map[string]any{"dispatch": sess.ID, "waiting_m": int(now.Sub(since).Minutes())}
		full := ""
		if rows, err := w.Backend.Screen(sess); err == nil {
			if block := promptBlock(rows); block != "" {
				ev["prompt"] = block
				full = note + "\nvisible prompt:\n" + block
				note = note + " | prompt: " + truncate(strings.ReplaceAll(block, "\n", " "), 200)
			}
		}
		wk := wake.Wake{Epic: filepath.Base(w.EpicDir), Story: s.ID, Kind: wake.KindStuck, Note: note, Evidence: ev}
		if full != "" {
			wk.Full = full
		}
		if _, err := wake.Append(w.EpicDir, wk); err != nil {
			return appended, urgent, err
		}
		appended++
		urgent = true
		w.markBlockedFired(s.ID)
	}
	return appended, urgent, nil
}

// blockedNote is the fixed lead of every stuck-on-a-prompt wake: what happened and the remedy the leader runs (steer
// the ruling, then dismiss the prompt from the worker's terminal). The captured dialog, when the screen read succeeds,
// is appended after it.
const blockedNote = "worker waiting on a local prompt (approval or input); check the terminal. " +
	"Remedy: cox steer the ruling, then `orca terminal send --enter` (or the option number) to dismiss."

// blockLineMax bounds the captured prompt block so a long screen never bloats a wake (DESIGN item 2: ~40 lines).
const blockLineMax = 40

// promptBlock extracts the visible prompt a blocked worker is waiting on from the terminal's rendered screen rows: the
// trailing run of lines (the question and its numbered options) with surrounding blank lines trimmed, bounded to
// blockLineMax lines. The rows are the screen capture, never env, so no secret is included by construction.
func promptBlock(rows []string) string {
	lines := make([]string, len(rows))
	for i, l := range rows {
		lines[i] = strings.TrimRight(l, " \t")
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > blockLineMax {
		lines = lines[len(lines)-blockLineMax:]
	}
	return strings.Join(lines, "\n")
}

func (w *Watcher) reconcileEvery() int {
	if w.ReconcileEvery > 0 {
		return w.ReconcileEvery
	}
	return DefaultReconcileEvery
}

// reconcilePass runs one fleet reconcile every reconcileEvery ticks, applying only the transitions the probe confirms
// (an unknown probe keeps the story pending_external, F08). It never re-issues a side effect. Returns the count applied.
func (w *Watcher) reconcilePass() (int, error) {
	if w.tickCount%w.reconcileEvery() != 0 {
		return 0, nil
	}
	decisions, err := reconcile.Run(w.EpicDir, w.Backend, w.Sessions, true)
	if err != nil {
		return 0, err
	}
	applied := 0
	for _, d := range decisions {
		if d.Applied {
			applied++
		}
	}
	return applied, nil
}

// newestWorkerDoneWake returns the timestamp of the most recent worker_done wake for a story, or the zero time when
// there is none. This is the plane-independent completion signal: mailPass appends a worker_done wake for a mail
// completion and `cox story report done` appends one directly, so the wake queue is the single source of truth for
// "the worker reported done" (F8c, ADR 0012 item 10).
func newestWorkerDoneWake(epicDir, story string) time.Time {
	wakes, err := wake.Load(epicDir)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, wk := range wakes {
		if wk.Kind != wake.KindWorkerDone || wk.Story != story {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, wk.TS); err == nil && ts.After(newest) {
			newest = ts
		}
	}
	return newest
}

// laterTime returns the later of two times (either may be zero).
func laterTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// newestSteer returns the mtime of the most recent steer record for a story (handled or not), or the zero time when
// there is none. A steer whose worker has acked it (moved it to handled/) still awaits a worker_done, so both dirs count.
func newestSteer(epicDir, story string) time.Time {
	recs, err := inbox.All(epicDir, story)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, r := range recs {
		if r.Urgency != inbox.Steer {
			continue
		}
		if info, err := os.Stat(r.Path); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// OpenStories returns the ids of stories the resolver reports as working or input_required. Idle rearm counts only
// these, never historical keys (the parked-story rearm bug).
func OpenStories(epicDir string) ([]string, error) {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return nil, err
	}
	snap := state.Fold(events)
	var open []string
	for _, s := range snap.SortedStories() {
		if s.State == state.Working || s.State == state.InputRequired {
			open = append(open, s.ID)
		}
	}
	return open, nil
}

// LoadSessions maps every story with a saved session file (<epic>/.cox/sessions/<story>.json) to its session. Tick
// calls it each pass so a story dispatched after the watcher started is tracked without a restart (item 1). A missing
// dir or an unreadable file yields an empty/partial map rather than an error: a watch pass must never die on it.
func LoadSessions(epicDir string) map[string]backend.Session {
	out := map[string]backend.Session{}
	dir := filepath.Join(epicDir, state.ControlDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".busy.json") {
			continue // skip the sibling harness-owned busy-state records that live in the same dir
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s backend.Session
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		story := strings.TrimSuffix(e.Name(), ".json")
		s.Story = story // stamp the story so the backend can consult the busy record even for a session persisted before this field existed
		out[story] = s
	}
	return out
}

// --- state files ---

func (w *Watcher) watchDir() string { return filepath.Join(w.EpicDir, state.ControlDir, "watch") }

func (w *Watcher) idleNoDoneWait() time.Duration {
	return orDur(w.IdleNoDoneWait, DefaultIdleNoDoneWait)
}

func (w *Watcher) blockedWait() time.Duration { return orDur(w.BlockedWait, DefaultBlockedWait) }

// blockedSince returns when the worker's current waiting interval began (zero when it is not currently blocked). The
// start time is stored as a unix second in <watch>/blocked/<story>.
func (w *Watcher) blockedSince(story string) time.Time {
	b, err := os.ReadFile(filepath.Join(w.watchDir(), "blocked", story))
	if err != nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// recordBlocked stamps the start of a waiting interval.
func (w *Watcher) recordBlocked(story string, since time.Time) {
	dir := filepath.Join(w.watchDir(), "blocked")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, story), []byte(strconv.FormatInt(since.Unix(), 10)), 0o644)
}

// blockedFired reports whether a stuck wake was already raised for the current waiting interval.
func (w *Watcher) blockedFired(story string) bool {
	_, err := os.Stat(filepath.Join(w.watchDir(), "blockedfired", story))
	return err == nil
}

// markBlockedFired records that a stuck wake fired for the current waiting interval, so it fires at most once per interval.
func (w *Watcher) markBlockedFired(story string) {
	dir := filepath.Join(w.watchDir(), "blockedfired")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if f, err := os.OpenFile(filepath.Join(dir, story), os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
	}
}

// clearBlocked ends a waiting interval: the next block starts fresh and re-arms the wake.
func (w *Watcher) clearBlocked(story string) {
	_ = os.Remove(filepath.Join(w.watchDir(), "blocked", story))
	_ = os.Remove(filepath.Join(w.watchDir(), "blockedfired", story))
}

// touchWatchFile stamps <watch>/<sub>/<key> with the current time (creating it), for last-seen tracking.
func (w *Watcher) touchWatchFile(sub, key string) {
	if key == "" {
		return
	}
	dir := filepath.Join(w.watchDir(), sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	p := filepath.Join(dir, key)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
	}
	now := w.now()
	_ = os.Chtimes(p, now, now)
}

// watchFileMtime returns the mtime of <watch>/<sub>/<key>, or the zero time when it does not exist.
func (w *Watcher) watchFileMtime(sub, key string) time.Time {
	info, err := os.Stat(filepath.Join(w.watchDir(), sub, key))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// firedIdleNoDone returns the steer unix time an idle_no_done was last raised for a story (0 when none), for dedup.
func (w *Watcher) firedIdleNoDone(story string) int64 {
	b, err := os.ReadFile(filepath.Join(w.watchDir(), "idlenodone", story))
	if err != nil {
		return 0
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return sec
}

// recordIdleNoDone remembers the steer an idle_no_done fired for, so it is raised at most once per steer.
func (w *Watcher) recordIdleNoDone(story string, steerTS time.Time) {
	dir := filepath.Join(w.watchDir(), "idlenodone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, story), []byte(strconv.FormatInt(steerTS.Unix(), 10)), 0o644)
}

func (w *Watcher) touchHeartbeat(disp, phase string) {
	if disp == "" {
		return
	}
	w.bumpHeartbeat(disp)
	if phase != "" {
		_ = os.WriteFile(filepath.Join(w.watchDir(), "phase", disp), []byte(phase), 0o644)
	}
}

func (w *Watcher) bumpHeartbeat(disp string) {
	if disp == "" {
		return
	}
	dir := filepath.Join(w.watchDir(), "hb")
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, disp)
	now := w.now()
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
	}
	_ = os.Chtimes(path, now, now)
}

func (w *Watcher) forgetHeartbeat(disp string) {
	if disp == "" {
		return
	}
	_ = os.Remove(filepath.Join(w.watchDir(), "hb", disp))
	_ = os.Remove(filepath.Join(w.watchDir(), "phase", disp))
}

func (w *Watcher) loadSeen() (map[string]bool, error) {
	seen := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(w.watchDir(), "seen"))
	if err != nil {
		if os.IsNotExist(err) {
			return seen, nil
		}
		return nil, fmt.Errorf("read seen: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			seen[line] = true
		}
	}
	return seen, nil
}

func (w *Watcher) markSeen(id string) {
	if id == "" {
		return
	}
	_ = os.MkdirAll(w.watchDir(), 0o755)
	f, err := os.OpenFile(filepath.Join(w.watchDir(), "seen"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, id)
}

// dispatchStoryMap builds dispatch-id -> story from the event log (evidence.dispatch), so a worker's mail is attributed
// to the right story without grepping prefixes (F07).
func (w *Watcher) dispatchStoryMap() (map[string]string, error) {
	events, _, err := state.Load(w.EpicDir)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, ev := range events {
		if ev.Evidence == nil {
			continue
		}
		if d, ok := ev.Evidence["dispatch"].(string); ok && d != "" {
			m[d] = ev.Story
		}
	}
	return m, nil
}

// dispatchStoryReverse finds the story whose session id or handle equals disp (so stalePass can pick the session).
func dispatchStoryReverse(sessions map[string]backend.Session, disp string) string {
	for story, s := range sessions {
		if s.ID == disp || s.Handle == disp {
			return story
		}
	}
	return disp
}

func storyForDisp(sessions map[string]backend.Session, disp string) string {
	if s := dispatchStoryReverse(sessions, disp); s != "" {
		return s
	}
	return disp
}

// payloadFields extracts dispatchId and phase from an Orca message payload (a JSON string).
func payloadFields(payload string) (dispatch, phase string) {
	if payload == "" {
		return "", ""
	}
	var p struct {
		DispatchID string `json:"dispatchId"`
		Phase      string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", ""
	}
	return p.DispatchID, p.Phase
}

// wakeNote renders a wake's note, the 200-char-capped summary the leader sees by default. A status wake carries the
// actual progress text (subject plus the first 200 chars of the body), because the subject alone is a phase label that
// reads like a story name and misleads the leader into thinking the worker is still in progress. Body leads when the
// subject is empty. A worker_done note strips the "Rejected worker_done:" prefix Orca adds to a second worker_done on a
// dispatch (F9), so the leader reads the real summary. Every other kind keeps the subject.
func wakeNote(kind wake.Kind, subject, body string) string {
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	if kind == wake.KindWorkerDone {
		return truncate(stripPrefixFold(subject, "rejected worker_done:"), 200)
	}
	if kind != wake.KindStatus {
		return truncate(subject, 200)
	}
	switch {
	case subject == "":
		return truncate(body, 200)
	case body == "":
		return subject
	default:
		return subject + " | " + truncate(body, 200)
	}
}

// wakeFull renders the untruncated note - the same shape as wakeNote but with the whole body - for `cox wake drain
// --full`. It is stored on the wake only when it differs from the capped note, so short wakes carry no duplicate text.
func wakeFull(kind wake.Kind, subject, body string) string {
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	if kind == wake.KindWorkerDone {
		return stripPrefixFold(subject, "rejected worker_done:")
	}
	if kind != wake.KindStatus {
		return subject
	}
	switch {
	case subject == "":
		return body
	case body == "":
		return subject
	default:
		return subject + " | " + body
	}
}

// stripPrefixFold removes a case-insensitive prefix (and following spaces) from s, or returns s unchanged.
func stripPrefixFold(s, prefix string) string {
	if strings.HasPrefix(strings.ToLower(s), prefix) {
		return strings.TrimSpace(s[len(prefix):])
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
