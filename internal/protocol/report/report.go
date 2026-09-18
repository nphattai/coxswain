// Package report is the worker->leader report channel shared by every backend (ADR 0012): a worker writes its progress,
// completion, blocker, or question straight into the epic directory instead of through a backend mailbox. Each report
// logs a status event (like `cox status`) and appends a wake to <epic>/.cox/wake.jsonl; a question additionally
// allocates a durable question id under <epic>/questions/<story>/ (internal/protocol/question). There is no completion
// cap: two `report done` calls append two worker_done wakes.
package report

import (
	"fmt"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/protocol/question"
	"github.com/nphattai/coxswain/internal/protocol/status"
	"github.com/nphattai/coxswain/internal/wake"
)

// Report kinds a worker may send with `cox story report <kind>`.
const (
	KindStatus = "status"
	KindDone   = "done"
	KindStuck  = "stuck"
)

// wakeKindFor maps a report kind to its wake kind.
func wakeKindFor(kind string) (wake.Kind, error) {
	switch kind {
	case KindStatus:
		return wake.KindStatus, nil
	case KindDone:
		return wake.KindWorkerDone, nil
	case KindStuck:
		return wake.KindStuck, nil
	default:
		return "", fmt.Errorf("unknown report kind %q (want status|done|stuck)", kind)
	}
}

// Report logs a worker status event and appends the matching wake. done -> worker_done, stuck -> stuck, status ->
// status (the note becomes the wake body). Evidence is merged onto the wake. The status event is the durable log; the
// wake is the leader's signal. Returns the assigned wake generation.
func Report(epicDir, story string, attempt int, kind, note string, evidence map[string]any) (int, error) {
	wk, err := wakeKindFor(kind)
	if err != nil {
		return 0, err
	}
	// The status event is the durable progress record (reuses the same working->working log as `cox status`, with no
	// mailbox notification: on this channel the wake is the notification).
	if err := status.Report(epicDir, story, attempt, kind, note, nil, ""); err != nil {
		return 0, err
	}
	return wake.Append(epicDir, wake.Wake{
		Epic: filepath.Base(epicDir), Story: story, Kind: wk, Note: note, Evidence: copyEvidence(evidence),
	})
}

// Question allocates a durable question id, logs a status event, and appends an input_required wake carrying
// evidence.question. It returns the allocated id (qNNN) so the worker can `cox question wait` on it.
func Question(epicDir, story string, attempt int, body string) (string, error) {
	id, err := question.Alloc(epicDir, story, body)
	if err != nil {
		return "", err
	}
	if err := status.Report(epicDir, story, attempt, "question", "asked "+id, nil, ""); err != nil {
		return "", err
	}
	if _, err := wake.Append(epicDir, wake.Wake{
		Epic: filepath.Base(epicDir), Story: story, Kind: wake.KindInputRequired,
		Note: "question " + id + ": " + body, Evidence: map[string]any{"question": id},
	}); err != nil {
		return "", err
	}
	return id, nil
}

// copyEvidence returns a shallow copy of evidence (nil-safe), so the caller's map is never aliased into a wake.
func copyEvidence(evidence map[string]any) map[string]any {
	if len(evidence) == 0 {
		return nil
	}
	out := make(map[string]any, len(evidence))
	for k, v := range evidence {
		out[k] = v
	}
	return out
}
