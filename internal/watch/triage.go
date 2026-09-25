package watch

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/protocol/decision"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// Wake triage, ported from firstmate bin/fm-watch.sh's main loop (pinned a8572f6): classify every worker signal and
// every quiet worker, ABSORB the benign majority, and SURFACE everything else. Absorb is only ever on positive evidence
// that the crew is still executing (crewClass); a crew that stopped its turn without a report is surfaced, so a finish
// reported only through an interactive menu is never swallowed (B-50).
//
// Name map (firstmate -> cox): a status-file line is a worker status mail or report; a .turn-ended marker is the busy
// record turning idle; the pane hash is the story's activity signature (busy record gen:seq, the last worker message or
// report, and a hash of the rendered screen tail); a queued `stale` row is a stale wake; `wake` (enqueue and exit) is an urgent wake for the story,
// after which the per-story stale loop skips that story for the rest of the tick.

const (
	// DefaultStaleQuiet is how long a worker's activity signature must stay unchanged before its pane reads stale: two
	// unchanged polls at firstmate's FM_POLL of 15s.
	DefaultStaleQuiet = 30 * time.Second
	// DefaultPauseResurface is FM_PAUSE_RESURFACE_SECS_DEFAULT: a declared wait (paused / captain-held) or a bounded
	// deferral re-surfaces once per this cadence for a recheck, far longer than the wedge threshold but finite.
	DefaultPauseResurface = 14400 * time.Second
	// WedgeDemandInspectCount is FM_WEDGE_DEMAND_INSPECT_COUNT: consecutive escalations on one quiet worker at which the
	// wake demands deep inspection.
	WedgeDemandInspectCount = 3
)

// Heartbeat backstop cadence: FM_HEARTBEAT base, doubling per consecutive no-change scan up to FM_HEARTBEAT_MAX.
const (
	DefaultHeartbeat    = 600 * time.Second
	DefaultHeartbeatMax = 7200 * time.Second
)

func (w *Watcher) pauseResurface() time.Duration {
	return orDur(w.PauseResurface, DefaultPauseResurface)
}

// --- per-story watch state (<epic>/.cox/watch/<sub>/<story>) ---

func (w *Watcher) spath(sub, story string) string { return filepath.Join(w.watchDir(), sub, story) }

