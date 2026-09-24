// Package control is the control channel: the verb-allowlisted operations a leader runs against a worker
// (interrupt | park | relaunch). Every verb that has an external side effect records from->pending_external with
// evidence.intended_to first, calls the adapter, and only appends pending_external->intended_to (external_confirmed)
// when the adapter confirms; an unconfirmed effect keeps pending_external and ownership (event.v1, F03/F04/F05). A
// crash between the two appends leaves the story in pending_external with intended_to, and Reconcile finishes it by
// probing what already happened instead of repeating the side effect.
package control

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/brief"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
)

// DefaultParkWait is the maximum time park waits for a matching checkpoint before refusing to park blind (v1).
const DefaultParkWait = 1200 * time.Second

// Controller runs control verbs against one epic's stories.
type Controller struct {
	EpicDir      string
	Backend      backend.Backend
	Harness      harness.Harness
	ParkWait     time.Duration // 0 => DefaultParkWait
	PollInterval time.Duration // 0 => 5s; the checkpoint-wait poll
	ExitWait     time.Duration // 0 => DefaultExitWait; how long a stopped agent has to read settled
	Warn         io.Writer     // warnings; nil => os.Stderr
	// Unwire retires the prior incarnation's harness wiring (worker hooks) during a relaunch, after the prior agent is
	// stopped and before the replacement is armed; an error refuses the relaunch. nil = nothing to retire here.
	Unwire func() error
	// Arm arms the replacement incarnation's busy record and wiring during a relaunch and returns its gen (threaded to
	// the spawn as spec.BusyGen). nil = the caller armed it already and passed spec.BusyGen.
	Arm func() (string, error)
}

// DefaultExitWait is how long park and relaunch wait for a stopped agent to read settled (fm FM_CONTROL_EXIT_WAIT).
const DefaultExitWait = 30 * time.Second

// exitPoll is the agent-state poll interval while waiting for a stop to take effect (fm FM_CONTROL_POLL).
const exitPoll = 500 * time.Millisecond

func (c *Controller) exitWait() time.Duration {
	if c.ExitWait > 0 {
		return c.ExitWait
	}
	return DefaultExitWait
}

func (c *Controller) parkWait() time.Duration {
	if c.ParkWait > 0 {
		return c.ParkWait
	}
	return DefaultParkWait
}

func (c *Controller) poll() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return 5 * time.Second
}

func (c *Controller) warn() io.Writer {
	if c.Warn != nil {
		return c.Warn
	}
	return os.Stderr
}

// backendInterrupts reports whether the backend keystroke interrupt actually aborts this harness's turn (card
// BackendInterrupt). begin has already refused a Controller with no Harness.
func (c *Controller) backendInterrupts() bool {
	return c.Harness.Card().BackendInterrupt
}

// ResolveHarness is the control plane's harness resolution (fm_control_harness_*): a recorded harness name resolves to
// its adapter, and a name with no adapter is refused rather than guessed at - a lifecycle key or stop sent through the
// wrong harness's mechanics can land anywhere.
func ResolveHarness(name string) (harness.Harness, error) {
	if h, ok := registry.Adapter(name); ok {
		return h, nil
	}
	return nil, fmt.Errorf("harness %q has no verified control mechanics; refusing to guess at one", name)
}

