//go:build port

// Port tests (wave 1, cox-supervision-port-triage): firstmate's wake triage translated case by case against cox's
// watcher passes. Firstmate pinned at 1e0e773 (references/firstmate, read only). Every case is
// t.Run("FM/<suite>/<case>") with a `// fm: path:line` citation and a `// cox:` mechanism tag; a case whose mechanism
// cox lacks calls notImplemented and fails. Red is the deliverable (DESIGN translation contract rules 1-8).
package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// notImplemented fails a case whose firstmate mechanism cox does not have, naming the gap (contract rule 3).
func notImplemented(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("not implemented in cox: %s", mechanism)
}

// portRig is one hermetic watcher under test: a temp epic, the repo's fake backend, and a mutable clock.
type portRig struct {
	t     *testing.T
	epic  string
	b     *fake.Backend
	mb    *fake.Mailbox
	clock time.Time
	w     *Watcher
}

const portStory = "s1"

// newPortRig builds an epic with story s1 in state working, a session for it (dispatch "ctx_s1"), and a leader handle.
// The clock starts at the real now so busy records (stamped with time.Now by the busy package) line up with it.
func newPortRig(t *testing.T) *portRig {
	t.Helper()
	epic := t.TempDir()
	r := &portRig{t: t, epic: epic, b: fake.New(), clock: time.Now()}
	r.mb = r.b.Mail().(*fake.Mailbox)
	r.w = &Watcher{EpicDir: epic, Backend: r.b, Now: func() time.Time { return r.clock }}
	portMust(t, state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: portStory, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true,
		Evidence: map[string]any{"dispatch": "ctx_" + portStory}}))
	portSession(t, epic, portStory, backend.Session{ID: "ctx_" + portStory, Handle: "term_" + portStory})
	portMust(t, os.WriteFile(filepath.Join(epic, state.ControlDir, "leader"), []byte("term_leader"), 0o644))
	return r
}

func portMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func portSession(t *testing.T, epic, story string, sess backend.Session) {
	t.Helper()
	dir := filepath.Join(epic, state.ControlDir, "sessions")
	portMust(t, os.MkdirAll(dir, 0o755))
	b, err := json.Marshal(sess)
	portMust(t, err)
	portMust(t, os.WriteFile(filepath.Join(dir, story+".json"), b, 0o644))
}

// busySet arms the story's busy record for a claude worker and applies state s through the trusted claude-hook source.
func (r *portRig) busySet(s string) {
	r.t.Helper()
	gen, err := busy.Arm(r.epic, portStory, "claude", []string{"dispatch", "claude-hook", "recovery"})
	portMust(r.t, err)
	if s != busy.Busy {
		portMust(r.t, busy.Apply(r.epic, portStory, s, gen, "claude-hook", "Stop"))
	}
}

// mail queues one worker message from the story's dispatch.
func (r *portRig) mail(id, typ, subject string) {
	r.mb.Queue = append(r.mb.Queue, backend.Message{ID: id, From: "dispatch:ctx_" + portStory, Type: typ, Subject: subject,
		Payload: `{"dispatchId":"ctx_` + portStory + `"}`})
}

// advance moves the fake clock forward.
func (r *portRig) advance(d time.Duration) { r.clock = r.clock.Add(d) }

// tick runs one watcher pass and returns the wakes appended by it.
func (r *portRig) tick() []wake.Wake {
	r.t.Helper()
	before, err := wake.Drain(r.epic, true)
	portMust(r.t, err)
	_, err = r.w.Tick()
	portMust(r.t, err)
	after, err := wake.Drain(r.epic, true)
	portMust(r.t, err)
	return after[len(before):]
}

// urgentFor reports whether any wake in ws is urgent for the story.
func urgentFor(ws []wake.Wake, story string) bool {
	for _, w := range ws {
		if w.Story == story && wake.IsUrgent(w.Kind) {
			return true
		}
	}
	return false
}

// composer sets what the backend's own composer classifier reports (used only when no busy record answers).
func (r *portRig) composer(cs string) { r.b.ComposerState = cs }

// liveness sets what a backend Probe reports.
func (r *portRig) liveness(l backend.Liveness) { r.b.Liveness = l }

// steer writes one leader steer into the story's durable inbox (its mtime is the real now; advance the clock to age it).
func (r *portRig) steer(text string) string {
	r.t.Helper()
	p, err := inbox.Write(r.epic, portStory, text, inbox.Steer, "")
	portMust(r.t, err)
	return p
}

// reply writes a question answer into the inbox (kind=reply), as `cox reply` does.
func (r *portRig) reply(text string) string {
	r.t.Helper()
	p, err := inbox.WriteReply(r.epic, portStory, text)
	portMust(r.t, err)
	return p
}

