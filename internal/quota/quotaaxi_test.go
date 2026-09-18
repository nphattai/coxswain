package quota

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func findReading(rs []Reading, harness, model string) (Reading, bool) {
	for _, r := range rs {
		if r.Harness == harness && r.Model == model {
			return r, true
		}
	}
	return Reading{}, false
}

// A real schema-5 claude report projects to an account-wide reading and a per-model (fable) reading, both known, with
// runway and usable-runway-seconds from effectiveAvailability. codex, absent from this report, is unknown.
func TestParseSchema5ClaudeFresh(t *testing.T) {
	rs := parseQuotaAxi(mustFixture(t, "schema5-claude-fresh.json"), time.Now())

	all, ok := findReading(rs, "claude", "")
	if !ok || !all.Known || all.PercentRemaining != 39 || all.Runway != RunwayProjected || all.UsableRunwaySeconds != 164222 {
		t.Fatalf("claude all_models: %+v", all)
	}
	if all.ResetsAt == "" || len(all.WindowIDs) == 0 {
		t.Fatalf("claude all_models missing reset/windows: %+v", all)
	}
	fable, ok := findReading(rs, "claude", "fable")
	if !ok || !fable.Known || fable.PercentRemaining != 35 || fable.UsableRunwaySeconds != 138309 {
		t.Fatalf("claude model:fable: %+v", fable)
	}
	codex, ok := findReading(rs, "codex", "")
	if !ok || codex.Known {
		t.Fatalf("codex should be unknown (not in report): %+v", codex)
	}
}

// A mixed report: claude is auth_required (unknown + remedy command in the reason), codex is fresh through_reset.
func TestParseSchema5Mixed(t *testing.T) {
	rs := parseQuotaAxi(mustFixture(t, "schema5-mixed.json"), time.Now())

	claude, ok := findReading(rs, "claude", "")
	if !ok || claude.Known {
		t.Fatalf("claude should be unknown: %+v", claude)
	}
	if !strings.Contains(claude.Reason, "keychain") || !strings.Contains(claude.Reason, "remedy:") {
		t.Fatalf("claude reason should carry keychain + remedy: %q", claude.Reason)
	}
	codex, ok := findReading(rs, "codex", "")
	if !ok || !codex.Known || codex.Runway != RunwayThroughReset || codex.UsableRunwaySeconds != NoRunway {
		t.Fatalf("codex all_models: %+v", codex)
	}
}

// Schema 4 (any non-5) is drift: every harness is unknown with a schema reason.
func TestParseSchemaDrift(t *testing.T) {
	rs := parseQuotaAxi(mustFixture(t, "schema4.json"), time.Now())
	for _, r := range rs {
		if r.Known || !strings.Contains(r.Reason, "schema 4 unsupported") {
			t.Fatalf("schema 4 must be unknown: %+v", r)
		}
	}
}

// A stale provider is unknown even when it carries windows (a stale raw percentage is not current headroom).
func TestParseStale(t *testing.T) {
	rs := parseQuotaAxi(mustFixture(t, "schema5-stale.json"), time.Now())
	codex, ok := findReading(rs, "codex", "")
	if !ok || codex.Known || !strings.Contains(codex.Reason, "stale") {
		t.Fatalf("stale provider must be unknown: %+v", codex)
	}
}

// A missing binary (invoke error) yields unknown for every harness, never an error.
func TestReadMissingBinary(t *testing.T) {
	q := &QuotaAxi{run: func(context.Context) ([]byte, error) { return nil, fmt.Errorf("quota-axi not found") }}
	rs, err := q.Read(context.Background())
	if err != nil {
		t.Fatalf("Read must not error on a missing binary: %v", err)
	}
	for _, r := range rs {
		if r.Known || !strings.Contains(r.Reason, "not found") {
			t.Fatalf("missing binary must be unknown: %+v", r)
		}
	}
}

// The policy binary override wins over any PATH lookup and carries the fixed --provider/--json args.
func TestCommandBinaryOverride(t *testing.T) {
	q := &QuotaAxi{Config: QuotaAxiConfig{Binary: "/opt/quota-axi"}}
	bin, args, err := q.command()
	if err != nil || bin != "/opt/quota-axi" {
		t.Fatalf("binary override: bin=%q err=%v", bin, err)
	}
	if strings.Join(args, " ") != "--provider claude,codex --json" {
		t.Fatalf("args: %v", args)
	}
}
