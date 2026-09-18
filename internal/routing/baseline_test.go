package routing

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleTable = `# Baseline replays - claude - 2026-09-15

Some prose, not a table row.

| story | before sha | harness | condition | test result | leader fixes |
|---|---|---|---|---|---|
| s1 | abc | claude | bare | dry-run | - |
| s1 | abc | claude | v2 | dry-run | - |
| s1 | def | claude | bare | pass (go test ok, commit a2c781f) | 0 |
`

func TestParseTableMeasured(t *testing.T) {
	rows := parseTable(sampleTable)
	if len(rows) != 3 {
		t.Fatalf("parsed %d rows, want 3 (header, separator, and prose skipped)", len(rows))
	}
	measured := Measured(rows)
	if len(measured) != 1 {
		t.Fatalf("measured %d rows, want 1 (dry-run rows are not measurements)", len(measured))
	}
	m := measured[0]
	if m.Story != "s1" || m.Harness != "claude" || m.Condition != "bare" || m.LeaderFixes != 0 {
		t.Fatalf("measured row wrong: %+v", m)
	}
	if got, want := m.Cite(), "s1 | claude | bare | pass"; got != want {
		t.Fatalf("Cite() = %q, want %q", got, want)
	}
}

func TestParseBaselinesDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude-2026-09-15.md"), []byte(sampleTable), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := ParseBaselines(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("ParseBaselines = %d rows, want 3", len(rows))
	}

	// A missing dir is not an error (no baseline yet).
	rows, err = ParseBaselines(filepath.Join(dir, "nope"))
	if err != nil || rows != nil {
		t.Fatalf("missing dir: rows=%v err=%v, want nil,nil", rows, err)
	}
}