// heartbeat queues a heartbeat mail (phase optional), the liveness ping stalePass ages.
func (r *portRig) heartbeat(id string) {
	r.mb.Queue = append(r.mb.Queue, backend.Message{ID: id, From: "dispatch:ctx_" + portStory, Type: "heartbeat",
		Payload: `{"dispatchId":"ctx_` + portStory + `"}`})
}

// kinds lists the kinds of ws for failure messages.
func kinds(ws []wake.Wake) []wake.Kind {
	out := make([]wake.Kind, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Kind)
	}
	return out
}

// wantSurfaced asserts the tick raised an urgent wake for the story (firstmate: "surfaced" - queued and the watcher exits).
func wantSurfaced(t *testing.T, ws []wake.Wake, why string) {
	t.Helper()
	if !urgentFor(ws, portStory) {
		t.Errorf("want an urgent wake (%s), got %v", why, kinds(ws))
	}
}

// wantAbsorbed asserts the tick raised no wake for the story (firstmate: "absorbed" - no queue entry, no exit).
func wantAbsorbed(t *testing.T, ws []wake.Wake, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory {
			t.Errorf("want absorbed (%s), got wake %s: %s", why, w.Kind, w.Note)
		}
	}
}

func TestPortHarness(t *testing.T) {
	t.Run("Harness/smoke", func(t *testing.T) {
		r := newPortRig(t)
		if ws := r.tick(); len(ws) != 0 {
			t.Fatalf("empty epic tick appended %v", ws)
		}
	})
}

// Mechanism names (strings shared with the other parts; the report groups the red list by them).
const (
	pA1MechTurnEnd    = "turn-end triage: a stopped worker with no report since its turn began surfaces (idleNoDonePass requires a steer)"
	pA1MechStale      = "stale escalation: a silent worker past the stale threshold escalates (stalePass only fires on a failed/unknown probe)"
	pA1MechDeclWait   = "declared wait: paused / captain-held status verbs"
	pA1MechFold       = "decision fold: open/close decisions by [key=] across a worker's status history"
	pA1MechWedge      = "wedge detector: escalation schedule, deep inspection, write deferral"
	pA1MechRunStep    = "run-step authority: an active pipeline run overrides a stale/terminal status"
	pA1MechOverride   = "captain-relevance override (FM_CAPTAIN_RE)"
	pA1MechStatusMail = "status mail classification: mailPass + wake.Classify"
)

// pA1Lines queues each line as one worker status mail on a fresh rig, runs one tick, and returns the rig and its wakes.
func pA1Lines(t *testing.T, lines ...string) (*portRig, []wake.Wake) {
	t.Helper()
	r := newPortRig(t)
	for i, l := range lines {
		r.mail(fmt.Sprintf("a1-%d", i), "status", l)
	}
	return r, r.tick()
}

// pA1Routine asserts no urgent wake for the story (firstmate: benign / not captain-relevant).
func pA1Routine(t *testing.T, ws []wake.Wake, why string) {
	t.Helper()
	if urgentFor(ws, portStory) {
		t.Errorf("want routine (%s), got urgent %v", why, kinds(ws))
	}
}

// pA1UrgentNote asserts an urgent wake for the story whose note carries want (the event reported as itself).
func pA1UrgentNote(t *testing.T, ws []wake.Wake, want string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory && wake.IsUrgent(w.Kind) && strings.Contains(w.Note, want) {
			return
		}
	}
	t.Errorf("want an urgent wake naming %q, got %v", want, ws)
}

// pA1AddStory adds a second working story with its own session and dispatch id ctx_<id>.
func pA1AddStory(r *portRig, id string) {
	r.t.Helper()
	portMust(r.t, state.Append(r.epic, state.Event{Epic: filepath.Base(r.epic), Story: id, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true,
		Evidence: map[string]any{"dispatch": "ctx_" + id}}))
	portSession(r.t, r.epic, id, backend.Session{ID: "ctx_" + id, Handle: "term_" + id})
}

// pA1BusySet is busySet for any story.
func pA1BusySet(r *portRig, story, s string) {
	r.t.Helper()
	gen, err := busy.Arm(r.epic, story, "claude", []string{"dispatch", "claude-hook", "recovery"})
	portMust(r.t, err)
	if s != busy.Busy {
		portMust(r.t, busy.Apply(r.epic, story, s, gen, "claude-hook", "Stop"))
	}
}

// pA1RunOnce runs the real Run loop for exactly one pass (tick + beacon) with a pre-closed stop channel.
func pA1RunOnce(r *portRig) []wake.Wake {
	r.t.Helper()
	before, err := wake.Drain(r.epic, true)
	portMust(r.t, err)
	stop := make(chan struct{})
	close(stop)
	r.w.Run(stop, time.Hour)
	after, err := wake.Drain(r.epic, true)
	portMust(r.t, err)
	return after[len(before):]
}

