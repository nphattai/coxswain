package bearings

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nphattai/coxswain/internal/wake"
)

// Stages is the digest's ordered stage list; the STARTUP TRUNCATED banner names every stage at and after the one that
// did not finish (fm-session-start.sh SESSION_START_STAGES, mapped to cox names).
var Stages = []string{"lease", "doctor", "wake-queue", "supervision-instructions", "read-once", "fleet-state", "forge-checks", "notes", "next-step"}

// session is one running digest. The digest runs in its own goroutine under the runtime bound, so every field the
// bound's path reads is guarded by mu.
type session struct {
	o     Opts
	p     *printer
	epics []string
	procs stageProcs
	final string // the complete digest, set before run returns when it finished inside the bound

	mu       sync.Mutex
	readOnly bool
	current  string
	frozen   bool // the runtime bound fired; current is final
	deferred *deferredRun
}

func withDefaults(o Opts) Opts {
	if o.StatusTail <= 0 {
		o.StatusTail = DefaultStatusTail
	}
	if o.QueuedLimit <= 0 {
		o.QueuedLimit = DefaultQueuedLimit
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if abs, err := filepath.Abs(o.Workspace); err == nil {
		o.Workspace = abs
	}
	return o
}

// stage records the stage being entered (the truncation banner's breadcrumb) and runs its test-seam subprocess.
func (s *session) stage(name string) {
	s.mu.Lock()
	if !s.frozen {
		s.current = name
	}
	s.mu.Unlock()
	s.procs.run(s.o.StageCmd[name])
}

func (s *session) isReadOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOnly
}

// Compose prints the one ordered session-start digest. It is a reporting command, never a gate: a refused lease is a
// loud read-only banner inline, and a digest that hits its runtime bound returns what it printed plus a STARTUP
// TRUNCATED banner, still without an error (fm-session-start.sh "RUNTIME BOUND").
func Compose(o Opts) (Digest, error) {
	o = withDefaults(o)
	// Read-only until the lease is verified, so a digest the bound cuts inside the lease stage never claims ownership.
	s := &session{o: o, p: &printer{}, epics: ActiveEpics(o.Workspace), readOnly: true}
	done := make(chan struct{})
	go func() { defer close(done); s.run() }()
	timer := time.NewTimer(o.Timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		if text, first := s.p.seal(); first {
			// Read the breadcrumb before reaping: once its subprocess dies the sealed goroutine runs on through later
			// stages, which must not rename the stage that hit the bound.
			s.mu.Lock()
			d, stage, ro := s.deferred, s.current, s.readOnly
			s.frozen = true
			s.mu.Unlock()
			s.procs.reap()
			if d != nil {
				d.missed()
			}
			return Digest{Text: withEstimate(text + truncationBanner(o.Timeout, stage)), ReadOnly: ro, Truncated: true}, nil
		}
		<-done // the digest completed at the bound itself
	}
	return Digest{Text: withEstimate(s.final), ReadOnly: s.isReadOnly()}, nil
}

