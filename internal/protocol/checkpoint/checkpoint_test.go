package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCheckpoint(t *testing.T, epic, story, attempt, head string) {
	t.Helper()
	dir := filepath.Join(epic, "handoffs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" +
		"schema: coxswain.checkpoint.v1\n" +
		"story: " + story + "\n" +
		"attempt: " + attempt + "\n" +
		"head: " + head + "\n" +
		"base: origin/epic/e@000\n" +
		"written_at: 2026-09-15T00:00:00Z\n" +
		"reason: park\n" +
		"---\n" +
		"## Next action\nRerun the failing test.\n"
	if err := os.WriteFile(filepath.Join(dir, story+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeStory(t *testing.T, epic, story string) {
	t.Helper()
	dir := filepath.Join(epic, "stories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: " + story + "\n---\n## Read first\n- Contract: DESIGN.md\n\n## Goal\nBuild it.\n"
	if err := os.WriteFile(filepath.Join(dir, story+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInjectRejectsWrongAttempt(t *testing.T) {
	epic := t.TempDir()
	writeCheckpoint(t, epic, "s", "2", "abc123")
	_, err := Inject(epic, "s", 1, "abc123")
	var wa *ErrWrongAttempt
	if !errors.As(err, &wa) {
		t.Fatalf("want ErrWrongAttempt, got %v", err)
	}
	if wa.Got != 2 || wa.Want != 1 {
		t.Fatalf("ErrWrongAttempt fields wrong: %+v", wa)
	}
}

func TestInjectWarnsOnStaleHead(t *testing.T) {
	epic := t.TempDir()
	writeCheckpoint(t, epic, "s", "2", "oldsha")
	writeStory(t, epic, "s")
	inj, err := Inject(epic, "s", 2, "newsha")
	if err != nil {
		t.Fatal(err)
	}
	if !inj.Stale {
		t.Fatal("expected Stale=true")
	}
	first := strings.SplitN(inj.Text, "\n", 2)[0]
	if !strings.HasPrefix(first, "CHECKPOINT STALE: head oldsha") {
		t.Fatalf("first line is not the stale warning: %q", first)
	}
	if !strings.Contains(inj.Text, "Read first") || !strings.Contains(inj.Text, "DESIGN.md") {
		t.Fatalf("Read first block not injected:\n%s", inj.Text)
	}
	if !strings.Contains(inj.Text, "Rerun the failing test") {
		t.Fatalf("checkpoint body not injected:\n%s", inj.Text)
	}
}

func TestInjectMatchingHeadNoWarning(t *testing.T) {
	epic := t.TempDir()
	writeCheckpoint(t, epic, "s", "1", "samesha")
	inj, err := Inject(epic, "s", 1, "samesha")
	if err != nil {
		t.Fatal(err)
	}
	if inj.Stale {
		t.Fatal("expected Stale=false when heads match")
	}
	if strings.Contains(inj.Text, "CHECKPOINT STALE") {
		t.Fatalf("stale warning should be absent:\n%s", inj.Text)
	}
}

func TestHeadMatches(t *testing.T) {
	full := "62a7daf682fb1c2d3e4f5a6b7c8d9e0f11223344"
	short := "62a7daf"
	cases := []struct {
		a, b string
		want bool
	}{
		{full, short, true},                  // full checkpoint vs short HEAD
		{short, full, true},                  // short checkpoint vs full HEAD (the park bug)
		{full, full, true},                   // identical full
		{short, short, true},                 // identical short (>= 7)
		{full, "62a7daf682fb1c2d3e4f", true}, // two lengths, same prefix
		{full, "deadbeef1234", false},        // genuinely different head
		{"62a7da", "62a7daf682fb", false},    // shorter side < 7 chars: not confident
		{"unknown", full, false},             // unknown never matches
		{"", full, false},                    // empty never matches
	}
	for _, c := range cases {
		if got := HeadMatches(c.a, c.b); got != c.want {
			t.Errorf("HeadMatches(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A checkpoint that recorded a short sha is not stale against a full current HEAD sharing that prefix, and vice versa.
func TestInjectHeadPrefixNotStale(t *testing.T) {
	full := "62a7daf682fb1c2d3e4f5a6b7c8d9e0f11223344"
	short := "62a7daf"

	epic := t.TempDir()
	writeCheckpoint(t, epic, "s", "1", short) // old checkpoint wrote a short sha
	inj, err := Inject(epic, "s", 1, full)    // current HEAD is the full sha
	if err != nil {
		t.Fatal(err)
	}
	if inj.Stale {
		t.Fatalf("short checkpoint vs full HEAD must not be stale:\n%s", inj.Text)
	}

	epic2 := t.TempDir()
	writeCheckpoint(t, epic2, "s", "1", full) // checkpoint wrote a full sha
	inj2, err := Inject(epic2, "s", 1, short) // current HEAD reported short
	if err != nil {
		t.Fatal(err)
	}
	if inj2.Stale {
		t.Fatalf("full checkpoint vs short HEAD must not be stale:\n%s", inj2.Text)
	}
}

func TestInjectMissingCheckpointIsNotAnError(t *testing.T) {
	epic := t.TempDir()
	writeStory(t, epic, "s")
	inj, err := Inject(epic, "s", 1, "abc")
	if err != nil {
		t.Fatalf("missing checkpoint should not error: %v", err)
	}
	if !strings.Contains(inj.Text, "No checkpoint") || !strings.Contains(inj.Text, "Read first") {
		t.Fatalf("expected first-attempt guidance + Read first:\n%s", inj.Text)
	}
}

func TestValidate(t *testing.T) {
	good := Frontmatter{Schema: Schema, Story: "s", Attempt: 1, Head: "a", Base: "b", WrittenAt: "t", Reason: "park"}
	if err := good.Validate(); err != nil {
		t.Fatalf("good frontmatter rejected: %v", err)
	}
	bad := good
	bad.Reason = "nope"
	if err := bad.Validate(); err == nil {
		t.Fatal("bad reason accepted")
	}
	bad = good
	bad.Attempt = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("attempt 0 accepted")
	}
}

func TestFactsMarkdown(t *testing.T) {
	// Facts on a non-repo temp dir: git degrades to unknown, inbox listing empty. Markdown must still render the block.
	epic := t.TempDir()
	f, err := Facts(epic, epic, "s")
	if err != nil {
		t.Fatal(err)
	}
	md := f.Markdown()
	if !strings.HasPrefix(md, "## Facts (máy tính)") {
		t.Fatalf("facts block header wrong:\n%s", md)
	}
	if !strings.Contains(md, "head: unknown") || !strings.Contains(md, "CI: unknown") {
		t.Fatalf("expected unknown git/CI:\n%s", md)
	}
}