// begin opens a lifecycle action on story (fm-control.sh:305-330): it takes the story's lifecycle lock FIRST, before any
// mutable state is read, then resolves the story - it must be recorded (dispatched: it has events), the session must be
// bound to it (a session record naming another story is refused), and the Controller must carry a harness with verified
// control mechanics. The returned release drops the lock; the caller holds it to its last write.
func (c *Controller) begin(story string, session backend.Session) (func(), *state.StorySnap, error) {
	release, err := tryLock(c.EpicDir, story)
	if err != nil {
		return nil, nil, err
	}
	snap, err := c.recorded(story)
	if err == nil && session.Story != "" && session.Story != story {
		err = fmt.Errorf("story %s's endpoint %s belongs to story %s, not %s; refusing to act on it", story, sessionHandle(session), session.Story, story)
	}
	if err == nil && c.Harness == nil {
		err = fmt.Errorf("story %s has no harness with verified control mechanics; refusing to guess at one", story)
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return release, snap, nil
}

// recorded returns the snapshot of a story that has been dispatched, refusing an id with no events (fm: "no task").
func (c *Controller) recorded(story string) (*state.StorySnap, error) {
	events, _, err := state.Load(c.EpicDir)
	if err != nil {
		return nil, err
	}
	s := state.Fold(events).Stories[story]
	if s == nil {
		return nil, fmt.Errorf("no story '%s' in %s (cox control resolves a recorded story id only)", story, c.EpicDir)
	}
	if s.Attempt < 1 {
		s.Attempt = 1
	}
	return s, nil
}

func sessionHandle(s backend.Session) string {
	if s.Handle != "" {
		return s.Handle
	}
	return s.ID
}

// Interrupt breaks a worker out of a runaway turn (fm-control.sh interrupt, do_interrupt). It refuses when no agent is
// running at the story's endpoint (probe settled: "there is nothing to interrupt") before any key is sent; an unknown
// probe (a backend that cannot classify the agent) proceeds, because an interrupt is non-destructive and the proof it
// prints says exactly what was verified. Delivery goes through the one mechanism the harness honours: the backend
// keystroke for a card with BackendInterrupt, otherwise ONLY the durable inbox interrupt record its extension aborts on
// (a key the harness does not honour is never sent). After delivery the agent is revalidated: an interrupt must leave
// the agent running, so a settled agent afterwards is an error, and the stale pre-delivery proof is not published. The
// event records ONE working->working attempt with {verb, delivered, verified, cancel}; cancel is always "unconfirmed"
// because no cox harness acknowledges a cancellation. The story never leaves its state.
func (c *Controller) Interrupt(story string, session backend.Session) error {
	release, snap, err := c.begin(story, session)
	if err != nil {
		return err
	}
	defer release()
	if live, perr := c.Backend.Probe(session); perr == nil && live == backend.Settled {
		return fmt.Errorf("no agent is running at story %s's recorded endpoint (state: settled); there is nothing to interrupt", story)
	}
	ev := map[string]any{"verb": "interrupt", "delivered": false}
	var deliverErr error
	if c.backendInterrupts() {
		if deliverErr = c.Backend.Interrupt(session); deliverErr == nil {
			ev["delivered"] = true
			ev["via"] = "backend"
			_, _ = c.Backend.Send(session, inbox.Doorbell(inbox.Dir(c.EpicDir, story))) // ring so it reads its inbox now
		} else {
			ev["error"] = deliverErr.Error()
		}
	} else {
		recPath, werr := inbox.WriteInterrupt(c.EpicDir, story)
		if werr != nil {
			deliverErr = werr
			ev["harness_interrupt_error"] = werr.Error()
		} else {
			ev["delivered"] = true
			ev["via"] = "inbox+extension"
			ev["interrupt_record"] = recPath
			_, _ = c.Backend.Send(session, inbox.Doorbell(inbox.Dir(c.EpicDir, story))) // best-effort ring
		}
	}
	var postErr error
	if deliverErr == nil {
		ev["cancel"] = "unconfirmed"
		switch live, perr := c.Backend.Probe(session); {
		case perr == nil && live == backend.Settled:
			ev["agent_state"] = live.String()
			postErr = fmt.Errorf("story %s's agent is '%s' after its interrupt; an interrupt must leave the agent running", story, live)
		case perr == nil && live == backend.Alive:
			ev["verified"] = "agent-alive"
		default:
			ev["verified"] = "unverified"
		}
	}
	if err := state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: snap.Attempt, Actor: state.Leader,
		From: snap.State, To: snap.State, Evidence: ev, ExternalConfirmed: true,
	}); err != nil {
		return err
	}
	if deliverErr != nil {
		return fmt.Errorf("interrupt not delivered (state unchanged): %w", deliverErr)
	}
	return postErr
}

