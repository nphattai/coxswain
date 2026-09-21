package busy

import (
	"sync"
	"testing"
)

func TestArmSeedsBusyAndRead(t *testing.T) {
	epic := t.TempDir()
	gen, err := Arm(epic, "w1")
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if gen == "" {
		t.Fatal("Arm returned an empty gen")
	}
	if got := Read(epic, "w1"); got != Busy {
		t.Fatalf("Read after Arm = %q, want busy (the launch prompt is a submitted turn)", got)
	}
	rec, ok := ReadRecord(epic, "w1")
	if !ok || rec.Seq != 1 || rec.Gen != gen || rec.Source != "dispatch" {
		t.Fatalf("armed record = %+v ok=%v, want seq=1 gen=%s source=dispatch", rec, ok, gen)
	}
}

func TestApplyTogglesStateAndBumpsSeq(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1")
	if err := Apply(epic, "w1", Idle, gen, "pi-ext", "agent_settled"); err != nil {
		t.Fatalf("Apply idle: %v", err)
	}
	if got := Read(epic, "w1"); got != Idle {
		t.Fatalf("Read after Apply idle = %q, want idle", got)
	}
	rec, _ := ReadRecord(epic, "w1")
	if rec.Seq != 2 {
		t.Fatalf("seq after one Apply = %d, want 2 (monotonic)", rec.Seq)
	}
	if err := Apply(epic, "w1", Busy, gen, "pi-ext", "agent_start"); err != nil {
		t.Fatalf("Apply busy: %v", err)
	}
	rec, _ = ReadRecord(epic, "w1")
	if rec.Seq != 3 || rec.State != Busy {
		t.Fatalf("record after second Apply = %+v, want seq=3 state=busy", rec)
	}
}

func TestApplyRejectsStaleGen(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1")
	// A re-arm mints a new incarnation; an Apply carrying the old gen is now stale and must be rejected.
	newGen, _ := Arm(epic, "w1")
	if newGen == gen {
		t.Fatal("re-Arm reused the gen")
	}
	if err := Apply(epic, "w1", Idle, gen, "pi-ext", "agent_settled"); err == nil {
		t.Fatal("Apply with a stale gen must be rejected")
	}
	// The stale event must not have mutated the live state (re-arm seeded busy).
	if got := Read(epic, "w1"); got != Busy {
		t.Fatalf("state after a rejected stale Apply = %q, want busy (unchanged)", got)
	}
	// The current gen still works.
	if err := Apply(epic, "w1", Idle, newGen, "pi-ext", "agent_settled"); err != nil {
		t.Fatalf("Apply with the current gen: %v", err)
	}
	if got := Read(epic, "w1"); got != Idle {
		t.Fatalf("state after a valid Apply = %q, want idle", got)
	}
}

func TestApplyUnarmedRejected(t *testing.T) {
	epic := t.TempDir()
	if err := Apply(epic, "w1", Idle, "gsomething", "pi-ext", "agent_settled"); err == nil {
		t.Fatal("Apply on an unarmed story must be rejected (no record to bind to)")
	}
}

func TestReadAbsentIsUnknown(t *testing.T) {
	if got := Read(t.TempDir(), "nobody"); got != Unknown {
		t.Fatalf("Read of an absent record = %q, want unknown (never a guess)", got)
	}
}

func TestApplyValidatesInput(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1")
	if err := Apply(epic, "w1", "sleeping", gen, "pi-ext", "e"); err == nil {
		t.Fatal("Apply with an invalid state must be rejected")
	}
	if err := Apply(epic, "w1", Idle, gen, "bad source", "e"); err == nil {
		t.Fatal("Apply with a source carrying a space must be rejected (token charset)")
	}
}

// TestConcurrentApplyIsSerialized proves the writer lock keeps seq monotonic under concurrent writers: N applies with
// the current gen all land, and the final seq reflects every one (no lost update).
func TestConcurrentApplyIsSerialized(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1")
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Apply(epic, "w1", Idle, gen, "pi-ext", "agent_settled")
		}()
	}
	wg.Wait()
	rec, _ := ReadRecord(epic, "w1")
	if rec.Seq != n+1 { // seq started at 1 (Arm), +1 per applied event
		t.Fatalf("seq after %d concurrent applies = %d, want %d (lock lost an update)", n, rec.Seq, n+1)
	}
}
