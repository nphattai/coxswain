//go:build port

// Port tests (wave 1, cox-supervision-port-triage): firstmate's wake triage translated case by case against cox's
// watcher passes. Firstmate pinned at 1e0e773 (references/firstmate, read only). Every case is
// t.Run("FM/<suite>/<case>") with a `// fm: path:line` citation and a `// cox:` mechanism tag; a case whose mechanism
// cox lacks calls notImplemented and fails. Red is the deliverable (DESIGN translation contract rules 1-8).
package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/adapter/forge"
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
	before := portQueueGen(r)
	_, err := r.w.Tick()
	portMust(r.t, err)
	return portWakesAfter(r, before)
}

// portQueueGen is the highest gen in the raw wake queue (the drain dedupes by kind and story, so a count diff over it
// would hide a second wake of the same kind).
func portQueueGen(r *portRig) int {
	r.t.Helper()
	ws, err := wake.Load(r.epic)
	portMust(r.t, err)
	g := 0
	for _, w := range ws {
		if w.Gen > g {
			g = w.Gen
		}
	}
	return g
}

// portWakesAfter returns the raw queue's wakes appended after gen.
func portWakesAfter(r *portRig, gen int) []wake.Wake {
	r.t.Helper()
	ws, err := wake.Load(r.epic)
	portMust(r.t, err)
	var out []wake.Wake
	for _, w := range ws {
		if w.Gen > gen {
			out = append(out, w)
		}
	}
	return out
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

// ci stands in firstmate's run-step verdict with the cox observable (captain ruling 2026-09-24): the story PR's CI
// checks at its live head. "running" is an active step (working); "passed" / "failed" are every check completed (a
// finished or failed run); "unknown" is a forge that cannot be read (never absorbs).
func (r *portRig) ci(st string) { r.ciFor(portStory, st) }

// ciFor sets one story's run-step verdict (a batch can mix them, as firstmate's per-task crew states do).
func (r *portRig) ciFor(story, st string) {
	f, ok := r.w.Forge.(*portForge)
	if !ok {
		f = &portForge{st: map[string]string{}}
		r.w.Forge = f
	}
	f.st[story] = st
}

// portForge is a forge whose CI verdict is set per story branch (story/<id>); other methods are unused here.
type portForge struct{ st map[string]string }

func (f *portForge) PR(head string) (forge.PR, error) {
	if _, ok := f.st[strings.TrimPrefix(head, "story/")]; !ok {
		return forge.PR{}, errors.New("no PR for " + head)
	}
	return forge.PR{Number: 1, HeadRef: head, Head: "abc123", State: "open"}, nil
}

func (f *portForge) Checks(pr forge.PR) ([]forge.Check, error) {
	switch f.st[strings.TrimPrefix(pr.HeadRef, "story/")] {
	case "running":
		return []forge.Check{{Name: "test", Status: "completed", Conclusion: "success"}, {Name: "e2e", Status: "in_progress"}}, nil
	case "passed":
		return []forge.Check{{Name: "test", Status: "completed", Conclusion: "success"}}, nil
	case "failed":
		return []forge.Check{{Name: "test", Status: "completed", Conclusion: "failure"}}, nil
	}
	return nil, errors.New("forge unavailable")
}

func (f *portForge) Diff(forge.PR) (string, error)              { return "", errors.New("unused") }
func (f *portForge) Comments(forge.PR) ([]forge.Comment, error) { return nil, errors.New("unused") }
func (f *portForge) Merged(forge.PR) (bool, error)              { return false, errors.New("unused") }
func (f *portForge) Merge(forge.PR, string) error               { return errors.New("unused") }

// firstSight runs a quiet worker's first stale sighting and discards its wakes: firstmate's wedge fixtures start from a
// lane "already surfaced once, as it is after the supervision turn that handled the first sight".
func (r *portRig) firstSight() {
	r.t.Helper()
	r.advance(DefaultStaleQuiet)
	r.tick()
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
// The crew is provably working (busy record busy), so a no-verb line is absorbed and only the line's own
// captain-relevance decides: firstmate's span classifier cases read the status span alone, with no crew state.
func pA1Lines(t *testing.T, lines ...string) (*portRig, []wake.Wake) {
	t.Helper()
	r := newPortRig(t)
	r.busySet(busy.Busy)
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
	before := portQueueGen(r)
	stop := make(chan struct{})
	close(stop)
	r.w.Run(stop, time.Hour)
	return portWakesAfter(r, before)
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
		r.busySet(busy.Busy)
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
		r.busySet(busy.Busy)
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
		// cox: stale path classifier (captainRelevantRE over the story's last status line)
		// stale_is_terminal is captain-relevance of the last status line; cox reads it from the recorded status log.
		r := newPortRig(t)
		r.busySet(busy.Busy)
		r.mail("e0", "status", "done: ready in branch fm/x")
		r.tick()
		if line, _ := r.w.statusLine(portStory); !captainRelevant(line) {
			t.Errorf("terminal stale status not classified terminal: %q", line)
		}
		if captainRelevant("working: compiling") {
			t.Errorf("non-terminal stale classified terminal")
		}
		// A multi-line pause whose continuation prose mentions a legacy token: the status line is the verb line only.
		r = newPortRig(t)
		r.busySet(busy.Busy)
		r.mb.Queue = append(r.mb.Queue, backend.Message{ID: "e1", From: "dispatch:ctx_" + portStory, Type: "status",
			Subject: "paused: waiting on upstream PR #123 to land", Body: "Once it is merged I will rebase and continue.",
			Payload: `{"dispatchId":"ctx_` + portStory + `"}`})
		pA1Routine(t, r.tick(), "a multi-line pause mentioning merged")
		line, _ := r.w.statusLine(portStory)
		if captainRelevant(line) {
			t.Errorf("prose mentioning a legacy token escalated a multi-line pause as terminal: %q", line)
		}
		if !statusDeclaredWait(line) {
			t.Errorf("prose mentioning a legacy token hid a multi-line pause from the wait cadence: %q", line)
		}
		// No status at all is not terminal.
		if l, _ := newPortRig(t).w.statusLine(portStory); captainRelevant(l) {
			t.Errorf("stale with no status classified terminal")
		}
		// The herdr metadata resolution is a firstmate-only surface (n/a).
	})

	t.Run(s+"classifier_primitives", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:342
		// cox: status grammar (statusVerb, captainRelevantRE) + Watcher.CaptainRE (FM_CAPTAIN_RE)
		// last_status_line: a cox status line is one message subject, so blank lines never exist; continuation prose
		// cannot hide the declared verb, including a correlation-token prefix.
		if v := statusVerb("paused [corr=aaaa1111bbbb2222]: waiting for release"); v != "paused" {
			t.Errorf("continuation prose / corr token hid the last declared status verb: %q", v)
		}
		for _, l := range []string{"done: b", "needs-decision [key=q1]: b", "done: PR https://x/pull/76 checks green",
			"merged", "PR ready https://x/pull/2"} {
			if !captainRelevant(l) {
				t.Errorf("not captain-relevant: %q", l)
			}
			_, ws := pA1Lines(t, l)
			wantSurfaced(t, ws, "captain-relevant: "+l)
		}
		for _, l := range []string{"working: b",
			"working: stage 2 setup complete on PR #74 exact source branch rebased onto merged #76; task dates preserved",
			"working: rebased onto predecessor #76",
			"working: PR ready checks green merged ready in branch",
			"working: rebased onto merged #76"} {
			if captainRelevant(l) {
				t.Errorf("wrongly captain-relevant: %q", l)
			}
			_, ws := pA1Lines(t, l)
			pA1Routine(t, ws, "not captain-relevant: "+l)
		}
		// FM_CAPTAIN_RE override: replaces the default verb set, never bypasses working:/paused: suppression.
		re := regexp.MustCompile(`custom-verb:`)
		if !captainRelevantRE("custom-verb: x", re) {
			t.Errorf("FM_CAPTAIN_RE override not honored")
		}
		if captainRelevantRE("done: x", re) {
			t.Errorf("FM_CAPTAIN_RE override did not replace the default verb set")
		}
		if captainRelevantRE("working: rebased onto merged #76", regexp.MustCompile(`merged|custom-verb:`)) {
			t.Errorf("FM_CAPTAIN_RE override bypassed working: suppression")
		}
		if captainRelevantRE("paused: checks green pending approval", regexp.MustCompile(`checks green|custom-verb:`)) {
			t.Errorf("FM_CAPTAIN_RE override bypassed paused: suppression")
		}
		// The watcher reads a quiet worker's last line through its own override.
		r := newPortRig(t)
		r.w.CaptainRE = re
		r.busySet(busy.Idle)
		r.mail("o1", "status", "custom-verb: x")
		r.tick()
		if last, _ := r.w.statusLine(portStory); !captainRelevantRE(last, r.w.CaptainRE) {
			t.Errorf("the watcher did not read the last line through its override: %q", last)
		}
		// Keyed open decisions and keyed activity phases are the decision fold (wave 2b, w2-watch-decisions).
		notImplemented(t, pA1MechFold)
	})

	t.Run(s+"crew_is_provably_working_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:423
		// cox: crewClass (run-step = CI running at the PR head; pane = a trusted, uncontradicted busy record)
		cls := func(setup func(r *portRig)) string {
			r := newPortRig(t)
			setup(r)
			return r.w.crewClass(portStory)
		}
		if cls(func(r *portRig) { r.busySet(busy.Idle); r.ci("running") }) != crewWorking {
			t.Errorf("active run-step not treated as provably working")
		}
		if cls(func(r *portRig) { r.busySet(busy.Busy) }) != crewWorking {
			t.Errorf("busy pane not treated as provably working")
		}
		if cls(func(r *portRig) { r.busySet(busy.Idle); r.mail("f0", "status", "working: compiling"); r.tick() }) == crewWorking {
			t.Errorf("stale status-log working: treated as provably working")
		}
		if cls(func(r *portRig) { r.busySet(busy.Idle); r.ci("passed") }) == crewWorking {
			t.Errorf("finished run treated as provably working")
		}
		if cls(func(r *portRig) { r.busySet(busy.Idle); r.ci("failed") }) == crewWorking {
			t.Errorf("failed run treated as provably working")
		}
		if cls(func(r *portRig) { r.ci("unknown") }) == crewWorking {
			t.Errorf("unknown crew treated as provably working")
		}
		// A parked run (a no-mistakes gate) has no cox observable; the empty id is bash argument handling (n/a).
		// Watcher-level: busy pane absorbed past the idle window; a stale working: status from a stopped worker, or a
		// working: line alone, is not provable and surfaces; a busy record the backend contradicts surfaces (B-51).
		r := newPortRig(t)
		r.busySet(busy.Busy)
		r.mail("f1", "status", "working: compiling")
		r.tick()
		r.advance(6 * time.Minute)
		wantAbsorbed(t, r.tick(), "a provably working (busy) worker")
		r = newPortRig(t)
		r.busySet(busy.Idle)
		r.mail("f2", "status", "working: compiling")
		r.tick()
		r.advance(6 * time.Minute)
		wantSurfaced(t, r.tick(), "a stale working: status with the worker stopped is not provably working")
		r = newPortRig(t)
		r.composer(backend.ComposerEmpty)
		r.mail("f3", "status", "working: compiling")
		r.tick()
		r.advance(6 * time.Minute)
		wantSurfaced(t, r.tick(), "a status-log working: alone is not provably working")
		r = newPortRig(t)
		r.busySet(busy.Busy)
		r.liveness(backend.Settled)
		r.heartbeat("f4")
		r.tick()
		r.advance(21 * time.Minute)
		wantSurfaced(t, r.tick(), "a busy record contradicted by a settled session")
	})

	t.Run(s+"status_is_paused_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:455
		// cox: status grammar (statusPaused, statusCaptainHeld, statusDeclaredWait)
		if !statusPaused("paused: holding for the upstream release") || !statusPaused("  paused:   waiting on a rate-limit reset") {
			t.Errorf("paused verb not recognized")
		}
		for _, l := range []string{"blocked: the build is paused upstream", "working: paused the animation loop", "done: shipped", ""} {
			if statusPaused(l) {
				t.Errorf("classified as paused: %q", l)
			}
		}
		if captainRelevant("paused: holding for the upstream release") {
			t.Errorf("paused is captain-relevant (should not be)")
		}
		if !statusDeclaredWait("paused: holding for the upstream release") ||
			!statusDeclaredWait("captain-held [key=route]: tracked by task-decision-route") {
			t.Errorf("a declared wait not recognized by the bounded-idle classifier")
		}
		if statusDeclaredWait("resolved [key=route]: captain answered") {
			t.Errorf("resolved decision remained classed as captain-held")
		}
		if !statusCaptainHeld("captain-held [key=route]: tracked by task-decision-route") {
			t.Errorf("captain-held verb not recognized")
		}
		for _, l := range []string{"paused: holding for the upstream release", "working: the captain-held backlog item is next", ""} {
			if statusCaptainHeld(l) {
				t.Errorf("classified as captain-held: %q", l)
			}
		}
		_, ws := pA1Lines(t, "paused: holding for the upstream release")
		pA1Routine(t, ws, "a pause is not captain-relevant")
		_, ws = pA1Lines(t, "blocked: the build is paused upstream")
		wantSurfaced(t, ws, "a genuine blocker stays a blocker")
	})

	t.Run(s+"crew_absorb_class_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:488
		// cox: pauseStateClass (crew_absorb_class + the declared-wait reading of the status line)
		cls := func(setup func(r *portRig)) string {
			r := newPortRig(t)
			setup(r)
			return r.w.pauseStateClass(portStory)
		}
		if cls(func(r *portRig) { r.busySet(busy.Idle); r.ci("running") }) != crewWorking {
			t.Errorf("active run-step not classed working")
		}
		if cls(func(r *portRig) { r.busySet(busy.Busy) }) != crewWorking {
			t.Errorf("busy pane not classed working")
		}
		// A declared pause is classed paused only when the agent is confidently gone (fm pause_state_class): the
		// firstmate fixture's `state: paused` verdict is a crew with no live agent behind its declaration.
		if cls(func(r *portRig) {
			r.busySet(busy.Idle)
			r.liveness(backend.Settled)
			r.mail("g0", "status", "paused: awaiting upstream")
			r.tick()
		}) != "paused" {
			t.Errorf("declared pause not classed paused")
		}
		if cls(func(r *portRig) { r.busySet(busy.Idle); r.mail("g1", "status", "working: compiling"); r.tick() }) != crewNone {
			t.Errorf("stale working: status-log classed absorbable")
		}
		if cls(func(r *portRig) { r.ci("unknown") }) != crewNone {
			t.Errorf("unknown crew classed absorbable")
		}
		// Watcher-level: working absorbed on the stale path; a stopped or unknown crew surfaces past the threshold.
		stale := func(r *portRig) []wake.Wake {
			r.heartbeat("g-hb")
			r.tick()
			r.advance(21 * time.Minute)
			return r.tick()
		}
		r := newPortRig(t)
		r.busySet(busy.Busy)
		r.liveness(backend.Alive)
		wantAbsorbed(t, stale(r), "a busy worker on the stale path")
		r = newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Alive)
		r.mail("g2", "status", "working: compiling")
		wantSurfaced(t, stale(r), "a stopped worker silent past the stale threshold")
		r = newPortRig(t)
		r.liveness(backend.Unknown)
		wantSurfaced(t, stale(r), "an unknown crew past the stale threshold")
	})

	t.Run(s+"crew_worktree_written_since_classifier", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:515
		// cox: worktreeWrittenSince (the wedge detector's third liveness input; the worktree is <epic>/.cox/wt/<story>)
		r := newPortRig(t)
		anchor := time.Now().Add(-2 * time.Minute)
		wt := filepath.Join(t.TempDir(), "wt")
		portMust(t, os.MkdirAll(filepath.Join(wt, "src"), 0o755))
		portMust(t, os.MkdirAll(filepath.Join(wt, ".git", "objects"), 0o755))
		old := filepath.Join(wt, "src", "existing.c")
		portMust(t, os.WriteFile(old, []byte("old\n"), 0o644))
		past := time.Now().Add(-5 * time.Minute)
		portMust(t, os.Chtimes(old, past, past))
		if r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("a task with no recorded worktree reported write evidence")
		}
		portMust(t, os.MkdirAll(filepath.Dir(state.WorktreePath(r.epic, portStory)), 0o755))
		portMust(t, os.WriteFile(state.WorktreePath(r.epic, portStory), []byte(filepath.Join(t.TempDir(), "missing")), 0o644))
		if r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("a torn-down worktree reported write evidence")
		}
		portMust(t, os.WriteFile(state.WorktreePath(r.epic, portStory), []byte(wt), 0o644))
		if r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("a quiet worktree reported write evidence")
		}
		portMust(t, os.WriteFile(filepath.Join(wt, ".git", "objects", "fresh"), []byte("pack\n"), 0o644))
		portMust(t, os.WriteFile(filepath.Join(wt, ".git", "index"), []byte("ref\n"), 0o644))
		if r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf(".git churn alone reported write evidence (a supervisor read could fake liveness)")
		}
		portMust(t, os.WriteFile(filepath.Join(wt, "src", "new.c"), []byte("new\n"), 0o644))
		if !r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("a file written after the anchor was not reported as write evidence")
		}
		// A missing anchor is the caller's missing idle timer, which wedgeTimerCheck repairs before any probe; the
		// secondmate home exclusion is firstmate-only (n/a). An ordinary directory named state is real work.
		sd := filepath.Join(t.TempDir(), "wt-with-state")
		portMust(t, os.MkdirAll(filepath.Join(sd, "state"), 0o755))
		portMust(t, os.WriteFile(filepath.Join(sd, "state", "machine.go"), []byte("machine\n"), 0o644))
		portMust(t, os.WriteFile(state.WorktreePath(r.epic, portStory), []byte(sd), 0o644))
		if !r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("a source directory named state was hidden from the write probe")
		}
	})

	t.Run(s+"empty_write_prune_widens_the_probe", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:580
		// cox: writePrune (FM_WORKTREE_WRITE_PRUNE): an empty skip list widens the walk, never disables it
		r := newPortRig(t)
		anchor := time.Now().Add(-2 * time.Minute)
		wt := filepath.Join(t.TempDir(), "wt")
		portMust(t, os.MkdirAll(filepath.Join(wt, "src"), 0o755))
		portMust(t, os.MkdirAll(filepath.Join(wt, ".git"), 0o755))
		portMust(t, os.MkdirAll(filepath.Dir(state.WorktreePath(r.epic, portStory)), 0o755))
		portMust(t, os.WriteFile(state.WorktreePath(r.epic, portStory), []byte(wt), 0o644))
		saved := writePrune
		defer func() { writePrune = saved }()
		writePrune = nil
		if r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("an empty prune list reported write evidence for a quiet worktree")
		}
		nw := filepath.Join(wt, "src", "new.c")
		portMust(t, os.WriteFile(nw, []byte("new\n"), 0o644))
		if !r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("an empty prune list disabled the probe instead of widening it")
		}
		past := time.Now().Add(-15 * time.Minute)
		portMust(t, os.Chtimes(nw, past, past))
		portMust(t, os.WriteFile(filepath.Join(wt, ".git", "index"), []byte("pack\n"), 0o644))
		if !r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("an empty prune list still skipped a directory the default list prunes")
		}
		writePrune = saved
		if r.w.worktreeWrittenSince(portStory, anchor) {
			t.Errorf("the default prune list stopped keeping .git out of the probe")
		}
	})

	// n/a test_empty_write_prune_from_the_environment_widens_the_probe (fm-watch-triage.test.sh:615): bash-only concern,
	// an exported empty FM_WORKTREE_WRITE_PRUNE vs the ${VAR:-default} colon form; the widening requirement itself is
	// pinned by empty_write_prune_widens_the_probe.

	t.Run(s+"worktree_write_probe_is_wall_clock_bounded", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:642
		// cox: worktreeWrittenSince bounds the walk (walkNewerFn stands in the fake find): past the bound is no evidence
		r := newPortRig(t)
		wt := t.TempDir()
		portMust(t, os.MkdirAll(filepath.Dir(state.WorktreePath(r.epic, portStory)), 0o755))
		portMust(t, os.WriteFile(state.WorktreePath(r.epic, portStory), []byte(wt), 0o644))
		savedFn, savedBound := walkNewerFn, writeProbeDeadline
		defer func() { walkNewerFn, writeProbeDeadline = savedFn, savedBound }()
		walkNewerFn = func(string, os.FileInfo, time.Time) bool { return true } // the prompt walk reports a hit
		if !r.w.worktreeWrittenSince(portStory, time.Now()) {
			t.Errorf("a walk that reported a hit inside its bound was not read as write evidence")
		}
		release := make(chan struct{})
		defer close(release)
		walkNewerFn = func(string, os.FileInfo, time.Time) bool { <-release; return true } // a hung mount
		writeProbeDeadline = 100 * time.Millisecond
		start := time.Now()
		if r.w.worktreeWrittenSince(portStory, time.Now()) {
			t.Errorf("a walk that outlived its bound was reported as write evidence")
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("the worktree write probe was not wall-clock bounded: one walk held the caller for %v", d)
		}
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
		// cox: signalTriage over crewClass (run-step = CI running at the PR head; pane = busy record busy)
		for _, src := range []string{"run-step", "pane"} {
			r := newPortRig(t)
			if src == "run-step" {
				r.ci("running")
			} else {
				r.busySet(busy.Busy)
			}
			r.mail("h1", "status", "working: compiling step 2")
			wantAbsorbed(t, pA1RunOnce(r), src+": a no-verb working: status from a provably working worker")
			wantAbsorbed(t, r.tick(), src+": the suppressor did not advance: the same status re-fired")
			if _, err := os.Stat(filepath.Join(r.w.watchDir(), "lasttick")); err != nil {
				t.Errorf("%s: watcher beacon not written while absorbing: %v", src, err)
			}
		}
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
		// cox: stalePass (a changed activity signature starts a fresh stale-classification interval)
		// The earlier quiet interval was already classified (first sight surfaced, its idle timer running); the worker
		// then renders a new turn and stops again: the new quiet interval surfaces through ordinary staleness, never
		// inheriting the earlier interval's wedge timer.
		r := newPortRig(t)
		r.liveness(backend.Alive)
		r.heartbeat("hb1")
		r.tick()
		r.firstSight()
		r.busySet(busy.Busy) // churn: a fresh interval of activity
		wantAbsorbed(t, r.tick(), "an active worker")
		r.busySet(busy.Idle) // then it stops (its turn-end is a signal of its own)
		r.tick()
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
		for _, w := range ws {
			if strings.Contains(w.Note, "possible wedge") {
				t.Errorf("the returned stale inherited the earlier quiet interval's wedge classification: %s", w.Note)
			}
		}
	})

	t.Run(s+"turn_ended_churn_resets_wedge_state_before_stale_poll", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:907
		// cox: stalePass (activity resets the wedge escalation count before the next stale poll)
		r := newPortRig(t)
		r.liveness(backend.Alive)
		r.ci("running") // provably working on a quiet pane: absorbed, wedge timer runs
		r.heartbeat("hb1")
		r.tick()
		r.firstSight()
		r.advance(r.w.staleMin())
		pBWantNote(t, r.tick(), "possible wedge, escalation 1", "a prior wedge round")
		r.busySet(busy.Busy)
		wantAbsorbed(t, r.tick(), "a churning (busy) turn-end")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "esc", portStory)); err == nil {
			t.Errorf("activity did not reset the wedge-escalation count")
		}
	})

	// n/a test_turn_ended_churn_existing_marker_absorbed (fm-watch-triage.test.sh:943): stock bash 3.2 empty-array
	// regression under set -u plus preservation of the .churn-since pane-churn marker; the absorb requirement itself is
	// covered by turn_ended_churning_pane_absorbed.

	t.Run(s+"turn_ended_still_pane_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:979
		// cox: turnEndPass + signalTriage
		r := newPortRig(t)
		r.busySet(busy.Idle) // the turn ended; no status line, no steer, nothing rendered since
		ws := r.tick()
		wantSurfaced(t, ws, "a bare turn-end with no evidence of work")
		pBWantKind(t, ws, wake.KindIdleNoDone, "the turn-end is surfaced as itself")
	})

	t.Run(s+"turn_ended_malformed_prior_hash_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1007
		// cox: turnEndPass (a malformed prior activity signature is no evidence of work)
		r := newPortRig(t)
		r.busySet(busy.Idle)
		portMust(t, os.MkdirAll(filepath.Join(r.w.watchDir(), "sig"), 0o755))
		portMust(t, os.WriteFile(filepath.Join(r.w.watchDir(), "sig", portStory), []byte("x"), 0o644)) // fm: a malformed prior hash
		wantSurfaced(t, r.tick(), "a turn-end backed by malformed evidence")
		// A malformed busy record is never read as busy (it cannot hide a stopped worker).
		r = newPortRig(t)
		r.busySet(busy.Busy)
		portMust(t, os.WriteFile(busy.Path(r.epic, portStory), []byte("x"), 0o644))
		if busy.Read(r.epic, portStory) == busy.Busy {
			t.Errorf("a malformed busy record read as busy")
		}
		if r.w.crewClass(portStory) == crewWorking {
			t.Errorf("a malformed busy record read as provably working")
		}
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
		// cox: signalTriage (a batch is benign when every crew is provably working, each on its own evidence)
		r := newPortRig(t)
		pA1AddStory(r, "s2")
		r.busySet(busy.Busy)           // s1: busy pane
		pA1BusySet(r, "s2", busy.Idle) // s2: a turn-end ...
		r.ciFor("s2", "running")       // ... under an active run-step
		ws := r.tick()
		wantAbsorbed(t, ws, "the pane-proven task in the batch")
		for _, w := range ws {
			if w.Story == "s2" {
				t.Errorf("the run-step-proven turn-end was not absorbed: %s %s", w.Kind, w.Note)
			}
		}
	})

	// n/a test_turn_ended_mixed_positive_evidence_batch_default_off (fm-watch-triage.test.sh:1190): the home opt-in
	// config flag for pane-churn evidence; cox's busy record is harness-verified, so there is no opt-in knob.

	t.Run(s+"status_and_turn_end_batch_never_uses_churn_evidence", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1230
		// cox: signalTriage (a batch with one crew not provably working queues every signal in it)
		// Translation note: firstmate's batch is two tasks - a status line from a run-step-working task and a bare
		// turn-end from a task whose state is unknown; the wave 1 translation collapsed it to one busy task, which
		// contradicted provably_working_signal_absorbed. Restored to firstmate's two-task shape.
		r := newPortRig(t)
		pA1AddStory(r, "s2")
		r.ci("running") // s1: working, source run-step
		r.mail("m1", "status", "working: authoritative task still running")
		pA1BusySet(r, "s2", busy.Idle) // s2: a bare turn-end, not provably working
		ws := r.tick()
		queued := false
		for _, w := range ws {
			queued = queued || (w.Story == portStory && w.Evidence["msg"] == "m1")
		}
		if !queued {
			t.Errorf("the status line from the surfaced mixed batch was not queued: %v", kinds(ws))
		}
		if !urgentFor(ws, "s2") {
			t.Errorf("the turn-end from the surfaced mixed batch was not surfaced: %v", kinds(ws))
		}
	})

	// n/a test_turn_ended_churn_absorb_off_by_default (fm-watch-triage.test.sh:1271): the pane-churn opt-in flag
	// (absent => pre-change triage); cox infers no execution from rendered bytes, so there is nothing to opt into.

	// n/a test_turn_ended_churn_absorb_bounded (fm-watch-triage.test.sh:1307): FM_TURNEND_CHURN_ABSORB_SECS bounds the
	// opt-in pane-churn deferral of a bare turn-end (config/turnend-churn-absorb, tmux pane bytes); cox infers no
	// execution from rendered bytes, like the churn siblings above (DESIGN rule 5). Wave 1 had mapped it onto
	// BusyTurnMax, which contradicted busy_pane_stable_hash_escalates_past_turn_age_bound; leader ruling q003.

	// n/a test_turn_ended_churn_timer_write_failure_surfaced (fm-watch-triage.test.sh:1340): failure to write the
	// .churn-since pane-churn deadline file; cox opens no churn deferral window.

	// n/a test_turn_ended_invalid_churn_bound_surfaced (fm-watch-triage.test.sh:1369): parsing the
	// FM_TURNEND_CHURN_ABSORB_SECS env knob; cox windows are typed time.Duration fields with a default fallback.

	// n/a test_turn_ended_oversized_churn_bound_surfaced (fm-watch-triage.test.sh:1399): bash integer overflow of the
	// churn-bound env knob; cox windows are typed time.Duration fields.

	// n/a test_turn_ended_invalid_churn_deadline_surfaced (fm-watch-triage.test.sh:1429): parsing the pane-churn
	// deferral deadline file (.churn-since) of the same opt-in feature; firstmate-only surface (DESIGN rule 5), leader
	// ruling q003.

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
		// fm also records the surfaced marker the heartbeat backstop reads.
		if sf := r.w.loadSurfaced(); !sf["m1"] || !sf["m2"] {
			t.Errorf("the surfaced signal was not recorded for the heartbeat backstop: %v", sf)
		}
	})
}