// Park stops a worker for later resume (fm-control.sh do_exit, with Stop = terminal close per ADR 0012). The agent's
// state is read first and must be positively classified: a settled agent is already stopped (idempotent success, no
// second Stop), an unknown one is refused before any effect (an endpoint whose agent cannot be attributed never
// receives a lifecycle command, nor is it claimed stopped). An alive agent must first leave a checkpoint whose attempt
// and head match (steered and waited for up to ParkWait, unless an idle worker's fresh checkpoint already covers it);
// then park records working->pending_external{intended_to:parked}, stops the worker, and completes to parked only when
// the agent is proven settled within ExitWait. A Stop error is reconciled against the agent's real state; an agent that
// does not stop fails closed and the story stays pending_external (ownership kept).
func (c *Controller) Park(story, worktree string, session backend.Session) error {
	release, snap, err := c.begin(story, session)
	if err != nil {
		return err
	}
	defer release()
	switch live, perr := c.Backend.Probe(session); {
	case perr == nil && live == backend.Settled:
		c.retireBusy(story)
		if snap.State == state.Parked {
			return nil
		}
		if err := c.appendPending(story, snap, state.Parked, "park", nil); err != nil {
			return err
		}
		return c.appendConfirmed(story, snap.Attempt, state.Parked, map[string]any{"result": "already-stopped"})
	case perr == nil && live == backend.Alive:
	default:
		return fmt.Errorf("story %s's endpoint reads '%s' rather than a positively classified state; refusing to send a lifecycle command into an unattributed endpoint", story, live)
	}
	// An idle worker (empty composer) whose last checkpoint is newer than its last event has nothing left to write:
	// parking on that checkpoint immediately beats steering it and waiting ParkWait for a checkpoint it will never
	// produce (finding 14). Otherwise fall through to the normal ensure-and-wait path.
	if !c.freshIdleCheckpoint(story, snap, session) {
		if err := c.ensureCheckpoint(story, worktree, snap.Attempt, session); err != nil {
			return err
		}
	}
	if err := c.appendPending(story, snap, state.Parked, "park", nil); err != nil {
		return err
	}
	note, err := c.stopAgent(story, session)
	if err != nil {
		return fmt.Errorf("%w; story stays pending_external (ownership kept)", err)
	}
	ev := map[string]any{"result": "stopped"}
	if note != "" {
		ev["note"] = note
	}
	c.retireBusy(story)
	return c.appendConfirmed(story, snap.Attempt, state.Parked, ev)
}

// stopAgent closes an alive agent's terminal and proves it stopped (do_exit's wait_agent_state ... dead): a Stop error is
// reconciled by probing (a transport failure on an agent that did stop is still a stop), and otherwise the agent must
// read Settled within ExitWait. It never claims a stop it could not observe; the note names a Stop error the probe
// overrode.
func (c *Controller) stopAgent(story string, session backend.Session) (string, error) {
	if _, err := c.Backend.Stop(session); err != nil {
		if live, perr := c.Backend.Probe(session); perr == nil && live == backend.Settled {
			return "stop errored but probe settled", nil
		}
		return "", fmt.Errorf("stop failed and the agent is not proven stopped: %w", err)
	}
	deadline := time.Now().Add(c.exitWait())
	for {
		live, perr := c.Backend.Probe(session)
		if perr == nil && live == backend.Settled {
			return "", nil
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("exit-delivered %s exit-command=delivered agent-state=%s exit=unconfirmed; the agent did not stop within %s", story, live, c.exitWait())
		}
		time.Sleep(exitPoll)
	}
}

// retireBusy retires the harness-owned busy record for the stopped incarnation (fm retire_busy_incarnation): it reads the
// armed gen and Retires against it, so a re-arm on the next resume gets a clean record and a parked story never leaves a
// stale "busy" behind. Best-effort - a mismatch (a newer incarnation exists) or an absent record is fine.
func (c *Controller) retireBusy(story string) {
	rec, ok := busy.ReadRecord(c.EpicDir, story)
	if !ok {
		return
	}
	if err := busy.Retire(c.EpicDir, story, rec.Gen); err != nil {
		fmt.Fprintf(c.warn(), "warn: busy retire for %s: %v\n", story, err)
	}
}