func (w *Watcher) sread(sub, story string) (string, bool) {
	b, err := os.ReadFile(w.spath(sub, story))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// swrite publishes a state file atomically and stamps its mtime with the watcher clock, so ages read on the same clock.
func (w *Watcher) swrite(sub, story, val string) {
	if !w.controlTreePresent() {
		return // never resurrect a vanished control tree: the loop must still self-evict
	}
	p := w.spath(sub, story)
	if writeAtomic(p, []byte(val)) == nil {
		now := w.now()
		_ = os.Chtimes(p, now, now)
	}
}

// controlTreePresent reports whether <epic>/.cox exists; watch state is never written into a vanished one.
func (w *Watcher) controlTreePresent() bool {
	_, err := os.Stat(filepath.Join(w.EpicDir, state.ControlDir))
	return err == nil
}

func (w *Watcher) sexists(sub, story string) bool {
	_, err := os.Stat(w.spath(sub, story))
	return err == nil
}

func (w *Watcher) srm(story string, subs ...string) {
	for _, sub := range subs {
		_ = os.Remove(w.spath(sub, story))
	}
}

// sage is firstmate's age_of: time since the file's mtime, or "due immediately" (a very large age) when it is missing
// or dated in the future.
func (w *Watcher) sage(sub, story string) time.Duration {
	info, err := os.Stat(w.spath(sub, story))
	if err != nil || info.ModTime().After(w.now()) {
		return 1 << 62
	}
	return w.now().Sub(info.ModTime())
}

func (w *Watcher) sunix(sub, story string) (time.Time, bool) {
	v, ok := w.sread(sub, story)
	if !ok {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

func (w *Watcher) sstamp(sub, story string) {
	w.swrite(sub, story, strconv.FormatInt(w.now().Unix(), 10))
}

// --- the worker's status log ---

// statusLine is the last status line a story's worker wrote and when (its status-file mtime in firstmate).
func (w *Watcher) statusLine(story string) (string, time.Time) {
	v, ok := w.sread("status", story)
	if !ok {
		return "", time.Time{}
	}
	info, err := os.Stat(w.spath("status", story))
	if err != nil {
		return v, time.Time{}
	}
	return v, info.ModTime()
}

// recordStatus records a status line: the latest line (watch/status, whose mtime is the declaration time) and the
// append-only history (watch/statuslog) the declared-wait read folds, since a later answer for another key must not
// hide a standing wait (c6e816f). The history is best-effort: a failed append only loses that line from the fold.
func (w *Watcher) recordStatus(story, line string) {
	line = strings.TrimSpace(line)
	w.swrite("status", story, line)
	if !w.controlTreePresent() {
		return
	}
	p := w.spath("statuslog", story)
	if err := mkdirControl(filepath.Dir(p)); err != nil {
		return
	}
	if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
}

// declaredWaitLine is status_declared_wait_line over the story's status history: the paused / captain-held line that
// holds the worker in a declared wait, or "" when it is in none. Every "is this a declared wait" read goes through it;
// captain relevance and the declaration signature keep reading the latest line (statusLine).
func (w *Watcher) declaredWaitLine(story string) string {
	b, err := os.ReadFile(w.spath("statuslog", story))
	if err != nil {
		// No history (a watcher state written before the log existed): the latest line is the whole history.
		last, _ := w.statusLine(story)
		return decision.DeclaredWait([]string{last}, decision.Verbs{}, w.captainOverride())
	}
	return decision.DeclaredWait(strings.Split(string(b), "\n"), decision.Verbs{}, w.captainOverride())
}

// statusSig is fm_wake_signal_sig over the status file: the line and when it was written. Any new status event starts
// a new declaration.
func (w *Watcher) statusSig(story string) string {
	line, at := w.statusLine(story)
	return fmt.Sprintf("%d:%s", at.Unix(), line)
}

// statusFromMail maps a worker message to the status line firstmate's status file would carry.
func statusFromMail(m backend.Message) string {
	line := strings.TrimSpace(m.Subject)
	if line == "" {
		line = strings.TrimSpace(strings.SplitN(m.Body, "\n", 2)[0])
	}
	switch m.Type {
	case "worker_done", "merge_ready":
		return "done: " + line
	case "question":
		return "needs-decision: " + line
	case "escalation":
		return "blocked: " + line
	}
	return line
}

// markActivity records worker activity (a heartbeat) for the activity signature.
func (w *Watcher) markActivity(story, id string) { w.swrite("act", story, id) }

// spanAdd appends a status line to this tick's span for the story (fm: the bytes appended since the seen offset).
func (w *Watcher) spanAdd(story, line string) {
	if w.spans == nil {
		w.spans = map[string][]string{}
	}
	w.spans[story] = append(w.spans[story], line)
}

// captainOverride is the FM_CAPTAIN_RE override the span fold reads relevance with ("" => the default set).
func (w *Watcher) captainOverride() string {
	if w.CaptainRE == nil {
		return ""
	}
	return w.CaptainRE.String()
}

// spanEvents is a story span's actionable events as a multiset (decision.Actionable, the one fold).
type spanEvents map[string]int

func (w *Watcher) spanEvents(story string, span []string) spanEvents {
	events, _ := decision.Actionable(span, wake.StoryKind(w.EpicDir, story), w.captainOverride())
	m := spanEvents{}
	for _, e := range events {
		m[e]++
	}
	return m
}

// take consumes the event a span line produced: the line itself, or its "reconciliation-required: " label for a
// reserved key a foreign writer spoke. It returns the event text and whether the line was actionable.
func (m spanEvents) take(line string) (string, bool) {
	for _, e := range []string{line, reconPrefix + line} {
		if m[e] > 0 {
			m[e]--
			return e, true
		}
	}
	return "", false
}

const reconPrefix = "reconciliation-required: "

// spanDecision reports a needs-decision/blocked line whose key parses (an unkeyed line is the default key): the only
// lines the span fold can retire as closed or superseded.
func spanDecision(line string) bool {
	v := decision.Verb(line)
	_, ok := decision.Key(line)
	return ok && (v == "needs-decision" || v == "blocked")
}

// --- signals (fm scan_signals + signal_files_actionable + signal_crew_provably_working) ---

type signal struct {
	statuses []wake.Wake // routine status wakes this batch would queue for the story (mail path)
	lines    []string
	msgIDs   []string
	turnEnd  bool
}

// addSignal records a no-verb signal for a story in this tick's batch.
func (w *Watcher) addSignal(story string, fn func(*signal)) {
	if w.signals == nil {
		w.signals = map[string]*signal{}
	}
	s := w.signals[story]
	if s == nil {
		s = &signal{}
		w.signals[story] = s
	}
	fn(s)
}

// reportPass reads wakes a worker appended directly (the terminal plane's `cox story report`), so the status log and
// the turn-end coalescing see them like mail. A report's routine status is a no-verb signal.
func (w *Watcher) reportPass(working map[string]bool) {
	wakes, err := wake.Load(w.EpicDir)
	if err != nil {
		return
	}
	cursor, max := 0, 0
	v, ok := w.sread("cursor", "reports")
	if ok {
		cursor, _ = strconv.Atoi(strings.TrimSpace(v))
	} else {
		// First run (or wiped watch state): start at the queue's tip; history is not a fresh signal.
		for _, wk := range wakes {
			if wk.Gen > cursor {
				cursor = wk.Gen
			}
		}
		w.swrite("cursor", "reports", strconv.Itoa(cursor))
	}
	max = cursor
	for _, wk := range wakes {
		if wk.Gen <= cursor {
			continue
		}
		if wk.Gen > max {
			max = wk.Gen
		}
		if !working[wk.Story] || !workerOrigin(wk) || wk.Evidence["msg"] != nil {
			continue
		}
		line := ""
		switch wk.Kind {
		case wake.KindWorkerDone:
			line = "done: " + wk.Note
		case wake.KindStuck:
			line = "blocked: " + wk.Note
		case wake.KindInputRequired, wake.KindQuestion:
			line = "needs-decision: " + wk.Note
		case wake.KindStatus:
			line = wk.Note
			w.addSignal(wk.Story, func(s *signal) { s.lines = append(s.lines, line) })
		default:
			continue
		}
		w.recordStatus(wk.Story, line)
		w.spanAdd(wk.Story, line)
	}
	if max > cursor {
		w.swrite("cursor", "reports", strconv.Itoa(max))
	}
}

// workerOrigin reports whether a wake carries a worker's own report (mail or `cox story report`), not a watcher alarm.
func workerOrigin(wk wake.Wake) bool {
	if wk.Evidence != nil && wk.Evidence["by"] == "watch" {
		return false
	}
	switch wk.Kind {
	case wake.KindWorkerDone, wake.KindStuck, wake.KindInputRequired, wake.KindQuestion, wake.KindStatus, wake.KindPRReady:
		return true
	}
	return false
}

// turnEndPass raises a turn-end signal for every working story whose busy record turned idle since the last pass (fm
// .turn-ended). A turn-end already covered by a worker report that surfaced since the previous turn-end is coalesced
// into that report (fm SIGNAL_GRACE: a final status write and the same turn's turn-end are one wake).
func (w *Watcher) turnEndPass(working map[string]bool) {
	wakes, _ := wake.Load(w.EpicDir)
	maxGen := 0
	for _, wk := range wakes {
		if wk.Gen > maxGen {
			maxGen = wk.Gen
		}
	}
	for story := range working {
		rec, ok := busy.ReadRecord(w.EpicDir, story)
		if !ok || busy.Read(w.EpicDir, story) != busy.Idle {
			continue
		}
		sig := fmt.Sprintf("%s:%d", rec.Gen, rec.Seq)
		if prev, _ := w.sread("turnend", story); prev == sig {
			continue
		}
		cursor := 0
		if v, ok := w.sread("turnend-gen", story); ok {
			cursor, _ = strconv.Atoi(strings.TrimSpace(v))
		}
		covered := false
		for _, wk := range wakes {
			if wk.Gen > cursor && wk.Story == story && wake.IsUrgent(wk.Kind) && workerOrigin(wk) {
				covered = true
			}
		}
		w.swrite("turnend", story, sig)
		w.swrite("turnend-gen", story, strconv.Itoa(maxGen))
		if !covered {
			w.addSignal(story, func(s *signal) { s.turnEnd = true })
		}
	}
}

// signalTriage absorbs this tick's no-verb signals when EVERY crew in the batch is provably working, and otherwise
// queues them: a crew that is not provably working gets one urgent wake (it stopped its turn with nothing running, so
// it may be done, waiting on a decision, or wedged), a working one its routine status.
func (w *Watcher) signalTriage(open map[string]bool) (int, error) {
	if len(w.signals) == 0 {
		return 0, nil
	}
	stories := make([]string, 0, len(w.signals))
	for s := range w.signals {
		stories = append(stories, s)
	}
	sort.Strings(stories)
	// Actionable (fm signal_files_actionable / signal_crew_provably_working): a story in the batch already surfaced a
	// captain-relevant event this tick, or some crew in it is not provably working.
	class := map[string]string{}
	relevant := map[string]string{} // an actionable span event wake.Classify left routine (fm signal_files_actionable)
	held := map[string]string{}     // the span's side-band needs-decision flag with nothing surfaced for it (fm :2026)
	actionable := false
	for _, s := range stories {
		if !open[s] {
			continue
		}
		events, needsDecision := decision.Actionable(w.spans[s], wake.StoryKind(w.EpicDir, s), w.captainOverride())
		isEvent := map[string]bool{}
		for _, e := range events {
			isEvent[e] = true
		}
		for _, l := range w.signals[s].lines {
			if isEvent[l] {
				relevant[s] = l
			}
			if needsDecision && decision.IsCaptainHeld(l) {
				held[s] = l
			}
		}
		class[s] = w.crewClass(s)
		actionable = actionable || w.surfaced[s] || class[s] != crewWorking || relevant[s] != "" || held[s] != ""
	}
	appended := 0
	for _, s := range stories {
		sg := w.signals[s]
		switch {
		case open[s] && !actionable:
			// Benign: every crew is provably working, so the batch is absorbed (the suppressor still advances).
		case !open[s] || w.surfaced[s] || class[s] == crewWorking:
			// Queued as routine: an unsupervised story's log line, a story that already surfaced, or a working crew in
			// an actionable batch; a captain-relevant line the classifier left routine surfaces on its own.
			for _, wk := range sg.statuses {
				if _, err := wake.Append(w.EpicDir, wk); err != nil {
					return appended, err
				}
				appended++
			}
			if l := held[s]; l != "" && open[s] && !w.surfaced[s] {
				// A captain-held declaration is a leader-owed decision even while the crew works: fm marks its row
				// payload "needs-decision:" (fm-watch.sh:2806), cox raises input_required.
				if err := w.surface(s, wake.KindInputRequired, s+" holds a decision for the captain: "+l,
					map[string]any{"payload": "needs-decision:" + s}); err != nil {
					return appended, err
				}
				appended++
			}
			if l := relevant[s]; l != "" && open[s] && !w.surfaced[s] {
				if err := w.surface(s, wake.KindStale, s+" reported a captain-relevant status: "+l, nil); err != nil {
					return appended, err
				}
				appended++
			}
		default:
			// Not provably working: the story's last signal is surfaced (its earlier status lines queue as routine).
			n := len(sg.statuses)
			for _, wk := range sg.statuses[:max(n-1, 0)] {
				if _, err := wake.Append(w.EpicDir, wk); err != nil {
					return appended, err
				}
				appended++
			}
			// Name the CI evidence as read: "no running CI" only when the forge answered; an unreadable forge (or none
			// wired) is unknown, never a claim that nothing runs.
			ciNote := "no running CI"
			if _, known := w.ciRunning(s); !known {
				ciNote = "CI state unknown"
			}
			note := s + " turn ended with no report since it began and is not provably working (no busy turn, " + ciNote + ") - it may be done, waiting on a decision, or wedged"
			var ev map[string]any
			if len(sg.lines) > 0 {
				note = s + " is not provably working after its status: " + sg.lines[len(sg.lines)-1]
			}
			if n > 0 {
				ev = map[string]any{}
				for k, v := range sg.statuses[n-1].Evidence {
					ev[k] = v
				}
			}
			kind := wake.KindIdleNoDone
			if held[s] != "" {
				// fm-watch.sh:2806: a decision-owned span marks the row payload "needs-decision:".
				kind = wake.KindInputRequired
				if ev == nil {
					ev = map[string]any{}
				}
				ev["payload"] = "needs-decision:" + s
			}
			if err := w.surface(s, kind, note, ev); err != nil {
				return appended, err
			}
			appended++
		}
		for _, id := range sg.msgIDs {
			w.markSeen(id)
		}
		if !open[s] || actionable {
			w.markSurfaced(sg.msgIDs...)
		}
	}
	return appended, nil
}

// surface appends an urgent watcher wake for a story (the unread steer named in any stale wake) and records that the
// story surfaced this tick, so the stale loop leaves it alone until the next pass (fm: `wake` exits the cycle).
func (w *Watcher) surface(story string, kind wake.Kind, note string, ev map[string]any) error {
	if ev == nil {
		ev = map[string]any{}
	}
	ev["by"] = "watch"
	if kind == wake.KindStale || kind == wake.KindUnknownProbe {
		if steer := unreadSteer(w.EpicDir, story); steer != "" {
			note += "; unread steer " + steer
		}
	}
	wk := wake.Wake{Epic: filepath.Base(w.EpicDir), Story: story, Kind: kind, Note: truncate(note, 400), Evidence: ev}
	if len(note) > 400 {
		wk.Full = note
	}
	if _, err := wake.Append(w.EpicDir, wk); err != nil {
		return err
	}
	if w.surfaced == nil {
		w.surfaced = map[string]bool{}
	}
	w.surfaced[story] = true
	return nil
}

// unreadSteer names the oldest steer the worker has not acknowledged ("<story>/NNN.msg: <first line>"), or "".
func unreadSteer(epic, story string) string {
	recs, err := inbox.List(epic, story)
	if err != nil {
		return ""
	}
	for _, r := range recs {
		if r.Urgency == inbox.FYI || r.Kind == inbox.KindReply {
			continue
		}
		first := strings.TrimSpace(strings.SplitN(r.Body, "\n", 2)[0])
		return fmt.Sprintf("%s/%03d.msg: %s", story, r.Seq, truncate(first, 80))
	}
	return ""
}

// --- the per-story stale loop (fm "Layer 1 backbone: pane staleness") ---

// activitySig is the pane hash (fm-watch.sh hash_pane): it changes whenever the worker renders something - a harness
// busy event, a worker heartbeat, or new rows on its screen (a worker waiting on background agents keeps rendering, so
// it is never stale). A dispatch (re-)arm renders nothing, so a successor armed onto an identical dead display keeps the
// signature (its incarnation is told apart by the busy gen, wedgeDeadRecord). The screen is a staleness signal only,
// never a busy/idle source (fm-busy-lib.sh header): busyNow reads the harness busy record alone.
func (w *Watcher) activitySig(story string) string {
	sig := "-"
	if rec, ok := busy.ReadRecord(w.EpicDir, story); ok && rec.Source != "dispatch" {
		sig = fmt.Sprintf("%s:%d", rec.Gen, rec.Seq)
	}
	act, _ := w.sread("act", story)
	sig += "|" + act
	if p := w.paneHash(story); p != "" {
		sig += "|p:" + p
	}
	return sig
}

// paneTailRows bounds the hashed screen to its last rows (fm-watch.sh captures tail40 for hash_pane).
const paneTailRows = 40

// paneHash hashes the story's rendered screen tail read through Backend.Screen. An unreadable screen keeps the last good
// hash (fm skips the window for that poll), so a transient read error neither restarts the quiet clock nor fakes a
// change, and a backend that never reads a screen keeps a constant component. An empty screen renders nothing: "".
func (w *Watcher) paneHash(story string) string {
	sess, ok := w.Sessions[story]
	if !ok {
		return ""
	}
	rows, err := w.Backend.Screen(sess)
	if err != nil {
		prev, _ := w.sread("pane", story)
		return prev
	}
	rows = rows[max(0, len(rows)-paneTailRows):]
	h := ""
	if len(rows) > 0 {
		h = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(rows, "\n"))))[:16]
	}
	if prev, _ := w.sread("pane", story); prev != h {
		w.swrite("pane", story, h)
	}
	return h
}