const (
	pBMechTurnEnd    = "turn-end triage: a stopped worker with no report since its turn began surfaces (idleNoDonePass requires a steer)"
	pBMechStale      = "stale escalation: a silent worker past the stale threshold escalates (stalePass only fires on a failed/unknown probe)"
	pBMechDeclWait   = "declared wait: paused / captain-held status verbs"
	pBMechFold       = "decision fold: open/close decisions by [key=] across a worker's status history"
	pBMechWedge      = "wedge detector: escalation schedule, deep inspection, write deferral"
	pBMechRunStep    = "run-step authority: an active pipeline run overrides a stale/terminal status"
	pBMechCadence    = "wait cadence: paused-until / resurface throttle"
	pBMechUnreadable = "unreadable status source: a failed status read is reported once per failure state and preserved content surfaces on recovery"

	pBPoll       = 5 * time.Second // one watcher poll (DefaultPoll)
	pBDeepInsp   = "demand-deep-inspection: same pane has wedge-escalated 3 times in a row"
	pBGateKeyLog = "needs-decision [key=nm-01RUNGATE-review]: the gate raised an authority question"
)

// pBRig builds a port rig whose backend Probe reports live and whose busy record is set to st ("" leaves none).
func pBRig(t *testing.T, live backend.Liveness, st string) *portRig {
	t.Helper()
	r := newPortRig(t)
	r.liveness(live)
	if st != "" {
		r.busySet(st)
	}
	return r
}

// pBSay queues one worker status line (a firstmate status-file line) with a unique message id.
func pBSay(r *portRig, line string) { r.mail(fmt.Sprintf("pB%d", len(r.mb.Queue)), "status", line) }

// pBPrime delivers lines plus a closing heartbeat and consumes them in one tick, discarding the wakes: the firstmate
// fixture's `.seen-*` priming (the lines were already classified and handled) with the worker's last liveness ping.
func pBPrime(r *portRig, lines ...string) {
	r.t.Helper()
	for _, l := range lines {
		pBSay(r, l)
	}
	r.heartbeat(fmt.Sprintf("pB%d", len(r.mb.Queue)))
	r.tick()
}

// pBRound advances the clock by d and runs one tick.
func pBRound(r *portRig, d time.Duration) []wake.Wake {
	r.t.Helper()
	r.advance(d)
	return r.tick()
}

// pBText is a wake's note plus its full text, for wording assertions.
func pBText(w wake.Wake) string { return w.Note + "\n" + w.Full }