// Relaunch resumes a story in the same worktree at attempt+1 (fm-control.sh do_relaunch). Everything that can refuse
// runs before any record is written or any agent touched, so a refusal leaves the event log byte-identical: the story
// must not be closed (completed, failed, canceled), its instructions must exist, the progress note is required (the
// replacement inherits the local copy but none of the conversation), and the checkpoint must prove the unlanded work
// (recorded worktree present and a git root, HEAD and status inspectable). The prior agent must be positively
// classified: settled needs no stop; unknown is refused; alive must not hold pending or unproven composer text. Then it
// records from->pending_external{intended_to: working, worktree_head, worktree_dirty}, stops the prior agent and proves it
// settled (ExitWait), retires the prior harness's wiring (Unwire), arms the replacement (Arm), and spawns; the working
// event is appended only when Spawn succeeds, otherwise the story stays pending_external. Any failure after the
// replacement was armed - by Arm, or by the caller through spec.BusyGen - retires that incarnation's busy record, so a
// worker that never launched never reads busy. prior is the previous attempt's session (zero value when none); extra is
// merged into the working event's evidence (e.g. a reroute {from,to,reason}).
func (c *Controller) Relaunch(story, worktree, note string, prior backend.Session, spec backend.HarnessSpec, extra map[string]any) (sess backend.Session, err error) {
	armed := spec.BusyGen
	defer func() {
		if err != nil && armed != "" {
			_ = busy.Retire(c.EpicDir, story, armed)
		}
	}()
	release, snap, err := c.begin(story, prior)
	if err != nil {
		return backend.Session{}, err
	}
	defer release()
	switch snap.State {
	case state.Completed, state.Failed, state.Canceled:
		return backend.Session{}, fmt.Errorf("story %s is %s; its close is authoritative, refusing to relaunch it", story, snap.State)
	}
	storyPath := storyFile(c.EpicDir, story)
	if _, err := os.Stat(storyPath); err != nil {
		return backend.Session{}, fmt.Errorf("story %s has no instructions at %s; refusing to relaunch a worker with nothing to work from", story, storyPath)
	}
	if strings.TrimSpace(note) == "" {
		return backend.Session{}, fmt.Errorf("relaunch of story %s requires --note: the replacement worker inherits the local copy but none of the conversation, so it must be told what happened", story)
	}
	head, dirty, err := safeCheckpoint(story, worktree)
	if err != nil {
		return backend.Session{}, err
	}
	stop, err := c.relaunchPreStop(story, prior)
	if err != nil {
		return backend.Session{}, err
	}
	newAttempt := snap.Attempt + 1
	ctxPath, err := brief.Build(c.EpicDir, story)
	if err != nil {
		return backend.Session{}, err
	}
	if err := state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: newAttempt, Actor: state.Leader,
		From: snap.State, To: state.PendingExternal,
		Evidence: map[string]any{"intended_to": string(state.Working), "verb": "relaunch", "note": note,
			"worktree_head": head, "worktree_dirty": dirty}, ExternalConfirmed: false,
	}); err != nil {
		return backend.Session{}, err
	}
	pending := func(format string, args ...any) error {
		return fmt.Errorf("relaunch of %s: %s; story stays pending_external", story, fmt.Sprintf(format, args...))
	}
	if stop {
		if _, err := c.stopAgent(story, prior); err != nil {
			return backend.Session{}, pending("%v", err)
		}
	}
	if c.Unwire != nil {
		if err := c.Unwire(); err != nil {
			return backend.Session{}, pending("could not retire %s wiring: %v; the replacement was not armed", c.Harness.Card().Name, err)
		}
	}
	if c.Arm != nil {
		g, err := c.Arm()
		if err != nil {
			return backend.Session{}, pending("arm the replacement: %v", err)
		}
		armed, spec.BusyGen = g, g
	}
	b := backend.Brief{StoryPath: storyPath, Text: note}
	sess, err = c.Backend.Spawn(backend.Worktree{Path: worktree, Branch: "story/" + story}, spec, b)
	if err != nil {
		return backend.Session{}, fmt.Errorf("relaunch spawn failed, story stays pending_external: %w", err)
	}
	ev := map[string]any{"dispatch": sess.ID, "context": ctxPath, "note": note, "worktree_head": head, "worktree_dirty": dirty}
	if stop {
		ev["closed_terminal"] = sessionHandle(prior)
	}
	for k, v := range extra {
		ev[k] = v
	}
	if err := state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: newAttempt, Actor: state.Leader,
		From: state.PendingExternal, To: state.Working,
		Evidence: ev, ExternalConfirmed: true,
	}); err != nil {
		armed = "" // the replacement is live; its record is not retired over a log write failure
		return sess, err
	}
	return sess, nil
}

// relaunchPreStop classifies the prior agent before anything is recorded (do_exit's first half): no prior session or a
// settled agent needs no stop; an unknown one is refused (a relaunch never stacks a second agent on an endpoint it
// cannot attribute); an alive one must not hold pending or unproven composer text, which closing its terminal would
// silently discard. It reports whether the prior agent must be stopped.
func (c *Controller) relaunchPreStop(story string, prior backend.Session) (bool, error) {
	if sessionHandle(prior) == "" {
		return false, nil
	}
	live, perr := c.Backend.Probe(prior)
	switch {
	case perr == nil && live == backend.Settled:
		return false, nil
	case perr == nil && live == backend.Alive:
	default:
		return false, fmt.Errorf("story %s's endpoint reads '%s' rather than a positively classified state; refusing to relaunch over an unattributed endpoint", story, live)
	}
	composer, err := c.Backend.Composer(prior)
	switch {
	case err != nil || composer == backend.ComposerUnknown || composer == "":
		return false, fmt.Errorf("story %s's composer state is 'unknown', not proven empty; refusing to stop it because pending text could be lost. Clear the composer, then retry relaunch", story)
	case composer == backend.ComposerPending:
		return false, fmt.Errorf("story %s's composer visibly holds pending text; refusing to stop it because that text would be lost. Clear or submit the pending text, then retry relaunch", story)
	}
	return true, nil
}

