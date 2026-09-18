package main

import (
	"flag"
	"strings"
	"testing"
)

// parseInterspersed must accept a positional before its flags (the story usage is
// `cox review poll <artifact> --epic <dir> --max ...`), which the stdlib flag package alone cannot do.
func TestParseInterspersedPositionalBeforeFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	epic := fs.String("epic", "", "")
	max := fs.Duration("max", 0, "")
	pos, ok := parseInterspersed(fs, []string{"art.html", "--epic", "/e", "--max", "2s"})
	if !ok {
		t.Fatal("parse failed")
	}
	if len(pos) != 1 || pos[0] != "art.html" || *epic != "/e" || max.String() != "2s" {
		t.Fatalf("pos=%v epic=%q max=%s", pos, *epic, max)
	}
}

// cox review share refuses to publish to ht-ml.app while policy review.share is false and no --share is given. The test
// exercises only the refusal path, never an actual share (the worker must never run lavish-axi share).
func TestReviewShareRefusedByDefault(t *testing.T) {
	if code := reviewShare([]string{"some-artifact.html", "--epic", t.TempDir()}); code != 1 {
		t.Fatalf("share without --share and share=false must be refused (exit 1), got %d", code)
	}
}

func TestReviewShareNeedsArtifact(t *testing.T) {
	if code := reviewShare([]string{"--epic", t.TempDir()}); code != 2 {
		t.Fatalf("missing artifact must be a usage error (exit 2), got %d", code)
	}
}

func TestReviewReplyNeedsMessageAndArtifact(t *testing.T) {
	if code := reviewReply([]string{"only-one", "--epic", t.TempDir()}); code != 2 {
		t.Fatalf("reply without both message and artifact must be a usage error, got %d", code)
	}
}

func TestUsageIncludesReviewReply(t *testing.T) {
	if !strings.Contains(usage, `cox review reply "<message>" <artifact>`) {
		t.Fatalf("top-level usage must advertise review reply")
	}
}