// pBAlarms counts the story's wakes that alarm the leader: an urgent kind, or a stale / unknown_probe wake (firstmate
// counts `stale` queue rows).
func pBAlarms(ws []wake.Wake) int {
	n := 0
	for _, w := range ws {
		if w.Story == portStory && (wake.IsUrgent(w.Kind) || w.Kind == wake.KindStale || w.Kind == wake.KindUnknownProbe) {
			n++
		}
	}
	return n
}

// pBWantNote asserts an urgent wake for the story whose text contains sub.
func pBWantNote(t *testing.T, ws []wake.Wake, sub, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory && wake.IsUrgent(w.Kind) && strings.Contains(pBText(w), sub) {
			return
		}
	}
	t.Errorf("want an urgent wake containing %q (%s), got %v", sub, why, kinds(ws))
}

// pBNoNote asserts no wake for the story carries sub.
func pBNoNote(t *testing.T, ws []wake.Wake, sub, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory && strings.Contains(pBText(w), sub) {
			t.Errorf("unwanted %q in a %s wake (%s): %s", sub, w.Kind, why, w.Note)
		}
	}
}

// pBWantKind asserts a wake of kind k for the story.
func pBWantKind(t *testing.T, ws []wake.Wake, k wake.Kind, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory && w.Kind == k {
			return
		}
	}
	t.Errorf("want a %s wake (%s), got %v", k, why, kinds(ws))
}

// pBNoKind asserts no wake of kind k for the story.
func pBNoKind(t *testing.T, ws []wake.Wake, k wake.Kind, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory && w.Kind == k {
			t.Errorf("unwanted %s wake (%s): %s", k, why, w.Note)
		}
	}
}

// pBWantStale asserts the tick raised a stale-kind wake (stale or unknown_probe) for the story.
func pBWantStale(t *testing.T, ws []wake.Wake, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory && (w.Kind == wake.KindStale || w.Kind == wake.KindUnknownProbe) {
			return
		}
	}
	t.Errorf("want a stale wake (%s), got %v", why, kinds(ws))
}

// pBLadder runs n wedge-threshold rounds and wants escalation k at round k, with the deep-inspection demand at 3.
func pBLadder(t *testing.T, r *portRig, n int, why string) {
	t.Helper()
	var all []wake.Wake
	for k := 1; k <= n; k++ {
		ws := pBRound(r, pBPoll)
		all = append(all, ws...)
		pBWantNote(t, ws, fmt.Sprintf("possible wedge, escalation %d", k), why)
	}
	if n >= 3 {
		pBWantNote(t, all, pBDeepInsp, why)
	}
}

// pBAbsorbRounds runs n poll rounds and wants no alarm and no wedge wording in any.
func pBAbsorbRounds(t *testing.T, r *portRig, n int, why string) {
	t.Helper()
	for k := 1; k <= n; k++ {
		ws := pBRound(r, pBPoll)
		if a := pBAlarms(ws); a != 0 {
			t.Errorf("round %d: %d alarm(s) (%s): %v", k, a, why, kinds(ws))
		}
		pBNoNote(t, ws, "possible wedge", why)
	}
}