// safeCheckpoint proves, before anything is stopped, that the work a relaunch must preserve is there and recoverable
// afterwards (fm-control.sh safe_checkpoint): the recorded worktree exists and is a git worktree root, and its HEAD and
// status are inspectable. It returns the head it proved (or "unborn") and whether uncommitted work is present.
func safeCheckpoint(story, wt string) (head, dirty string, err error) {
	if wt == "" {
		return "", "", fmt.Errorf("story %s has no recorded worktree; refusing to relaunch without a recorded local copy to preserve", story)
	}
	if info, err := os.Stat(wt); err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("story %s's recorded worktree %s is missing; refusing to relaunch and lose track of its work", story, wt)
	}
	real, err := filepath.EvalSymlinks(wt)
	if err != nil {
		return "", "", fmt.Errorf("story %s's recorded worktree %s cannot be resolved", story, wt)
	}
	top, err := gitOut(wt, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("story %s's recorded worktree %s is not a git worktree; refusing to relaunch without a checkout whose unlanded work can be accounted for", story, wt)
	}
	if topReal, err := filepath.EvalSymlinks(top); err == nil {
		top = topReal
	}
	if real != top {
		return "", "", fmt.Errorf("story %s's recorded worktree %s is not a worktree root (root is %s); refusing to relaunch against an ambiguous checkout", story, wt, top)
	}
	if head, err = gitOut(wt, "rev-parse", "--verify", "HEAD"); err != nil {
		ref, rerr := gitOut(wt, "symbolic-ref", "-q", "HEAD")
		if rerr != nil {
			return "", "", fmt.Errorf("story %s's worktree HEAD cannot be inspected; refusing to relaunch from an unreadable checkout", story)
		}
		if _, err := gitOut(wt, "show-ref", "--verify", "--quiet", ref); err == nil {
			return "", "", fmt.Errorf("story %s's worktree HEAD exists but cannot be resolved; refusing to relaunch from an unreadable checkout", story)
		} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			return "", "", fmt.Errorf("story %s's worktree HEAD cannot be inspected; refusing to relaunch from an unreadable checkout", story)
		}
		head = "unborn"
	}
	status, err := gitOut(wt, "status", "--porcelain")
	if err != nil {
		return "", "", fmt.Errorf("story %s's worktree status cannot be inspected; refusing to relaunch without accounting for local changes", story)
	}
	if status != "" {
		return head, "yes", nil
	}
	return head, "no", nil
}

func gitOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// Reconcile finishes a story left in pending_external by a crash between the two appends. It reads intended_to from the
// last event and probes what already happened rather than repeating the side effect: a park that reached Settled
// completes to parked; a relaunch whose worker is Alive completes to working. When the probe cannot confirm the effect
// (Unknown, or a state that does not match), it leaves pending_external untouched so ownership is not cleared wrongly.
// It never re-issues Stop/Spawn/Interrupt, so a resume never repeats a side effect (F03/F04/F05, scenario 2).
func (c *Controller) Reconcile(story string, session backend.Session) error {
	snap, err := c.snap(story)
	if err != nil {
		return err
	}
	if snap.State != state.PendingExternal || !snap.PendingExternal {
		return nil // nothing pending
	}
	intended, _ := snap.LastEvent.Evidence["intended_to"].(string)
	live, probeErr := c.Backend.Probe(session)
	switch state.State(intended) {
	case state.Parked:
		if probeErr == nil && live == backend.Settled {
			return c.appendConfirmed(story, snap.Attempt, state.Parked, map[string]any{"note": "reconciled: probe settled"})
		}
	case state.Working:
		if probeErr == nil && live == backend.Alive {
			return c.appendConfirmed(story, snap.Attempt, state.Working, map[string]any{"note": "reconciled: probe alive"})
		}
	}
	return fmt.Errorf("cannot reconcile %s: intended_to=%q, probe=%v (err=%v); left pending_external", story, intended, live, probeErr)
}

