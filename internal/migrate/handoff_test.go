package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
)

// A v1 handoff is free-form markdown with no frontmatter.
const v1Handoff = "# Handoff - story-a\n\nUpdated 2026-09-15. Phase 3 done, waiting on leader.\n\n## Branch\nstory/story-a at abc123.\n"

func writeHandoffEpic(t *testing.T, story, body string) string {
	t.Helper()
	epic := t.TempDir()
	hdir := filepath.Join(epic, "handoffs")
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hdir, story+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return epic
}

func TestWrapHandoffPreservesBodyAndInjects(t *testing.T) {
	epic := writeHandoffEpic(t, "story-a", v1Handoff)
	wraps, err := handoffWraps(epic, map[string]string{}) // no worktree => head unknown
	if err != nil {
		t.Fatal(err)
	}
	if len(wraps) != 1 || !wraps[0].Wrapped {
		t.Fatalf("want 1 wrappable handoff, got %+v", wraps)
	}
	if err := wrapHandoffs(wraps); err != nil {
		t.Fatal(err)
	}

	path := checkpoint.Path(epic, "story-a")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Body is byte-equal: the wrapped file is the frontmatter block followed by the original bytes unchanged.
	if !bytes.HasSuffix(got, []byte(v1Handoff)) {
		t.Errorf("wrapped body not byte-equal to original:\n%s", got)
	}
	// The backup holds the exact original.
	if b, err := os.ReadFile(path + ".v1"); err != nil || string(b) != v1Handoff {
		t.Errorf("backup .v1 mismatch: err=%v body=%q", err, b)
	}
	// Inject accepts the wrapped checkpoint at attempt 1.
	inj, err := checkpoint.Inject(epic, "story-a", 1, "")
	if err != nil {
		t.Fatalf("Inject rejected wrapped handoff: %v", err)
	}
	if !strings.Contains(inj.Text, "reason migrated-v1") {
		t.Errorf("injection missing migrated-v1 reason:\n%s", inj.Text)
	}
}

func TestWrapHandoffSkipsExistingCheckpoint(t *testing.T) {
	existing := "---\nschema: " + checkpoint.Schema + "\nstory: story-b\nattempt: 2\nhead: deadbeef\nbase: origin/epic@x\nwritten_at: 2026-09-15T00:00:00Z\nreason: phase-end\n---\nbody stays\n"
	epic := writeHandoffEpic(t, "story-b", existing)
	wraps, err := handoffWraps(epic, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(wraps) != 1 || wraps[0].Wrapped {
		t.Fatalf("checkpoint.v1 file should be marked not-wrappable, got %+v", wraps)
	}
	if err := wrapHandoffs(wraps); err != nil {
		t.Fatal(err)
	}
	// Unchanged, and no spurious backup written.
	if b, _ := os.ReadFile(checkpoint.Path(epic, "story-b")); string(b) != existing {
		t.Errorf("existing checkpoint changed:\n%s", b)
	}
	if _, err := os.Stat(checkpoint.Path(epic, "story-b") + ".v1"); !os.IsNotExist(err) {
		t.Error("no backup should be written for an already-wrapped checkpoint")
	}
}

// A second wrap must not overwrite the .v1 backup with the (now already-wrapped) file.
func TestWrapHandoffBackupNotClobbered(t *testing.T) {
	epic := writeHandoffEpic(t, "story-a", v1Handoff)
	wraps, _ := handoffWraps(epic, map[string]string{})
	if err := wrapHandoffs(wraps); err != nil {
		t.Fatal(err)
	}
	// Re-scan: the file now has frontmatter, so it is not wrappable; even if forced, the backup stays the original.
	wraps2, _ := handoffWraps(epic, map[string]string{})
	if wraps2[0].Wrapped {
		t.Fatal("already-wrapped file should not be wrappable on re-scan")
	}
	if b, _ := os.ReadFile(checkpoint.Path(epic, "story-a") + ".v1"); string(b) != v1Handoff {
		t.Errorf("backup was clobbered on re-run: %q", b)
	}
}
