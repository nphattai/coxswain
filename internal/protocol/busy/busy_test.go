package busy

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// claudeSources / piSources mirror the trust tables the capability cards carry (adapter cards own the real list); a
// harness trusts its own hook plus the leader-side dispatch/interrupt/recovery writers.
var (
	claudeSources = []string{"claude-hook", "dispatch", "interrupt", "recovery"}
	piSources     = []string{"pi-ext", "dispatch", "interrupt", "recovery"}
)

func TestArmSeedsBusyAndRead(t *testing.T) {
	epic := t.TempDir()
	gen, err := Arm(epic, "w1", "pi", piSources)
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
	if !ok || rec.Seq != 1 || rec.Gen != gen || rec.Source != "dispatch" || rec.Harness != "pi" {
		t.Fatalf("armed record = %+v ok=%v, want seq=1 gen=%s source=dispatch harness=pi", rec, ok, gen)
	}
}

func TestApplyTogglesStateAndBumpsSeq(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1", "pi", piSources)
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
	// The trust table survives an Apply so later events keep enforcing it.
	if len(rec.Sources) == 0 || rec.Harness != "pi" {
		t.Fatalf("record after Apply lost its trust table: harness=%q sources=%v", rec.Harness, rec.Sources)
	}
}

func TestApplyRejectsStaleGen(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1", "pi", piSources)
	// A re-arm mints a new incarnation; an Apply carrying the old gen is now stale and must be rejected.
	newGen, _ := Arm(epic, "w1", "pi", piSources)
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

// TestApplyRejectsUntrustedSource proves the source trust table is enforced on Apply: a claude story (whose card does
// not list pi-ext) rejects a pi-ext event even with the current gen, and the live state is unchanged. This fails on the
// base sha, where Apply enforced only the gen and any source could mutate the record.
func TestApplyRejectsUntrustedSource(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1", "claude", claudeSources)
	if err := Apply(epic, "w1", Idle, gen, "pi-ext", "agent_settled"); err == nil {
		t.Fatal("Apply with a source the harness does not trust must be rejected")
	}
	if got := Read(epic, "w1"); got != Busy {
		t.Fatalf("state after a rejected untrusted Apply = %q, want busy (unchanged)", got)
	}
	// The harness's own trusted source is accepted.
	if err := Apply(epic, "w1", Idle, gen, "claude-hook", "stop"); err != nil {
		t.Fatalf("Apply with a trusted source: %v", err)
	}
	if got := Read(epic, "w1"); got != Idle {
		t.Fatalf("state after a trusted Apply = %q, want idle", got)
	}
}

// TestReadUntrustedSourceIsUnknown proves the read side is fail-closed too: a record whose current source is not in its
// trust table (e.g. one written out-of-band) reads as Unknown, so an untrusted writer never classifies a story. Base
// sha returns the raw state.
func TestReadUntrustedSourceIsUnknown(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1", "claude", claudeSources)
	// Craft a record out-of-band with a trusted-looking gen but an untrusted source (bypassing Apply's gate).
	rogue := Record{Schema: Schema, State: Idle, Gen: gen, Seq: 9, TS: 1, Source: "pi-ext", Event: "agent_settled", Harness: "claude", Sources: claudeSources}
	if err := write(Path(epic, "w1"), rogue); err != nil {
		t.Fatalf("write rogue record: %v", err)
	}
	if got := Read(epic, "w1"); got != Unknown {
		t.Fatalf("Read of a record written by an untrusted source = %q, want unknown", got)
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
	gen, _ := Arm(epic, "w1", "pi", piSources)
	if err := Apply(epic, "w1", "sleeping", gen, "pi-ext", "e"); err == nil {
		t.Fatal("Apply with an invalid state must be rejected")
	}
	if err := Apply(epic, "w1", Idle, gen, "bad source", "e"); err == nil {
		t.Fatal("Apply with a source carrying a space must be rejected (token charset)")
	}
}

// TestRetireExactGen proves Retire removes only the incarnation whose gen it is given: a stale gen keeps the record, the
// current gen removes it, and a second Retire on the now-absent record is a no-op. Base sha has no Retire at all.
func TestRetireExactGen(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1", "pi", piSources)
	if err := Retire(epic, "w1", "gstale.deadbeef"); err == nil {
		t.Fatal("Retire with a non-matching gen must be refused (a late retire must not clobber a newer record)")
	}
	if _, ok := ReadRecord(epic, "w1"); !ok {
		t.Fatal("record removed by a stale-gen Retire")
	}
	if err := Retire(epic, "w1", gen); err != nil {
		t.Fatalf("Retire with the current gen: %v", err)
	}
	if _, ok := ReadRecord(epic, "w1"); ok {
		t.Fatal("record survived a matching Retire")
	}
	if err := Retire(epic, "w1", gen); err != nil {
		t.Fatalf("Retire of an already-absent record must be a no-op, got %v", err)
	}
}

// TestRetireDropsNewerIncarnationSafely proves that a retire from an old incarnation cannot delete a record a re-arm
// minted in between: Retire(oldGen) fails, and the newer record is intact.
func TestRetireDropsNewerIncarnationSafely(t *testing.T) {
	epic := t.TempDir()
	oldGen, _ := Arm(epic, "w1", "pi", piSources)
	newGen, _ := Arm(epic, "w1", "pi", piSources)
	if err := Retire(epic, "w1", oldGen); err == nil {
		t.Fatal("Retire from the old incarnation must be refused")
	}
	rec, ok := ReadRecord(epic, "w1")
	if !ok || rec.Gen != newGen {
		t.Fatalf("newer incarnation lost to an old-gen Retire: rec=%+v ok=%v", rec, ok)
	}
}

// TestConcurrentApplyIsSerialized proves the writer lock keeps seq monotonic under concurrent writers: N applies with
// the current gen all land, and the final seq reflects every one (no lost update).
func TestConcurrentApplyIsSerialized(t *testing.T) {
	epic := t.TempDir()
	gen, _ := Arm(epic, "w1", "pi", piSources)
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

// fm_busy_record_read: the armed gen lives in its own sidecar, so a record with no sidecar (never armed, or a legacy
// record) and a record from a superseded incarnation both read unknown with their reason, never idle or busy.
func TestClassifyBindsTheArmedGen(t *testing.T) {
	epic := t.TempDir()
	old, _ := Arm(epic, "w1", "pi", piSources)
	if _, err := Arm(epic, "w1", "pi", piSources); err != nil {
		t.Fatal(err)
	}
	rec, _ := ReadRecord(epic, "w1")
	rec.Gen = old
	if err := write(Path(epic, "w1"), rec); err != nil {
		t.Fatal(err)
	}
	if got := Classify(epic, "w1", "pi", "").String(); got != "unknown gen-mismatch" {
		t.Fatalf("superseded record = %q, want unknown gen-mismatch", got)
	}
	if err := os.Remove(GenPath(epic, "w1")); err != nil {
		t.Fatal(err)
	}
	if got := Classify(epic, "w1", "pi", "").String(); got != "unknown malformed" {
		t.Fatalf("record without sidecar = %q, want unknown malformed", got)
	}
}

// A field outside the schema, or a second line, is a malformed record (fm rejects both), never a busy one.
func TestStrictParse(t *testing.T) {
	epic := t.TempDir()
	if _, err := Arm(epic, "w1", "pi", piSources); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(Path(epic, "w1"))
	line := strings.TrimSuffix(string(good), "\n")
	for name, body := range map[string]string{
		"rogue field": strings.TrimSuffix(line, "}") + `,"rogue":1}` + "\n",
		"two lines":   line + "\n" + line + "\n",
	} {
		if err := os.WriteFile(Path(epic, "w1"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Read(epic, "w1"); got != Unknown {
			t.Fatalf("%s: Read = %q, want unknown", name, got)
		}
	}
}

// B-51: a busy record whose endpoint is gone classifies dead, never busy.
func TestClassifyLiveDeadOverridesBusy(t *testing.T) {
	epic := t.TempDir()
	if _, err := Arm(epic, "w1", "pi", piSources); err != nil {
		t.Fatal(err)
	}
	if got := ClassifyLive(epic, "w1", "pi", "term", func(string) bool { return false }); got.State != Dead {
		t.Fatalf("gone endpoint = %v, want dead", got)
	}
}
