package main

import (
	"os"
	"path/filepath"
	"testing"
)

// parseClaudeLog sums an assistant call's input-side tokens and output, and prices it by the message model.
func TestParseClaudeLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "s.jsonl")
	lines := "" +
		`{"type":"assistant","timestamp":"2026-09-15T10:05:00Z","message":{"model":"claude-opus-4-8","usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":4000,"cache_creation_input_tokens":0}}}` + "\n" +
		`{"type":"user","timestamp":"2026-09-15T10:05:01Z"}` + "\n" + // not an assistant call, skipped
		`{"type":"assistant","timestamp":"bad-ts","message":{"usage":{"input_tokens":9}}}` + "\n" // bad timestamp, skipped
	if err := os.WriteFile(log, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := parseClaudeLog(log)
	if len(calls) != 1 {
		t.Fatalf("want 1 usable call, got %d", len(calls))
	}
	c := calls[0]
	if c.In != 5000 { // 1000 input + 4000 cache read + 0 cache creation
		t.Fatalf("In = %d, want 5000", c.In)
	}
	if c.Out != 200 {
		t.Fatalf("Out = %d, want 200", c.Out)
	}
	// opus list price: (1000*5 + 200*25 + 4000*0.5) / 1e6 = 0.012
	if want := 0.012; c.USD < want-1e-9 || c.USD > want+1e-9 {
		t.Fatalf("USD = %v, want %v", c.USD, want)
	}
}

// A flat cache_creation_input_tokens with no ephemeral breakdown is charged at the 1h write rate (v1 behavior).
func TestParseClaudeLogFlatCacheCreation(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "s.jsonl")
	line := `{"type":"assistant","timestamp":"2026-09-15T10:05:00Z","message":{"model":"claude-opus-4-8","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":2000}}}` + "\n"
	if err := os.WriteFile(log, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := parseClaudeLog(log)
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	// 2000 cache creation at the opus 1h write rate (10/MTok) = 0.02
	if want := 0.02; calls[0].USD < want-1e-9 || calls[0].USD > want+1e-9 {
		t.Fatalf("USD = %v, want %v", calls[0].USD, want)
	}
}

// An unknown model falls back to the opus list price rather than crashing.
func TestPriceForFallback(t *testing.T) {
	if got := priceFor("some-future-model"); got != priceFor("claude-opus-4-8") {
		t.Fatalf("unknown model should default to opus price, got %+v", got)
	}
	if priceFor("claude-haiku-4-5").out != 5.0 {
		t.Fatal("haiku out price should be 5.0")
	}
}
