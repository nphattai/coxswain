package wake

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// Golden: read the same captured Orca batch v1 bin/test/watch-parse.sh checks and confirm the Go classifier produces
// the same verdicts (two heartbeats, one question, one worker_done). This is the port-parity guard the M2 risk names.
func TestClassifyMatchesV1Fixture(t *testing.T) {
	raw, err := os.ReadFile("../../bin/test/watch-fixture.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc struct {
		Result struct {
			Messages []struct {
				ID      string `json:"id"`
				From    string `json:"from_handle"`
				Subject string `json:"subject"`
				Body    string `json:"body"`
				Type    string `json:"type"`
			} `json:"messages"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	msgs := doc.Result.Messages
	if len(msgs) != 4 {
		t.Fatalf("expected 4 fixture messages, got %d", len(msgs))
	}
	want := []Kind{KindHeartbeat, KindHeartbeat, KindQuestion, KindWorkerDone}
	for i, m := range msgs {
		got := Classify(backend.Message{ID: m.ID, From: m.From, Subject: m.Subject, Body: m.Body, Type: m.Type})
		if got != want[i] {
			t.Errorf("message %d (%s): Classify = %q, want %q", i, m.Type, got, want[i])
		}
	}
}

func TestClassifyStatusActionable(t *testing.T) {
	cases := []struct {
		typ, subj, body string
		want            Kind
	}{
		{"status", "phase 3 done", "still working", KindStatus},
		{"status", "PR #12 ready for review", "", KindPRReady},
		{"status", "blocked on a decision", "need a ruling on the schema", KindInputRequired},
		{"status", "ready to compact", "", KindInputRequired},
		{"merge_ready", "PR up", "", KindPRReady},
		{"escalation", "cannot proceed", "", KindInputRequired},
		// F9: a re-run reports completion as a status whose subject starts "done:" (Orca allows one worker_done per
		// dispatch), and the watcher treats it as a worker_done.
		{"status", "done: F9 and F10 landed", "3-line summary", KindWorkerDone},
		{"status", "DONE: case-insensitive", "", KindWorkerDone},
		{"status", "done things but not a completion", "", KindStatus},
	}
	for _, c := range cases {
		got := Classify(backend.Message{Type: c.typ, Subject: c.subj, Body: c.body})
		if got != c.want {
			t.Errorf("Classify(%s,%q) = %q, want %q", c.typ, c.subj, got, c.want)
		}
	}
}

func TestReviewKindUrgency(t *testing.T) {
	if !IsUrgent(KindReviewDecision) {
		t.Error("review_decision must be urgent (starts a leader turn)")
	}
	if IsUrgent(KindReviewFeedback) {
		t.Error("review_feedback must be routine (batched)")
	}
}