func TestPortTriageA1(t *testing.T) {
	const s = "FM/fm-watch-triage/"

	t.Run(s+"status_span_actionable_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:193
		// cox: pA1MechStatusMail
		_, ws := pA1Lines(t, "working: step 1", "working: step 2")
		pA1Routine(t, ws, "a benign working: span")
		_, ws = pA1Lines(t, "working: x", "needs-decision: pick A or B")
		wantSurfaced(t, ws, "a needs-decision span is captain-relevant")
		_, ws = pA1Lines(t, "failed: build broke on main")
		wantSurfaced(t, ws, "a failed: line must always wake")
		_, ws = pA1Lines(t, "merged")
		wantSurfaced(t, ws, "a legacy merged line must always wake")
		// Already-classified events must not re-fire: cox dedups by message id (the offset's analog), so a second tick
		// with nothing new raises nothing, and a routine append after the decision is routine.
		r := newPortRig(t)
		r.mail("b1", "status", "working: x")
		r.mail("b2", "status", "needs-decision: pick A or B")
		r.tick()
		wantAbsorbed(t, r.tick(), "an already-classified span re-fired on the next tick")
		r.mail("b3", "status", "working: tidying up")
		pA1Routine(t, r.tick(), "a routine append after a classified decision")
		// The empty / malformed / past-the-end offset cases are bash byte-offset parsing: cox has no offset (each line
		// is its own message, deduped by id), so there is nothing further to assert.
	})

	t.Run(s+"status_span_survives_a_later_routine_append", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:224
		// cox: pA1MechStatusMail
		_, ws := pA1Lines(t, "working: setup", "needs-decision: pick A or B", "working: still tidying the branch")
		pA1UrgentNote(t, ws, "needs-decision: pick A or B")
		_, ws = pA1Lines(t, "working: publishing", "done: release 1.4.0 published and installed",
			"working: cleaning the build dir", "note: cache pruned")
		pA1UrgentNote(t, ws, "release 1.4.0 published and installed")
		_, ws = pA1Lines(t, "blocked: cannot reach the release host", "paused: waiting for release access")
		pA1UrgentNote(t, ws, "blocked: cannot reach the release host")
	})

	t.Run(s+"status_span_respects_decision_closure", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:253
		// cox: pA1MechFold
		_, ws := pA1Lines(t, "needs-decision [key=api]: pick A or B", "resolved [key=api]: took A")
		pA1Routine(t, ws, "a decision the same span already closed")
		_, ws = pA1Lines(t, "needs-decision [key=api]: pick A or B", "resolved [key=api]: took A",
			"needs-decision: [key=api] pick A or B")
		pA1UrgentNote(t, ws, "needs-decision: [key=api] pick A or B")
		_, ws = pA1Lines(t, "failed: build broke on main", "resolved [key=api]: unrelated")
		wantSurfaced(t, ws, "a failed: event is never retired by a closure")
		_, ws = pA1Lines(t, "needs-decision [key=api]: pick A or B", "needs-decision [key=db]: pick a store",
			"resolved [key=db]: took sqlite")
		pA1UrgentNote(t, ws, "needs-decision [key=api]: pick A or B")
		// A rejected reserved-key request surfaces labeled reconciliation-required and opens no decision: needs the fold.
		notImplemented(t, pA1MechFold)
	})

	t.Run(s+"status_span_closure_from_an_offset", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:292
		// cox: pA1MechFold
		r := newPortRig(t)
		r.mail("c1", "status", "needs-decision [key=api]: pick A or B")
		r.mail("c2", "status", "working: prototyping both")
		r.tick()
		r.mail("c3", "status", "resolved [key=api]: took A")
		r.mail("c4", "status", "working: shipping A")
		pA1Routine(t, r.tick(), "a close for a decision opened before the span")
		r.mail("c5", "status", "needs-decision [key=db]: pick a store")
		r.mail("c6", "status", "resolved [key=db]: took sqlite")
		r.mail("c7", "status", "needs-decision [key=api]: revisit A or B")
		r.mail("c8", "status", "working: waiting")
		pA1UrgentNote(t, r.tick(), "needs-decision [key=api]: revisit A or B")
		notImplemented(t, pA1MechFold)
	})

	t.Run(s+"malformed_seen_signature_reads_the_whole_log", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:309
		// cox: pA1MechStatusMail
		// cox's suppressor is watch/seen (message ids); a malformed one must not skip the actionable message.
		r := newPortRig(t)
		portMust(t, os.MkdirAll(r.w.watchDir(), 0o755))
		portMust(t, os.WriteFile(filepath.Join(r.w.watchDir(), "seen"), []byte("40"), 0o644))
		r.mail("d1", "status", "needs-decision: choose the release target")
		r.mail("d2", "status", "working: cleanup")
		ws := r.tick()
		found := false
		for _, w := range ws {
			if w.Story == portStory && w.Evidence["msg"] == "d1" {
				found = true
			}
		}
		if !found {
			t.Errorf("a malformed seen file suppressed the actionable message, got %v", ws)
		}
		wantSurfaced(t, ws, "the needs-decision at the start of the log")
	})

	t.Run(s+"stale_is_terminal_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:324
		// cox: pA1MechDeclWait
		// A terminal last status surfaces; cox surfaces a done: status at once as worker_done (no stale wait needed).
		_, ws := pA1Lines(t, "done: ready in branch fm/x")
		wantSurfaced(t, ws, "a terminal done: status")
		_, ws = pA1Lines(t, "working: compiling")
		pA1Routine(t, ws, "a non-terminal working: status")
		// Prose mentioning a legacy token inside a multi-line pause is not terminal.
		r := newPortRig(t)
		r.mb.Queue = append(r.mb.Queue, backend.Message{ID: "e1", From: "dispatch:ctx_" + portStory, Type: "status",
			Subject: "paused: waiting on upstream PR #123 to land", Body: "Once it is merged I will rebase and continue.",
			Payload: `{"dispatchId":"ctx_` + portStory + `"}`})
		pA1Routine(t, r.tick(), "a multi-line pause mentioning merged")
		// No status at all is benign (the herdr metadata resolution is a firstmate-only surface).
		pA1Routine(t, newPortRig(t).tick(), "no status")
		// The multi-line pause must be recognized by the wait cadence: cox has no declared-wait verb.
		notImplemented(t, pA1MechDeclWait)
	})

	t.Run(s+"classifier_primitives", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:342
		// cox: pA1MechStatusMail
		// last_status_line (blank lines, continuation prose) is bash file parsing: a cox status is one message whose
		// subject is the verb line. window_to_task is tmux/herdr pane naming (n/a).
		for _, l := range []string{"done: b", "needs-decision [key=q1]: b", "done: PR https://x/pull/76 checks green",
			"merged", "PR ready https://x/pull/2"} {
			_, ws := pA1Lines(t, l)
			wantSurfaced(t, ws, "captain-relevant: "+l)
		}
		for _, l := range []string{"working: b",
			"working: stage 2 setup complete on PR #74 exact source branch rebased onto merged #76; task dates preserved",
			"working: rebased onto predecessor #76",
			"working: PR ready checks green merged ready in branch",
			"working: rebased onto merged #76"} {
			_, ws := pA1Lines(t, l)
			pA1Routine(t, ws, "not captain-relevant: "+l)
		}
		// FM_CAPTAIN_RE override, keyed open decisions and keyed activity phases have no cox mechanism.
		notImplemented(t, pA1MechOverride)
	})

	t.Run(s+"crew_is_provably_working_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:423
		// cox: pA1MechTurnEnd
		// Busy pane -> busy record busy: absorbed past the idle window.
		r := newPortRig(t)
		r.busySet(busy.Busy)
		r.mail("f1", "status", "working: compiling")
		r.tick()
		r.advance(6 * time.Minute)
		wantAbsorbed(t, r.tick(), "a provably working (busy) worker")
		// Stale status-log working: with the harness idle -> not provable, must surface.
		r = newPortRig(t)
		r.busySet(busy.Idle)
		r.mail("f2", "status", "working: compiling")
		r.tick()
		r.advance(6 * time.Minute)
		wantSurfaced(t, r.tick(), "a stale working: status with the worker stopped is not provably working")
		// Same with no busy record at all and an empty composer.
		r = newPortRig(t)
		r.composer(backend.ComposerEmpty)
		r.mail("f3", "status", "working: compiling")
		r.tick()
		r.advance(6 * time.Minute)
		wantSurfaced(t, r.tick(), "a status-log working: alone is not provably working")
		// Finished / unknown crew: a busy record the backend contradicts (session settled) must surface (B-51).
		r = newPortRig(t)
		r.busySet(busy.Busy)
		r.liveness(backend.Settled)
		r.heartbeat("f4")
		r.tick()
		r.advance(21 * time.Minute)
		wantSurfaced(t, r.tick(), "a busy record contradicted by a settled session")
		// Active run-step as proof of work has no cox analog.
		notImplemented(t, pA1MechRunStep)
	})

	t.Run(s+"status_is_paused_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:455
		// cox: pA1MechDeclWait
		_, ws := pA1Lines(t, "paused: holding for the upstream release")
		pA1Routine(t, ws, "a pause is not captain-relevant")
		_, ws = pA1Lines(t, "blocked: the build is paused upstream")
		wantSurfaced(t, ws, "a genuine blocker stays a blocker")
		notImplemented(t, pA1MechDeclWait)
	})

	t.Run(s+"crew_absorb_class_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:488
		// cox: pA1MechStale
		stale := func(r *portRig) []wake.Wake {
			r.heartbeat("g-hb")
			r.tick()
			r.advance(21 * time.Minute)
			return r.tick()
		}
		// working (busy pane) -> absorbed on the stale path.
		r := newPortRig(t)
		r.busySet(busy.Busy)
		r.liveness(backend.Alive)
		wantAbsorbed(t, stale(r), "a busy worker on the stale path")
		// Stale working: status-log with the worker stopped -> none -> surface (the stale threshold escalates).
		r = newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Alive)
		r.mail("g1", "status", "working: compiling")
		wantSurfaced(t, stale(r), "a stopped worker silent past the stale threshold")
		// Unknown crew -> none -> surface (cox raises a routine unknown_probe, not an urgent wake).
		r = newPortRig(t)
		r.liveness(backend.Unknown)
		wantSurfaced(t, stale(r), "an unknown crew past the stale threshold")
		// paused -> absorbed as a declared wait: no cox verb.
		notImplemented(t, pA1MechDeclWait)
	})

	t.Run(s+"crew_worktree_written_since_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:515
		// cox: pA1MechWedge
		// Worktree writes as the wedge detector's third liveness input (.git pruned, secondmate homes excluded).
		notImplemented(t, pA1MechWedge)
	})

	t.Run(s+"empty_write_prune_widens_the_probe", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:580
		// cox: pA1MechWedge
		// An empty prune list widens the worktree write probe instead of disabling it.
		notImplemented(t, pA1MechWedge)
	})

	// n/a test_empty_write_prune_from_the_environment_widens_the_probe (fm-watch-triage.test.sh:615): bash-only concern,
	// an exported empty FM_WORKTREE_WRITE_PRUNE vs the ${VAR:-default} colon form; the widening requirement itself is
	// pinned by empty_write_prune_widens_the_probe.

	t.Run(s+"worktree_write_probe_is_wall_clock_bounded", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:642
		// cox: pA1MechWedge
		// The write probe runs inside the escalating poll and must be wall-clock bounded (a hit past the bound = none).
		notImplemented(t, pA1MechWedge)
	})

	t.Run(s+"signal_crew_provably_working_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:683
		// cox: pA1MechTurnEnd
		// A coalesced batch is benign only when EVERY crew is provably working: s1 busy, s2 stopped with no report.
		r := newPortRig(t)
		pA1AddStory(r, "s2")
		r.busySet(busy.Busy)
		pA1BusySet(r, "s2", busy.Idle)
		r.tick()
		r.advance(6 * time.Minute)
		ws := r.tick()
		wantAbsorbed(t, ws, "the provably working crew in the batch")
		if !urgentFor(ws, "s2") {
			t.Errorf("want an urgent wake for the stopped crew s2 in the batch, got %v", kinds(ws))
		}
		// A non-signal file / an empty file list are bash argument handling (n/a).
	})

	// n/a test_secondmate_status_signal_never_absorbed_classifier (fm-watch-triage.test.sh:703): secondmates (a mate's
	// routed-reply channel keyed on kind=secondmate) are a firstmate-only surface; cox has no nested leaders.

	t.Run(s+"provably_working_signal_absorbed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:729
		// cox: pA1MechRunStep
		// Proof of work here is an active run-step; the closest cox proof is a busy record.
		r := newPortRig(t)
		r.busySet(busy.Busy)
		r.mail("h1", "status", "working: compiling step 2")
		wantAbsorbed(t, pA1RunOnce(r), "a no-verb working: status from a provably working worker")
		wantAbsorbed(t, r.tick(), "the suppressor did not advance: the same status re-fired")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "lasttick")); err != nil {
			t.Errorf("watcher beacon not written while absorbing: %v", err)
		}
		notImplemented(t, pA1MechRunStep)
	})

	t.Run(s+"turn_ended_provably_working_absorbed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:751
		// cox: pA1MechTurnEnd
		// Turn ended, then a queued continuation made the harness busy again: absorbed.
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.busySet(busy.Busy)
		wantAbsorbed(t, r.tick(), "a turn-end whose worker is busy again")
		r.advance(6 * time.Minute)
		wantAbsorbed(t, r.tick(), "a turn-end whose worker is busy again, past the idle window")
	})

	t.Run(s+"turn_ended_not_working_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:774
		// cox: pA1MechTurnEnd
		// The swallowed-finish fix (B-50): the worker's turn ended, no status line, no steer, nothing running.
		r := newPortRig(t)
		r.busySet(busy.Idle)
		ws := r.tick()
		r.advance(6 * time.Minute) // give cox its own idle window too
		ws = append(ws, r.tick()...)
		wantSurfaced(t, ws, "a turn-end whose worker is not provably working")
	})
}

