package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ControlDir is the per-epic control directory holding the runtime event log.
const ControlDir = ".cox"

// EventsFile is the append-only runtime event log inside ControlDir (story lifecycle events). It is machine-local and
// discarded on attach/close, so it must never hold durable epic history.
const EventsFile = "events.jsonl"

// LedgerFile is the append-only durable epic log, committed in the epic dir (not under .cox/). Epic-scoped facts that
// must survive a machine move - design_signed, design_amended - are written here so they travel with git clone and a
// re-attach needs no replay (finding 2). Story lifecycle events stay in EventsFile.
const LedgerFile = "ledger.jsonl"

// EventsPath returns <epic>/.cox/events.jsonl.
func EventsPath(epicDir string) string {
	return filepath.Join(epicDir, ControlDir, EventsFile)
}

// LedgerPath returns <epic>/ledger.jsonl.
func LedgerPath(epicDir string) string {
	return filepath.Join(epicDir, LedgerFile)
}

// Append writes one event as a single line to <epic>/.cox/events.jsonl. It takes an exclusive file lock
// (syscall.Flock, which works on macOS and Linux) around the write so concurrent appenders never interleave, marshals
// to one newline-free JSON object, writes it plus "\n" in a single call, and fsyncs. It never writes a partial line:
// marshal failures happen before any write, and the record is one write of one complete line.
//
// ev.Schema and ev.TS default to the current schema and now (RFC 3339 UTC) when empty, so callers only fill the
// transition fields.
func Append(epicDir string, ev Event) error {
	if err := validatePendingExternal(ev); err != nil {
		return err
	}
	return appendLine(filepath.Join(epicDir, ControlDir), EventsFile, ev)
}

// AppendLedger writes one durable epic event to <epic>/ledger.jsonl (design_signed / design_amended), so it travels
// with git and survives a machine move and re-attach (finding 2). It uses the same schema, defaults and locked,
// fsync'd single-line write as Append.
func AppendLedger(epicDir string, ev Event) error {
	return appendLine(epicDir, LedgerFile, ev)
}

// appendLine marshals ev to one line and appends it to <dir>/<file> under an exclusive flock, fsync'd. Schema and TS
// default to the current schema and now (RFC 3339 UTC) when empty. It never writes a partial line: marshal happens
// before any write, and the record is one write of one complete line.
func appendLine(dir, file string, ev Event) error {
	if ev.Schema == "" {
		ev.Schema = Schema
	}
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	line = append(line, '\n')

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", file, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", file, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync %s: %w", file, err)
	}
	return nil
}

// validatePendingExternal enforces the pending_external contract at the one chokepoint every writer routes through: a
// transition that lands on pending_external without external confirmation MUST name the state it is trying to reach in
// evidence.intended_to, and that name must be a real state. Without it, Reconcile has nothing to finish the transition
// toward after a crash between the two appends (event.v1, M2). A confirmed pending_external (rare) is exempt, as is any
// non-pending transition.
func validatePendingExternal(ev Event) error {
	if ev.To != PendingExternal || ev.ExternalConfirmed {
		return nil
	}
	intended, _ := ev.Evidence["intended_to"].(string)
	if intended == "" {
		return fmt.Errorf("pending_external event for story %q missing evidence.intended_to (reconcile cannot finish it)", ev.Story)
	}
	switch State(intended) {
	case Working, InputRequired, Parked, Completed, Failed, Canceled:
		return nil
	default:
		return fmt.Errorf("pending_external event for story %q has invalid intended_to %q", ev.Story, intended)
	}
}
