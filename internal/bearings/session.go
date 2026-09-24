package bearings

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Stages is the digest's ordered stage list; the STARTUP TRUNCATED banner names every stage at and after the one that
// did not finish (fm-session-start.sh SESSION_START_STAGES, mapped to cox names).
var Stages = []string{"lease", "doctor", "wake-queue", "supervision-instructions", "read-once", "fleet-state", "forge-checks", "notes", "next-step"}

// session is one running digest.
type session struct {
	o        Opts
	p        *printer
	epics    []string
	readOnly bool
	stage    func(string)
}

func withDefaults(o Opts) Opts {
	if o.StatusTail <= 0 {
		o.StatusTail = DefaultStatusTail
	}
	if o.QueuedLimit <= 0 {
		o.QueuedLimit = DefaultQueuedLimit
	}
	if abs, err := filepath.Abs(o.Workspace); err == nil {
		o.Workspace = abs
	}
	return o
}

// Compose prints the one ordered session-start digest. It is a reporting command, never a gate: a refused lease is a
// loud read-only banner inline, and the error is non-nil only when the workspace itself cannot be read.
func Compose(o Opts) (Digest, error) {
	o = withDefaults(o)
	s := &session{o: o, p: &printer{}, epics: ActiveEpics(o.Workspace), stage: func(string) {}}
	s.run()
	text := s.p.seal()
	return Digest{Text: withEstimate(text), ReadOnly: s.readOnly}, nil
}

// withEstimate appends the digest's own token estimate as its last line, so the startup ceiling is checked on every
// start rather than assumed.
func withEstimate(text string) string {
	line := "\ndigest estimate: %d tokens (ceil(UTF-8 bytes / 3), this line included)\n"
	n := Estimate(len(text))
	for i := 0; i < 3; i++ { // a fixed point: the line's own digits count
		n = Estimate(len(text) + len(fmt.Sprintf(line, n)))
	}
	return text + fmt.Sprintf(line, n)
}

func (s *session) run() {
	o, p := s.o, s.p
	if o.Reemit {
		p.section("SESSION START (CONTEXT RE-EMIT) - " + o.Workspace)
		p.lines("This session already took the helm at its own startup and has only lost its",
			"context. Lease ownership is re-verified and the durable records below are",
			"reprinted, but the sweeps startup already ran - the deferred forge checks and the",
			"inactive-story state reads - are NOT repeated.",
			"Queued wakes ARE still shown: they arrived after startup and are this turn's work.")
	} else {
		p.section("SESSION START - " + o.Workspace)
	}

	// 1. lease: acquired first, before any mutating step.
	s.stage("lease")
	p.sub("LEADER LEASE")
	lr := acquire(o.Workspace, o.LeaderID, o.Live)
	p.line(lr.line)
	if !lr.ok {
		s.readOnly = true
		cause := "LEADER LEASE OWNERSHIP WAS NOT VERIFIED"
		p.lines(bar,
			"●  READ-ONLY SESSION - "+cause,
			"●  "+lr.line,
			"●  Skipping every mutating step: the deferred forge checks, the AGENTS.md",
			"●  baseline, and wake-queue presentation. Detect-only doctor diagnostics and the",
			"●  rest of this read-only-safe digest still ran below.",
			"●  Operate read-only until this resolves - do not dispatch, steer, merge, or",
			"●  otherwise mutate fleet state from this session.",
			bar)
	}

	// 2. doctor: detect-only diagnostics always run.
	s.stage("doctor")
	p.sub("DOCTOR")
	doc, err := DoctorSummary(o.Workspace)
	if err != nil {
		doc = "doctor failed: " + err.Error()
	}
	p.line(doc)

	// 3. wake queue: presented only with verified lease ownership; never acked here.
	s.stage("wake-queue")
	p.sub("WAKE QUEUE")
	if s.readOnly {
		n := 0
		for _, ep := range s.epics {
			ws, _ := WakeDrain(ep)
			n += len(ws)
		}
		p.line(fmt.Sprintf("skipped (read-only session) - %d record(s) left untouched because this session lacks verified leader-lease ownership.", n))
	} else {
		s.printWakeQueue()
	}

	// 4. supervision operating instructions.
	s.stage("supervision-instructions")
	printSupervision(p, o.Workspace, o.Harness, s.readOnly)

	// 5. read-once contract, ahead of the digests it governs.
	s.stage("read-once")
	p.section("READ-ONCE CONTRACT")
	p.line(readOnceContract)

	// 6. fleet state: backlog, story inventory, orphan status, the four bearings sections.
	s.stage("fleet-state")
	p.section("FLEET STATE")
	printBacklogCompact(p, o.Workspace, o.QueuedLimit)
	printFleet(p, o, s.epics)

	// 7. forge checks.
	s.stage("forge-checks")
	p.section("FORGE CHECKS")
	switch {
	case s.readOnly:
		p.lines("skipped (read-only session) - GitHub authentication and the inactive-story state reads were not run.",
			"They need the leader lease, and this session must not dispatch, steer, or merge, so it",
			"has no action they would gate. The session holding the lease runs them.")
	default:
		p.line("not configured - no deferred forge checks run for this session.")
	}

	// 8. notes: curated memory, the cheapest thing for a truncated tail to lose.
	s.stage("notes")
	printNotes(p, o.Workspace)

	// 9. closing reminder.
	s.stage("next-step")
	printNextStep(p, o.Harness, s.readOnly, s.epics)
}

// printWakeQueue prints cox wake drain's records for every active epic, the drain annotation, and the generation-bound
// acknowledgement the leader runs after handling them.
func (s *session) printWakeQueue() {
	p, shown := s.p, 0
	for _, ep := range s.epics {
		ws, err := WakeDrain(ep)
		if err != nil {
			p.line(fmt.Sprintf("epic %s: wake queue unreadable: %v", filepath.Base(ep), err))
			continue
		}
		if len(ws) == 0 {
			continue
		}
		shown += len(ws)
		p.line(fmt.Sprintf("epic %s (%s):", filepath.Base(ep), ep))
		for _, w := range ws {
			p.line(fmt.Sprintf("[gen %d] %s %s: %s", w.Gen, w.Kind, w.Story, strings.ReplaceAll(w.Note, "\n", " ")))
		}
		p.line("wake annotation: latest wake-EVENT observed at drain, not current state")
		p.line("WAKE_ACK_REQUIRED: after handling every record above, acknowledge with:")
		p.line(fmt.Sprintf("  cox wake ack-through %d --epic %s", ws[len(ws)-1].Gen, ep))
	}
	if shown == 0 {
		p.line("(no queued wakes)")
	}
}
