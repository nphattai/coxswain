// Package status is the worker->leader status channel. A status is a log, not a state: `cox status <phase> "<note>"`
// appends a working->working event carrying evidence.phase and evidence.note and sends a backend status mail so the
// watcher can classify it. It never changes the story's state.
package status

import (
	"fmt"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/state"
)

// Report appends the working->working status event and, when mail and leader are set, sends a backend status mail to
// the leader. The event is the durable record; the mail is the notification. A nil mailbox skips the notification (the
// event still lands), so a worker with no backend wiring still logs its phase.
func Report(epicDir, story string, attempt int, phase, note string, mail backend.Mailbox, leader string) error {
	if attempt < 1 {
		attempt = 1
	}
	ev := state.Event{
		Epic:              filepath.Base(epicDir),
		Story:             story,
		Attempt:           attempt,
		Actor:             state.Worker,
		From:              state.Working,
		To:                state.Working,
		Evidence:          map[string]any{"phase": phase, "note": note},
		ExternalConfirmed: true,
	}
	if err := state.Append(epicDir, ev); err != nil {
		return fmt.Errorf("append status event: %w", err)
	}
	if mail != nil && leader != "" {
		subject := "status: " + phase
		if err := mail.Send(leader, subject, note); err != nil {
			return fmt.Errorf("send status mail: %w", err)
		}
	}
	return nil
}