// clockPath is the story's quiet clock: watch/hb/<dispatch>, whose mtime is when its activity signature last changed.
func (w *Watcher) clockKey(story string) string {
	if sess, ok := w.Sessions[story]; ok && sess.ID != "" {
		return sess.ID
	}
	return story
}

// stalePass walks every open story once (fm's per-window loop): a story whose activity signature changed restarts its
// quiet clock; one that stays quiet past DefaultStaleQuiet while not busy is classified once per quiet interval
// (terminal / working / declared wait / none) and then timed toward a possible-wedge escalation.
func (w *Watcher) stalePass(open map[string]bool) (int, error) {
	stories := make([]string, 0, len(open))
	for s := range open {
		stories = append(stories, s)
	}
	sort.Strings(stories)
	before := w.appendedCount
	for _, story := range stories {
		if _, ok := w.Sessions[story]; !ok {
			continue
		}
		if w.surfaced[story] {
			// fm exits the cycle on a surface; cox keeps the bookkeeping so the next tick reads the same quiet interval.
			w.recordQuiet(story)
			continue
		}
		if err := w.staleStory(story); err != nil {
			return w.appendedCount - before, err
		}
	}
	return w.appendedCount - before, nil
}

// recordQuiet keeps a surfaced story's quiet-interval bookkeeping (signature and clock) current without classifying it.
func (w *Watcher) recordQuiet(story string) {
	sig := w.activitySig(story)
	if prev, _ := w.sread("sig", story); prev != sig || w.sage("hb", w.clockKey(story)) > 1<<61 {
		w.swrite("sig", story, sig)
		w.sstamp("hb", w.clockKey(story))
	}
}