// pBWaitSecs reads the wait age a recheck publishes ("waiting <N>s"), -1 when absent.
func pBWaitSecs(ws []wake.Wake) int {
	re := regexp.MustCompile(`waiting (\d+)s`)
	for _, w := range ws {
		if m := re.FindStringSubmatch(pBText(w)); w.Story == portStory && m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return -1
}

// pBWedgeFixture is firstmate's wedge_threshold_fixture: a lane already stably stale and already surfaced once (the
// supervision turn handled the first sight), so every further poll goes straight to the wedge timer; a captain-relevant
// last line takes that timer only when it is already armed, so the fixture arms it. FM_STALE_ESCALATE_SECS=1 puts every
// round at the threshold; FM_PAUSE_RESURFACE_SECS defaults to 999. verdict "working" is an active run-step, "parked" a
// run parked at a gate (not provably working); the agent is live (fm pane command grok).
func pBWedgeFixture(t *testing.T, verdict string, lines ...string) *portRig {
	t.Helper()
	r := pBRig(t, backend.Alive, busy.Idle)
	r.w.StaleMin = time.Second
	r.w.PauseResurface = 999 * time.Second
	if verdict == "working" {
		r.ci("running")
	}
	pBPrime(r, lines...)
	r.firstSight()
	if _, err := os.Stat(filepath.Join(r.w.watchDir(), "since", portStory)); err != nil {
		portMust(t, os.MkdirAll(filepath.Join(r.w.watchDir(), "since"), 0o755))
		portMust(t, os.WriteFile(filepath.Join(r.w.watchDir(), "since", portStory), []byte(strconv.FormatInt(r.clock.Unix(), 10)), 0o644))
	}
	return r
}

// pBFlakyMail is the fake mailbox with an injectable read failure (an unreadable status source).
type pBFlakyMail struct {
	*fake.Mailbox
	err error
}

func (m *pBFlakyMail) Check() ([]backend.Message, string, error) {
	if m.err != nil {
		return nil, "", m.err
	}
	return m.Mailbox.Check()
}

type pBFlakyBackend struct {
	*fake.Backend
	mb *pBFlakyMail
}

func (b *pBFlakyBackend) Mail() backend.Mailbox { return b.mb }

// pBFlaky swaps the rig's watcher onto a mailbox whose reads can fail.
func pBFlaky(r *portRig) *pBFlakyMail {
	m := &pBFlakyMail{Mailbox: r.mb}
	r.w.Backend = &pBFlakyBackend{Backend: r.b, mb: m}
	return m
}

// pBTickErr runs one tick that may fail and returns the wakes it appended (for any story) and the tick error.
func pBTickErr(r *portRig) ([]wake.Wake, error) {
	r.t.Helper()
	before := portQueueGen(r)
	_, tickErr := r.w.Tick()
	return portWakesAfter(r, before), tickErr
}

func TestPortTriageB(t *testing.T) {
	const s = "FM/fm-watch-triage/"

	t.Run(s+"needs_decision_signal_payload_marked_for_branch_exclusion", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1797
		// cox: mailPass (wake.Classify)
		// The branch-exclusion payload marker is firstmate's main-vs-branch routing; cox has one leader, so the
		// requirement is that the needs-decision reaches it as a leader-blocking decision (input_required, urgent).
		r := pBRig(t, backend.Alive, "")
		pBSay(r, "working: setup")
		pBSay(r, "needs-decision: pick A or B")
		ws := r.tick()
		wantSurfaced(t, ws, "an actionable needs-decision signal")
		pBWantKind(t, ws, wake.KindInputRequired, "needs-decision is a leader-owed decision")
	})

	t.Run(s+"needs_decision_reconciliation_required_still_marked", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1816
		// cox: mailPass (wake.Classify)
		// firstmate wraps a rejected reserved key as reconciliation-required; the decision must still reach the leader.
		r := pBRig(t, backend.Alive, "")
		pBSay(r, "needs-decision [key=pending-reply-x]: unrelated request")
		pBSay(r, "working: awaiting reconciliation")
		ws := r.tick()
		wantSurfaced(t, ws, "a rejected-reserved-key needs-decision")
		pBWantKind(t, ws, wake.KindInputRequired, "still a needs-decision")
	})

	t.Run(s+"captain_held_signal_payload_marked_for_branch_exclusion", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1834
		// cox: mechDeclWait (mailPass)
		r := pBRig(t, backend.Alive, busy.Busy) // positive evidence the worker is still working
		pBSay(r, "captain-held [key=route]: awaiting the captain")
		ws := r.tick()
		wantSurfaced(t, ws, "a captain-held declaration stays actionable while the worker is busy")
		pBWantKind(t, ws, wake.KindInputRequired, "captain-held is a leader-owed decision")
	})

	t.Run(s+"pending_reply_escalation_signal_payload_marked_for_branch_exclusion", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1851
		// cox: mailPass (wake.Classify)
		r := pBRig(t, backend.Alive, "")
		const corr = "0123456789abcdef"
		pBSay(r, "blocked [key=pending-reply-"+corr+"]: pending-reply-missed: task=task pending-reply-id="+corr+" request=finish report")
		ws := r.tick()
		wantSurfaced(t, ws, "a pending-reply escalation")
		pBWantKind(t, ws, wake.KindInputRequired, "a missed pending reply is a leader-owed decision")
	})

	t.Run(s+"ordinary_blocked_signal_payload_remains_branch_eligible", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1867
		// cox: mailPass (wake.Classify)
		// Branch eligibility has no cox analog (one leader); the translatable half is that an ordinary blocker surfaces.
		r := pBRig(t, backend.Alive, "")
		pBSay(r, "blocked [key=dependency]: waiting for an upstream release")
		wantSurfaced(t, r.tick(), "an ordinary blocked event")
	})

	t.Run(s+"routine_signal_payload_not_marked_needs_decision", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1886
		// cox: mailPass (wake.Classify)
		r := pBRig(t, backend.Alive, "")
		pBSay(r, "working: setup")
		pBSay(r, "done: migration complete ; needs-decision: documented in follow-up")
		ws := r.tick()
		wantSurfaced(t, ws, "an actionable done signal")
		pBWantKind(t, ws, wake.KindWorkerDone, "done: is the completion verb")
		pBNoKind(t, ws, wake.KindInputRequired, "a done line quoting a needs-decision phrase is not a decision")
	})

	t.Run(s+"actionable_signal_survives_a_later_routine_append", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1909
		// cox: mailPass (wake.Classify); firstmate mechanism: mechSpan
		r := pBRig(t, backend.Alive, busy.Busy)
		pBPrime(r, "working: setup")
		pBSay(r, "needs-decision: pick A or B")
		pBSay(r, "working: still tidying the branch")
		ws := r.tick()
		wantSurfaced(t, ws, "a needs-decision followed by a routine working: line while the worker is busy")
		pBWantKind(t, ws, wake.KindInputRequired, "the masked decision")
	})

	t.Run(s+"keyed_decision_signal_reads_only_the_new_span", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1941
		// cox: mechFold (mailPass)
		// The span-reader byte bound is a bash cost concern; the cox analog is that a long, already-classified history
		// yields no wake again and only the new messages are classified.
		r := pBRig(t, backend.Alive, "")
		var hist []string
		for i := 1; i <= 60; i++ {
			hist = append(hist, fmt.Sprintf("needs-decision [key=q%d]: choose option %d", i, i),
				fmt.Sprintf("resolved [key=q%d]: took the first option", i))
		}
		pBPrime(r, hist...)
		pBSay(r, "needs-decision [key=fresh]: pick the rollout window")
		pBSay(r, "working: preparing both windows")
		ws := r.tick()
		n := 0
		for _, w := range ws {
			if w.Story == portStory {
				n++
			}
		}
		if n > 2 {
			t.Errorf("classifying 2 new lines re-read the history: %d wakes %v", n, kinds(ws))
		}
		wantSurfaced(t, ws, "a still-open keyed decision appended to a long decision history")
		pBWantKind(t, ws, wake.KindInputRequired, "the fresh keyed decision is open")
	})

	t.Run(s+"release_completion_survives_a_later_routine_append", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1978
		// cox: mailPass (wake.Classify)
		r := pBRig(t, backend.Alive, busy.Busy)
		pBPrime(r, "working: publishing")
		pBSay(r, "done: release 1.4.0 published and installed")
		pBSay(r, "working: cleaning the build dir")
		ws := r.tick()
		wantSurfaced(t, ws, "a finished release before routine cleanup chatter")
		pBWantKind(t, ws, wake.KindWorkerDone, "done: is the completion verb")
	})

	t.Run(s+"routine_appends_after_a_classified_event_stay_absorbed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:1999
		// cox: mailPass (wake.Classify)
		r := pBRig(t, backend.Alive, busy.Busy)
		pBPrime(r, "working: setup", "needs-decision: pick A or B")
		pBSay(r, "working: still tidying the branch")
		ws := r.tick()
		if urgentFor(ws, portStory) {
			t.Errorf("a routine append re-surfaced an already-classified decision: %v", kinds(ws))
		}
		wantAbsorbed(t, ws, "a routine append after an already-classified event")
	})

	t.Run(s+"unreadable_status_reports_once_per_file_state", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2021
		// cox: mechUnreadable (mailPass)
		// The status source is the mailbox; a dangling status symlink is a mailbox read that fails, and a changed target
		// is a failure with a different cause.
		r := pBRig(t, backend.Alive, "")
		m := pBFlaky(r)
		m.err = errors.New("status source dangling: missing-status-target")
		ws, err := pBTickErr(r)
		if err != nil {
			t.Errorf("an unreadable status source aborted the whole watch pass: %v", err)
		}
		if len(ws) == 0 {
			t.Errorf("an unreadable status source was not reported to the leader")
		}
		if ws, _ := pBTickErr(r); len(ws) != 0 {
			t.Errorf("an unchanged unreadable status source reported again: %v", kinds(ws))
		}
		m.err = errors.New("status source dangling: status-target-two-longer")
		if ws, _ := pBTickErr(r); len(ws) == 0 {
			t.Errorf("a changed unreadable status source did not report again")
		}
		m.err = nil
		pBSay(r, "blocked: changed target state with a longer path")
		ws, err = pBTickErr(r)
		if err != nil {
			t.Fatalf("recovered status source tick failed: %v", err)
		}
		wantSurfaced(t, ws, "content preserved across the unreadable window surfaces on recovery")
	})

	t.Run(s+"permission_recovery_surfaces_preserved_status", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2067
		// cox: mechUnreadable (mailPass)
		r := pBRig(t, backend.Alive, "")
		m := pBFlaky(r)
		pBSay(r, "blocked: release approval required")
		pBSay(r, "working: preserving context")
		m.err = errors.New("status source: permission denied")
		ws, err := pBTickErr(r)
		if err != nil {
			t.Errorf("an unreadable status source aborted the whole watch pass: %v", err)
		}
		if len(ws) == 0 {
			t.Errorf("an unreadable regular status source was not reported")
		}
		if ws, _ := pBTickErr(r); len(ws) != 0 {
			t.Errorf("an unchanged unreadable status source reported again: %v", kinds(ws))
		}
		m.err = nil
		ws, err = pBTickErr(r)
		if err != nil {
			t.Fatalf("recovered status source tick failed: %v", err)
		}
		wantSurfaced(t, ws, "readability recovery surfaces the preserved blocked line")
		pBWantKind(t, ws, wake.KindInputRequired, "the preserved blocker")
	})

	t.Run(s+"terminal_stale_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2104
		// cox: mechStale (stalePass)
		// Crew state unset in firstmate (not provably working): no busy record. The done: line was already handled.
		r := pBRig(t, backend.Alive, "")
		pBPrime(r, "done: PR https://example.test/pr/3")
		ws := pBRound(r, DefaultStaleMin+time.Second)
		pBWantStale(t, ws, "a silent worker sitting on a terminal status")
		wantSurfaced(t, ws, "a stale worker on a terminal status is surfaced")
	})

	t.Run(s+"stale_terminal_status_overridden_by_active_run", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2137
		// cox: stalePass (an active run-step outranks a leftover captain-relevant line, then the wedge timer runs)
		r := pBRig(t, backend.Alive, "")
		r.ci("running") // the active run: provably working
		pBPrime(r, "done: implementation complete, ready to validate")
		r.w.StaleMin = 999 * time.Second
		wantAbsorbed(t, pBRound(r, DefaultStaleQuiet), "a stale done: under an active run is absorbed")
		r.w.StaleMin = 240 * time.Second
		ws := pBRound(r, 500*time.Second)
		pBWantStale(t, ws, "the run wedges past the threshold")
		wantSurfaced(t, ws, "the wedged run is escalated")
		pBWantNote(t, ws, "possible wedge", "escalation flags a possible wedge")
	})

	t.Run(s+"nonterminal_stale_provably_working_absorbed_then_escalated", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2192
		// cox: stalePass (working -> absorbed, wedge timer; past StaleMin -> possible wedge)
		r := pBRig(t, backend.Alive, "")
		r.ci("running")
		pBPrime(r, "working: still compiling")
		r.w.StaleMin = 999 * time.Second
		wantAbsorbed(t, pBRound(r, DefaultStaleQuiet), "a fresh provably-working stale is absorbed")
		r.w.StaleMin = 240 * time.Second
		ws := pBRound(r, 500*time.Second)
		pBWantStale(t, ws, "a provably-working stale past the threshold")
		wantSurfaced(t, ws, "a provably-working stale past the threshold escalates")
		pBWantNote(t, ws, "possible wedge", "escalation flags a possible wedge")
	})

	t.Run(s+"nonterminal_stale_not_working_surfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2248
		// cox: mechTurnEnd (idleNoDonePass)
		// Not provably working: the turn ended (busy idle, composer empty) on a non-terminal status and no steer is
		// outstanding. The clock is given the full stale and idle windows, far more than firstmate's first poll.
		r := pBRig(t, backend.Alive, busy.Idle)
		r.composer(backend.ComposerEmpty)
		r.w.StaleMin = 999 * time.Second
		pBPrime(r, "working: implementing")
		ws := pBRound(r, DefaultIdleNoDoneWait+time.Second)
		wantSurfaced(t, ws, "a stopped worker on a non-terminal status surfaces at once")
		pBNoNote(t, ws, "possible wedge", "an immediate stopped-worker surface is not a wedge")
	})

	t.Run(s+"nonterminal_stale_paused_absorbed_then_resurfaced", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2289
		// cox: handlePausedStale (a declared pause on an exited agent - fm pane command zsh - takes the pause cadence)
		r := pBRig(t, backend.Settled, busy.Idle)
		pBPrime(r, "paused: holding for the upstream tool release")
		r.w.PauseResurface = 999 * time.Second
		wantAbsorbed(t, pBRound(r, DefaultStaleQuiet), "a fresh declared pause is absorbed")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "since", portStory)); err == nil {
			t.Errorf("a paused absorb must not start the wedge timer")
		}
		r.w.PauseResurface = 240 * time.Second
		ws := pBRound(r, 500*time.Second)
		wantSurfaced(t, ws, "a declared pause past the resurface threshold is rechecked")
		pBWantNote(t, ws, "awaiting external", "labelled a paused / awaiting-external recheck")
		pBNoNote(t, ws, "possible wedge", "a declared pause is never a wedge")
	})

	t.Run(s+"exited_declared_pause_is_bounded_but_live_gate_surfaces", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2358
		// cox: mechDeclWait (stalePass)
		// Exited agent: the backend probe reports Settled.
		r := pBRig(t, backend.Settled, busy.Idle) // fm pane command zsh: the agent exited
		r.w.PauseResurface = 240 * time.Second
		pBPrime(r, "paused: held per captain while an external decision is pending")
		r.advance(500 * time.Second)
		var all []wake.Wake
		for round := 1; round <= 6; round++ {
			all = append(all, pBRound(r, pBPoll)...)
		}
		if a := pBAlarms(all); a > 1 {
			t.Errorf("an exited declared pause flooded %d alarms across six unchanged polls", a)
		}
		pBWantNote(t, all, "awaiting external", "an exited declared pause takes the bounded paused recheck")

		r = pBRig(t, backend.Settled, busy.Idle)
		r.w.PauseResurface = 240 * time.Second
		pBPrime(r, "captain-held [key=route]: tracked by held-decision-route")
		ws := pBRound(r, 500*time.Second)
		pBWantNote(t, ws, "awaiting the captain", "an exited captain-held lane is a captain-owned recheck")
		pBNoNote(t, ws, "awaiting external", "captain-held must not borrow the pause wording")

		r = pBRig(t, backend.Alive, busy.Idle) // fm pane command grok: a live agent
		r.w.PauseResurface = 999 * time.Second
		pBPrime(r, "paused: waiting at an active external-decision gate")
		wantSurfaced(t, pBRound(r, DefaultStaleQuiet), "a live external-decision gate surfaces on first sight")
		r.w.StaleMin = 240 * time.Second
		ws = pBRound(r, 500*time.Second)
		if a := pBAlarms(ws); a != 0 {
			t.Errorf("an acknowledged live gate replayed %d alarm(s): %v", a, kinds(ws))
		}
		pBNoNote(t, ws, "possible wedge", "a live gate keeps the pause cadence, not the wedge timer")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "paused", portStory)); err != nil {
			t.Errorf("live external-decision gate lost its pause cadence marker")
		}
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "since", portStory)); err == nil {
			t.Errorf("live external-decision gate retained the wedge timer")
		}
	})

	t.Run(s+"absorbed_replacement_wait_does_not_inherit_the_old_throttle", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2481
		// cox: mechCadence (stalePass)
		for _, c := range []struct{ name, initial, replacement, expected string }{
			{"paused-replacement", "paused: waiting on validation run one", "paused: waiting on validation run two", "awaiting external"},
			{"captain-held-replacement", "captain-held [key=route]: awaiting the routing call", "captain-held [key=release]: awaiting the release call", "awaiting the captain"},
		} {
			r := pBRig(t, backend.Settled, busy.Idle) // fm pane command zsh
			r.w.PauseResurface = 240 * time.Second
			pBPrime(r, c.initial)
			if !urgentFor(pBRound(r, 500*time.Second), portStory) {
				t.Errorf("[%s] the initial declared wait did not re-surface", c.name)
			}
			pBSay(r, c.replacement)
			r.tick() // fm primes .seen for the replacement line: its own signal round is not under test
			ws := pBRound(r, pBPoll)
			if a := pBAlarms(ws); a != 1 {
				t.Errorf("[%s] the replacement declared wait produced %d alarms instead of one: %v", c.name, a, kinds(ws))
			}
			pBWantNote(t, ws, c.expected, c.name+" replacement recheck reason")
		}
	})

	t.Run(s+"live_declared_wait_churn_honors_the_resurface_throttle", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2582
		// cox: mechCadence (stalePass)
		// Pane churn (a ticking clock on an idle pane) has no cox signal; the rounds are plain polls.
		for _, c := range []struct{ name, line, replacement string }{
			{"paused-pipeline-churn", "paused: waiting on the validation run to finish", "paused: waiting on the replacement validation run"},
			{"captain-held-churn", "captain-held [key=route]: awaiting the captain on the routing call", "captain-held [key=release]: awaiting the captain on the release call"},
		} {
			r := pBRig(t, backend.Alive, busy.Idle) // fm parked_watch_round: pane command grok, FM_PAUSE_RESURFACE_SECS=999
			r.w.PauseResurface = 999 * time.Second
			pBPrime(r, c.line)
			if !urgentFor(pBRound(r, DefaultStaleQuiet), portStory) {
				t.Errorf("[%s] first sight of a parked live worker did not surface", c.name)
			}
			pBAbsorbRounds(t, r, 3, c.name+" churn inside the resurface window")
			pBSay(r, c.replacement)
			r.tick() // fm writes the replacement pre-seen: its own signal round is not under test
			if a := pBAlarms(pBRound(r, pBPoll)); a != 1 {
				t.Errorf("[%s] the replacement declared wait produced %d first alarms instead of one", c.name, a)
			}
			pBAbsorbRounds(t, r, 1, c.name+" replacement inside its own window")
			if a := pBAlarms(pBRound(r, 2000*time.Second)); a != 1 {
				t.Errorf("[%s] the elapsed resurface window produced %d alarms instead of one", c.name, a)
			}
		}
	})

	t.Run(s+"live_paused_until_controls_recheck_time", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2669
		// cox: mechCadence (stalePass)
		r := pBRig(t, backend.Alive, busy.Idle)
		r.w.PauseResurface = 999 * time.Second
		pBPrime(r, "paused: rate limit until "+r.clock.Add(2*time.Hour).UTC().Format(time.RFC3339))
		pBAbsorbRounds(t, r, 2, "a live worker before its declared future time")
		pBSay(r, "paused: rate limit until "+r.clock.Add(-2*time.Minute).UTC().Format(time.RFC3339))
		if a := pBAlarms(r.tick()); a != 1 {
			t.Errorf("a passed declared time produced %d alarms instead of one", a)
		}
		pBAbsorbRounds(t, r, 1, "a due declared time rechecks only once inside the long cadence")
	})

	t.Run(s+"wedge_threshold_defers_to_a_declared_wait_under_a_working_verdict", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2814
		// cox: mechWedge (stalePass)
		// FM_STALE_ESCALATE_SECS=1 puts every round at the threshold: StaleMin 1s, one poll per round.
		rig := func(line string) *portRig { return pBWedgeFixture(t, "working", line) }
		r := rig("paused: final validation at step 6/6 - clean whole-assembly baseline (~20 min)")
		pBAbsorbRounds(t, r, 3, "a declared wait under a working verdict is not wedge-escalated")

		r = rig("paused: waiting on the upstream release cut")
		ws := pBRound(r, 2000*time.Second)
		pBWantNote(t, ws, "declared wait", "the recheck names its evidence as declared")
		pBWantNote(t, ws, "awaiting external", "the recheck names who the wait is on")
		pBWantNote(t, ws, "confirm the wait still holds", "the external-wait action")
		if n := pBWaitSecs(ws); n < 1900 {
			t.Errorf("the declared-wait recheck reported %ds rather than the declaration's own age", n)
		}
		pBNoNote(t, ws, "release the hold", "an external wait must not borrow the captain-held action")
		pBNoNote(t, ws, "possible wedge", "a declared-wait recheck is not a wedge")

		r = rig("paused: waiting on the build queue until " + time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
		pBWantNote(t, pBRound(r, pBPoll), "possible wedge, escalation 1", "an elapsed declaration keeps the unchanged ladder")

		r = rig("working: validation under way")
		pBLadder(t, r, 3, "an undeclared working lane keeps the ladder")
	})

	t.Run(s+"wedge_threshold_recheck_names_the_captain_for_a_held_lane", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:2899
		// cox: mechWedge (stalePass); firstmate mechanism: mechDeclWait
		// The away-posture legs (write_away_record / archive_away_record) are the afk daemon: n/a, not translated.
		r := pBWedgeFixture(t, "working", "captain-held: which retention window wins")
		ws := pBRound(r, 2000*time.Second)
		pBWantNote(t, ws, "awaiting the captain", "the hold names the captain")
		pBWantNote(t, ws, "answer the held decision or release the hold", "the action that clears the hold")
		pBNoNote(t, ws, "awaiting external", "a hold is not an external wait")
		pBNoNote(t, ws, "confirm the wait still holds", "a hold must not borrow the external-wait action")
		pBNoNote(t, ws, "possible wedge", "a hold is not a wedge")

		r = pBWedgeFixture(t, "working", "captain-held: which retention window wins")
		pBAbsorbRounds(t, r, 3, "a fresh hold inside its recheck cadence is absorbed")
	})

	t.Run(s+"wedge_threshold_defers_to_a_parked_gate_awaiting_a_human", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3000
		// cox: mechRunStep (stalePass)
		// The parked-gate verdict (human- vs crewmate-owed) comes from the run pipeline's gate state, which cox has no
		// analog of. What cox can express: the crewmate-owed / unrelated-key / runless directions keep the ladder.
		for _, c := range []struct {
			name, log string
			rounds    int
		}{
			{"crewmate", pBGateKeyLog + "\nworking: still parked at that gate", 3},
			{"unrelated-key", "needs-decision [key=earlier-question]: which changelog section fits\nworking: still parked at that gate", 3},
			{"runless", pBGateKeyLog + "\nworking: still parked at that gate", 1},
		} {
			r := pBWedgeFixture(t, "parked", strings.Split(c.log, "\n")...)
			pBLadder(t, r, c.rounds, c.name+" gate keeps the unchanged ladder")
		}
		// n/a armed half (fm config/wedge-defer-parked-gate): the deferral reads a no-mistakes gate's human-owed finding
		// (the findings table's ask-user action); cox has no human-owed verdict observable (firstmate-only surface, DESIGN
		// rule 5), leader ruling q003. The unarmed ladder above is the cox requirement.
	})

	t.Run(s+"wedge_threshold_parked_gate_is_off_until_armed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3164
		// cox: mechRunStep (stalePass)
		// The config/wedge-defer-parked-gate flag gates run-pipeline evidence; unarmed, the lane keeps the ladder.
		r := pBWedgeFixture(t, "parked", pBGateKeyLog, "working: still parked at that gate")
		pBLadder(t, r, 3, "an unarmed home keeps the unchanged ladder")
		pBNoNote(t, r.tick(), "verified wait at a parked gate", "an unarmed home never defers a parked gate")
		// n/a armed half (fm config/wedge-defer-parked-gate): the deferral reads a no-mistakes gate's human-owed finding
		// (the findings table's ask-user action); cox has no human-owed verdict observable (firstmate-only surface, DESIGN
		// rule 5), leader ruling q003. The unarmed ladder above is the cox requirement.
	})

	t.Run(s+"wedge_threshold_parked_gate_needs_an_unanswered_decision", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3231
		// cox: mechRunStep (stalePass); firstmate mechanism: mechFold
		for _, c := range []struct {
			name   string
			lines  []string
			rounds int
		}{
			{"decided", []string{pBGateKeyLog, "resolved [key=nm-01RUNGATE-review]: firstmate chose the second fix"}, 3},
			{"unescalated", []string{"working: validation under way"}, 1},
			{"blocked", []string{"blocked [key=nm-01RUNGATE-review]: the fixture cannot reach its dependency"}, 1},
		} {
			r := pBWedgeFixture(t, "parked", c.lines...)
			pBLadder(t, r, c.rounds, c.name+" gate keeps the unchanged ladder")
		}
		// n/a armed half (fm config/wedge-defer-parked-gate): the deferral reads a no-mistakes gate's human-owed finding
		// (the findings table's ask-user action); cox has no human-owed verdict observable (firstmate-only surface, DESIGN
		// rule 5), leader ruling q003. The unarmed ladder above is the cox requirement.
	})

	// n/a test_wedge_defer_refuses_a_half_filled_wait_record (fm-watch-triage.test.sh:3344): bash-only - it sources
	// fm-watch.sh, overrides wedge_wait_evidence and checks the US-delimited wait_record field parsing (empty/surplus
	// fields under `IFS= read`); cox has no serialized wait record, so there is no cox-side requirement.
}

// Mechanism names for the report's red list (exact strings from the translator brief).
const (
	pCMechDeclWait    = "declared wait: paused / captain-held status verbs"
	pCMechWedge       = "wedge detector: escalation schedule, deep inspection, write deferral"
	pCMechRunStep     = "run-step authority: an active pipeline run overrides a stale/terminal status"
	pCMechWaitCadence = "wait cadence: paused-until / resurface throttle"
)

// pCWedge mirrors firstmate's FM_STALE_ESCALATE_SECS=240 wedge threshold as the watcher's StaleMin.
const pCWedge = 240 * time.Second

// pCWedgeRig is a rig whose stale threshold is the firstmate wedge threshold.
func pCWedgeRig(t *testing.T) *portRig {
	r := newPortRig(t)
	r.w.StaleMin = pCWedge
	return r
}

// pCSilentPast advances the clock past the stale threshold (one wedge-threshold sighting).
func (r *portRig) pCSilentPast() { r.advance(r.w.staleMin() + time.Minute) }

// pCAgeBusy backdates the story's busy record so its last busy event is d before the rig clock (firstmate: touch -t on
// the spawn record / turn-ended marker). The record's trust fields are kept, so busy.Read still trusts it.
func (r *portRig) pCAgeBusy(d time.Duration) {
	r.t.Helper()
	rec, ok := busy.ReadRecord(r.epic, portStory)
	if !ok {
		r.t.Fatal("pCAgeBusy: no busy record")
	}
	rec.TS = r.clock.Add(-d).Unix()
	b, err := json.Marshal(rec)
	portMust(r.t, err)
	portMust(r.t, os.WriteFile(busy.Path(r.epic, portStory), b, 0o644))
}

