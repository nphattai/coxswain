package question

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Alloc assigns increasing ids under a per-story sequence and writes qNNN.md with the body.
func TestAllocSequential(t *testing.T) {
	epic := t.TempDir()
	id1, err := Alloc(epic, "m10", "first")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := Alloc(epic, "m10", "second")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != "q001" || id2 != "q002" {
		t.Fatalf("ids = %q,%q want q001,q002", id1, id2)
	}
	b, err := os.ReadFile(filepath.Join(Dir(epic, "m10"), "q001.md"))
	if err != nil || !contains(string(b), "first") {
		t.Fatalf("q001.md missing body: %q err=%v", b, err)
	}
}

// Answer refuses an unknown id and an already-answered id unless --again.
func TestAnswerRejectsUnknownAndDouble(t *testing.T) {
	epic := t.TempDir()
	if _, err := Answer(epic, "m10", "q999", "x", false); err == nil {
		t.Fatal("unknown id must error")
	}
	id, _ := Alloc(epic, "m10", "q?")
	if _, err := Answer(epic, "m10", id, "yes", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Answer(epic, "m10", id, "again", false); err == nil {
		t.Fatal("double answer without --again must error")
	}
	if _, err := Answer(epic, "m10", id, "again", true); err != nil {
		t.Fatalf("--again must allow a second reply: %v", err)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Round trip: a worker allocs a question, the leader answers, and Wait returns the answer and moves the pair to
// handled/. Exercises the same file mechanics cox reply and cox question wait drive end to end.
func TestWaitRoundTripMovesToHandled(t *testing.T) {
	epic := t.TempDir()
	id, err := Alloc(epic, "m10", "which port?")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = Answer(epic, "m10", id, "use 41000-41099", false)
	}()
	answer, timedOut, err := Wait(epic, "m10", id, 5*time.Second)
	if err != nil || timedOut {
		t.Fatalf("round trip failed: answer=%q timedOut=%v err=%v", answer, timedOut, err)
	}
	if answer != "use 41000-41099" {
		t.Fatalf("answer body = %q", answer)
	}
	// Both files moved into handled/; the live dir no longer holds the question.
	if _, err := os.Stat(filepath.Join(Dir(epic, "m10"), id+".md")); !os.IsNotExist(err) {
		t.Errorf("question should be moved out of the live dir")
	}
	if _, err := os.Stat(filepath.Join(Dir(epic, "m10"), "handled", id+".md")); err != nil {
		t.Errorf("question should be in handled/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(epic, "m10"), "handled", id+".answer.md")); err != nil {
		t.Errorf("answer should be in handled/: %v", err)
	}
}

// Wait exits with timedOut=true (which the CLI maps to exit 3) when no answer arrives, leaving the question in place.
func TestWaitTimeout(t *testing.T) {
	epic := t.TempDir()
	id, _ := Alloc(epic, "m10", "blocked?")
	_, timedOut, err := Wait(epic, "m10", id, 80*time.Millisecond)
	if err != nil || !timedOut {
		t.Fatalf("want timeout, got timedOut=%v err=%v", timedOut, err)
	}
	if _, err := os.Stat(filepath.Join(Dir(epic, "m10"), id+".md")); err != nil {
		t.Errorf("timed-out question must stay in place: %v", err)
	}
}