func (w *Watcher) staleStory(story string) error {
	last, _ := w.statusLine(story)
	wait := w.declaredWaitLine(story) // the declared-wait read; captain relevance stays on the latest line
	if !statusDeclaredWait(wait) && w.sexists("paused", story) {
		w.clearPauseTracking(story)
	}
	sig := w.activitySig(story)
	hb := w.clockKey(story)
	prev, _ := w.sread("sig", story)
	busyNow := w.busyNow(story)
	quiet := w.sage("hb", hb)
	if sig != prev || quiet > 1<<61 {
		// A new pane hash (or a missing / future-dated clock, repaired to now): a fresh quiet interval.
		w.swrite("sig", story, sig)
		w.sstamp("hb", hb)
		pausedBound := false
		if busyNow && w.busyTurnOverAge(story) {
			b, err := w.busyTurnBoundCheck(story, sig)
			if err != nil {
				return err
			}
			pausedBound = b
		} else {
			w.srm(story, "since", "esc")
			w.clearWriteTracking(story)
		}
		if statusDeclaredWait(wait) && !busyNow {
			switch w.pauseStateClass(story) {
			case "paused":
				return w.handlePausedStale(story, sig)
			case crewNone:
				w.clearStaleHashTracking(story)
			default:
				w.clearPauseTracking(story)
			}
		} else if !pausedBound && w.sexists("paused", story) {
			w.clearPauseTracking(story)
		}
		return nil
	}
	if quiet < DefaultStaleQuiet || busyNow {
		// Busy, or not yet stably stale: a busy worker past its turn-age bound is handed to the wedge timer.
		pausedBound := false
		if busyNow && w.busyTurnOverAge(story) {
			b, err := w.busyTurnBoundCheck(story, sig)
			if err != nil {
				return err
			}
			pausedBound = b
		} else {
			w.srm(story, "since", "esc")
			w.clearWriteTracking(story)
		}
		if !pausedBound && w.sexists("paused", story) && (quiet >= DefaultStaleQuiet || !statusDeclaredWait(wait)) {
			w.clearPauseTracking(story)
		}
		return nil
	}
	suppressed, _ := w.sread("stale", story)
	if captainRelevantRE(last, w.CaptainRE) {
		// The log's latest event is captain-relevant, but an active run or busy crew outranks a leftover line.
		if suppressed != sig {
			switch {
			case w.crewClass(story) == crewWorking:
				w.swrite("stale", story, sig)
				w.sstamp("since", story)
				w.clearWriteTracking(story)
			case w.captainCallBound(story):
				w.swrite("stale", story, sig)
				w.srm(story, "since")
				w.clearWriteTracking(story)
			default:
				if err := w.surface(story, wake.KindStale, "stale: "+story, nil); err != nil {
					return err
				}
				w.appendedCount++
				w.staleWaitRecord(story)
				w.swrite("stale", story, sig)
				w.srm(story, "since")
				w.clearWriteTracking(story)
			}
			return nil
		}
		if w.sexists("since", story) {
			return w.wedgeTimerCheck(story, "stale (overridden terminal status)", sig)
		}
		return nil
	}
	if suppressed != sig {
		switch w.pauseStateClass(story) {
		case crewWorking:
			w.clearPauseTracking(story)
			w.swrite("stale", story, sig)
			w.sstamp("since", story)
			return nil
		case "paused":
			return w.handlePausedStale(story, sig)
		default:
			return w.surfaceNonterminalStale(story, sig)
		}
	}
	if w.sexists("paused", story) || statusDeclaredWait(wait) {
		switch w.pauseStateClass(story) {
		case "paused":
			return w.handlePausedStale(story, sig)
		case crewWorking:
			w.clearPauseState(story)
			w.swrite("stale", story, sig)
			return w.wedgeTimerCheck(story, "non-terminal stale (provably working after a declared pause)", sig)
		default:
			return w.handlePausedStale(story, sig)
		}
	}
	return w.wedgeTimerCheck(story, "non-terminal stale", sig)
}

