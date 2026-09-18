package inbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// 40 concurrent writers must produce exactly 40 records with sequences 001..040, none lost or duplicated (F06).
func TestConcurrentWriteKeepsEverySequence(t *testing.T) {
	epic := t.TempDir()
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Mix fyi in so the budget does not reject writers; fyi never counts against the 5-steer budget.
			urg := FYI
			if _, err := Write(epic, "story", fmt.Sprintf("steer %d", i), urg, "force"); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}

	entries, err := os.ReadDir(Dir(epic, "story"))
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".msg") {
			s, err := seqFromName(e.Name())
			if err != nil {
				t.Fatalf("bad record name %q: %v", e.Name(), err)
			}
			seqs = append(seqs, s)
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("orphan temp left behind: %s", e.Name())
		}
	}
	sort.Ints(seqs)
	if len(seqs) != n {
		t.Fatalf("got %d records, want %d: %v", len(seqs), n, seqs)
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("sequence gap/dup at index %d: got %d want %d (all: %v)", i, s, i+1, seqs)
		}
	}
}

// A publish into a read-only inbox dir must return a real error, not a swallowed success.
func TestWritePublishErrorSurfaces(t *testing.T) {
	epic := t.TempDir()
	d := Dir(epic, "story")
	if err := os.MkdirAll(filepath.Join(d, "handled"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Make the story dir read-only so the temp create/rename fails. (root can still write, so skip when euid 0.)
	if os.Geteuid() == 0 {
		t.Skip("running as root: cannot test permission failure")
	}
	if err := os.Chmod(d, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d, 0o755)

	_, err := Write(epic, "story", "blocked", Steer, "")
	if err == nil {
		t.Fatal("expected an error writing into a read-only inbox, got nil")
	}
}

func TestSteerBudget(t *testing.T) {
	epic := t.TempDir()
	// Five steers succeed.
	for i := 0; i < DefaultBudget; i++ {
		if _, err := Write(epic, "s", fmt.Sprintf("steer %d", i), Steer, ""); err != nil {
			t.Fatalf("steer %d: %v", i, err)
		}
	}
	// fyi records never count against the budget.
	if _, err := Write(epic, "s", "fyi note", FYI, ""); err != nil {
		t.Fatalf("fyi should be free: %v", err)
	}
	// The sixth steer is rejected with ErrBudget carrying the current count.
	_, err := Write(epic, "s", "steer 6", Steer, "")
	var be *ErrBudget
	if !errors.As(err, &be) {
		t.Fatalf("want ErrBudget, got %v", err)
	}
	if be.Count != DefaultBudget {
		t.Fatalf("ErrBudget.Count = %d, want %d", be.Count, DefaultBudget)
	}
	// override forces it and records the reason in the header.
	path, err := Write(epic, "s", "steer 6 forced", Steer, "captain override")
	if err != nil {
		t.Fatalf("override should succeed: %v", err)
	}
	rec, err := parseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Override != "captain override" {
		t.Fatalf("override not recorded: %q", rec.Override)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "override=captain override") {
		t.Fatalf("override= not in header:\n%s", raw)
	}
}

// Replies (kind=reply) are budget-exempt: 7 replies plus a real steer stay under the 5-steer budget (M14, the dogfood
// bug where 7 cox reply records consumed the budget and refused a real steer). The reply record carries kind=reply and
// no override.
func TestReplyDoesNotCountAgainstSteerBudget(t *testing.T) {
	epic := t.TempDir()
	for i := 0; i < 7; i++ {
		if _, err := WriteReply(epic, "s", fmt.Sprintf("answer to q%03d: ok", i)); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}
	// A real steer still succeeds - the 7 replies did not consume the budget.
	path, err := Write(epic, "s", "please re-verify", Steer, "")
	if err != nil {
		t.Fatalf("steer after 7 replies must succeed (replies are budget-exempt), got %v", err)
	}
	// The reply record shape: kind=reply, no override, urgency=steer (so the re-ring ladder still delivers it).
	recs, err := All(epic, "s")
	if err != nil {
		t.Fatal(err)
	}
	replies := 0
	for _, r := range recs {
		if r.Kind == KindReply {
			replies++
			if r.Urgency != Steer || r.Override != "" {
				t.Errorf("reply record wrong: %+v", r)
			}
		}
	}
	if replies != 7 {
		t.Fatalf("want 7 reply records, got %d", replies)
	}
	raw, _ := os.ReadFile(recs[0].Path)
	if !strings.Contains(string(raw), "kind=reply") {
		t.Fatalf("reply header missing kind=reply:\n%s", raw)
	}

	// Budget still bites for real steers: five steers total (one already written) then the sixth is refused.
	for i := 0; i < DefaultBudget-1; i++ {
		if _, err := Write(epic, "s", fmt.Sprintf("steer %d", i), Steer, ""); err != nil {
			t.Fatalf("steer %d should fit: %v", i, err)
		}
	}
	if _, err := Write(epic, "s", "one too many", Steer, ""); !errors.As(err, new(*ErrBudget)) {
		t.Fatalf("the 6th real steer must be refused, got %v", err)
	}
	_ = path
}

func TestListOrderAndHandledAck(t *testing.T) {
	epic := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := Write(epic, "s", fmt.Sprintf("m%d", i), FYI, ""); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := List(epic, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || recs[0].Seq != 1 || recs[2].Seq != 3 {
		t.Fatalf("List order wrong: %+v", recs)
	}
	// Ack (mv to handled) removes it from the unhandled list.
	if err := Handled(recs[0]); err != nil {
		t.Fatal(err)
	}
	recs, err = List(epic, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Seq != 2 {
		t.Fatalf("after ack List wrong: %+v", recs)
	}
	// A new steer after acking still gets a fresh, non-reused sequence.
	path, err := Write(epic, "s", "m3", FYI, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(path); got != "004.msg" {
		t.Fatalf("sequence reused: got %s, want 004.msg", got)
	}
}
