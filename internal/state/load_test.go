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
