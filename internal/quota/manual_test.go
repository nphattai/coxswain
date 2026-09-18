package quota

import (
	"context"
	"os"
	"testing"
	"time"
)

// An unexpired manual entry reads as known, source manual, runway always unknown (never through_reset), with provenance.
func TestManualUnexpired(t *testing.T) {
	epic := t.TempDir()
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := WriteManual(epic, ManualEntry{Harness: "claude", Percent: 15, Until: until, Actor: LeaderActor, At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("WriteManual: %v", err)
	}
	rs, _ := (&Manual{EpicDir: epic}).Read(context.Background())
	if len(rs) != 1 {
		t.Fatalf("want 1 reading, got %d", len(rs))
	}
	r := rs[0]
	if !r.Known || r.PercentRemaining != 15 || r.Runway != RunwayUnknown || r.Source != SourceManual {
		t.Fatalf("manual reading: %+v", r)
	}
	// Owner-only file.
	info, err := os.Stat(manualPath(epic, "claude"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("manual file mode: %v err=%v", info.Mode().Perm(), err)
	}
}

// An expired manual entry reads as unknown with a reason; it never masquerades as a current reading.
func TestManualExpired(t *testing.T) {
	epic := t.TempDir()
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if err := WriteManual(epic, ManualEntry{Harness: "codex", Percent: 50, Until: past, Actor: LeaderActor, At: past}); err != nil {
		t.Fatalf("WriteManual: %v", err)
	}
	rs, _ := (&Manual{EpicDir: epic}).Read(context.Background())
	if len(rs) != 1 || rs[0].Known || rs[0].Reason == "" {
		t.Fatalf("expired must be unknown with reason: %+v", rs)
	}
}

// A manual entry never asserts a runway even at 0 percent (a bare percentage cannot prove reset-and-pace).
func TestManualNeverThroughReset(t *testing.T) {
	epic := t.TempDir()
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	_ = WriteManual(epic, ManualEntry{Harness: "claude", Percent: 0, Until: until, Actor: LeaderActor, At: until})
	rs, _ := (&Manual{EpicDir: epic}).Read(context.Background())
	if rs[0].Runway != RunwayUnknown {
		t.Fatalf("manual runway must stay unknown, got %q", rs[0].Runway)
	}
}

// ClearManual is idempotent and removes the entry.
func TestClearManual(t *testing.T) {
	epic := t.TempDir()
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	_ = WriteManual(epic, ManualEntry{Harness: "claude", Percent: 10, Until: until, Actor: LeaderActor, At: until})
	if err := ClearManual(epic, "claude"); err != nil {
		t.Fatalf("ClearManual: %v", err)
	}
	if err := ClearManual(epic, "claude"); err != nil {
		t.Fatalf("ClearManual should be idempotent: %v", err)
	}
	rs, _ := (&Manual{EpicDir: epic}).Read(context.Background())
	if len(rs) != 0 {
		t.Fatalf("cleared entry still read: %+v", rs)
	}
}