// busyTurnOverAge is busy_turn_over_age: the last completed turn or explicit native progress (the busy record's latest
// event, or a fresher checkpoint) is at least BusyTurnMax old. Before any event the spawn record (Arm) is aged.
func (w *Watcher) busyTurnOverAge(story string) bool {
	rec, ok := busy.ReadRecord(w.EpicDir, story)
	if !ok {
		return false
	}
	last := time.Unix(rec.TS, 0)
	if cp := w.lastCheckpointTime(story); cp.After(last) {
		last = cp
	}
	if p, ok := busy.ProgressAt(w.EpicDir, story); ok && p.After(last) {
		last = p // explicit native progress (fm: .progress newer than the turn marker)
	}
	return w.now().Sub(last) >= w.busyTurnMax()
}

// busyTurnBoundCheck owns a busy worker past its turn-age bound: a worker that declared its own wait takes the long
// recheck cadence (true); anything else is handed to the wedge timer (false). A first crossing also leaves the leader a
// routine note that the turn is long (a nudge, never an interrupt).
func (w *Watcher) busyTurnBoundCheck(story, sig string) (bool, error) {
	last := w.declaredWaitLine(story)
	if statusDeclaredWait(last) {
		return true, w.handlePausedStale(story, sig)
	}
	if !w.sexists("since", story) {
		rec, _ := busy.ReadRecord(w.EpicDir, story)
		// The timer starts when the bound was crossed, but never before the previous poll that could have seen it (a
		// watcher that just started, or a first sighting, starts it now: fm's first poll past the bound).
		crossed := time.Unix(rec.TS, 0).Add(w.busyTurnMax())
		if w.prevTick.IsZero() || crossed.Before(w.prevTick) {
			crossed = w.now()
		}
		w.swrite("since", story, strconv.FormatInt(crossed.Unix(), 10))
		if _, err := wake.Append(w.EpicDir, wake.Wake{
			Epic: filepath.Base(w.EpicDir), Story: story, Kind: wake.KindStatus,
			Note: fmt.Sprintf("%s busy %dm with no completed turn or native progress since - still running? (BusyTurnMax %s; the wedge timer starts now, never an interrupt)",
				story, int(w.now().Sub(time.Unix(rec.TS, 0)).Minutes()), w.busyTurnMax()),
			Evidence: map[string]any{"by": "watch", "busy_since": time.Unix(rec.TS, 0).UTC().Format(time.RFC3339)},
		}); err != nil {
			return false, err
		}
		w.appendedCount++
	}
	return false, w.wedgeTimerCheck(story, "busy (no completed turn)", sig)
}