// truncationBanner is fm-session-start.sh's STARTUP TRUNCATED banner: the stage that did not finish and every stage
// that therefore never printed.
func truncationBanner(bound time.Duration, stage string) string {
	pending := "(unknown - the digest may be incomplete anywhere)"
	for i, st := range Stages {
		if st == stage {
			pending = strings.Join(Stages[i:], " ")
		}
	}
	if stage == "" {
		stage = "unknown"
	}
	return strings.Join([]string{"", bar,
		fmt.Sprintf("●  STARTUP TRUNCATED - SESSION START HIT ITS RUNTIME BOUND (%s)", bound),
		fmt.Sprintf("●  It stopped during the %q stage, so everything above is COMPLETE", stage),
		"●  only up to that point.",
		"●  RECONCILE these stages before acting on anything they would have shown:",
		"●    " + pending,
		"●  Rerun cox bearings now to finish taking the helm. If it truncates",
		"●  again, raise --timeout and report the slow stage - a stage that",
		"●  cannot finish inside the bound is a fleet problem, not a reporting detail.",
		bar, ""}, "\n")
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
	trueStart := !o.Reemit && (o.Source == "" || o.Source == "startup")
	startHash := ""
	if trueStart {
		startHash = agentsHash(o.Workspace)
	}
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
	s.mu.Lock()
	s.readOnly = !lr.ok
	s.mu.Unlock()
	if !lr.ok {
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
	readOnly := !lr.ok
	printAgentsRefresh(p, o)
	detached := ""
	switch {
	case readOnly || o.Reemit:
	case o.Forge != nil || o.StateRead != nil:
		d := startDeferred(o, s.epics)
		s.mu.Lock()
		s.deferred = d
		s.mu.Unlock()
	case o.Detach != nil:
		if err := o.Detach(); err != nil {
			detached = "FORGE_CHECKS: the deferred forge worker could not start (" + err.Error() + "); rerun cox bearings deferred"
		} else {
			detached = "started"
		}
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
	if readOnly {
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
	printSupervision(p, o.Workspace, o.Harness, readOnly)

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
	case readOnly:
		p.lines("skipped (read-only session) - GitHub authentication and the inactive-story state reads were not run.",
			"They need the leader lease, and this session must not dispatch, steer, or merge, so it",
			"has no action they would gate. The session holding the lease runs them.")
	case o.Reemit:
		p.line("not repeated on a context re-emit - this session's startup ran them; a failed result arrived as a startup-forge wake.")
	case s.deferred != nil:
		s.deferred.harvestInto(p)
	case detached == "started":
		p.lines("IN PROGRESS - the deferred forge checks have not finished yet.",
			"NOT yet confirmed: GitHub authentication and the inactive-story state reads.",
			"They run in a detached cox bearings deferred worker. Only a FAILED or otherwise actionable result arrives",
			"as a startup-forge or inactive-outcome wake; a clean success stays silent.")
	case detached != "":
		p.line(detached)
	default:
		p.line("not configured - no deferred forge checks run for this session.")
	}

	// 8. notes: curated memory, the cheapest thing for a truncated tail to lose.
	s.stage("notes")
	printNotes(p, o.Workspace)

	// 9. closing reminder.
	s.stage("next-step")
	printNextStep(p, o.Harness, readOnly, s.epics)

	text, first := p.seal()
	if !first {
		return // the runtime bound fired first: no completion, so no AGENTS baseline
	}
	s.final = text
	if trueStart && !readOnly && startHash != "" {
		_ = writeAgentsBaseline(o.Workspace, o.LeaderID, startHash)
	}
}

// printWakeQueue prints cox wake drain's own presentation (wake.Present: the unacked records, the outcome backstop and
// OPEN DECISIONS, its notices included) for every active epic, then the drain annotation and the generation-bound
// acknowledgement the leader runs after handling them. The digest never acknowledges.
func (s *session) printWakeQueue() {
	p, shown := s.p, false
	for _, ep := range s.epics {
		var out bytes.Buffer
		if err := wake.Present(ep, &out, &out, wake.PresentOptions{}); err != nil {
			p.line(fmt.Sprintf("epic %s: wake queue unreadable: %v", filepath.Base(ep), err))
			continue
		}
		ws, _ := WakeDrain(ep)
		if out.Len() == 0 && len(ws) == 0 {
			continue
		}
		shown = true
		p.line(fmt.Sprintf("epic %s (%s):", filepath.Base(ep), ep))
		p.raw(strings.TrimRight(out.String(), "\n") + "\n")
		if len(ws) > 0 {
			p.line("wake annotation: latest wake-EVENT observed at drain, not current state")
			p.line("WAKE_ACK_REQUIRED: after handling every record above, acknowledge with:")
			p.line(fmt.Sprintf("  cox wake ack-through %d --epic %s", ws[len(ws)-1].Gen, ep))
		}
	}
	if !shown {
		p.line("(no queued wakes)")
	}
}
