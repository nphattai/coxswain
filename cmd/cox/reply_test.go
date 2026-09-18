package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nphattai/coxswain/internal/protocol/question"
	"github.com/nphattai/coxswain/internal/protocol/report"
)

// End to end over the CLI: a worker asks (report.Question), the leader answers (cox reply, file-based on the terminal
// plane), and the worker's `cox question wait` returns the answer. No live backend is configured, so the reply's ring
// is skipped, which is the intended best-effort behavior.
func TestReplyThenQuestionWaitTerminalPlane(t *testing.T) {
	t.Setenv("COX_PLANE", "terminal")
	epic := t.TempDir()
	id, err := report.Question(epic, "m10", 1, "which port range?")
	if err != nil {
		t.Fatal(err)
	}
	if code := cmdReply([]string{"m10", id, "use 41000-41099", "--epic", epic}); code != 0 {
		t.Fatalf("cox reply exit = %d, want 0", code)
	}
	// The answer file exists and an inbox record was written.
	if _, err := os.Stat(filepath.Join(question.Dir(epic, "m10"), id+".answer.md")); err != nil {
		t.Fatalf("answer file missing: %v", err)
	}
	inboxDir := filepath.Join(epic, "inbox", "m10")
	entries, _ := os.ReadDir(inboxDir)
	if len(entries) == 0 {
		t.Fatalf("no inbox record for the answer in %s", inboxDir)
	}
	if code := cmdQuestion([]string{"wait", id, "--epic", epic, "--story", "m10", "--max", "3s"}); code != 0 {
		t.Fatalf("cox question wait exit = %d, want 0", code)
	}
}

// question wait exits 3 on timeout (the worker checkpoints and parks).
func TestQuestionWaitTimeoutExit3(t *testing.T) {
	epic := t.TempDir()
	if _, err := question.Alloc(epic, "m10", "blocked?"); err != nil {
		t.Fatal(err)
	}
	if code := cmdQuestion([]string{"wait", "q001", "--epic", epic, "--story", "m10", "--max", "80ms"}); code != 3 {
		t.Fatalf("timeout exit = %d, want 3", code)
	}
}

// On the terminal plane a reply for an unknown question id is refused.
func TestReplyUnknownQuestionRefused(t *testing.T) {
	t.Setenv("COX_PLANE", "terminal")
	epic := t.TempDir()
	if code := cmdReply([]string{"m10", "q404", "hi", "--epic", epic}); code == 0 {
		t.Fatal("reply to unknown question must fail")
	}
}