// wedgeTimerCheck is wedge_timer_check: repair a missing timer, or at StaleMin consult (in order) the wait evidence,
// the worktree write probe and the dead-record probe, and only then escalate a possible wedge with a climbing count.
// Deviation (one poll): an escalation re-arms the timer at once instead of removing it for the next poll to reset.
func (w *Watcher) wedgeTimerCheck(story, label, sig string) error {
	since, ok := w.sunix("since", story)
	if !ok || since.After(w.now()) {
		w.clearWriteTracking(story)
		w.sstamp("since", story)
		return nil
	}
	age := w.now().Sub(since)
	if age < w.staleMin() {
		return nil
	}
	if rec, ok := w.wedgeWaitEvidence(story); ok {
		return w.wedgeDeferWait(story, age, rec)
	}
	if w.worktreeWrittenSince(story, since) {
		return w.wedgeDeferWriting(story, age)
	}
	if handled, err := w.wedgeDeadRecord(story, age, sig); handled || err != nil {
		return err
	}
	n := 1
	if v, ok := w.sread("esc", story); ok {
		if c, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			n = c + 1
		}
	}
	w.swrite("esc", story, strconv.Itoa(n))
	secs := int(age.Seconds())
	reason := fmt.Sprintf("stale: %s (idle %ds, possible wedge, escalation %d)", story, secs, n)
	if n >= WedgeDemandInspectCount {
		reason = fmt.Sprintf("stale: %s (idle %ds, possible wedge, escalation %d, demand-deep-inspection: same pane has wedge-escalated %d times in a row - do not re-absorb on the run-step/pane state alone)", story, secs, n, n)
	}
	if err := w.surface(story, wake.KindStale, reason, map[string]any{"escalation": n}); err != nil {
		return err
	}
	w.appendedCount++
	w.sstamp("since", story)
	w.clearWriteTracking(story)
	return nil
}

// waitRecord is fm wait_record: everything a declared-wait recheck prints, decided together.
type waitRecord struct {
	kind, subject, whom, action string
	anchor                      time.Time // when the wait began; zero when no written record exists
}

// wedgeWaitEvidence is wedge_wait_evidence: the worker's own declared wait explains a quiet worker. A paused line whose
// declared clearing time already passed explains nothing. (Firstmate's parked-gate evidence is a home opt-in over
// no-mistakes gate findings; cox has no gate-finding observable, so it is the unarmed default: off.)
func (w *Watcher) wedgeWaitEvidence(story string) (waitRecord, bool) {
	_, at := w.statusLine(story)
	last := w.declaredWaitLine(story)
	if statusCaptainHeld(last) {
		return waitRecord{"captain-held", "awaiting the captain - verified hold transfer", "captain",
			"answer the held decision or release the hold", at}, true
	}
	if statusPaused(last) {
		if until, ok := statusPausedUntil(last); ok && !w.now().Before(until) {
			return waitRecord{}, false
		}
		return waitRecord{"declared wait", "awaiting external", "external", "confirm the wait still holds", at}, true
	}
	return waitRecord{}, false
}

// wedgeDeferWait is wedge_defer_wait: defer one escalation to the declared wait (the idle timer restarts), re-surfacing
// it once per PauseResurface, aged from the declaration itself.
func (w *Watcher) wedgeDeferWait(story string, age time.Duration, rec waitRecord) error {
	wage, minAge, waited := age, time.Duration(0), ""
	if !rec.anchor.IsZero() {
		wage = w.now().Sub(rec.anchor)
		if wage < 0 {
			wage = 0
		}
		minAge = w.pauseResurface()
		waited = fmt.Sprintf(", waiting %ds", int(wage.Seconds()))
	}
	w.clearWriteTracking(story)
	w.sstamp("since", story)
	return w.resurfaceAbsorbed(story, "waiting-resurfaced", wage,
		fmt.Sprintf("stale: %s (idle %ds%s - %s, %s, rechecked on a long cadence not a wedge; %s)",
			story, int(age.Seconds()), waited, rec.kind, rec.subject, rec.action), "", minAge)
}

// wedgeDeferWriting is wedge_defer_writing: a quiet worker whose worktree is still being written defers one escalation;
// the write-deferral chain re-surfaces once per PauseResurface so churn without progress cannot stay invisible.
func (w *Watcher) wedgeDeferWriting(story string, age time.Duration) error {
	if !w.sexists("writing-since", story) {
		w.sstamp("writing-since", story)
	}
	wage := w.sage("writing-since", story)
	w.sstamp("since", story)
	return w.resurfaceAbsorbed(story, "writing-resurfaced", wage,
		fmt.Sprintf("stale: %s (idle %ds, writing its worktree for %ds, rechecked on a long cadence not a wedge; confirm the writes are real progress)",
			story, int(age.Seconds()), int(wage.Seconds())), "", w.pauseResurface())
}

// wedgeDeadRecord is wedge_dead_record: an endpoint the backend proves gone is reported once per incarnation instead of
// escalating forever; any verdict short of proof keeps the unchanged ladder. The incarnation is the busy record's gen,
// else the activity signature.
func (w *Watcher) wedgeDeadRecord(story string, age time.Duration, sig string) (bool, error) {
	live, err := w.probe(story)
	if err != nil || live != backend.Settled {
		w.srm(story, "dead")
		return false, nil
	}
	w.sstamp("since", story)
	id := sig
	if rec, ok := busy.ReadRecord(w.EpicDir, story); ok {
		id = rec.Gen
	}
	if v, _ := w.sread("dead", story); v == "gone "+id {
		return true, nil
	}
	reason := fmt.Sprintf("stale: %s (idle %ds, agent gone - the backend reports the worker session ended, so this is not a wedge; reported once and not re-escalated while it stays that way - reconcile this record, and check for unlanded work before any cleanup)",
		story, int(age.Seconds()))
	if err := w.surface(story, wake.KindStale, reason, map[string]any{"agent": "gone"}); err != nil {
		return true, err
	}
	w.appendedCount++
	w.swrite("dead", story, "gone "+id)
	w.clearWriteTracking(story)
	return true, nil
}