// ensureCheckpoint verifies a matching checkpoint exists, sending a steer and waiting up to ParkWait when it does not.
func (c *Controller) ensureCheckpoint(story, worktree string, attempt int, session backend.Session) error {
	if c.checkpointMatches(story, worktree, attempt) {
		return nil
	}
	// Ask the worker to write the checkpoint, then ring its doorbell.
	if _, err := inbox.Write(c.EpicDir, story, "PARK: viết checkpoint (state, next step, open questions, files touched), commit, rồi kết thúc turn.", inbox.Steer, "park request"); err != nil {
		return fmt.Errorf("send park steer: %w", err)
	}
	_, _ = c.Backend.Send(session, inbox.Doorbell(inbox.Dir(c.EpicDir, story)))

	deadline := time.Now().Add(c.parkWait())
	for {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("no matching checkpoint for %s after %s; refusing to park blind", story, c.parkWait())
		}
		time.Sleep(c.poll())
		if c.checkpointMatches(story, worktree, attempt) {
			return nil
		}
	}
}

// freshIdleCheckpoint reports whether park may skip the ensure-and-wait step: the worker's composer is observed empty
// (idle at a prompt, not mid-turn) and a valid checkpoint for the current attempt is newer than the story's last event.
// Such a checkpoint is the best state an idle worker can offer - it will not write another - so park on it rather than
// waiting ParkWait for one that never comes (finding 14). Only an observed-empty composer qualifies (F08: unknown/busy
// never do), and an unparsable timestamp on either side is treated as not-fresh so an ambiguous case takes the safe wait.
func (c *Controller) freshIdleCheckpoint(story string, snap *state.StorySnap, session backend.Session) bool {
	composer, err := c.Backend.Composer(session)
	if err != nil || composer != backend.ComposerEmpty {
		return false
	}
	fm, _, err := checkpoint.Parse(checkpoint.Path(c.EpicDir, story))
	if err != nil || fm.Validate() != nil || fm.Attempt != snap.Attempt {
		return false
	}
	written, werr := time.Parse(time.RFC3339, fm.WrittenAt)
	lastEvent, lerr := time.Parse(time.RFC3339, snap.LastEvent.TS)
	if werr != nil || lerr != nil {
		return false
	}
	return written.After(lastEvent)
}

// checkpointMatches reports whether a checkpoint exists whose attempt and head match the current attempt and HEAD.
func (c *Controller) checkpointMatches(story, worktree string, attempt int) bool {
	fm, _, err := checkpoint.Parse(checkpoint.Path(c.EpicDir, story))
	if err != nil {
		return false
	}
	if fm.Validate() != nil || fm.Attempt != attempt {
		return false
	}
	facts, err := checkpoint.Facts(worktree, c.EpicDir, story)
	if err != nil {
		return false
	}
	// Prefix-tolerant compare: a checkpoint that recorded a short sha still matches the full HEAD Facts now returns.
	return checkpoint.HeadMatches(fm.Head, facts.Head)
}

// appendPending records from->pending_external with the intended destination and verb (plus optional evidence).
func (c *Controller) appendPending(story string, snap *state.StorySnap, intended state.State, verb string, extra map[string]any) error {
	ev := map[string]any{"intended_to": string(intended), "verb": verb}
	for k, v := range extra {
		ev[k] = v
	}
	return state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: snap.Attempt, Actor: state.Leader,
		From: snap.State, To: state.PendingExternal, Evidence: ev, ExternalConfirmed: false,
	})
}

// appendConfirmed records pending_external->to with external_confirmed=true and optional extra evidence.
func (c *Controller) appendConfirmed(story string, attempt int, to state.State, extra map[string]any) error {
	ev := map[string]any{}
	for k, v := range extra {
		ev[k] = v
	}
	return state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: attempt, Actor: state.Leader,
		From: state.PendingExternal, To: to, Evidence: ev, ExternalConfirmed: true,
	})
}

// snap folds the event log and returns the story's snapshot, defaulting attempt to 1 and state to submitted for a
// story with no events (so a first control call has a defined from-state).
func (c *Controller) snap(story string) (*state.StorySnap, error) {
	events, _, err := state.Load(c.EpicDir)
	if err != nil {
		return nil, err
	}
	snap := state.Fold(events)
	s := snap.Stories[story]
	if s == nil {
		s = &state.StorySnap{ID: story, State: state.Submitted, Attempt: 1}
	}
	if s.Attempt < 1 {
		s.Attempt = 1
	}
	return s, nil
}

func snapEpic(epicDir string) string { return filepath.Base(epicDir) }

func storyFile(epicDir, story string) string {
	return filepath.Join(epicDir, "stories", story+".md")
}