// pCApply appends one busy event to the current incarnation through the trusted claude-hook source.
func (r *portRig) pCApply(s, event string) {
	r.t.Helper()
	rec, ok := busy.ReadRecord(r.epic, portStory)
	if !ok {
		r.t.Fatal("pCApply: no busy record")
	}
	portMust(r.t, busy.Apply(r.epic, portStory, s, rec.Gen, "claude-hook", event))
}

// pCWantNotSurfaced asserts the tick raised no urgent wake for the story (firstmate: the watcher kept running).
func pCWantNotSurfaced(t *testing.T, ws []wake.Wake, why string) {
	t.Helper()
	if urgentFor(ws, portStory) {
		t.Errorf("want not surfaced (%s), got %v", why, kinds(ws))
	}
}

// pCUrgentCount counts the story's urgent wakes (firstmate: wedge_stale_wakes / hold_stale_wakes).
func pCUrgentCount(ws []wake.Wake) int {
	n := 0
	for _, w := range ws {
		if w.Story == portStory && wake.IsUrgent(w.Kind) {
			n++
		}
	}
	return n
}

// pCWantKindFor asserts the tick raised a wake of kind k for the story.
func pCWantKindFor(t *testing.T, ws []wake.Wake, k wake.Kind, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory && w.Kind == k {
			return
		}
	}
	t.Errorf("want a %s wake (%s), got %v", k, why, kinds(ws))
}

// pCHoldRig is firstmate's make_hold_home: a delivered lane whose status line is pre-seen, the agent exited (pane
// command zsh), FM_PAUSE_RESURFACE_SECS=999, optionally held for the captain (cox: story state input_required).
func pCHoldRig(t *testing.T, line string, hold bool) *portRig {
	t.Helper()
	r := newPortRig(t)
	r.busySet(busy.Idle)
	r.liveness(backend.Settled)
	r.w.PauseResurface = 999 * time.Second
	if hold {
		pCHold(r, true)
	}
	r.mail("hold-line", "status", line)
	r.tick() // pre-seen in firstmate
	return r
}

// pCHold records the captain call opening (input_required) or being released (back to working).
func pCHold(r *portRig, open bool) {
	r.t.Helper()
	from, to := state.Working, state.InputRequired
	if !open {
		from, to = to, from
	}
	portMust(r.t, state.Append(r.epic, state.Event{Epic: filepath.Base(r.epic), Story: portStory, Attempt: 1,
		Actor: state.Leader, From: from, To: to, ExternalConfirmed: true}))
}

// pCChurn is one pane-churn sighting: the idle pane renders something new (a heartbeat), then sits quiet for d.
func pCChurn(r *portRig, d time.Duration) []wake.Wake {
	r.t.Helper()
	r.heartbeat(fmt.Sprintf("churn-%d", len(r.mb.Queue)))
	r.tick()
	r.advance(d)
	return r.tick()
}

func TestPortTriageC(t *testing.T) {
	const s = "FM/fm-watch-triage/"
	const donePR = "done: PR https://example.invalid/pull/1 checks green"

	t.Run(s+"gone_endpoint_reports_once_instead_of_escalating_forever", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3400
		// cox: wedgeDeadRecord (a Settled probe is proof the endpoint is gone; cox cannot tell dead from missing)
		// The lane is already stably stale and surfaced once (fm wedge_threshold_fixture); the run failed (not working).
		for _, verdict := range []string{"dead", "missing"} {
			r := pCWedgeRig(t)
			r.ci("failed")
			r.heartbeat("hb1")
			r.tick()
			r.firstSight()
			r.liveness(backend.Settled)
			r.pCSilentPast()
			ws := r.tick()
			wantSurfaced(t, ws, "a "+verdict+" endpoint is reported once at the wedge threshold")
			pBWantNote(t, ws, "agent gone", "the report names the endpoint verdict")
			pBNoNote(t, ws, "possible wedge", "a gone endpoint is not a possible wedge")
			if n := pCUrgentCount(ws); n > 1 {
				t.Errorf("a %s endpoint queued %d urgent wakes instead of one", verdict, n)
			}
			for round := 1; round <= 3; round++ {
				r.pCSilentPast()
				wantAbsorbed(t, r.tick(), verdict+" endpoint must not re-alarm on a later threshold")
			}
			if _, err := os.Stat(filepath.Join(r.w.watchDir(), "esc", portStory)); err == nil {
				t.Errorf("a %s endpoint advanced the wedge escalation count", verdict)
			}
		}
	})

	t.Run(s+"live_and_unproven_endpoints_still_wedge_escalate", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3444
		// cox: wedgeTimerCheck (Alive / Unknown / a failed probe are not proof of gone: the ladder is unchanged)
		for _, spec := range []string{"alive", "ambiguous", "unreadable"} {
			r := pCWedgeRig(t)
			r.ci("running") // fm verdict: working, run-step ci running
			r.heartbeat("hb1")
			r.tick()
			r.firstSight()
			for round := 1; round <= 2; round++ {
				switch spec {
				case "alive":
					r.liveness(backend.Alive)
				case "ambiguous":
					r.liveness(backend.Unknown)
				case "unreadable":
					r.b.FailNext("Probe", nil)
				}
				r.pCSilentPast()
				pBWantNote(t, r.tick(), fmt.Sprintf("possible wedge, escalation %d", round), spec+" endpoint keeps the ladder")
			}
		}
	})

	t.Run(s+"gone_report_rearms_when_the_endpoint_comes_back", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3478
		// cox: wedgeDeadRecord (a probe that reads alive again drops the once-record)
		r := pCWedgeRig(t)
		r.ci("failed")
		r.heartbeat("hb1")
		r.tick()
		r.firstSight()
		r.liveness(backend.Settled)
		r.pCSilentPast()
		pBWantNote(t, r.tick(), "agent gone", "the gone endpoint is reported")
		// A replacement is launched (it renders: a heartbeat) under an active run and then wedges for real.
		r.ci("running")
		r.liveness(backend.Alive)
		r.heartbeat("hb2")
		r.tick()
		r.firstSight()
		r.pCSilentPast()
		pBWantNote(t, r.tick(), "possible wedge, escalation 1", "a replacement agent's wedge escalates, not swallowed by the earlier gone report")
		// The replacement dies too: reported again in full.
		r.ci("failed")
		r.liveness(backend.Settled)
		r.pCSilentPast()
		pBWantNote(t, r.tick(), "agent gone", "a second death in the same window is reported")
	})

	t.Run(s+"second_death_after_a_same_window_relaunch_reports_in_full", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3525
		// cox: wedgeDeadRecord (no busy record: the incarnation is the activity signature)
		r := pCWedgeRig(t)
		r.ci("failed")
		r.heartbeat("hb1")
		r.tick()
		r.firstSight()
		r.liveness(backend.Settled)
		r.pCSilentPast()
		pBWantNote(t, r.tick(), "agent gone", "the first death is reported")
		// Relaunch: the pane churns under an active run; the round ends before its fresh window elapses.
		r.ci("running")
		r.liveness(backend.Alive)
		r.heartbeat("hb2")
		r.tick()
		r.advance(time.Minute)
		ws := r.tick()
		wantAbsorbed(t, ws, "the relaunch round ends before its fresh window elapses")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "dead", portStory)); err != nil {
			t.Errorf("the relaunch churn dropped the first death's once-record")
		}
		// The replacement dies without any intervening probe reading it alive.
		r.ci("failed")
		r.liveness(backend.Settled)
		r.pCSilentPast()
		ws = r.tick()
		pBWantNote(t, ws, "agent gone", "a second death after a same-window relaunch is reported")
		if n := pCUrgentCount(ws); n > 1 {
			t.Errorf("the second death queued %d urgent wakes instead of one", n)
		}
		r.pCSilentPast()
		wantAbsorbed(t, r.tick(), "an unchanged dead endpoint stays silent after the second report")
	})

	t.Run(s+"identical_dead_display_of_a_successor_still_reports", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3597
		// cox: wedgeDeadRecord (the busy.Arm gen tells a successor apart from the reported death on an identical display)
		r := pCWedgeRig(t)
		r.ci("failed")
		r.busySet(busy.Busy) // armed at spawn
		r.heartbeat("hb1")
		r.tick()
		r.liveness(backend.Settled)
		r.firstSight()
		r.pCSilentPast()
		ws := r.tick()
		pBWantNote(t, ws, "agent gone", "the first death (busy record still says busy) is reported")
		if n := pCUrgentCount(ws); n > 1 {
			t.Errorf("the first death queued %d urgent wakes instead of one", n)
		}
		// A successor re-arms the incarnation; its display is byte-identical, and it stays under the threshold.
		r.busySet(busy.Busy)
		r.advance(time.Minute)
		wantAbsorbed(t, r.tick(), "the successor's quiet round")
		// The successor dies into the identical display.
		r.pCSilentPast()
		ws = r.tick()
		pBWantNote(t, ws, "agent gone", "a successor's death (new incarnation) is reported")
		if n := pCUrgentCount(ws); n > 1 {
			t.Errorf("the successor's death queued %d urgent wakes instead of one", n)
		}
		r.pCSilentPast()
		wantAbsorbed(t, r.tick(), "the same incarnation's unchanged dead endpoint stays silent")
	})

	t.Run(s+"open_captain_call_bounds_stale_churn", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3773
		// cox: captainCallBound (fm backlog hold = the story held on input: state input_required; its transition is the
		// call identity). The agent exited (fm pane zsh); pane churn is a worker heartbeat; FM_PAUSE_RESURFACE_SECS=999.
		for _, line := range []string{donePR, "working: still tidying the branch"} {
			r := pCHoldRig(t, line, true)
			ws := pCChurn(r, DefaultStaleQuiet)
			wantSurfaced(t, ws, "first sight of held work surfaces: "+line)
			if n := pCUrgentCount(ws); n > 1 {
				t.Errorf("first sight produced %d urgent wakes instead of one: %s", n, line)
			}
			if _, err := os.Stat(filepath.Join(r.w.watchDir(), "paused-resurfaced", portStory)); err != nil {
				t.Errorf("the first sight recorded no re-surface throttle: %s", line)
			}
			for i := 0; i < 2; i++ {
				wantAbsorbed(t, pCChurn(r, DefaultStaleQuiet), "churn inside the re-surface window: "+line)
			}
			wantSurfaced(t, pCChurn(r, 5000*time.Second), "held work re-surfaces once its window elapsed: "+line)
		}
	})

	t.Run(s+"stale_churn_without_a_captain_call_still_alarms", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3819
		// cox: stale escalation
		// Unheld: a stopped worker keeps alarming once per new sighting (firstmate: a new pane hash; cox: a new stale
		// window).
		for _, line := range []string{donePR, "blocked: cannot reach the release host", "working: still tidying the branch"} {
			r := newPortRig(t)
			r.busySet(busy.Idle)
			r.mail("m1", "status", line)
			for round := 1; round <= 2; round++ {
				if round > 1 {
					r.advance(r.w.staleMin() + time.Minute)
				}
				ws := r.tick()
				wantSurfaced(t, ws, "unheld stale window alarms on round "+string(rune('0'+round))+": "+line)
				if n := pCUrgentCount(ws); n > 1 {
					t.Errorf("round %d produced %d urgent wakes instead of one: %s", round, n, line)
				}
			}
		}
	})

	t.Run(s+"failed_wake_append_does_not_arm_the_captain_hold_throttle", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3852
		// cox: mailPass
		// A wake that never reached the durable queue must not be recorded as delivered: the watcher fails, and the retry
		// alarms exactly once.
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.mail("m1", "status", donePR)
		q := filepath.Join(r.epic, state.ControlDir, "wake.jsonl")
		_ = os.Remove(q)
		portMust(t, os.MkdirAll(q, 0o755))
		if _, err := r.w.Tick(); err == nil {
			t.Errorf("the watcher reported success despite an unwritable durable queue")
		}
		portMust(t, os.Remove(q))
		ws := r.tick()
		wantSurfaced(t, ws, "the retry after a failed wake append alarms")
		if n := pCUrgentCount(ws); n != 1 {
			t.Errorf("the retry produced %d urgent wakes instead of one", n)
		}
	})

	t.Run(s+"reheld_captain_call_starts_its_own_resurface_window", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3893
		// cox: captainCallBound (a release and a re-hold with no status append is a new call identity)
		r := pCHoldRig(t, donePR, true)
		wantSurfaced(t, pCChurn(r, DefaultStaleQuiet), "first sight of the first captain call")
		wantAbsorbed(t, pCChurn(r, DefaultStaleQuiet), "the first call's churn inside its window")
		pCHold(r, false) // answered and released
		pCHold(r, true)  // held again: a genuinely different call
		ws := pCChurn(r, DefaultStaleQuiet)
		wantSurfaced(t, ws, "the re-held call's first sight alarms instead of inheriting the old window")
		if n := pCUrgentCount(ws); n != 1 {
			t.Errorf("the re-held call alarmed %d times instead of once", n)
		}
	})

	t.Run(s+"secondmate_paused_resurfaces_in_normal_mode", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3927
		// cox: handlePausedStale (the secondmate kind is firstmate-only; the requirement holds for any crew whose agent
		// is not reading: an exited agent; FM_PAUSE_RESURFACE_SECS=240)
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Settled)
		r.w.PauseResurface = 240 * time.Second
		r.mail("m1", "status", "paused: awaiting the upstream release")
		r.tick()                     // the status line itself (pre-seen in firstmate)
		r.advance(500 * time.Second) // past FM_PAUSE_RESURFACE_SECS=240
		ws := r.tick()
		wantSurfaced(t, ws, "a declared paused wait re-surfaces as a recheck")
		pBWantNote(t, ws, "awaiting external", "the recheck names the external wait")
		pBNoNote(t, ws, "awaiting the captain", "a pause is not a captain hold")
		pBNoNote(t, ws, "possible wedge", "a declared wait is not a wedge")
	})

	t.Run(s+"secondmate_captain_held_resurfaces_in_normal_mode", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:3961
		// cox: handlePausedStale (captain-held names the captain)
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Settled)
		r.w.PauseResurface = 240 * time.Second
		r.mail("m1", "status", "captain-held [key=route]: tracked by task-decision-route")
		r.tick()
		r.advance(500 * time.Second)
		ws := r.tick()
		wantSurfaced(t, ws, "a captain-held wait re-surfaces as a recheck")
		pBWantNote(t, ws, "awaiting the captain", "the recheck names the captain as the blocker")
		pBNoNote(t, ws, "awaiting external", "a hold is not an external wait")
		pBNoNote(t, ws, "possible wedge", "a declared wait is not a wedge")
	})

	// n/a test_secondmate_nonpaused_stale_remains_suppressed (fm-watch-triage.test.sh:3991): the parent-supervises stale
	// exemption exists only for firstmate secondmates; cox has no nested supervisor kind, so there is no requirement.

	// n/a test_secondmate_unpause_clears_pause_tracking (fm-watch-triage.test.sh:4015): clears secondmate-only .paused-* /
	// .stale-* marker files before the secondmate stale exemption; no cox-side behavior beyond that bookkeeping.

	t.Run(s+"nonterminal_stale_pause_transitions_reclassify_unchanged_hash", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4041
		// cox: staleStory (the same quiet interval is reclassified when the status line enters or leaves a declared pause)
		r := newPortRig(t)
		r.liveness(backend.Settled) // the agent exited behind its declaration
		r.heartbeat("hb1")
		r.mail("m1", "status", "paused: awaiting the upstream release")
		r.tick()
		r.firstSight()
		r.advance(r.w.staleMin() + time.Minute)
		wantAbsorbed(t, r.tick(), "a stale worker that entered a declared pause is not wedge-escalated")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "paused", portStory)); err != nil {
			t.Errorf("entering a declared pause did not flag the pause cadence")
		}
		r.ci("running") // leaving the pause under an active run
		r.mail("m2", "status", "working: upstream landed, resuming")
		r.tick()
		r.advance(time.Minute)
		wantAbsorbed(t, r.tick(), "leaving the pause restarts wedge tracking without surfacing")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "paused", portStory)); err == nil {
			t.Errorf("leaving the pause kept the pause cadence flag")
		}
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "since", portStory)); err != nil {
			t.Errorf("leaving the pause did not restart the wedge timer")
		}
	})

	t.Run(s+"nonterminal_paused_rechecks_authoritative_state", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4098
		// cox: pauseStateClass (an active run behind a declared pause reads working: wedge tracking, not the pause cadence)
		r := newPortRig(t)
		r.liveness(backend.Alive)
		r.ci("running")
		r.heartbeat("hb1")
		r.mail("m1", "status", "paused: awaiting the upstream release")
		r.tick()
		r.advance(DefaultStaleQuiet)
		wantAbsorbed(t, r.tick(), "an active run behind a declared pause resumes wedge tracking without surfacing")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "since", portStory)); err != nil {
			t.Errorf("an active run behind a declared pause did not start the wedge timer")
		}
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "paused", portStory)); err == nil {
			t.Errorf("an active run behind a declared pause kept the pause cadence")
		}
	})

	t.Run(s+"paused_authoritative_working_preserves_wedge_timer", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4128
		// cox: wedgeWaitEvidence (a declared wait under a working verdict defers each escalation; lifting it restores the
		// ladder on the same timer)
		r := pCWedgeRig(t)
		r.liveness(backend.Alive)
		r.ci("running") // authoritative working
		r.heartbeat("hb1")
		r.mail("m1", "status", "paused: awaiting the upstream release")
		r.tick()
		r.firstSight()
		r.pCSilentPast()
		ws := r.tick()
		wantAbsorbed(t, ws, "a still-declared wait is not wedge-escalated past the threshold")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "since", portStory)); err != nil {
			t.Errorf("the declared-wait deferral dropped the wedge timer")
		}
		r.mail("m2", "status", "working: resumed after the release landed")
		r.tick()
		r.pCSilentPast()
		pBWantNote(t, r.tick(), "possible wedge, escalation 1", "lifting the declaration restores the wedge escalation")
	})

	t.Run(s+"wedge_escalation_marks_demand_deep_inspection_after_threshold", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4207
		// cox: wedgeTimerCheck (FM_WEDGE_DEMAND_INSPECT_COUNT=3)
		r := pCWedgeRig(t)
		r.liveness(backend.Alive)
		r.ci("running") // fm verdict: working, run-step validating
		r.heartbeat("hb1")
		r.tick()
		r.advance(DefaultStaleQuiet)
		wantAbsorbed(t, r.tick(), "the priming round absorbs (first sight of a provably-working stale)")
		for n := 1; n <= 3; n++ {
			r.pCSilentPast()
			ws := r.tick()
			pBWantNote(t, ws, fmt.Sprintf("escalation %d", n), "consecutive wedge escalation round")
			if n < 3 {
				pBNoNote(t, ws, "demand-deep-inspection", "before the threshold")
			} else {
				pBWantNote(t, ws, "demand-deep-inspection", "at the threshold")
			}
		}
		if b, _ := os.ReadFile(filepath.Join(r.w.watchDir(), "esc", portStory)); string(b) != "3" {
			t.Errorf("escalation counter did not persist across consecutive rounds: %q", b)
		}
	})

	t.Run(s+"wedge_escalation_resets_when_pane_becomes_active", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4262
		// cox: staleStory (a changed activity signature resets the escalation bookkeeping)
		r := pCWedgeRig(t)
		r.liveness(backend.Alive)
		r.ci("running")
		r.heartbeat("hb1")
		r.tick()
		r.firstSight()
		r.pCSilentPast()
		pBWantNote(t, r.tick(), "escalation 1", "a prior wedge round")
		r.heartbeat("hb2") // the worker is active again
		wantAbsorbed(t, r.tick(), "fresh activity is absorbed")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "esc", portStory)); err == nil {
			t.Errorf("a changed pane did not reset the wedge-escalation counter")
		}
	})

	// n/a test_term_stops_a_watcher_blocked_inside_a_poll (fm-watch-triage.test.sh:4305): bash TERM-trap deferral inside
	// a blocked command substitution; cox's Run stops on a channel between Ticks, no bash loop to signal.

	t.Run(s+"busy_pane_below_turn_age_bound_is_absorbed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4354
		// cox: busyTurnMaxPass
		r := newPortRig(t)
		r.w.BusyTurnMax = 999 * time.Second
		r.busySet(busy.Busy) // a fresh busy event (turn-ended just touched)
		wantAbsorbed(t, r.tick(), "a busy worker below the turn-age bound")
	})

	t.Run(s+"busy_pane_stable_hash_escalates_past_turn_age_bound", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4380
		// cox: stale escalation
		r := newPortRig(t)
		r.w.BusyTurnMax = time.Second
		r.busySet(busy.Busy)
		r.pCAgeBusy(24 * time.Hour) // no completed turn ever: the spawn record is old
		pCWantNotSurfaced(t, r.tick(), "phase A: past the bound but below the wedge threshold starts the timer only")
		r.advance(500 * time.Second) // past FM_STALE_ESCALATE_SECS=240
		wantSurfaced(t, r.tick(), "phase B: a busy worker past the turn-age bound escalates as a possible wedge")
	})

	t.Run(s+"busy_pane_changing_hash_escalates_past_turn_age_bound", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4425
		// cox: stale escalation
		// The ticking footer maps to a screen that changes on every read; cox must not treat that as progress.
		r := newPortRig(t)
		r.w.BusyTurnMax = time.Second
		r.busySet(busy.Busy)
		r.pCAgeBusy(24 * time.Hour)
		r.b.ScreenRows = []string{"Working... (3600.1s)"}
		pCWantNotSurfaced(t, r.tick(), "phase A: first sight past the bound starts the timer only")
		r.b.ScreenRows = []string{"Working... (3601.2s)"}
		r.advance(500 * time.Second)
		wantSurfaced(t, r.tick(), "phase B: a busy worker whose display ticks still escalates past the bound")
	})

	t.Run(s+"busy_pane_turn_end_touch_resets_age", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4467
		// cox: busyTurnMaxPass
		r := newPortRig(t)
		r.w.BusyTurnMax = time.Hour
		r.busySet(busy.Busy)
		r.pCAgeBusy(2 * time.Hour)
		r.tick()                                 // mid-escalation, as if several over-age polls already ran
		r.pCApply(busy.Idle, "Stop")             // the turn just completed
		r.pCApply(busy.Busy, "UserPromptSubmit") // and the next one began
		wantAbsorbed(t, r.tick(), "a freshly completed turn resets the busy age")
		for _, f := range []string{"since", "esc"} {
			if _, err := os.Stat(filepath.Join(r.w.watchDir(), f, portStory)); err == nil {
				t.Errorf("a fresh turn event did not clear the wedge %s", f)
			}
		}
	})

	t.Run(s+"busy_pane_native_progress_resets_age", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4501
		// cox: busyTurnMaxPass
		// Native progress without a completed turn = a fresh busy event on the same incarnation.
		r := newPortRig(t)
		r.w.BusyTurnMax = time.Hour
		r.busySet(busy.Busy)
		r.pCAgeBusy(2 * time.Hour)
		r.tick()
		r.pCApply(busy.Busy, "PostToolUse")
		wantAbsorbed(t, r.tick(), "fresh native progress resets the busy age")
		for _, f := range []string{"since", "esc"} {
			if _, err := os.Stat(filepath.Join(r.w.watchDir(), f, portStory)); err == nil {
				t.Errorf("a fresh turn event did not clear the wedge %s", f)
			}
		}
	})

	t.Run(s+"busy_pane_repeated_escalation_reaches_demand_deep_inspection", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4537
		// cox: wedge detector
		r := newPortRig(t)
		r.w.BusyTurnMax = time.Second
		r.busySet(busy.Busy)
		r.pCAgeBusy(24 * time.Hour)
		pCWantNotSurfaced(t, r.tick(), "the priming round past the bound")
		for n := 1; n <= 3; n++ {
			r.advance(500 * time.Second)
			ws := r.tick()
			pBWantNote(t, ws, fmt.Sprintf("possible wedge, escalation %d", n), "busy turn-age escalation round")
			if n == 3 {
				pBWantNote(t, ws, "demand-deep-inspection", "the repeated busy escalation demands deep inspection")
			}
		}
	})

	t.Run(s+"busy_declared_pause_is_rechecked_not_wedge_escalated", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4596
		// cox: declared wait
		r := newPortRig(t)
		r.w.PauseResurface = 240 * time.Second
		r.busySet(busy.Busy)
		r.mail("m1", "status", "paused: hosting the Lavish review, awaiting captain feedback")
		r.tick() // the declaration itself (pre-seen in firstmate)
		r.w.BusyTurnMax = time.Second
		r.pCAgeBusy(24 * time.Hour)
		// Phase A: past the bound, the declared pause is absorbed and never starts a wedge.
		wantAbsorbed(t, r.tick(), "phase A: a declared pause on an over-age busy worker")
		// Phase B: past the long cadence it re-surfaces once as a recheck, never as a wedge.
		r.advance(500 * time.Second)
		ws := r.tick()
		wantSurfaced(t, ws, "phase B: a declared pause past its cadence is rechecked")
		pBWantNote(t, ws, "awaiting external", "the recheck is labeled an external wait")
		pBNoNote(t, ws, "possible wedge", "a declared pause is never a wedge")
		// Phase C: lifting the declaration on the same busy over-age worker restores the wedge escalation.
		r.mail("m2", "status", "working: review closed, resuming the sweep")
		pCWantNotSurfaced(t, r.tick(), "phase C priming: a lifted pause starts the wedge timer")
		r.advance(500 * time.Second)
		pBWantNote(t, r.tick(), "possible wedge", "phase C: a lifted pause on an over-age busy worker wedge-escalates")
	})

	// n/a test_afk_busy_declared_pause_hands_off_plain_stale (fm-watch-triage.test.sh:4703): the away-mode handoff to the
	// afk daemon (one-shot watcher, daemon-owned pause verdict); cox has no away daemon.

	// n/a test_afk_busy_declared_pause_ticking_pane_hands_off_once (fm-watch-triage.test.sh:4789): the same away-mode
	// daemon handoff, keyed against tmux pane-hash churn; afk daemon + pane hashing are firstmate-only surfaces.

	t.Run(s+"busy_pane_default_turn_age_bound_is_3600s", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4894
		// cox: busyTurnMaxPass
		if DefaultBusyTurnMax != 3600*time.Second {
			t.Errorf("DefaultBusyTurnMax = %v, want 3600s", DefaultBusyTurnMax)
		}
		r := newPortRig(t) // BusyTurnMax unset: the production default
		r.busySet(busy.Busy)
		r.pCAgeBusy(5 * time.Minute)
		wantAbsorbed(t, r.tick(), "a 5-minute-old turn is under the default bound")
		r.pCAgeBusy(66 * time.Minute)
		ws := r.tick()
		pCWantKindFor(t, ws, wake.KindStatus, "a 66-minute-old turn reaches the default bound")
		pCWantNotSurfaced(t, ws, "reaching the bound starts the timer, it does not surface before the wedge threshold")
	})
}

