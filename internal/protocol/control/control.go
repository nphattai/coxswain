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
	"path/filepath"
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
	Warn         io.Writer     // warnings (abandon-only); nil => os.Stderr
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

// Park stops a worker for later resume. It refuses to park blind: a checkpoint must exist whose attempt and head match
// the current attempt and HEAD. When it does not, park sends a "PARK: write checkpoint" steer and waits up to ParkWait
// for one, re-checking each poll; a timeout is an error. Once the checkpoint is verified, park records
// working->pending_external{intended_to:parked}, stops the worker, and completes to parked only on confirmation. An
// abandon-only stop (adapter returns confirmed=false with no error: the dispatch was fenced but the stop is not
// confirmed) still parks, with an evidence note and a stderr warning (M0 semantics). A hard stop error keeps ownership
// UNLESS a follow-up probe reports Settled (the worker already stopped, e.g. Orca closed the terminal itself), in
// which case it parks with a note; an Alive or Unknown probe keeps pending_external.
func (c *Controller) Park(story, worktree string, session backend.Session) error {
	release, snap, err := c.begin(story, session)
	if err != nil {
		return err
	}
	defer release()
	// An idle worker (empty composer) whose last checkpoint is newer than its last event has nothing left to write:
	// parking on that checkpoint immediately beats steering it and waiting ParkWait for a checkpoint it will never
	// produce (finding 14). Otherwise fall through to the normal ensure-and-wait path.
	if !c.freshIdleCheckpoint(story, snap, session) {
		if err := c.ensureCheckpoint(story, worktree, snap.Attempt, session); err != nil {
			return err
		}
	}
	if err := c.appendPending(story, snap, state.Parked, "park"); err != nil {
		return err
	}
	confirmed, err := c.Backend.Stop(session)
	if err != nil {
		// Stop can error even though the worker has actually settled (e.g. Orca closed the terminal itself).
		// Trust a Settled probe over the Stop error rather than stranding a stopped worker in pending_external.
		if live, perr := c.Backend.Probe(session); perr == nil && live == backend.Settled {
			c.retireBusy(story)
			return c.appendConfirmed(story, snap.Attempt, state.Parked, map[string]any{"note": "stop errored but probe settled"})
		}
		return fmt.Errorf("stop failed, story stays pending_external (ownership kept): %w", err)
	}
	if confirmed {
		c.retireBusy(story)
		return c.appendConfirmed(story, snap.Attempt, state.Parked, nil)
	}
	// Abandon-only: fenced but not confirmed stopped. Park anyway with a flag and a warning (M0).
	fmt.Fprintf(c.warn(), "warn: %s parked via abandon: dispatch fenced, worker NOT confirmed stopped - check its terminal\n", story)
	c.retireBusy(story)
	return c.appendConfirmed(story, snap.Attempt, state.Parked, map[string]any{"note": "fenced, not confirmed stopped"})
}

// retireBusy retires the harness-owned busy record for the parked incarnation (DESIGN wave-2 item 6c): it reads the
// current gen and Retires against it, so a re-arm on the next resume gets a clean record and a parked story never leaves
// a stale "busy" behind. Best-effort - a mismatch (a newer incarnation exists) or an absent record is fine.
func (c *Controller) retireBusy(story string) {
	rec, ok := busy.ReadRecord(c.EpicDir, story)
	if !ok {
		return
	}
	if err := busy.Retire(c.EpicDir, story, rec.Gen); err != nil {
		fmt.Fprintf(c.warn(), "warn: busy retire for %s: %v\n", story, err)
	}
}

// Relaunch resumes a story in the same worktree at attempt+1. It closes the previous attempt's terminal (ADR 0012: Stop
// = terminal close) before spawning a new session, so a relaunch never stacks a second live pane on the same worktree
// (M14). It replays the brief with the progress note and spawns a new session. The spawn is the external effect:
// from->pending_external{intended_to:working} at the new attempt, then pending_external->working (external_confirmed)
// only when Spawn succeeds; a spawn error keeps pending_external. prior is the previous attempt's session (zero value
// when none); extra is merged into the working event's evidence (e.g. a reroute {from,to,reason} when the leader resumes
// on another harness); pass nil for a plain resume.
func (c *Controller) Relaunch(story, worktree, note string, prior backend.Session, spec backend.HarnessSpec, extra map[string]any) (backend.Session, error) {
	release, snap, err := c.begin(story, prior)
	if err != nil {
		return backend.Session{}, err
	}
	defer release()
	newAttempt := snap.Attempt + 1

	ctxPath, err := brief.Build(c.EpicDir, story)
	if err != nil {
		return backend.Session{}, err
	}
	storyPath := storyFile(c.EpicDir, story)
	// Record the pending transition at the new attempt.
	if err := state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: newAttempt, Actor: state.Leader,
		From: snap.State, To: state.PendingExternal,
		Evidence: map[string]any{"intended_to": string(state.Working), "verb": "relaunch", "note": note}, ExternalConfirmed: false,
	}); err != nil {
		return backend.Session{}, err
	}

	// Close the previous attempt's terminal before spawning the new one. Best-effort: a close error is warned, not fatal
	// (the spawn still proceeds); the closed handle and whether the close was confirmed are recorded in the evidence.
	closedHandle, closedConfirmed := c.closePriorTerminal(story, prior)

	b := backend.Brief{StoryPath: storyPath, Text: note}
	sess, err := c.Backend.Spawn(backend.Worktree{Path: worktree, Branch: "story/" + story}, spec, b)
	if err != nil {
		return backend.Session{}, fmt.Errorf("relaunch spawn failed, story stays pending_external: %w", err)
	}
	ev := map[string]any{"dispatch": sess.ID, "context": ctxPath, "note": note}
	if closedHandle != "" {
		ev["closed_terminal"] = closedHandle
		ev["closed_confirmed"] = closedConfirmed
	}
	for k, v := range extra {
		ev[k] = v
	}
	if err := state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: newAttempt, Actor: state.Leader,
		From: state.PendingExternal, To: state.Working,
		Evidence: ev, ExternalConfirmed: true,
	}); err != nil {
		return sess, err
	}
	return sess, nil
}

// closePriorTerminal stops the previous attempt's session (ADR 0012: Stop closes the terminal) and returns the closed
// handle and whether the close was confirmed. A zero-value session (no prior attempt) is a no-op returning "". A close
// error is warned, never fatal: the relaunch proceeds and the outcome is recorded in the working event evidence.
func (c *Controller) closePriorTerminal(story string, prior backend.Session) (handle string, confirmed bool) {
	handle = prior.Handle
	if handle == "" {
		handle = prior.ID
	}
	if handle == "" {
		return "", false
	}
	confirmed, err := c.Backend.Stop(prior)
	if err != nil {
		fmt.Fprintf(c.warn(), "warn: relaunch could not close prior terminal %s for %s: %v\n", handle, story, err)
		return handle, false
	}
	return handle, confirmed
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

// appendPending records from->pending_external with the intended destination and verb.
func (c *Controller) appendPending(story string, snap *state.StorySnap, intended state.State, verb string) error {
	return state.Append(c.EpicDir, state.Event{
		Epic: snapEpic(c.EpicDir), Story: story, Attempt: snap.Attempt, Actor: state.Leader,
		From: snap.State, To: state.PendingExternal,
		Evidence: map[string]any{"intended_to": string(intended), "verb": verb}, ExternalConfirmed: false,
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
