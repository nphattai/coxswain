package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ControlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EventsPath(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// AppendLedger writes to the committed ledger, and Load merges the ledger with the runtime log sorted by timestamp, so
// a design_signed written to the ledger is seen alongside story lifecycle events (finding 2).
func TestLoadMergesLedgerAndRuntime(t *testing.T) {
	dir := t.TempDir()
	// A lifecycle event in the runtime log (older).
	if err := Append(dir, Event{TS: "2026-09-20T10:00:00Z", Epic: "e", Story: "s", Attempt: 1, Actor: Leader, From: Submitted, To: Working, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	// A design_signed in the durable ledger (newer).
	if err := AppendLedger(dir, Event{TS: "2026-09-21T10:00:00Z", Type: DesignSigned, Epic: "e", Story: EpicStory, Actor: Captain, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	// The ledger file lives in the epic dir, not under .cox/.
	if _, err := os.Stat(LedgerPath(dir)); err != nil {
		t.Fatalf("ledger.jsonl not written to the epic dir: %v", err)
	}
	events, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("Load must merge both logs, got %d events", len(events))
	}
	// Sorted by timestamp: the working event first, the signature second.
	if events[0].To != Working || events[1].Type != DesignSigned {
		t.Fatalf("events not merged in timestamp order: %+v", events)
	}
}

// A design_signed written to an OLDER .cox/events.jsonl (before the ledger existed) still counts as signed: Load reads
// both files, so no migration is needed (finding 2, scope note).
func TestLoadCountsLegacyEventsJSONLSignature(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, `{"schema":"coxswain.event.v1","type":"design_signed","epic":"e","story":"_epic","actor":"captain","external_confirmed":true,"ts":"2026-09-19T00:00:00Z"}
`)
	events, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	signed := false
	for _, ev := range events {
		if ev.Type == DesignSigned {
			signed = true
		}
	}
	if !signed {
		t.Fatal("a design_signed in the legacy events.jsonl must still be read by Load")
	}
}

// A missing log is not an error.
func TestLoadMissingLog(t *testing.T) {
	events, warnings, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("missing log should not error: %v", err)
	}
	if len(events) != 0 || len(warnings) != 0 {
		t.Fatalf("expected empty, got %d events %d warnings", len(events), len(warnings))
	}
}

// An unknown-schema line is skipped with a warning, not fatal; a v1 line still loads.
func TestLoadSkipsUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, `{"schema":"coxswain.event.v9","story":"a"}
{"schema":"coxswain.event.v1","story":"b","attempt":1,"to":"working","external_confirmed":false}
`)
	events, warnings, err := Load(dir)
	if err != nil {
		t.Fatalf("unknown schema should not be fatal: %v", err)
	}
	if len(events) != 1 || events[0].Story != "b" {
		t.Fatalf("expected only story b, got %+v", events)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "line 1") {
		t.Fatalf("expected a line-1 warning, got %v", warnings)
	}
}

// A corrupt JSON line is a hard error naming its line number.
func TestLoadCorruptLineNumber(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, `{"schema":"coxswain.event.v1","story":"a","attempt":1,"to":"working","external_confirmed":false}
this is not json
`)
	_, _, err := Load(dir)
	if err == nil {
		t.Fatal("expected an error on corrupt line")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error should name line 2, got: %v", err)
	}
}