const (
	pDMechStale       = "stale escalation: a silent worker past the stale threshold escalates (stalePass only fires on a failed/unknown probe)"
	pDMechWedge       = "wedge detector: escalation schedule, deep inspection, write deferral"
	pDMechHeartbeat   = "heartbeat backstop: periodic re-surface of unsurfaced status"
	pDMechDeclWait    = "declared wait: paused / captain-held status verbs"
	pDMechWaitCadence = "wait cadence: paused-until / resurface throttle"
)

// pDHbPath is cox's stale timer for the story's dispatch: the heartbeat file whose mtime stalePass ages.
func pDHbPath(r *portRig) string {
	return filepath.Join(r.epic, state.ControlDir, "watch", "hb", "ctx_"+portStory)
}

// pDSeen delivers an already-seen status line: the arrival tick is consumed and its wakes discarded (firstmate seeds the
// .seen-* signature so only the later poll is under test).
func pDSeen(r *portRig, id, line string) {
	r.mail(id, "status", line)
	r.tick()
}

// pDStaleWorker puts the story into firstmate's "quiet pane past the escalation threshold" shape: a heartbeat that
// aged `age` past a StaleMin of `threshold`, a live worker, and an idle busy record.
func pDStaleWorker(r *portRig, threshold, age time.Duration) {
	r.w.StaleMin = threshold
	r.liveness(backend.Alive)
	r.busySet(busy.Idle)
	r.heartbeat("hb1")
	r.tick()
	r.advance(age)
}

// pDWedgeRig is a working lane (active run-step, live agent) with a recorded worktree, already surfaced once so the
// wedge timer runs (fm wedge fixture), at the firstmate wedge threshold.
func pDWedgeRig(t *testing.T) *portRig {
	t.Helper()
	r := newPortRig(t)
	r.w.StaleMin = 240 * time.Second
	r.liveness(backend.Alive)
	r.ci("running")
	wt := t.TempDir()
	portMust(t, os.MkdirAll(filepath.Dir(state.WorktreePath(r.epic, portStory)), 0o755))
	portMust(t, os.WriteFile(state.WorktreePath(r.epic, portStory), []byte(wt), 0o644))
	pDSeen(r, "m0", "working: implementing")
	r.heartbeat("hb0")
	r.tick()
	r.firstSight()
	return r
}

// pDWrite writes a file in the story worktree stamped just before the rig clock (a write inside the idle window).
func pDWrite(t *testing.T, r *portRig, rel string) {
	t.Helper()
	p := filepath.Join(state.ReadWorktree(r.epic, portStory), rel)
	portMust(t, os.MkdirAll(filepath.Dir(p), 0o755))
	portMust(t, os.WriteFile(p, []byte("work\n"), 0o644))
	at := r.clock.Add(-time.Second)
	portMust(t, os.Chtimes(p, at, at))
}

