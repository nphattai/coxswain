package fake

import (
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// The fake satisfies the Backend interface.
var _ backend.Backend = New()

func TestFailNextIsOneShot(t *testing.T) {
	b := New()
	b.FailNext("Probe", nil)
	if _, err := b.Probe(backend.Session{}); err == nil {
		t.Fatal("first Probe should fail")
	}
	if _, err := b.Probe(backend.Session{}); err != nil {
		t.Fatalf("second Probe should succeed, got %v", err)
	}
	if len(b.Calls) != 2 || b.Calls[0] != "Probe" {
		t.Fatalf("call log = %v", b.Calls)
	}
}

func TestStopReturnsConfiguredConfirmation(t *testing.T) {
	b := New()
	b.StopConfirmed = true
	confirmed, err := b.Stop(backend.Session{})
	if err != nil || !confirmed {
		t.Fatalf("confirmed=%v err=%v", confirmed, err)
	}
	b.FailNext("Stop", nil)
	confirmed, err = b.Stop(backend.Session{})
	if err == nil || confirmed {
		t.Fatalf("a failed stop must be unconfirmed: confirmed=%v err=%v", confirmed, err)
	}
}

// Check must not consume the queue and must not ack.
func TestMailboxCheckDoesNotConsume(t *testing.T) {
	b := New()
	mb := b.Mail().(*Mailbox)
	mb.Queue = []backend.Message{{ID: "m1"}, {ID: "m2"}}
	if got, _, _ := mb.Check(); len(got) != 2 {
		t.Fatalf("first check got %d", len(got))
	}
	if got, _, _ := mb.Check(); len(got) != 2 {
		t.Fatalf("second check got %d - Check consumed the queue", len(got))
	}
	if len(mb.Acked) != 0 {
		t.Fatalf("Check must not ack, acked=%v", mb.Acked)
	}
}
