package wake

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		// Distinct notes: identical same-story same-kind rows are obvious duplicates the drain collapses.
		if _, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStatus, Note: fmt.Sprintf("note %d", i)}); err != nil {
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

func TestDrainRetiresUnusableAndAdoptsLegacyRows(t *testing.T) {
	epic := t.TempDir()
	if _, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStatus, Note: "ok"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(queuePath(epic), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"gen":"x"}` + "\n" + `{"gen":9,"story":"s","kind":"stuck","note":"legacy"}` + "\n" + `{"schema":"coxswain.wake.v9","gen":10}` + "\n")
	f.Close()
	if r, err := DrainReport(epic, true); err != nil || len(r.Retired) != 0 || len(r.Wakes) != 2 {
		t.Fatalf("peek drain = %+v %v; want 2 wakes and no retirement", r, err)
	}
	r, err := DrainReport(epic, false)
	if err != nil || len(r.Retired) != 1 || r.RetireErr != nil || len(r.Wakes) != 2 || r.Wakes[1].Note != "legacy" {
		t.Fatalf("drain = %+v %v", r, err)
	}
	if !strings.Contains(r.RetiredNotice(epic), "retired 1 unusable queue row") {
		t.Errorf("notice = %q", r.RetiredNotice(epic))
	}
	b, _ := os.ReadFile(queuePath(epic))
	if strings.Contains(string(b), `"gen":"x"`) || !strings.Contains(string(b), "coxswain.wake.v9") {
		t.Errorf("retirement rewrote the wrong rows: %s", b)
	}
	if g, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStatus, Note: "next"}); err != nil || g != 11 {
		t.Errorf("append after retirement = %d %v, want 11", g, err)
	}
}

func TestDrainDedupesOnlyObviousDuplicates(t *testing.T) {
	epic := t.TempDir()
	for _, w := range []Wake{
		{Story: "s", Kind: KindStatus, Note: "phase 2 building"},
		{Story: "s", Kind: KindStatus, Note: "phase 2 building (turn ended)"},
		{Story: "s", Kind: KindStatus, Note: "captain said use REST"},
		{Story: "t", Kind: KindStatus, Note: "phase 2 building"},
		{Story: "s", Kind: KindStuck, Note: "phase 2 building"},
		{Story: "s", Kind: KindStuck, Note: "phase 2 buildings"},
	} {
		w.Epic = "e"
		if _, err := Append(epic, w); err != nil {
			t.Fatal(err)
		}
	}
	ws, _ := Drain(epic, true)
	var got []string
	for _, w := range ws {
		got = append(got, fmt.Sprintf("%d %s %s %s", w.Gen, w.Story, w.Kind, w.Note))
	}
	want := []string{"2 s status phase 2 building (turn ended)", "3 s status captain said use REST", "4 t status phase 2 building", "5 s stuck phase 2 building", "6 s stuck phase 2 buildings"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dedupe = %q\nwant %q", got, want)
	}
}

func TestAckReportsANoOpWithTheCurrentWake(t *testing.T) {
	epic := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		if _, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStatus, Note: n}); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := Ack(epic, 1); err != nil || r.Consumed != 1 || r.Current != 3 || r.Notice(epic) != "" {
		t.Fatalf("first ack = %+v %v", r, err)
	}
	r, err := Ack(epic, 1)
	if err != nil || r.Consumed != 0 || !strings.Contains(r.Notice(epic), "nothing was acknowledged through 1") ||
		!strings.Contains(r.Notice(epic), "run cox wake ack-through 3 --epic") {
		t.Fatalf("stale ack = %+v %q %v", r, r.Notice(epic), err)
	}
}

// The queue lock records its holder ("pid=N", the form the bearings deferred worker's failed record reads, 5842d42)
// while it is held; a busy tryLock never overwrites it.
func TestLockRecordsHolderPid(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, ControlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(epic)
	if err != nil {
		t.Fatal(err)
	}
	want := "pid=" + strconv.Itoa(os.Getpid()) + "\n"
	if b, _ := os.ReadFile(LockPath(epic)); string(b) != want {
		t.Fatalf("held lock records %q, want %q", b, want)
	}
	if _, err := tryLock(epic); err == nil {
		t.Fatal("tryLock took a held lock")
	}
	if b, _ := os.ReadFile(LockPath(epic)); string(b) != want {
		t.Errorf("a refused tryLock rewrote the holder: %q", b)
	}
	unlock()
}
