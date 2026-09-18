package main

import (
	"errors"
	"flag"
	"io"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
)

// Flags may appear before, between, or after the two positionals; parseInterleaved recovers story and text in order.
func TestParseInterleaved(t *testing.T) {
	cases := [][]string{
		{"s1", "hello there", "--epic", "/e", "--fyi"},                      // flags after
		{"--epic", "/e", "s1", "hello there", "--fyi"},                      // flags before
		{"--epic", "/e", "s1", "--fyi", "hello there"},                      // flag between positionals
		{"s1", "--fyi", "hello there", "--override", "why", "--epic", "/e"}, // interleaved
	}
	for _, args := range cases {
		fs := flag.NewFlagSet("steer", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		epic := fs.String("epic", "", "")
		fyi := fs.Bool("fyi", false, "")
		fs.String("override", "", "")
		pos, err := parseInterleaved(fs, args)
		if err != nil {
			t.Fatalf("%v: parse error %v", args, err)
		}
		if len(pos) < 2 || pos[0] != "s1" || pos[1] != "hello there" {
			t.Fatalf("%v: positionals = %v, want [s1, \"hello there\"]", args, pos)
		}
		if *epic != "/e" || !*fyi {
			t.Fatalf("%v: epic=%q fyi=%v, want /e true", args, *epic, *fyi)
		}
	}
}

func TestSteerKnock(t *testing.T) {
	// No backend or no session -> no-session.
	if got := steerKnock(nil, backend.Session{}, false, "door"); got != "no-session" {
		t.Fatalf("nil backend = %q, want no-session", got)
	}
	if got := steerKnock(fake.New(), backend.Session{ID: "x"}, false, "door"); got != "no-session" {
		t.Fatalf("no session = %q, want no-session", got)
	}
	// A live empty composer rings.
	b := fake.New() // SendRang defaults true
	if got := steerKnock(b, backend.Session{ID: "x"}, true, "door"); got != "rang" {
		t.Fatalf("empty composer = %q, want rang", got)
	}
	// A busy composer is skipped, not rung.
	busy := fake.New()
	busy.SendRang = false
	if got := steerKnock(busy, backend.Session{ID: "x"}, true, "door"); got != "skipped:busy" {
		t.Fatalf("busy composer = %q, want skipped:busy", got)
	}
	// A send error is surfaced, never a false ring.
	boom := fake.New()
	boom.FailNext("Send", errors.New("nope"))
	if got := steerKnock(boom, backend.Session{ID: "x"}, true, "door"); got != "error:nope" {
		t.Fatalf("send error = %q, want error:nope", got)
	}
}

// steer --ring rings the doorbell for an existing unhandled steer without writing a new record or spending budget.
func TestSteerRing(t *testing.T) {
	epic := t.TempDir()

	// Missing args -> usage error.
	if got := steerRing("", "s"); got != 2 {
		t.Fatalf("missing epic = %d, want 2", got)
	}
	if got := steerRing(epic, ""); got != 2 {
		t.Fatalf("missing story = %d, want 2", got)
	}

	// No unhandled inbox -> nothing to re-ring, but success.
	if got := steerRing(epic, "s"); got != 0 {
		t.Fatalf("empty inbox = %d, want 0", got)
	}

	// One unhandled steer -> re-ring writes no new record (no budget spend).
	if _, err := inbox.Write(epic, "s", "please fix", inbox.Steer, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := inbox.List(epic, "s")
	if got := steerRing(epic, "s"); got != 0 {
		t.Fatalf("re-ring = %d, want 0", got)
	}
	after, _ := inbox.List(epic, "s")
	if len(after) != len(before) {
		t.Fatalf("re-ring changed inbox count: before %d after %d (must not write a record)", len(before), len(after))
	}
}