func pDUntil(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func TestPortTriageD(t *testing.T) {
	const s = "FM/fm-watch-triage/"

	t.Run(s+"nonterminal_stale_repairs_missing_or_corrupt_timer", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4936
		// cox: pDMechStale
		// Missing timer: a working worker (quiet, idle, status already seen) whose stale clock was never started. Firstmate
		// initializes stale-since without waking; cox's clock is watch/hb/<dispatch>, which only a heartbeat creates.
		r := newPortRig(t)
		r.w.StaleMin = 999 * time.Second
		r.busySet(busy.Idle)
		pDSeen(r, "m1", "working: still compiling")
		r.advance(time.Second)
		wantAbsorbed(t, r.tick(), "missing stale timer repair must not wake")
		if _, err := os.Stat(pDHbPath(r)); err != nil {
			t.Errorf("missing stale timer was not initialized for a working worker: %v", err)
		}
		// Corrupt timer: cox's timer is an mtime, so the corrupt analog is a timer in the future (it would never age).
		// Firstmate replaces a corrupt stale-since with now, without waking.
		p := pDHbPath(r)
		portMust(t, os.MkdirAll(filepath.Dir(p), 0o755))
		portMust(t, os.WriteFile(p, []byte("corrupt\n"), 0o644))
		future := r.clock.Add(24 * time.Hour)
		portMust(t, os.Chtimes(p, future, future))
		r.advance(time.Second)
		wantAbsorbed(t, r.tick(), "corrupt stale timer repair must not wake")
		if info, err := os.Stat(p); err != nil || info.ModTime().After(r.clock) {
			t.Errorf("corrupt (future) stale timer was left in place: %v", err)
		}
	})

	t.Run(s+"wedge_escalation_deferred_while_worktree_is_written", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:4991
		// cox: wedgeTimerCheck -> wedgeDeferWriting (worktree writes since the idle window opened defer one escalation)
		r := pDWedgeRig(t)
		r.pCSilentPast()
		pDWrite(t, r, "src/a.go") // written inside the idle window
		ws := r.tick()
		wantAbsorbed(t, ws, "phase A: a worktree written since the idle window opened defers the escalation")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "writing-since", portStory)); err != nil {
			t.Errorf("the write deferral did not start its chain marker")
		}
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "esc", portStory)); err == nil {
			t.Errorf("a write deferral advanced the escalation count")
		}
		r.pCSilentPast() // phase B: nothing written this window
		pBWantNote(t, r.tick(), "possible wedge, escalation 1", "a silent worker past the stale threshold escalates as a possible wedge")
	})

	t.Run(s+"write_deferral_resurfaces_on_the_bounded_cadence", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5059
		// cox: wedgeDeferWriting -> resurfaceAbsorbed (the deferral chain re-surfaces once per PauseResurface, aged from
		// the chain's own start, so churn without progress cannot stay invisible)
		r := pDWedgeRig(t)
		r.w.PauseResurface = 600 * time.Second
		round := func(name string) []wake.Wake {
			r.pCSilentPast()
			pDWrite(t, r, "src/"+name)
			return r.tick()
		}
		wantAbsorbed(t, round("a.go"), "the first deferral opens the chain")
		wantAbsorbed(t, round("b.go"), "a chain younger than the cadence is absorbed")
		ws := round("c.go")
		pBWantNote(t, ws, "writing its worktree for", "a chain older than the cadence re-surfaces for a recheck")
		pBNoNote(t, ws, "possible wedge", "a write deferral is not a wedge")
		if a := pBAlarms(round("d.go")); a != 0 {
			t.Errorf("the write-deferral recheck repeated inside its cadence: %d alarms", a)
		}
	})

	t.Run(s+"secondmate_home_supervision_churn_is_not_write_evidence", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5104
		// cox: busyTurnBoundCheck (the secondmate home exclusion is firstmate-only; the kind-agnostic half: a worker busy
		// past the turn bound with no progress escalates on the wedge schedule)
		r := newPortRig(t)
		r.w.StaleMin = 240 * time.Second
		r.w.BusyTurnMax = time.Second // FM_BUSY_TURN_MAX_SECS=1
		r.liveness(backend.Alive)
		r.busySet(busy.Busy)
		pDSeen(r, "m1", "working: implementing")
		r.advance(500 * time.Second)
		pBWantNote(t, r.tick(), "possible wedge", "busy past the turn bound with no progress escalates a possible wedge")
	})

	t.Run(s+"timer_repair_drops_a_finished_write_deferral_chain", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5154
		// cox: wedgeTimerCheck (a corrupt idle timer is repaired without a wake, and the old write-deferral chain goes
		// with it, so the next deferral measures its cadence from the current quiet stretch)
		r := pDWedgeRig(t)
		dir := r.w.watchDir()
		for _, f := range []string{"writing-since", "writing-resurfaced"} {
			portMust(t, os.MkdirAll(filepath.Join(dir, f), 0o755))
			portMust(t, os.WriteFile(filepath.Join(dir, f, portStory), []byte("1"), 0o644))
		}
		portMust(t, os.WriteFile(filepath.Join(dir, "since", portStory), []byte("corrupt\n"), 0o644))
		r.advance(time.Second)
		wantAbsorbed(t, r.tick(), "idle-window timer repair must not enqueue a wake")
		if b, _ := os.ReadFile(filepath.Join(dir, "since", portStory)); strings.TrimSpace(string(b)) == "corrupt" {
			t.Errorf("the corrupt idle timer was left in place")
		}
		for _, f := range []string{"writing-since", "writing-resurfaced"} {
			if _, err := os.Stat(filepath.Join(dir, f, portStory)); err == nil {
				t.Errorf("the timer repair kept the finished write-deferral chain (%s)", f)
			}
		}
	})

	t.Run(s+"terminal_first_sight_drops_a_finished_write_deferral_chain", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5223
		// cox: staleStory terminal branch (both first-sight paths drop an old write-deferral chain)
		for _, phase := range []string{"A: an active run outranks the stale done: line", "B: nothing overrides it"} {
			r := newPortRig(t)
			r.busySet(busy.Idle)
			if strings.HasPrefix(phase, "A") {
				r.ci("running")
			}
			r.mail("m1", "status", "done: implementation complete, ready to validate")
			ws := r.tick()
			if strings.HasPrefix(phase, "B") {
				wantSurfaced(t, ws, "first sight of a done: status with no active run surfaces")
			}
			dir := r.w.watchDir()
			for _, f := range []string{"writing-since", "writing-resurfaced"} {
				portMust(t, os.MkdirAll(filepath.Join(dir, f), 0o755))
				portMust(t, os.WriteFile(filepath.Join(dir, f, portStory), []byte("1"), 0o644))
			}
			r.firstSight()
			for _, f := range []string{"writing-since", "writing-resurfaced"} {
				if _, err := os.Stat(filepath.Join(dir, f, portStory)); err == nil {
					t.Errorf("phase %s: the terminal first sight kept the finished write-deferral chain (%s)", phase, f)
				}
			}
		}
	})

	// n/a test_triage_log_size_cap_accepts_spaced_wc_counts (fm-watch-triage.test.sh:5283): bash-only concern - a fake
	// `wc -c` printing a space-padded byte count for firstmate's triage debug log cap; cox has no triage log and no wc.

	// n/a test_procevent_captured_result_surfaces_proactively (fm-watch-triage.test.sh:5383): procevent (fm process-event
	// runner publishing `check` rows into its own queue); cox has no process-event sources.

	// n/a test_procevent_unacknowledged_result_redrains_until_handled (fm-watch-triage.test.sh:5409): procevent re-arm
	// recovery and the WAKE_ACK_REQUIRED replay boundary of fm-wake-drain; cox's queue is drained/acked by the leader,
	// not re-surfaced by the watcher.

	// n/a test_procevent_marker_keys_are_injective (fm-watch-triage.test.sh:5456): procevent .seen-procevent-* marker
	// file naming; no cox analog.

	// n/a test_procevent_headlines_classify_queue_keys (fm-watch-triage.test.sh:5485): procevent headline wording per
	// queue-key glob (captured / stranded / failed to start); no cox analog.

	// n/a test_procevent_launch_failed_episodes_are_each_delivered (fm-watch-triage.test.sh:5522): procevent
	// launch-failure episode keys; no cox analog.

	// n/a test_procevent_surface_serializes_with_drain (fm-watch-triage.test.sh:5599): procevent marker commit vs a
	// concurrent fm drain, driven by a fake `mv`; no cox analog.

	// n/a test_procevent_surface_crash_boundaries (fm-watch-triage.test.sh:5621): procevent output/marker crash
	// boundaries (fifo, killed mv); no cox analog.

	// n/a test_procevent_marker_failure_exits_and_replays (fm-watch-triage.test.sh:5686): procevent marker failure and
	// the fm queue lock; no cox analog.

	t.Run(s+"heartbeat_no_change_absorbed", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5711
		// cox: heartbeatPass (a due scan with nothing unsurfaced is absorbed and backs the cadence off)
		r := newPortRig(t)
		pDSeen(r, "m1", "working: routine heartbeat history")
		r.advance(time.Second)
		r.heartbeat("h1")
		wantAbsorbed(t, r.tick(), "a heartbeat with no captain-relevant change is absorbed")
		if b, _ := os.ReadFile(filepath.Join(r.w.watchDir(), "heartbeat", "streak")); strings.TrimSpace(string(b)) != "1" {
			t.Errorf("the no-change heartbeat did not back the cadence off (streak %q)", b)
		}
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "heartbeat", "last")); err != nil {
			t.Errorf("the heartbeat scan left no schedule record: %v", err)
		}
		r.advance(time.Second)
		r.tick()
		if b, _ := os.ReadFile(filepath.Join(r.w.watchDir(), "heartbeat", "streak")); strings.TrimSpace(string(b)) != "1" {
			t.Errorf("a heartbeat scan ran again inside its backed-off interval (streak %q)", b)
		}
	})

	t.Run(s+"heartbeat_backstop_surfaces_a_masked_status", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5743
		// cox: heartbeatPass (a decision the per-wake path absorbed - a busy crew, the decision line masked by a later
		// routine append - is caught by the heartbeat backstop)
		r := newPortRig(t)
		r.busySet(busy.Busy)
		r.mail("m1", "status", "working: setup")
		r.mail("m2", "status", "needs-decision: pick A or B")
		r.mail("m3", "status", "working: tidying the branch")
		ws := r.tick()
		wantSurfaced(t, ws, "a needs-decision line followed by a routine append still surfaces")
		pBWantNote(t, ws, "needs-decision: pick A or B", "the backstop names the masked decision")
		r.advance(DefaultHeartbeat + time.Second) // the next scan is due; the busy turn is still under its bound
		r.heartbeat("h2")
		wantAbsorbed(t, r.tick(), "the next heartbeat does not re-fire a surfaced event")
	})

	t.Run(s+"heartbeat_backstop_surfaces_unsurfaced_status", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5764
		// cox: heartbeatPass (a captain-relevant status marked seen but never surfaced)
		r := newPortRig(t)
		r.w.markSeen("m1")
		r.mail("m1", "status", "done: PR https://example.test/pr/5")
		r.heartbeat("h1")
		ws := r.tick()
		wantSurfaced(t, ws, "heartbeat backstop surfaces a seen-but-unsurfaced done: status")
		pBWantNote(t, ws, "done: PR https://example.test/pr/5", "the backstop names the status")
		if !r.w.loadSurfaced()["m1"] {
			t.Errorf("the backstop did not record the status as surfaced")
		}
	})

	t.Run(s+"beacon_stays_fresh_while_absorbing", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5789
		// cox: markTick beacon (<epic>/.cox/watch/lasttick) + mailPass absorb
		// Provably working (busy record busy) so the working: notes must be absorbed; each pass is Run's body: Tick then
		// markTick.
		r := newPortRig(t)
		r.busySet(busy.Busy)
		beacon := filepath.Join(r.epic, state.ControlDir, "watch", "lasttick")
		readBeacon := func() time.Time {
			b, err := os.ReadFile(beacon)
			if err != nil {
				t.Errorf("watcher beacon missing while absorbing: %v", err)
				return time.Time{}
			}
			ts, err := time.Parse(time.RFC3339, string(b))
			if err != nil {
				t.Errorf("watcher beacon unparsable: %q", b)
			}
			return ts
		}
		r.mail("m1", "status", "working: a")
		wantAbsorbed(t, r.tick(), "absorbing a benign working: note enqueues nothing")
		r.w.markTick()
		m1 := readBeacon()
		r.advance(5 * time.Second)
		r.mail("m2", "status", "working: b")
		wantAbsorbed(t, r.tick(), "absorbing a second benign working: note enqueues nothing")
		r.w.markTick()
		m2 := readBeacon()
		if m2.Before(m1) {
			t.Errorf("beacon regressed while absorbing: %v -> %v", m1, m2)
		}
		if age := r.clock.Sub(m2); age >= 10*time.Second {
			t.Errorf("beacon went stale while absorbing (age %v)", age)
		}
	})

	// n/a test_afk_signal_records_heartbeat_endpoint (fm-watch-triage.test.sh:5822): afk/away daemon handoff (.afk makes
	// the watcher one-shot for the supervise daemon); cox has no away daemon.

	// n/a test_afk_present_reverts_watcher_to_one_shot (fm-watch-triage.test.sh:5839): afk/away daemon owning triage; no
	// cox analog.

	// n/a test_afk_paused_changed_pane_hands_off_plain_stale (fm-watch-triage.test.sh:5863): afk daemon handoff of a
	// changed paused pane's plain stale identity; afk-only, and pane hashing.

	t.Run(s+"captain_held_never_rechecked_while_away_record_exists", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:5920
		// cox: pDMechDeclWait
		// Phase A (silence under the away-posture record) is afk-only. Phase B is a cox-side requirement: once no away
		// record exists, a captain-held item past the recheck cadence (240s) is rechecked, naming the captain.
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Settled) // fm hold fixture: the agent exited (pane zsh)
		r.w.PauseResurface = 240 * time.Second
		pDSeen(r, "m1", "captain-held [key=route]: tracked by task-decision-route")
		r.advance(500 * time.Second)
		ws := r.tick()
		wantSurfaced(t, ws, "a captain-held item past the recheck cadence is rechecked")
		pBWantNote(t, ws, "awaiting the captain", "the recheck names the captain")
	})

	// n/a test_live_captain_held_first_sight_silenced_by_away_record (fm-watch-triage.test.sh:5967): the only behavior is
	// the away-posture record (afk contract) silencing a live captain-held first sight; cox has no away record.

	// n/a test_backlog_hold_never_rechecked_while_away_record_exists (fm-watch-triage.test.sh:5996): away-posture record
	// over a tasks-axi backlog hold with pane-hash churn; afk-only and tasks-axi-only.

	// n/a test_afk_one_shot_never_hands_off_captain_held_under_away_record (fm-watch-triage.test.sh:6014): afk daemon
	// one-shot handoff under the away record; afk-only.

	t.Run(s+"paused_until_near_future_is_quiet_before_the_cadence", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:6073
		// cox: pDMechWaitCadence
		// A declared wait whose until time (120s ahead) falls inside the 240s cadence stays quiet until then.
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Alive)
		r.w.PauseResurface = 240 * time.Second
		start := r.clock
		pDSeen(r, "m1", "paused: rate limit resets, until "+pDUntil(start.Add(180*time.Second))+", then resuming")
		r.advance(60 * time.Second)
		wantAbsorbed(t, r.tick(), "near-future declared wait is quiet before its time (poll 1)")
		r.advance(time.Second)
		wantAbsorbed(t, r.tick(), "near-future declared wait is quiet before its time (poll 2)")
		if _, err := os.Stat(filepath.Join(r.w.watchDir(), "paused", portStory)); err != nil {
			t.Errorf("the declared wait was not taken onto the pause cadence (the quiet is vacuous)")
		}
	})

	t.Run(s+"paused_until_wrong_year_is_bounded_by_the_cadence", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:6087
		// cox: pDMechWaitCadence
		// A declared time a year out cannot silence the wait past the 240s cadence: a status 300s old is rechecked.
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Alive)
		r.w.PauseResurface = 240 * time.Second
		pDSeen(r, "m1", "paused: rate limit resets, until "+pDUntil(r.clock.Add(365*24*time.Hour))+", then resuming")
		r.advance(300 * time.Second)
		r.tick() // first sight of the live wait: absorbed before its declared time
		ws := pBRound(r, pBPoll)
		wantSurfaced(t, ws, "a wrong-year declared time is bounded by the recheck cadence")
		pBWantNote(t, ws, "beyond the recheck cadence", "the recheck says the declared time is past the cadence")
	})

	t.Run(s+"paused_until_that_passed_is_rechecked_before_the_cadence", func(t *testing.T) {
		// fm: tests/fm-watch-triage.test.sh:6102
		// cox: pDMechWaitCadence
		// The until time passed 30s ago (cadence 999s): rechecked at once, then once per declaration.
		r := newPortRig(t)
		r.busySet(busy.Idle)
		r.liveness(backend.Alive)
		r.w.PauseResurface = 999 * time.Second
		pDSeen(r, "m1", "paused: rate limit resets, until "+pDUntil(r.clock.Add(30*time.Second))+", then resuming")
		r.advance(60 * time.Second)
		wantSurfaced(t, r.tick(), "a declared wait whose until time passed is rechecked ahead of the cadence")
		r.advance(time.Second)
		wantAbsorbed(t, r.tick(), "the due recheck fires once per declaration")
	})
}

// Mechanism names for the report's red list (strings shared with the translated triage parts).
const (
	pPMechFold      = "decision fold: open/close decisions by [key=] across a worker's status history"
	pPMechRecovery  = "recovery triage: a finished (landed) story is not a stale/recovery case"
	pPMechStaleNote = "stale escalation names the unread steer (the durable instruction the worker never acknowledged)"
	pPMechReplyRun  = "runaway ladder: a reply already consumed through the question channel is not an unread steer (B-53)"
	fmProto         = "docs/supervision-protocols/"
	fmRecovery      = ".agents/skills/stuck-crewmate-recovery/SKILL.md"
)

