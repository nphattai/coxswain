package main

import (
	"fmt"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/state"
)

// commitDispatch persists a successful spawn as a compensating transaction: the working event, the session file, and the
// worktree file, in that order. Every write is on the data path, so none is discarded with `_ =`. If any write fails the
// spawned worker is already live and unmonitored, so commitDispatch stops the backend session to release it. A confirmed
// Stop means nothing is orphaned; an unconfirmed Stop keeps ownership and records a pending_external event carrying the
// dispatch id, so the session id is never lost (event.v1 F03/F04/F05). It returns success only when all three writes
// land.
func commitDispatch(b backend.Backend, epicDir, slug, story string, attempt int, actor state.Actor, sess backend.Session, wtPath string, extra map[string]any, from state.State) error {
	if from == "" {
		from = state.Submitted
	}
	ev := state.Event{
		Epic: slug, Story: story, Attempt: attempt, Actor: actor,
		From: from, To: state.Working,
		Evidence: dispatchEvidence(sess.ID, extra), ExternalConfirmed: true,
	}
	if err := state.Append(epicDir, ev); err != nil {
		return rollbackDispatch(b, epicDir, slug, story, attempt, actor, sess, fmt.Errorf("append dispatch event: %w", err))
	}
	if err := saveSession(epicDir, story, sess, attempt); err != nil {
		return rollbackDispatch(b, epicDir, slug, story, attempt, actor, sess, fmt.Errorf("save session: %w", err))
	}
	if err := saveWorktree(epicDir, story, wtPath, attempt); err != nil {
		return rollbackDispatch(b, epicDir, slug, story, attempt, actor, sess, fmt.Errorf("save worktree: %w", err))
	}
	return nil
}

// dispatchEvidence builds the evidence map for a working event, always carrying the dispatch id so the session is never
// anonymous. extra keys (e.g. arena_role) are merged on top.
func dispatchEvidence(dispatchID string, extra map[string]any) map[string]any {
	ev := map[string]any{"dispatch": dispatchID}
	for k, v := range extra {
		ev[k] = v
	}
	return ev
}

// rollbackDispatch releases a spawned session whose persistence failed. It returns the original cause wrapped with what
// happened to the session, so the caller reports the real failure, not the compensation.
func rollbackDispatch(b backend.Backend, epicDir, slug, story string, attempt int, actor state.Actor, sess backend.Session, cause error) error {
	confirmed, stopErr := b.Stop(sess)
	if confirmed {
		return fmt.Errorf("%w (session %s stopped)", cause, sess.ID)
	}
	pending := state.Event{
		Epic: slug, Story: story, Attempt: attempt, Actor: actor,
		From: state.Working, To: state.PendingExternal,
		Evidence: map[string]any{"intended_to": string(state.Working), "dispatch": sess.ID, "error": cause.Error()}, ExternalConfirmed: false,
	}
	if stopErr != nil {
		pending.Evidence["stop_error"] = stopErr.Error()
	}
	if err := state.Append(epicDir, pending); err != nil {
		return fmt.Errorf("%w; stop unconfirmed and could not record pending_external for session %s: %v", cause, sess.ID, err)
	}
	return fmt.Errorf("%w (session %s pending_external, ownership retained)", cause, sess.ID)
}