const (
	pA2MechTurnEnd    = "turn-end triage: a stopped worker with no report since its turn began surfaces (idleNoDonePass requires a steer)"
	pA2MechStale      = "stale escalation: a silent worker past the stale threshold escalates (stalePass only fires on a failed/unknown probe)"
	pA2MechFold       = "decision fold: open/close decisions by [key=] across a worker's status history"
	pA2MechSpan       = "status span: an actionable status survives later routine appends"
	pA2MechWedge      = "wedge detector: escalation schedule, deep inspection, write deferral"
	pA2MechRunStep    = "run-step authority: an active pipeline run overrides a stale/terminal status"
	pA2MechHBBackstop = "heartbeat backstop: periodic re-surface of unsurfaced status"
)

// pA2QuietWithSteer makes s1 look finished-but-unreported to idleNoDonePass: its last mail is older than IdleNoDoneWait
// and a leader steer is pending with no worker_done. Only the composer verdict then decides surface vs absorb, so an
// absorb assertion after it is not vacuous.
func pA2QuietWithSteer(r *portRig) {
	r.t.Helper()
	r.mail("m-quiet", "status", "working: started")
	r.tick()
	r.steer("carry on")
	r.advance(10 * time.Minute)
}

// pA2Wakes returns the wakes in ws for the story.
func pA2Wakes(ws []wake.Wake) []wake.Wake {
	var out []wake.Wake
	for _, w := range ws {
		if w.Story == portStory {
			out = append(out, w)
		}
	}
	return out
}