// resurfaceAbsorbed is resurface_absorbed: queue one recheck once the wait is at least minAge old and the throttle has
// not fired inside PauseResurface. A scoped throttle binds the recheck to one declaration: a different declaration
// re-surfaces at once.
func (w *Watcher) resurfaceAbsorbed(story, throttle string, age time.Duration, reason, scope string, minAge time.Duration) error {
	cur, has := w.sread(throttle, story)
	if scope == "" || !has || cur == scope {
		if age < minAge {
			return nil
		}
		if w.sage(throttle, story) < w.pauseResurface() {
			return nil
		}
	}
	if err := w.surface(story, wake.KindStale, reason, nil); err != nil {
		return err
	}
	w.appendedCount++
	if scope != "" {
		w.swrite(throttle, story, scope)
	} else {
		w.sstamp(throttle, story)
	}
	return nil
}

// handlePausedStale is handle_paused_stale: absorb a declared wait and re-surface it once per PauseResurface (anchored
// on the declaration's own age), naming whom the wait is on; a paused line's `until` time controls the recheck.
func (w *Watcher) handlePausedStale(story, sig string) error {
	w.swrite("stale", story, sig)
	w.swrite("paused", story, "")
	w.srm(story, "since", "esc")
	w.clearWriteTracking(story)
	_, at := w.statusLine(story)
	last := w.declaredWaitLine(story)
	now := w.now()
	if at.IsZero() {
		at = now
	}
	age := now.Sub(at)
	secs := int(age.Seconds())
	minAge := w.pauseResurface()
	declaration := "declared:" + w.statusSig(story)
	var reason string
	if statusCaptainHeld(last) {
		reason = fmt.Sprintf("captain-held %ds, awaiting the captain - verified hold transfer, rechecked on a long cadence not a wedge; answer the held decision or release the hold", secs)
	} else if until, ok := statusPausedUntil(last); ok {
		switch {
		case now.Before(until) && age < w.pauseResurface():
			return nil // declared time not reached
		case now.Before(until):
			reason = fmt.Sprintf("paused %ds, awaiting external - the declared time is beyond the recheck cadence; confirm the wait still holds", secs)
		default:
			reason = fmt.Sprintf("paused %ds, awaiting external - the declared clearing time has passed, rechecked on a long cadence not a wedge; confirm the wait cleared", secs)
			declaration += ":due"
			minAge = 0
		}
	} else {
		reason = fmt.Sprintf("paused %ds, awaiting external - declared pause, rechecked on a long cadence not a wedge; confirm the wait still holds", secs)
	}
	return w.resurfaceAbsorbed(story, "paused-resurfaced", age, "stale: "+story+" ("+reason+")", declaration, minAge)
}

// pauseStateClass is pause_state_class: a declared wait keeps the bounded cadence only for a crew whose agent is
// confidently gone; a live or ambiguously read agent reads none (surfaces on first sight, then the cadence), and an
// active run behind a declaration reads working.
func (w *Watcher) pauseStateClass(story string) string {
	last := w.declaredWaitLine(story)
	if !statusDeclaredWait(last) {
		w.srm(story, "paused-rechecked")
		return w.crewClass(story)
	}
	settled := func() bool {
		live, err := w.probe(story)
		return err == nil && live == backend.Settled
	}
	if w.sexists("paused", story) && w.sage("paused-rechecked", story) < w.staleMin() {
		if !settled() {
			w.srm(story, "paused-rechecked")
			return crewNone
		}
		return "paused"
	}
	if w.crewClass(story) == crewWorking {
		w.srm(story, "paused-rechecked")
		return crewWorking
	}
	if !settled() {
		w.srm(story, "paused-rechecked")
		return crewNone
	}
	w.sstamp("paused-rechecked", story)
	return "paused"
}

// captainCallBound is captain_call_stale_bound: while the story is held for the captain (cox: story state
// input_required, the leader's record that the work waits on a decision), a sighting inside the call's re-surface
// window is absorbed. The call identity is the input_required transition, so a release and a re-hold is a new call.
func (w *Watcher) captainCallBound(story string) bool {
	w.waitDecl = ""
	id, ok := w.captainCall(story)
	if !ok {
		return false
	}
	w.waitDecl = "captain-hold:" + id + ":" + w.statusSig(story)
	return w.waitThrottled(story, w.waitDecl)
}

// captainCall returns the identity of the story's open captain call (its latest transition into input_required).
func (w *Watcher) captainCall(story string) (string, bool) {
	events, _, err := state.Load(w.EpicDir)
	if err != nil {
		return "", false
	}
	id, open := "", false
	for i, ev := range events {
		if ev.Story != story || ev.To == "" {
			continue
		}
		open = ev.To == state.InputRequired
		if open {
			id = fmt.Sprintf("%d@%s", i, ev.TS)
		}
	}
	return id, open
}

// waitThrottled is stale_wait_throttled: this declaration already alarmed inside the current PauseResurface.
func (w *Watcher) waitThrottled(story, decl string) bool {
	cur, ok := w.sread("paused-resurfaced", story)
	return ok && cur == decl && w.sage("paused-resurfaced", story) < w.pauseResurface()
}

// staleWaitRecord is stale_wait_record: bind the fired alarm to its declaration, only after the append succeeded.
func (w *Watcher) staleWaitRecord(story string) {
	if w.waitDecl != "" {
		w.swrite("paused-resurfaced", story, w.waitDecl)
	}
}

