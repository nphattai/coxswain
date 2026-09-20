package pi

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const sessionHeader = `{"type":"session","version":3,"id":"sess-1","timestamp":"2026-09-20T10:00:00.000Z","cwd":"/wt"}`

func asstUsage(input, cacheRead, cacheWrite, cacheWrite1h int) string {
	return `{"type":"message","id":"a","parentId":null,"message":{"role":"assistant","content":[{"type":"text","text":"hi"}],` +
		`"model":"anthropic/claude-opus-4-8","stopReason":"stop","usage":{"input":` + strconv.Itoa(input) +
		`,"output":40,"cacheRead":` + strconv.Itoa(cacheRead) + `,"cacheWrite":` + strconv.Itoa(cacheWrite) +
		`,"cacheWrite1h":` + strconv.Itoa(cacheWrite1h) + `,"totalTokens":9999,"cost":{"total":0.1}}}}`
}

// writeSession writes a session file for worktree into the Pi-derived session dir under home, and returns the adapter.
func writeSession(t *testing.T, home, worktree, name string, lines ...string) {
	t.Helper()
	dir := sessionDir(home, worktree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A single session with an assistant turn reports the last assistant usage's context tokens (input + all cache) and the
// assistant turn count.
func TestTelemetryKnownWithCache(t *testing.T) {
	home, wt := t.TempDir(), "/wt"
	writeSession(t, home, wt, "2026_sess-1.jsonl",
		sessionHeader,
		`{"type":"message","id":"u","message":{"role":"user","content":"go"}}`,
		asstUsage(1000, 200, 300, 100), // context = 1000+200+300+100 = 1600
	)
	h := &Harness{Home: home}
	ctx, err := h.Telemetry(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.Known || ctx.Tokens != 1600 || ctx.Turns != 1 {
		t.Fatalf("telemetry = %+v, want Known tokens=1600 turns=1", ctx)
	}
}

// After a compaction the last assistant turn carries the reduced context, so taking the last assistant usage reflects
// compaction without special handling; turns counts both assistant messages.
func TestTelemetryCompactionUsesLastUsage(t *testing.T) {
	home, wt := t.TempDir(), "/wt"
	writeSession(t, home, wt, "s.jsonl",
		sessionHeader,
		asstUsage(50000, 0, 0, 0), // pre-compaction, large
		`{"type":"compaction","id":"c","parentId":"a","summary":"...","firstKeptEntryId":"a","tokensBefore":50000}`,
		asstUsage(800, 100, 0, 0), // post-compaction, small = 900
	)
	ctx, _ := (&Harness{Home: home}).Telemetry(wt)
	if !ctx.Known || ctx.Tokens != 900 || ctx.Turns != 2 {
		t.Fatalf("telemetry = %+v, want Known tokens=900 turns=2", ctx)
	}
}

// A truncated tool result and a malformed line do not break the read: the assistant usage is still parsed.
func TestTelemetryTruncatedAndMalformedLinesSkipped(t *testing.T) {
	home, wt := t.TempDir(), "/wt"
	writeSession(t, home, wt, "s.jsonl",
		sessionHeader,
		`{"type":"message","id":"tr","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"x"}],"truncated":true,"isError":false}}`,
		`{ this is not valid json`,
		asstUsage(1200, 0, 0, 0),
	)
	ctx, _ := (&Harness{Home: home}).Telemetry(wt)
	if !ctx.Known || ctx.Tokens != 1200 {
		t.Fatalf("telemetry = %+v, want Known tokens=1200", ctx)
	}
}

// A session with no assistant usage is Known=false, never 0 tokens (F11).
func TestTelemetryNoAssistantUsageUnknown(t *testing.T) {
	home, wt := t.TempDir(), "/wt"
	writeSession(t, home, wt, "s.jsonl", sessionHeader,
		`{"type":"message","id":"u","message":{"role":"user","content":"go"}}`)
	ctx, _ := (&Harness{Home: home}).Telemetry(wt)
	if ctx.Known {
		t.Fatalf("no assistant usage must be Known=false, got %+v", ctx)
	}
}

// A missing session directory is Known=false (unknown), never 0.
func TestTelemetryAbsentUnknown(t *testing.T) {
	ctx, _ := (&Harness{Home: t.TempDir()}).Telemetry("/wt")
	if ctx.Known {
		t.Fatalf("absent session must be Known=false, got %+v", ctx)
	}
}

// More than one session for the worktree is ambiguous -> Known=false (no newest-session heuristic, DESIGN section 5).
func TestTelemetryMultipleSessionsAmbiguousUnknown(t *testing.T) {
	home, wt := t.TempDir(), "/wt"
	writeSession(t, home, wt, "one.jsonl", sessionHeader, asstUsage(100, 0, 0, 0))
	writeSession(t, home, wt, "two.jsonl", sessionHeader, asstUsage(200, 0, 0, 0))
	ctx, _ := (&Harness{Home: home}).Telemetry(wt)
	if ctx.Known {
		t.Fatalf("ambiguous multi-session must be Known=false, got %+v", ctx)
	}
}
