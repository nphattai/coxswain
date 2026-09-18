package status

import (
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/state"
)

func TestReportAppendsEventAndMails(t *testing.T) {
	epic := t.TempDir()
	b := fake.New()
	if err := Report(epic, "s", 2, "phase 3", "tests green", b.Mail(), "term_leader"); err != nil {
		t.Fatal(err)
	}
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.From != state.Working || ev.To != state.Working {
		t.Fatalf("status must be working->working, got %s->%s", ev.From, ev.To)
	}
	if ev.Evidence["phase"] != "phase 3" || ev.Evidence["note"] != "tests green" {
		t.Fatalf("evidence wrong: %+v", ev.Evidence)
	}
	// Folded state stays working: a status is a log, not a transition.
	snap := state.Fold(events)
	if snap.Stories["s"].State != state.Working {
		t.Fatalf("status changed the state to %s", snap.Stories["s"].State)
	}
	mb := b.Mail().(*fake.Mailbox)
	if len(mb.Sent) != 1 || mb.Sent[0].Type != "status" {
		t.Fatalf("expected one status mail, got %+v", mb.Sent)
	}
}

func TestReportWithoutMailStillLogs(t *testing.T) {
	epic := t.TempDir()
	if err := Report(epic, "s", 1, "phase 1", "note", nil, ""); err != nil {
		t.Fatal(err)
	}
	events, _, _ := state.Load(epic)
	if len(events) != 1 {
		t.Fatalf("event should land even without a mailbox: %d", len(events))
	}
}
