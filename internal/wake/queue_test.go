package wake

import (
	"sync"
	"testing"
	"time"
)

func TestAppendAssignsIncreasingGen(t *testing.T) {
	epic := t.TempDir()
	for i := 1; i <= 3; i++ {
		gen, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStatus})
		if err != nil {
			t.Fatal(err)
		}
		if gen != i {
			t.Fatalf("gen = %d, want %d", gen, i)
		}
	}
}

func TestConcurrentAppendUniqueGen(t *testing.T) {
	epic := t.TempDir()
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStale}); err != nil {
				t.Errorf("append: %v", err)
			}
		}()
	}
	wg.Wait()
	all, err := Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n {
		t.Fatalf("got %d wakes, want %d", len(all), n)
	}
	seen := map[int]bool{}
	for _, w := range all {
		if seen[w.Gen] {
			t.Fatalf("duplicate gen %d", w.Gen)
		}
		seen[w.Gen] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("missing gen %d", i)
		}
	}
}

func TestDrainAckThroughIdempotent(t *testing.T) {
	epic := t.TempDir()
	for i := 0; i < 5; i++ {
		if _, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStatus}); err != nil {
			t.Fatal(err)
		}
	}
	un, err := Drain(epic, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 5 {
		t.Fatalf("drain got %d, want 5", len(un))
	}
	// Ack through gen 3.
	if err := AckThrough(epic, 3); err != nil {
		t.Fatal(err)
	}
	un, _ = Drain(epic, true)
	if len(un) != 2 || un[0].Gen != 4 {
		t.Fatalf("after ack 3, drain = %+v", un)
	}
	// Idempotent: acking through a lower gen does nothing.
	if err := AckThrough(epic, 1); err != nil {
		t.Fatal(err)
	}
	un, _ = Drain(epic, true)
	if len(un) != 2 {
		t.Fatalf("ack regression: drain = %d", len(un))
	}
	// Ack the rest.
	if err := AckThrough(epic, 5); err != nil {
		t.Fatal(err)
	}
	un, _ = Drain(epic, true)
	if len(un) != 0 {
		t.Fatalf("expected empty drain, got %d", len(un))
	}
}

func TestWaitReturnsOnExistingWake(t *testing.T) {
	epic := t.TempDir()
	if _, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindQuestion}); err != nil {
		t.Fatal(err)
	}
	w, timedOut, err := Wait(epic, time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || len(w) != 1 {
		t.Fatalf("Wait should return the queued wake at once: timedOut=%v n=%d", timedOut, len(w))
	}
}

func TestWaitTimesOut(t *testing.T) {
	epic := t.TempDir()
	start := time.Now()
	w, timedOut, err := Wait(epic, 60*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !timedOut || len(w) != 0 {
		t.Fatalf("expected timeout with no wakes: timedOut=%v n=%d", timedOut, len(w))
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("Wait returned before the deadline")
	}
}

func TestWaitWakesOnNewAppend(t *testing.T) {
	epic := t.TempDir()
	go func() {
		time.Sleep(30 * time.Millisecond)
		Append(epic, Wake{Epic: "e", Story: "s", Kind: KindWorkerDone})
	}()
	w, timedOut, err := Wait(epic, time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || len(w) != 1 || w[0].Kind != KindWorkerDone {
		t.Fatalf("Wait did not pick up the late append: timedOut=%v w=%+v", timedOut, w)
	}
}