func TestPortTriageA2(t *testing.T) {
	const s = "FM/fm-watch-triage/"

	t.Run(s+"turn_ended_churning_pane_absorbed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:827
		// cox: composerState (busy record busy = positive evidence of work)
		r := newPortRig(t)
		pA2QuietWithSteer(r)
		r.busySet(busy.Idle) // the turn ended ...
		r.busySet(busy.Busy) // ... and the worker is provably rendering a new turn (fm: pane churned since last poll)
		wantAbsorbed(t, r.tick(), "a turn-end from a provably working worker")
	})

	t.Run(s+"turn_ended_churn_resets_prior_stale_classification", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:861
		// cox: stale escalation
		r := newPortRig(t)
		r.liveness(backend.Alive)
		r.heartbeat("hb1")
		r.busySet(busy.Busy) // churn: a fresh interval of activity
		wantAbsorbed(t, r.tick(), "an active worker")
		r.busySet(busy.Idle) // then it stops and stays quiet
		r.advance(r.w.staleMin() + time.Minute)
		ws := r.tick()
		stale := false
		for _, w := range pA2Wakes(ws) {
			if w.Kind == wake.KindStale || w.Kind == wake.KindUnknownProbe {
				stale = true
			}
		}
		if !stale {
			t.Errorf("a worker stopped past StaleMin after a fresh activity interval did not surface as stale, got %v", kinds(ws))
		}
		notImplemented(t, pA2MechStale)
	})

	t.Run(s+"turn_ended_churn_resets_wedge_state_before_stale_poll", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:907
		// cox: wedge detector
		r := newPortRig(t)
		pA2QuietWithSteer(r)
		r.busySet(busy.Busy)
		wantAbsorbed(t, r.tick(), "a churning (busy) turn-end")
		// The observable is the prior quiet interval's wedge-escalation count being reset; cox keeps no such count.
		notImplemented(t, pA2MechWedge)
	})

	// n/a test_turn_ended_churn_existing_marker_absorbed (fm-watch-triage.test.sh:943): stock bash 3.2 empty-array
	// regression under set -u plus preservation of the .churn-since pane-churn marker; the absorb requirement itself is
	// covered by turn_ended_churning_pane_absorbed.

	t.Run(s+"turn_ended_still_pane_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:979
		// cox: turn-end triage
		r := newPortRig(t)
		r.busySet(busy.Idle) // the turn ended; no status line, no steer, nothing rendered since
		wantSurfaced(t, r.tick(), "a bare turn-end with no evidence of work")
		notImplemented(t, pA2MechTurnEnd)
	})

	t.Run(s+"turn_ended_malformed_prior_hash_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1007
		// cox: turn-end triage (malformed evidence of work fails toward waking)
		r := newPortRig(t)
		r.busySet(busy.Idle)
		portMust(t, os.WriteFile(busy.Path(r.epic, portStory), []byte("x"), 0o644)) // fm: a malformed prior hash
		if busy.Read(r.epic, portStory) == busy.Busy {
			t.Errorf("a malformed busy record read as busy")
		}
		wantSurfaced(t, r.tick(), "a turn-end backed by malformed evidence")
		notImplemented(t, pA2MechTurnEnd)
	})

	// n/a test_turn_ended_trailing_newline_prior_hash_surfaced (fm-watch-triage.test.sh:1035): byte-exact parsing of a
	// tmux pane-hash file (a trailing newline); cox has no pane hashing.

	// n/a test_secondmate_turn_ended_churning_pane_surfaced (fm-watch-triage.test.sh:1065): secondmate endpoints and pane
	// churn; cox has no secondmates.

	// n/a test_turn_ended_colliding_window_key_surfaced (fm-watch-triage.test.sh:1093): tmux window names flattened to a
	// state-file key (tr ':/.' '___') can collide; cox keys every record by story id, so no collision exists.

	// n/a test_turn_ended_duplicate_endpoint_records_surfaced (fm-watch-triage.test.sh:1122): two .meta records naming
	// one tmux window make pane churn unattributable; cox evidence (the busy record) is per story, not per pane.

	t.Run(s+"turn_ended_mixed_positive_evidence_batch_absorbed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1153
		// cox: run-step authority
		r := newPortRig(t)
		pA2QuietWithSteer(r)
		r.busySet(busy.Busy) // the churn-proven task: busy record busy
		wantAbsorbed(t, r.tick(), "a turn-end from a provably working worker")
		// The batch's other task is absorbed on run-step authority (source: run-step running); cox has no run step.
		notImplemented(t, pA2MechRunStep)
	})

	// n/a test_turn_ended_mixed_positive_evidence_batch_default_off (fm-watch-triage.test.sh:1190): the home opt-in
	// config flag for pane-churn evidence; cox's busy record is harness-verified, so there is no opt-in knob.

	t.Run(s+"status_and_turn_end_batch_never_uses_churn_evidence", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1230
		// cox: turn-end triage
		r := newPortRig(t)
		r.busySet(busy.Busy) // the evidence that must not absorb a status-bearing batch
		r.mail("m1", "status", "working: authoritative task still running")
		ws := r.tick()
		if len(pA2Wakes(ws)) == 0 {
			t.Errorf("busy evidence suppressed a status mail: no wake recorded")
		}
		wantSurfaced(t, ws, "a status-bearing turn-end batch")
		notImplemented(t, pA2MechTurnEnd)
	})

	// n/a test_turn_ended_churn_absorb_off_by_default (fm-watch-triage.test.sh:1271): the pane-churn opt-in flag
	// (absent => pre-change triage); cox infers no execution from rendered bytes, so there is nothing to opt into.

	t.Run(s+"turn_ended_churn_absorb_bounded", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1307
		// cox: busyTurnMaxPass (BusyTurnMax mirrors FM_TURNEND_CHURN_ABSORB_SECS=60)
		r := newPortRig(t)
		r.w.BusyTurnMax = 60 * time.Second
		r.busySet(busy.Busy) // perpetually "working" with no fresh busy event
		r.advance(10 * time.Minute)
		ws := r.tick()
		if len(pA2Wakes(ws)) == 0 {
			t.Errorf("a busy record past its bound raised no wake")
		}
		wantSurfaced(t, ws, "a busy record past its bounded deferral")
		r.advance(61 * time.Second) // the window restarts after surfacing
		if len(pA2Wakes(r.tick())) == 0 {
			t.Errorf("the bounded window did not restart: no wake after a second bound elapsed")
		}
	})

	// n/a test_turn_ended_churn_timer_write_failure_surfaced (fm-watch-triage.test.sh:1340): failure to write the
	// .churn-since pane-churn deadline file; cox opens no churn deferral window.

	// n/a test_turn_ended_invalid_churn_bound_surfaced (fm-watch-triage.test.sh:1369): parsing the
	// FM_TURNEND_CHURN_ABSORB_SECS env knob; cox windows are typed time.Duration fields with a default fallback.

	// n/a test_turn_ended_oversized_churn_bound_surfaced (fm-watch-triage.test.sh:1399): bash integer overflow of the
	// churn-bound env knob; cox windows are typed time.Duration fields.

	t.Run(s+"turn_ended_invalid_churn_deadline_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1429
		// cox: busyTurnMaxPass (watch/busy-max/<story> is cox's deferral-window marker)
		for _, v := range []struct{ name, value string }{
			{"empty", ""},
			{"leading-zero", "09"},
			{"nonnumeric", "bogus"},
			{"future", strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)},
			{"overflow", "999999999999999999999999999999999999"},
		} {
			r := newPortRig(t)
			r.w.BusyTurnMax = 60 * time.Second
			r.busySet(busy.Busy)
			marker := filepath.Join(r.w.watchDir(), "busy-max", portStory)
			portMust(t, os.MkdirAll(filepath.Dir(marker), 0o755))
			portMust(t, os.WriteFile(marker, []byte(v.value), 0o644))
			r.advance(10 * time.Minute)
			ws := r.tick()
			if len(pA2Wakes(ws)) == 0 {
				t.Errorf("%s deadline: a busy record past its bound raised no wake", v.name)
			}
			wantSurfaced(t, ws, fmt.Sprintf("a busy record with a %s deferral deadline", v.name))
			if b, _ := os.ReadFile(marker); string(b) != v.value {
				t.Errorf("%s deadline was rewritten: %q -> %q", v.name, v.value, b)
			}
		}
	})

	// n/a test_turn_ended_surfaced_batch_opens_no_partial_deadline (fm-watch-triage.test.sh:1471): all-or-nothing
	// creation of per-pane .churn-since markers across one fm signal batch; cox has no churn markers or batches.

	t.Run(s+"working_note_not_working_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1513
		// cox: turn-end triage
		r := newPortRig(t)
		r.busySet(busy.Idle) // no running pipeline, idle worker: the working: line is a stale claim, not evidence
		r.mail("m1", "status", "working: compiling step 2")
		wantSurfaced(t, r.tick(), "a working: note from an idle worker")
		wantAbsorbed(t, r.tick(), "the same note again (the seen suppressor advanced)")
		notImplemented(t, pA2MechTurnEnd)
	})

	// n/a test_secondmate_status_note_surfaced_despite_busy_agent (fm-watch-triage.test.sh:1533): secondmate routed-reply
	// status stream; cox has no secondmates.

	// n/a test_secondmate_buried_block_wakes_despite_busy_agent (fm-watch-triage.test.sh:1553): secondmate status stream;
	// cox has no secondmates (the buried-blocker status span is pinned by the status-span cases elsewhere).

	t.Run(s+"self_announced_close_does_not_rewake_but_next_note_does", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1574
		// cox: mailPass + wake.Classify (a leader close is an inbox reply, never worker mail)
		r := newPortRig(t)
		r.mail("m1", "status", "needs-decision [key=k1]: pick one")
		r.tick() // the announced baseline
		r.reply("resolved [key=k1]: answered: closed by this home")
		wantAbsorbed(t, r.tick(), "the home's own bookkeeping close")
		r.mail("m2", "status", "needs-decision [key=k2]: a genuinely new decision")
		wantSurfaced(t, r.tick(), "a later different note after a self-announced close")
		notImplemented(t, pA2MechFold)
	})

	t.Run(s+"self_announced_close_after_open_decisions_fold_does_not_rewake", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1605
		// cox: mailPass + wake.Classify (a leader close is an inbox reply, never worker mail)
		r := newPortRig(t)
		r.mail("m1", "status", "needs-decision [key=k1]: pick one")
		r.tick() // fm: the session-start OPEN DECISIONS fold; cox: the leader has the wake
		r.reply("resolved [key=k1]: answered: closed after fold")
		wantAbsorbed(t, r.tick(), "a close after the fold")
		r.mail("m2", "status", "blocked: worker still needs help")
		wantSurfaced(t, r.tick(), "a later worker line after a folded close")
	})

	t.Run(s+"folded_worker_decision_without_home_append_still_wakes", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1642
		// cox: decision fold
		r := newPortRig(t)
		r.mail("m1", "status", "working: building")
		r.tick()
		r.mail("m2", "status", "needs-decision [key=k3]: pick a region")
		wantSurfaced(t, r.tick(), "a fresh worker decision the home appended nothing to")
		notImplemented(t, pA2MechFold)
	})

	t.Run(s+"separate_self_announced_answers_after_fold_wake_once", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1668
		// cox: decision fold
		r := newPortRig(t)
		r.mail("m1", "status", "needs-decision [key=k1]: pick REST or RPC")
		r.mail("m2", "status", "needs-decision [key=k2]: pick us-east or eu-west")
		r.reply("resolved [key=k1]: answered: REST")
		r.reply("resolved [key=k2]: answered: eu-west")
		ws := r.tick()
		wantSurfaced(t, ws, "the unclassified worker decisions")
		if n := len(pA2Wakes(ws)); n != 2 {
			t.Errorf("want one wake per worker decision and none per answer (2), got %d: %v", n, kinds(ws))
		}
		wantAbsorbed(t, r.tick(), "the owned answers on the next cycle")
		r.mail("m3", "status", "blocked: need staging credentials")
		wantSurfaced(t, r.tick(), "a later worker line after two owned answers")
		notImplemented(t, pA2MechFold)
	})

	t.Run(s+"self_announced_close_after_fold_still_surfaces_folded_worker_failure", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1707
		// cox: status span
		r := newPortRig(t)
		r.mail("m1", "status", "needs-decision [key=budget]: approve spend?")
		r.tick()
		r.mail("m2", "status", "failed: crew c3 hit an unrecoverable migration error")
		r.mail("m3", "status", "working: retrying c3 in a fresh worktree")
		r.reply("resolved [key=budget]: answered: approved")
		wantSurfaced(t, r.tick(), "a worker failure inside the folded span, under a routine append")
		notImplemented(t, pA2MechSpan)
	})

	// n/a test_self_announced_close_after_fold_still_surfaces_folded_secondmate_lines (fm-watch-triage.test.sh:1738):
	// secondmate parent-directed appends (paused / self-closed decisions); cox has no secondmates.

	t.Run(s+"actionable_signal_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1775
		// cox: wake.Classify + heartbeat backstop
		r := newPortRig(t)
		r.mail("m1", "status", "working: setup")
		r.mail("m2", "status", "needs-decision: pick A or B")
		wantSurfaced(t, r.tick(), "a captain-relevant needs-decision signal")
		// fm also records the .hb-surfaced marker the heartbeat backstop reads; cox has no backstop.
		notImplemented(t, pA2MechHBBackstop)
	})
}