// surfaceNonterminalStale is surface_nonterminal_stale: a quiet worker no classifier resolved surfaces on first sight
// (it may be done, waiting on a decision, or wedged), bounded to once per PauseResurface for a declared wait or an open
// captain call. The idle timer then starts, so the same quiet interval escalates on the wedge schedule.
func (w *Watcher) surfaceNonterminalStale(story, sig string) error {
	last := w.declaredWaitLine(story)
	declared, bounded, fire := false, false, true
	w.waitDecl = ""
	switch {
	case statusPaused(last):
		declared, bounded = true, true
		w.waitDecl = "declared:" + w.statusSig(story)
		if until, ok := statusPausedUntil(last); ok {
			if w.now().Before(until) {
				fire = false
			} else {
				w.waitDecl += ":due"
				if w.waitThrottled(story, w.waitDecl) {
					fire = false
				}
			}
		} else if w.waitThrottled(story, w.waitDecl) {
			fire = false
		}
	case statusCaptainHeld(last):
		declared, bounded = true, true
		w.waitDecl = "declared:" + w.statusSig(story)
		if w.waitThrottled(story, w.waitDecl) {
			fire = false
		}
	case w.captainCallBound(story):
		bounded, fire = true, false
	case w.waitDecl != "":
		bounded = true
	}
	if fire {
		kind, note, ev := wake.KindStale, "stale: "+story, map[string]any{}
		if _, err := w.probe(story); err != nil {
			// F08: a failed probe is presence-not-proof - an unknown_probe, and the quiet clock is kept.
			kind, note, ev["error"] = wake.KindUnknownProbe, "stale: "+story+" (liveness probe failed: "+err.Error()+")", err.Error()
		}
		if err := w.surface(story, kind, note, ev); err != nil {
			return err
		}
		w.appendedCount++
		w.staleWaitRecord(story)
	}
	w.swrite("stale", story, sig)
	if declared || bounded {
		w.srm(story, "since") // a declared wait or an open captain call keeps the long cadence, never the wedge timer
	} else {
		w.sstamp("since", story) // one poll early: fm removes it and the next poll's wedge_timer_check restarts it
	}
	w.clearWriteTracking(story)
	switch {
	case declared:
		w.swrite("paused", story, "")
		w.sstamp("paused-rechecked", story)
	case bounded:
		w.srm(story, "paused", "paused-rechecked")
	default:
		w.clearPauseState(story)
	}
	return nil
}

func (w *Watcher) clearWriteTracking(story string) {
	w.srm(story, "writing-since", "writing-resurfaced")
}
func (w *Watcher) clearPauseState(story string) {
	w.srm(story, "paused", "paused-rechecked", "paused-resurfaced")
}
func (w *Watcher) clearStaleHashTracking(story string) {
	w.clearWriteTracking(story)
	w.srm(story, "stale", "since", "esc", "waiting-resurfaced")
}
func (w *Watcher) clearPauseTracking(story string) {
	w.clearPauseState(story)
	w.clearStaleHashTracking(story)
}

// --- heartbeat backstop (fm heartbeat_scan_finds_actionable / mark_all_captain_relevant_surfaced) ---

// markSurfaced records message ids whose content reached the leader (an urgent wake, or a queued actionable batch).
func (w *Watcher) markSurfaced(ids ...string) {
	if len(ids) == 0 || !w.controlTreePresent() {
		return
	}
	_ = mkdirControl(w.watchDir())
	f, err := os.OpenFile(filepath.Join(w.watchDir(), "surfaced"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, id := range ids {
		if id != "" {
			fmt.Fprintln(f, id)
		}
	}
}

func (w *Watcher) loadSurfaced() map[string]bool {
	out := map[string]bool{}
	b, _ := os.ReadFile(filepath.Join(w.watchDir(), "surfaced"))
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out[l] = true
		}
	}
	return out
}

// heartbeatPass is the backstop behind the per-wake path: on the heartbeat cadence it scans every worker message still
// in the mailbox for a captain-relevant status that was seen but never surfaced (absorbed by mistake), and surfaces it.
// A scan that finds nothing is absorbed and backs the cadence off; a tick that surfaced anything resets it (fm wake()).
func (w *Watcher) heartbeatPass(open map[string]bool, dispatchStory map[string]string) error {
	if len(w.surfaced) > 0 {
		w.swrite("heartbeat", "streak", "0")
		return nil // fm: a surfacing cycle exits before the heartbeat block
	}
	streak := 0
	if v, ok := w.sread("heartbeat", "streak"); ok {
		streak, _ = strconv.Atoi(strings.TrimSpace(v))
	}
	if streak < 0 {
		streak = 0
	}
	if streak > 12 {
		streak = 12
	}
	interval := DefaultHeartbeat * time.Duration(1<<streak)
	if interval > DefaultHeartbeatMax {
		interval = DefaultHeartbeatMax
	}
	if w.sage("heartbeat", "last") < interval {
		return nil
	}
	seen, err := w.loadSeen()
	if err != nil {
		return err
	}
	surfaced := w.loadSurfaced()
	// The backstop reads each story's unsurfaced span through the same fold as the signal pass (fm
	// mark_all_captain_relevant_surfaced marks the endpoints status_span_first_actionable_record classified), so a
	// decision its own span closed is never resurrected here.
	type cand struct {
		m           backend.Message
		story, disp string
		line        string
	}
	var cands []cand
	spans := map[string][]string{}
	for _, m := range w.mailbox {
		if m.ID == "" || !seen[m.ID] || surfaced[m.ID] || m.Type == "heartbeat" {
			continue
		}
		disp, _ := payloadFields(m.Payload)
		if disp == "" {
			disp = strings.TrimPrefix(m.From, "dispatch:")
		}
		story := dispatchStory[disp]
		if !open[story] {
			continue
		}
		line := statusFromMail(m)
		cands = append(cands, cand{m, story, disp, line})
		spans[story] = append(spans[story], line)
	}
	live := map[string]spanEvents{}
	for story, span := range spans {
		live[story] = w.spanEvents(story, span)
	}
	var found []string
	for _, c := range cands {
		line, ok := live[c.story].take(c.line)
		if !ok {
			continue
		}
		if err := w.surface(c.story, wake.KindStale, "heartbeat backstop: "+c.story+" has a captain-relevant status that never reached the leader: "+line,
			map[string]any{"msg": c.m.ID, "dispatch": c.disp}); err != nil {
			return err
		}
		w.appendedCount++
		w.markSurfaced(c.m.ID) // recorded per append, so a later failure never re-surfaces this one
		found = append(found, c.m.ID)
	}
	w.sstamp("heartbeat", "last")
	if len(found) > 0 {
		w.swrite("heartbeat", "streak", "0")
		return nil
	}
	w.swrite("heartbeat", "streak", strconv.Itoa(streak+1))
	return nil
}