// pPDrainDurable pins firstmate's drain contract: a queued wake is re-presented by every drain until the exact
// ack-through, and is gone after it (durable for idempotent re-handling after an interruption).
func pPDrainDurable(t *testing.T) {
	t.Helper()
	r := newPortRig(t)
	r.mail("m1", "question", "which base branch?")
	ws := r.tick()
	if len(ws) != 1 {
		t.Fatalf("want one queued wake, got %v", kinds(ws))
	}
	for i := 0; i < 2; i++ { // an interrupted leader drains again: the wake is still there
		again, err := wake.Drain(r.epic, true)
		portMust(t, err)
		if len(again) != 1 || again[0].Gen != ws[0].Gen {
			t.Errorf("drain %d lost the unacked wake: %v", i, kinds(again))
		}
	}
	portMust(t, wake.AckThrough(r.epic, ws[0].Gen))
	if left, _ := wake.Drain(r.epic, true); len(left) != 0 {
		t.Errorf("wake survived its ack-through: %v", kinds(left))
	}
}

// Protocol docs: each numbered rule with a testable consequence in the watcher/wake queue. Rules whose consequence is
// the Stop hook, the arm layer, the turn-end guard or the foreground checkpoint are n/a here, owned by
// cox-supervision-port-turnend (cmd/cox/hook.go, fm-watch-checkpoint, watcher-continuity.md).
func TestPortProtocols(t *testing.T) {
	// --- claude.md ---
	t.Run("FM/protocol-claude/rule-1", func(t *testing.T) {
		// fm: docs/supervision-protocols/claude.md:4
		// cox: wake.Drain / AckThrough + decision fold (drain's OPEN DECISIONS / UNREAD STATUS)
		pPDrainDurable(t)
		notImplemented(t, pPMechFold) // the drain also lists open decisions and unread status lines to reconcile
	})
	// n/a claude.md:6 rule 2 (Stop asyncRewake owns arm/re-arm): owned by cox-supervision-port-turnend
	// n/a claude.md:9 rule 3 (drain first on a Stop hook feedback wake; no manual re-arm): owned by cox-supervision-port-turnend
	// n/a claude.md:12 rule 4 (auto-arm FAILED notice handling): owned by cox-supervision-port-turnend
	// n/a claude.md:13 rule 5 (Stop hook does not claim the home): owned by cox-supervision-port-turnend
	// n/a claude.md:15 rule 6 (watcher started/attached is proof of one live cycle): owned by cox-supervision-port-turnend
	t.Run("FM/protocol-claude/rule-7", func(t *testing.T) {
		// fm: docs/supervision-protocols/claude.md:17
		// cox: wake queue durability across a watcher restart (the session-lock/guard half is owned by cox-supervision-port-turnend)
		r := newPortRig(t)
		r.mail("m1", "question", "which base branch?")
		if ws := r.tick(); len(ws) != 1 {
			t.Fatalf("want one wake, got %v", kinds(ws))
		}
		// A fresh watcher (the next arm) over the same epic: the actionable event is still queued and not duplicated.
		r.w = &Watcher{EpicDir: r.epic, Backend: r.b, Now: r.w.Now}
		r.mb.Queue = r.mb.Queue[:0]
		r.mail("m1", "question", "which base branch?") // the mailbox re-presents the unacked delivery
		if ws := r.tick(); len(ws) != 0 {
			t.Errorf("a restarted watcher re-queued an already-queued event: %v", kinds(ws))
		}
		if all, _ := wake.Drain(r.epic, true); len(all) != 1 {
			t.Errorf("the actionable event did not survive the re-arm: %v", kinds(all))
		}
	})
	// n/a claude.md:20 rule 8 (turn-end guard backstop): owned by cox-supervision-port-turnend
	t.Run("FM/protocol-claude/rule-9", func(t *testing.T) {
		// fm: docs/supervision-protocols/claude.md:23
		// cox: mailPass heartbeat sentinel (never queued)
		r := newPortRig(t)
		r.heartbeat("h1")
		wantAbsorbed(t, r.tick(), "waiting on a parked cycle is silent: a heartbeat is not a wake")
	})

	// --- codex.md ---
	t.Run("FM/protocol-codex/rule-1", func(t *testing.T) {
		// fm: docs/supervision-protocols/codex.md:4
		// cox: wake.Drain / AckThrough + decision fold
		pPDrainDurable(t)
		notImplemented(t, pPMechFold)
	})
	// n/a codex.md:6 rule 2 (source the Relay env): relay/X is firstmate-only (DESIGN rule 5)
	// n/a codex.md:7 rule 3 (first foreground watcher checkpoint): owned by cox-supervision-port-turnend (fm-watch-checkpoint)
	// n/a codex.md:8 rule 4 (ordinary wake inside the checkpoint): owned by cox-supervision-port-turnend (fm-watch-checkpoint)
	// n/a codex.md:9 rule 5 (checkpoint timeout, drain anyway): owned by cox-supervision-port-turnend (fm-watch-checkpoint)
	// n/a codex.md:10 rule 6 (never shell & for supervision): a model-conduct rule; its seatbelt is the arm layer, owned by cox-supervision-port-turnend
	// n/a codex.md:11 rule 7 (no fm-watch-arm as normal command; PreToolUse seatbelt): owned by cox-supervision-port-turnend
	// n/a codex.md:13 rule 8 (failure or missing cycle: fresh checkpoint): owned by cox-supervision-port-turnend

	// --- pi.md ---
	t.Run("FM/protocol-pi/rule-1", func(t *testing.T) {
		// fm: docs/supervision-protocols/pi.md:4
		// cox: wake.Drain / AckThrough + decision fold
		pPDrainDurable(t)
		notImplemented(t, pPMechFold)
	})
	// n/a pi.md:6 rule 2 (both project extensions auto-loaded at session start): owned by cox-supervision-port-session (session-start)
	// n/a pi.md:7 rule 3 (one initial fm_watch_arm_pi call): owned by cox-supervision-port-turnend
	// n/a pi.md:10 rule 4 (reclaim the session lock, re-arm): owned by cox-supervision-port-turnend
	// n/a pi.md:11 rule 5 (extension owns successor launches): owned by cox-supervision-port-turnend
	// n/a pi.md:12 rule 6 (same-process session replacement retires the prior generation): owned by cox-supervision-port-turnend
	// n/a pi.md:14 rule 7 (successor verified before the follow-up wake): owned by cox-supervision-port-turnend
	// n/a pi.md:15 rule 8 (never re-arm on ordinary wakes): owned by cox-supervision-port-turnend
	// n/a pi.md:16 rule 9 (unexpected child close: bounded retry, failure surfaced): owned by cox-supervision-port-turnend (watcher-continuity)
	// n/a pi.md:17 rule 10 (missing/failed cycle repair): owned by cox-supervision-port-turnend
	// n/a pi.md:19 rule 11 (never shell &; seatbelt in the turn-end guard extension): owned by cox-supervision-port-turnend
	// n/a pi.md:22-36 (the in-process Pi supervision branch, leases, branch outcomes): firstmate-only supervision branch, no cox analog
	// n/a pi.md:38-40 (extension file locations; session-start reports unloaded extensions): owned by cox-supervision-port-session

	// --- unknown.md (unnumbered: one case per directive line) ---
	// n/a unknown.md:3-4 (no verified adapter; follow the generic contract): descriptive, no testable consequence
	// n/a unknown.md:5 (first cycle: drain, then a wait the harness can wake from): the pull wait is owned by cox-supervision-port-turnend (fm-watch-checkpoint)
	t.Run("FM/protocol-unknown/L6", func(t *testing.T) {
		// fm: docs/supervision-protocols/unknown.md:6
		// cox: wake.Drain / AckThrough + decision fold
		pPDrainDurable(t)
		notImplemented(t, pPMechFold)
	})
	t.Run("FM/protocol-unknown/L7", func(t *testing.T) {
		// fm: docs/supervision-protocols/unknown.md:7
		// cox: wake.Drain / AckThrough
		pPDrainDurable(t)
	})
	// n/a unknown.md:8 (arm only with a tracked background mechanism): owned by cox-supervision-port-turnend
	// n/a unknown.md:9 (bounded foreground wait when unverified): owned by cox-supervision-port-turnend
	// n/a unknown.md:10 (never shell &): model-conduct rule, owned by cox-supervision-port-turnend
	// n/a unknown.md:11 (failure: restore the same wait shape): owned by cox-supervision-port-turnend
	// n/a unknown.md:13 (record verification evidence before promoting a harness): a documentation process, no runtime consequence
}

// The stuck-crewmate recovery ladder: each rung or rule that names an observable the watcher owns.
func TestPortStuckCrewmateRecovery(t *testing.T) {
	const s = "FM/stuck-crewmate-recovery/"

	t.Run(s+"landed-work-is-not-a-recovery-case", func(t *testing.T) {
		// fm: .agents/skills/stuck-crewmate-recovery/SKILL.md:16
		// cox: stalePass (heartbeat of a story that is no longer working)
		r := newPortRig(t)
		r.heartbeat("h1")
		r.tick()
		portMust(t, state.Append(r.epic, state.Event{Epic: filepath.Base(r.epic), Story: portStory, Attempt: 1,
			Actor: state.Leader, From: state.Working, To: state.Completed, ExternalConfirmed: true}))
		r.liveness(backend.Unknown) // the finished worker's endpoint is gone
		r.advance(2 * DefaultStaleMin)
		ws := r.tick()
		for _, w := range ws {
			if w.Story == portStory {
				t.Errorf("a completed story raised a recovery wake %s: %s", w.Kind, w.Note)
			}
		}
		if len(ws) != 0 {
			notImplemented(t, pPMechRecovery)
		}
	})
	// n/a SKILL.md:18 (crew-hosted lavish board): lavish is firstmate-only (DESIGN rule 5)
	// n/a SKILL.md:20 (interrupt/exit/relaunch through the control plane, verified): owned by cox-supervision-port-busy-wake (fm-control*)
	// n/a SKILL.md:21,30-31 (remote secondmates): secondmates are firstmate-only (DESIGN rule 5)
	// n/a SKILL.md:22-23 (load harness-adapters; harness recorded in meta): agent reading instruction, no runtime consequence
	// n/a SKILL.md:27-28 (ordinary kinds vs secondmate): secondmates are firstmate-only
	t.Run(s+"endpoint-result-is-presence-not-proof", func(t *testing.T) {
		// fm: .agents/skills/stuck-crewmate-recovery/SKILL.md:33
		// cox: stalePass F08 (a failed probe raises unknown_probe and never concludes gone)
		r := newPortRig(t)
		r.heartbeat("h1")
		r.tick()
		r.advance(DefaultStaleMin + time.Minute)
		r.b.FailNext("Probe", nil)
		ws := r.tick()
		found := false
		for _, w := range ws {
			if w.Kind == wake.KindUnknownProbe {
				found = true
			}
		}
		if !found {
			t.Errorf("a failed probe on a stale worker did not raise unknown_probe: %v", kinds(ws))
		}
		if _, err := os.Stat(filepath.Join(r.epic, state.ControlDir, "watch", "hb", "ctx_"+portStory)); err != nil {
			t.Errorf("a failed probe dropped the heartbeat (concluded gone): %v", err)
		}
	})
	// n/a SKILL.md:34-35 (read fm-crew-state; an active no-mistakes run stays authoritative): the no-mistakes pipeline is firstmate-only
	// n/a SKILL.md:37-39 (inspect only the recorded backend/worktree inventory): treehouse/herdr/tmux inventory is firstmate-only
	// n/a SKILL.md:41-48 (relaunch preconditions: no live owner, same worktree, same identity): owned by cox-supervision-port-busy-wake (fm-control relaunch)
	// n/a SKILL.md:49 (unreconcilable: leave state intact, report failed/blocked): a leader judgment, no watcher observable
	// n/a SKILL.md:51-68 (a live crewmate claiming the pipeline dead): the no-mistakes daemon is firstmate-only (DESIGN rule 5)

	t.Run(s+"live-endpoint-rung-1-unread-steer", func(t *testing.T) {
		// fm: .agents/skills/stuck-crewmate-recovery/SKILL.md:74
		// cox: inboxLadder stuck note + stale escalation naming the unread steer
		r := newPortRig(t)
		r.heartbeat("h1")
		r.tick()
		r.steer("rebase onto the epic tip")
		var stuck []wake.Wake
		for i := 0; i < 6; i++ { // grace + ringMax rings, each past the grace
			r.advance(DefaultInboxGrace + time.Second)
			for _, w := range r.tick() {
				if w.Kind == wake.KindStuck {
					stuck = append(stuck, w)
				}
			}
		}
		if len(stuck) != 1 || !strings.Contains(stuck[0].Note, "001.msg") {
			t.Errorf("an unacknowledged steer did not escalate once naming its record: %v", stuck)
		}
		// The stale wake itself must name the unread instruction.
		r.liveness(backend.Unknown)
		r.advance(DefaultStaleMin)
		named := false
		for _, w := range r.tick() {
			if (w.Kind == wake.KindStale || w.Kind == wake.KindUnknownProbe) && strings.Contains(w.Note, "001.msg") {
				named = true
			}
		}
		if !named {
			notImplemented(t, pPMechStaleNote)
		}
	})
	t.Run(s+"live-endpoint-rung-2-question-the-brief-answers", func(t *testing.T) {
		// fm: .agents/skills/stuck-crewmate-recovery/SKILL.md:75
		// cox: mailPass question -> urgent wake (answered with cox reply)
		r := newPortRig(t)
		r.mail("q1", "question", "which base branch should I cut from?")
		ws := r.tick()
		wantSurfaced(t, ws, "a worker question reaches the leader")
		if len(ws) != 1 || ws[0].Kind != wake.KindQuestion {
			t.Errorf("want one question wake, got %v", kinds(ws))
		}
	})
	t.Run(s+"live-endpoint-rung-3-interrupt-then-redirect", func(t *testing.T) {
		// fm: .agents/skills/stuck-crewmate-recovery/SKILL.md:76
		// cox: inboxLadder runaway (interrupt once per window, then the doorbell)
		r := newPortRig(t)
		r.liveness(backend.Alive)
		r.steer("stop looping on the flaky test; skip it and continue")
		r.advance(DefaultRunawayMin + time.Minute)
		ws := r.tick()
		ints := 0
		for _, c := range r.b.Calls {
			if c == "Interrupt" {
				ints++
			}
		}
		if ints != 1 {
			t.Errorf("want exactly one interrupt of the looping worker, got %d", ints)
		}
		runaway := false
		for _, w := range ws {
			runaway = runaway || w.Kind == wake.KindRunaway
		}
		if !runaway {
			t.Errorf("the interrupt raised no runaway wake: %v", kinds(ws))
		}
		r.advance(time.Minute)
		r.tick()
		n := 0
		for _, c := range r.b.Calls {
			if c == "Interrupt" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("a second interrupt inside the window: %d", n)
		}

		// B-53: an answer the worker already consumed through the question channel is not a looping signal; the
		// ladder must not interrupt a working crewmate over it.
		r2 := newPortRig(t)
		r2.liveness(backend.Alive)
		r2.busySet(busy.Busy)
		r2.reply("answer to q001: use epic/x")
		r2.advance(DefaultRunawayMin + time.Minute)
		r2.tick()
		for _, c := range r2.b.Calls {
			if c == "Interrupt" {
				t.Errorf("a consumed reply record interrupted a working worker (B-53)")
				notImplemented(t, pPMechReplyRun)
				break
			}
		}
	})
	// n/a SKILL.md:77-81 rung 4 (relaunch a wedged crewmate with a progress note): owned by cox-supervision-port-busy-wake (fm-control relaunch)
	// n/a SKILL.md:82 rung 5 (second relaunch fails: write failed, tell the captain): a leader action with no watcher observable
}
