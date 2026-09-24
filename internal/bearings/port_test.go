// Port tests for internal/bearings: firstmate's session-start digest and memory-curation contracts, translated case by
// case from firstmate@1e0e773 (epic cox-supervision-port, wave 1 story cox-supervision-port-session) and turned green
// by wave 2 (story cox-supervision-port-w2-bearings), so the file runs untagged under go test ./... . Every case runs
// against the Bearings interface below through the realBearings adapter. Firstmate names map to cox names as follows:
// home -> workspace, fleet lock -> leader lease, bootstrap -> cox doctor, state/*.status -> story status wakes,
// data/backlog.md -> BACKLOG.md, data/captain.md, data/captain-shared.md, data/learnings.md ->
// cox/notes/{captain,captain-shared,learnings}.md (captain ruling 2026-09-24: three files, firstmate's 7,500 budget).
package bearings_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
	"github.com/nphattai/coxswain/internal/bearings"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// Mechanisms name the cox gap a red case pins; the report's red list groups by them.
const (
	mechDigest   = "bearings digest composition"
	mechLease    = "leader lease (read-only session)"
	mechStatus   = "status tail in digest"
	mechBacklog  = "backlog compact rendering"
	mechDeferred = "deferred forge stage"
	mechBound    = "digest runtime bound"
	mechReemit   = "context re-emit and AGENTS baseline"
	mechEndpoint = "endpoint liveness in digest"
	mechPiLoaded = "pi extension loaded proof"
	mechBudget   = "notes budget accounting"
	mechTiers    = "notes tiers and decay"
	mechCurate   = "notes curation pass"
	mechFour     = "four-section fleet digest"
)

// Notes constants. Three memory files keep firstmate's per-file default tiers: captain.md and captain-shared.md
// pinned, learnings.md aging (captain ruling 2026-09-24). The budget lives in cox/notes-budget.
const (
	notesBudgetRel     = "cox/notes-budget"
	defaultNotesBudget = 7500 // docs/configuration.md:262
	agingDays          = 30   // stow SKILL.md:38
	perishableDays     = 7    // stow SKILL.md:39
	agingPasses        = 10   // stow SKILL.md:70
	perishablePasses   = 3    // stow SKILL.md:71
)

// Digest section headers, in the order the digest spec fixes (report "Digest spec").
const (
	hLease    = "LEADER LEASE"
	hDoctor   = "DOCTOR"
	hWake     = "WAKE QUEUE"
	hHarness  = "SUPERVISION OPERATING INSTRUCTIONS"
	hReadOnce = "READ-ONCE CONTRACT"
	hFleet    = "FLEET STATE"
	hNotes    = "NOTES"
	hNext     = "NEXT STEP"
)

// Bounds ported verbatim from firstmate's cases.
const (
	defaultStatusTail  = 5   // fm: tests/fm-session-start.test.sh:1107
	statusLineCap      = 220 // fm: tests/fm-session-start.test.sh:1147
	defaultQueuedLimit = 20  // fm: tests/fm-session-start.test.sh:1744
)

// The digest and curation types are the package's own.
type (
	Opts         = bearings.Opts
	Digest       = bearings.Digest
	StoryState   = bearings.StoryState
	BudgetReport = bearings.BudgetReport
	Entry        = bearings.Entry
	Receipt      = bearings.Receipt
)

// Bearings is the internal/bearings surface: what one session-start command needs from cox's sources of
// truth (cox doctor, cox wake drain, cox state, questions/, BACKLOG.md, cox/notes/*.md).
type Bearings interface {
	Digest(o Opts) (Digest, error)
	Acquire(ws, id string, live func(string) bool) (bool, error)
	DoctorSummary(ws string) (string, error)
	WakeDrain(epicDir string) ([]wake.Wake, error)
	StoryStates(epicDir string) ([]StoryState, error)
	BacklogOpenRows(ws string) (int, error)
	Notes(ws string, budgetTokens int) (string, error)
	Budget(ws string) (BudgetReport, error)
	Deferred(ws string, wait time.Duration) (string, error)
	RunBounded(timeout time.Duration, argv ...string) (int, error)
	// Classify reads one memory-file entry line under its file's default tier.
	Classify(section, line string, now time.Time, passHorizon bool) (Entry, error)
	// Curate runs the mechanical half of a notes pass: tick, decay, grace migration, budget eviction, archive, and the
	// receipt. reinforced lists the entries this session evidenced (the judgment half stays with the leader).
	Curate(ws string, now time.Time, reinforced []string) (Receipt, error)
}

type notImplementedErr struct{ what string }

func (e notImplementedErr) Error() string { return "not implemented: " + e.what }

// realBearings forwards to internal/bearings. notImplementedErr stays as the gap vocabulary: a method that ever regresses
// to it fails its cases naming the mechanism (DESIGN translation contract rule 3).
type realBearings struct{}

func (realBearings) Digest(o Opts) (Digest, error) { return bearings.Compose(o) }
func (realBearings) Acquire(ws, id string, live func(string) bool) (bool, error) {
	return bearings.Acquire(ws, id, live)
}
func (realBearings) DoctorSummary(ws string) (string, error) { return bearings.DoctorSummary(ws) }
func (realBearings) WakeDrain(epicDir string) ([]wake.Wake, error) {
	return bearings.WakeDrain(epicDir)
}
func (realBearings) StoryStates(epicDir string) ([]StoryState, error) {
	return bearings.StoryStates(epicDir)
}
func (realBearings) BacklogOpenRows(ws string) (int, error)      { return bearings.BacklogOpenRows(ws) }
func (realBearings) Notes(ws string, budget int) (string, error) { return bearings.Notes(ws, budget) }
func (realBearings) Budget(ws string) (BudgetReport, error)      { return bearings.Budget(ws) }
func (realBearings) Deferred(ws string, wait time.Duration) (string, error) {
	return bearings.Deferred(ws, wait)
}
func (realBearings) RunBounded(timeout time.Duration, argv ...string) (int, error) {
	return bearings.RunBounded(timeout, argv...)
}
func (realBearings) Classify(file, line string, now time.Time, horizon bool) (Entry, error) {
	return bearings.Classify(file, line, now, horizon)
}
func (realBearings) Curate(ws string, now time.Time, reinforced []string) (Receipt, error) {
	return bearings.Curate(ws, now, reinforced)
}

var impl Bearings = realBearings{}

// notImplemented fails the case naming the cox mechanism it pins (DESIGN translation contract rule 3).
func notImplemented(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("gap: %s (cox has no implementation)", mechanism)
}

// got unwraps a Bearings call: a not-implemented error is the named gap, any other error is a failure. It is curried
// so a multi-value call can be its sole argument: got(impl.Digest(o))(t, mechDigest).
func got[T any](v T, err error) func(*testing.T, string) T {
	return func(t *testing.T, mechanism string) T {
		t.Helper()
		var ni notImplementedErr
		if errors.As(err, &ni) {
			notImplemented(t, mechanism)
		}
		if err != nil {
			t.Fatalf("%s: %v", mechanism, err)
		}
		return v
	}
}

func digest(t *testing.T, mechanism string, o Opts) Digest {
	t.Helper()
	return got(impl.Digest(o))(t, mechanism)
}

// --- world ---------------------------------------------------------------------------------------------------------

// world is a hermetic workspace: a temp root with AGENTS.md and one active epic, seeded through cox's real writers.
type world struct {
	t    *testing.T
	ws   string
	epic string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ws := t.TempDir()
	w := &world{t: t, ws: ws, epic: filepath.Join(ws, "epics", "demo")}
	for _, d := range []string{"stories", "questions", ".cox"} {
		if err := os.MkdirAll(filepath.Join(w.epic, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.write("AGENTS.md", "# Leader job description\n")
	w.write("epics/demo/DESIGN.md", "# demo - design\n\nStatus: active\n")
	return w
}

func (w *world) path(rel string) string { return filepath.Join(w.ws, rel) }

func (w *world) write(rel, body string) {
	w.t.Helper()
	p := w.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) story(id string) {
	w.write("epics/demo/stories/"+id+".md", "---\nid: "+id+"\nrepo: coxswain\n---\n\n# "+id+"\n")
}

func (w *world) wake(story string, kind wake.Kind, note string) int {
	w.t.Helper()
	gen, err := wake.Append(w.epic, wake.Wake{Epic: "demo", Story: story, Kind: kind, Note: note})
	if err != nil {
		w.t.Fatal(err)
	}
	return gen
}

// status records story status lines the way cox stores them (status wakes) and acks them, so they are history for the
// status tail and not queued wakes the WAKE QUEUE section would also print (firstmate keeps them in state/*.status).
func (w *world) status(story string, notes ...string) {
	w.t.Helper()
	gen := 0
	for _, n := range notes {
		gen = w.wake(story, wake.KindStatus, n)
	}
	if err := wake.AckThrough(w.epic, gen); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) opts() Opts {
	return Opts{Workspace: w.ws, LeaderID: "leader-self", Harness: "claude", Live: func(string) bool { return true }}
}

// --- assertions ----------------------------------------------------------------------------------------------------

func contains(t *testing.T, out, want, why string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("%s: missing %q", why, want)
	}
}

func notContains(t *testing.T, out, bad, why string) {
	t.Helper()
	if strings.Contains(out, bad) {
		t.Errorf("%s: unexpected %q", why, bad)
	}
}

// lineOf is the 1-based line of the first exact line match, or 0.
func lineOf(out, line string) int {
	for i, l := range strings.Split(out, "\n") {
		if l == line {
			return i + 1
		}
	}
	return 0
}

func countLines(out, line string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if l == line {
			n++
		}
	}
	return n
}

func before(t *testing.T, out, a, b string) {
	t.Helper()
	la, lb := lineOf(out, a), lineOf(out, b)
	if la == 0 || lb == 0 {
		t.Fatalf("section missing: %q=%d %q=%d", a, la, b, lb)
	}
	if la >= lb {
		t.Errorf("%q (line %d) must precede %q (line %d)", a, la, b, lb)
	}
}

// section is the text between an exact header line and the next blank line.
func section(out, header string) string {
	var b strings.Builder
	in := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case l == header:
			in = true
		case in && l == "":
			return b.String()
		case in:
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// between is the text strictly between two exact header lines (for sections that contain blank lines).
func between(out, a, b string) string {
	la, lb := lineOf(out, a), lineOf(out, b)
	if la == 0 || lb <= la {
		return ""
	}
	return strings.Join(strings.Split(out, "\n")[la:lb-1], "\n")
}

func queueUntouched(t *testing.T, w *world, wantGen int) {
	t.Helper()
	acked, err := wake.Acked(w.epic)
	if err != nil {
		t.Fatal(err)
	}
	if acked >= wantGen {
		t.Errorf("wake queue mutated: acked through %d, want below %d", acked, wantGen)
	}
}

// --- fm-session-start ----------------------------------------------------------------------------------------------

func TestPortSessionStart(t *testing.T) {
	const s = "FM/fm-session-start/"

	t.Run(s+"context_digest_absent_empty_present", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:722
		w := newWorld(t)
		for _, rel := range notesFiles {
			w.write(rel, "") // present, empty; BACKLOG.md deliberately absent
		}
		d := digest(t, mechDigest, w.opts())
		contains(t, d.Text, "cox/notes/", "digest did not label the notes section")
		contains(t, d.Text, "BACKLOG.md", "digest did not label the backlog section")
		if n := countLines(d.Text, "ABSENT"); n != 1 {
			t.Errorf("want exactly 1 ABSENT marker (BACKLOG.md), got %d", n)
		}
		contains(t, section(d.Text, learningsRel), "(present, empty)", "empty-but-present learnings.md not distinguished from ABSENT")
		w.write(captainRel, "- the captain merges; leaders never push main\n")
		w.write("BACKLOG.md", "| Id | Source | Gap | Status |\n|---|---|---|---|\n| B-01 | x | a gap | open |\n")
		d = digest(t, mechDigest, w.opts())
		contains(t, d.Text, "- the captain merges; leaders never push main", "digest did not print the memory file content")
		if n := countLines(d.Text, "ABSENT"); n != 0 {
			t.Errorf("populated files printed %d ABSENT markers", n)
		}
	})

	t.Run(s+"lock_refusal_read_only_path", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:767
		w := newWorld(t)
		gen := w.wake("s1", wake.KindWorkerDone, "done: surfaced before refusal")
		if ok := got(impl.Acquire(w.ws, "leader-other", func(string) bool { return true }))(t, mechLease); !ok {
			t.Fatal("seed lease not acquired")
		}
		o := w.opts()
		o.LeaderID = "leader-self"
		d := digest(t, mechLease, o)
		if !d.ReadOnly {
			t.Error("a live competing leader did not force a read-only session")
		}
		contains(t, d.Text, "READ-ONLY SESSION", "read-only banner missing on lease refusal")
		contains(t, d.Text, "another live leader holds the lease", "banner did not name the refusal cause")
		contains(t, d.Text, "Skipping every mutating step", "banner did not explain what was skipped")
		contains(t, d.Text, "skipped (read-only session)", "wake-queue section did not report itself skipped")
		contains(t, d.Text, "left untouched because this session lacks verified leader-lease ownership", "queued wakes not reported untouched")
		contains(t, d.Text, "Stay read-only", "read-only next step did not block watcher repair")
		notContains(t, d.Text, "drain them with cox wake drain", "read-only digest printed a mutating drain instruction")
		notContains(t, d.Text, "After draining queued wakes", "read-only digest printed a drain-then-rearm instruction")
		notContains(t, d.Text, "\n  cox wake ack-through", "read-only digest printed a mutating ack command")
		notContains(t, d.Text, "\n  cox watch", "read-only digest printed a watcher-arm command")
		doc := got(impl.DoctorSummary(w.ws))(t, mechLease)
		contains(t, d.Text, strings.TrimSpace(doc), "detect-only doctor diagnostics did not run on the read-only path")
		contains(t, d.Text, hFleet, "fleet section missing on the read-only path")
		contains(t, d.Text, hNext, "closing reminder missing on the read-only path")
		queueUntouched(t, w, gen)
	})

	t.Run(s+"lock_write_failure_read_only_path", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:828
		w := newWorld(t)
		gen := w.wake("a", wake.KindWorkerDone, "done: must remain queued")
		ctl := filepath.Join(w.ws, ".cox")
		if err := os.MkdirAll(ctl, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ctl, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ctl, 0o700) })
		d := digest(t, mechLease, w.opts())
		contains(t, d.Text, "cannot write leader lease", "lease publication failure not surfaced")
		contains(t, d.Text, "READ-ONLY SESSION", "lease publication failure did not force read-only")
		contains(t, d.Text, "LEADER LEASE OWNERSHIP WAS NOT VERIFIED", "publication failure misreported")
		notContains(t, d.Text, "another live leader holds the lease", "publication failure falsely claimed a live holder")
		queueUntouched(t, w, gen)
	})

	t.Run(s+"session_lock_concurrent_single_winner", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:887
		w := newWorld(t)
		const n = 40
		var wg sync.WaitGroup
		var mu sync.Mutex
		winners, start := 0, make(chan struct{})
		var gap error
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				ok, err := impl.Acquire(w.ws, fmt.Sprintf("leader-%d", i), func(string) bool { return true })
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					gap = err
				}
				if ok {
					winners++
				}
			}(i)
		}
		close(start)
		wg.Wait()
		got(0, gap)(t, mechLease)
		if winners != 1 {
			t.Errorf("concurrent lease acquisition produced %d winners", winners)
		}
	})

	t.Run(s+"output_ordering_diagnostics_lead", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:966
		w := newWorld(t)
		w.story("task-a")
		w.status("task-a", "working: step 1")
		w.write(learningsRel, "Notes that may be truncated away safely.\n")
		d := digest(t, mechDigest, w.opts())
		for _, pair := range [][2]string{
			{hLease, hDoctor}, {hDoctor, hWake}, {hWake, hReadOnce},
			{hReadOnce, hFleet}, {hFleet, hNotes}, {hNotes, hNext},
		} {
			before(t, d.Text, pair[0], pair[1])
		}
		inv := lineOf(d.Text, "--- task-a ---")
		if inv == 0 || inv >= lineOf(d.Text, hNotes) {
			t.Error("the live story inventory was missing or buried behind the notes")
		}
		contains(t, d.Text, "Notes that may be truncated away safely.", "the fixture did not print the notes")
		doc := strings.TrimSpace(got(impl.DoctorSummary(w.ws))(t, mechDigest))
		if i, f := strings.Index(d.Text, doc), strings.Index(d.Text, hFleet); doc == "" || i < 0 || i > f {
			t.Error("the actionable doctor diagnostic was missing or buried after the fleet state")
		}
	})

	t.Run(s+"read_once_contract_is_stated_once_before_its_subject", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1024
		w := newWorld(t)
		d := digest(t, mechDigest, w.opts())
		contains(t, d.Text, "Do NOT re-read any of them after reading this digest", "contract lost its core instruction")
		contains(t, d.Text, "STARTUP TRUNCATED banner named the stage that would have printed it", "contract does not void itself for a stage that never ran")
		contains(t, d.Text, "The READ-ONCE CONTRACT", "closing reminder does not point back at the contract")
		if n := strings.Count(d.Text, "Do NOT re-read any of them"); n != 1 {
			t.Errorf("the read-once contract is stated %d times, want once", n)
		}
	})

	t.Run(s+"status_tail_bounding", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1091
		w := newWorld(t)
		w.story("task-a")
		for i := 1; i <= 7; i++ {
			w.status("task-a", fmt.Sprintf("working: step %d", i))
		}
		d := digest(t, mechStatus, w.opts())
		contains(t, d.Text, "working: step 7", "default tail missing the newest line")
		contains(t, d.Text, "working: step 3", "default tail (5) missing a recent line")
		notContains(t, d.Text, "working: step 1", "default tail (5) leaked an older line")
		contains(t, d.Text, "cox wake drain --full", "digest did not print the pointer for a deeper read")
		contains(t, d.Text, "a bounded tail of every story's status", "read-once contract does not name bounded status tails")
		o := w.opts()
		o.StatusTail = 2
		d = digest(t, mechStatus, o)
		contains(t, d.Text, "working: step 7", "tail=2 missing the newest line")
		notContains(t, d.Text, "working: step 5", "tail=2 did not bound to 2 lines")
	})

	t.Run(s+"status_tail_line_cap", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1123
		w := newWorld(t)
		w.story("task-cap")
		lede := "needs-decision: [key=cap] pick the rendering strategy"
		w.status("task-cap", lede+strings.Repeat(" padding", 400), "working: short line kept whole")
		d := digest(t, mechStatus, w.opts())
		contains(t, d.Text, lede, "the cap discarded the lede")
		contains(t, d.Text, " [truncated]", "an over-long line was not marked")
		contains(t, d.Text, "working: short line kept whole", "the cap mangled a short line")
		contains(t, d.Text, fmt.Sprintf("each capped at %d characters", statusLineCap), "tail header does not disclose its cap")
		tail, capped := "", 0
		for _, l := range strings.Split(d.Text, "\n") {
			if strings.HasPrefix(l, "status tail (") {
				tail = section(d.Text, l)
				break
			}
		}
		for _, l := range strings.Split(tail, "\n") {
			if len(l) > statusLineCap {
				t.Errorf("a tail line ran %d characters past the %d cap", len(l), statusLineCap)
			}
			if strings.HasSuffix(l, " [truncated]") {
				capped++
			}
		}
		if capped != 1 {
			t.Errorf("want exactly 1 truncated tail line, got %d", capped)
		}
	})

	t.Run(s+"orphan_status_logs_are_printed", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1161
		w := newWorld(t)
		w.story("task-a")
		w.status("task-a", "matched: surfaced once")
		for i := 1; i <= 6; i++ {
			w.status("task-orphan", fmt.Sprintf("orphan: step %d", i)) // no stories/task-orphan.md
		}
		d := digest(t, mechStatus, w.opts())
		contains(t, d.Text, "Orphan status (wakes for a story with no story file)", "orphan status not labelled")
		contains(t, d.Text, "--- task-orphan ---", "orphan id not printed")
		contains(t, d.Text, "orphan: step 6", "orphan tail missing the newest line")
		notContains(t, d.Text, "orphan: step 1", "orphan tail not bounded")
		if n := strings.Count(d.Text, "matched: surfaced once"); n != 1 {
			t.Errorf("matched status printed %d times", n)
		}
		if n := strings.Count(d.Text, "orphan: step 6"); n != 1 {
			t.Errorf("orphan status printed %d times", n)
		}
	})

	t.Run(s+"endpoint_liveness_tmux", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1330
		w := newWorld(t)
		w.story("task-live")
		w.story("task-dead")
		o := w.opts()
		o.Endpoint = func(_, story string) (bool, string) { return story == "task-live", "term-" + story }
		d := digest(t, mechEndpoint, o)
		contains(t, d.Text, "endpoint: alive (backend=orca terminal=term-task-live)", "live endpoint not reported alive")
		contains(t, d.Text, "endpoint: dead (backend=orca terminal=term-task-dead)", "dead endpoint not reported dead")
	})

	t.Run(s+"composition_invokes_real_scripts", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1372
		w := newWorld(t)
		w.story("task-z")
		w.wake("task-z", wake.KindQuestion, "needs-decision: pick a library")
		d := digest(t, mechDigest, w.opts())
		doc := got(impl.DoctorSummary(w.ws))(t, mechDigest)
		contains(t, d.Text, strings.TrimSpace(doc), "cox doctor's own output did not appear verbatim")
		contains(t, d.Text, "lease acquired: leader-self", "the lease step's own text did not appear")
		contains(t, d.Text, "task-z", "cox wake drain's drained record did not appear")
		contains(t, d.Text, "needs-decision: pick a library", "cox wake drain's note did not appear")
		contains(t, d.Text, "latest wake-EVENT observed at drain, not current state", "the drain annotation line was lost")
	})

	t.Run(s+"inactive_reconcile_never_blocks_the_digest", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1493
		w := newWorld(t)
		w.story("slow-child")
		w.transition("slow-child", state.Submitted, state.Working)
		w.status("slow-child", "working: validating")
		release, finished := make(chan struct{}), make(chan struct{})
		var once sync.Once
		var mu sync.Mutex
		blocking, deferred := 0, 0
		digestDone := false
		o := w.opts()
		o.StateRead = func(_, story string) (string, error) {
			mu.Lock()
			if digestDone {
				deferred++
			} else {
				blocking++
			}
			mu.Unlock()
			select {
			case <-release:
			case <-time.After(30 * time.Second):
			}
			once.Do(func() { close(finished) })
			return "state: done", nil
		}
		d := digest(t, mechDeferred, o)
		mu.Lock()
		digestDone = true
		mu.Unlock()
		contains(t, d.Text, hNext, "the digest did not complete")
		select {
		case <-finished:
			t.Fatal("the digest waited for the inactive story's still-unreleased state read")
		default:
		}
		close(release)
		got(impl.Deferred(w.ws, 15*time.Second))(t, mechDeferred)
		ws, err := wake.Load(w.epic)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, x := range ws {
			if x.Story == "slow-child" && strings.Contains(x.Note, "inactive-outcome") {
				found = true
			}
		}
		if !found {
			t.Error("the deferred scan's finding never reached the durable wake queue")
		}
		mu.Lock()
		defer mu.Unlock()
		if blocking != 0 {
			t.Errorf("the digest called the slow state reader %d time(s) on its blocking path", blocking)
		}
	})

	t.Run(s+"unreachable_network_never_blocks_the_digest", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1582
		w := newWorld(t)
		done := make(chan struct{})
		o := w.opts()
		o.Forge = func() error {
			time.Sleep(12 * time.Second)
			close(done)
			return errors.New("gh auth: host unreachable")
		}
		d := digest(t, mechDeferred, o)
		select {
		case <-done:
			t.Fatal("the digest waited for the 12s unreachable-host probe")
		default:
		}
		contains(t, d.Text, "IN PROGRESS - the deferred forge checks have not finished yet.", "digest did not disclose running checks")
		contains(t, d.Text, "NOT yet confirmed: GitHub authentication", "digest did not name the unconfirmed checks")
		notContains(t, d.Text, "NEEDS_GH_AUTH", "digest reported a verdict it could not yet have")
		rep := got(impl.Deferred(w.ws, 60*time.Second))(t, mechDeferred)
		contains(t, rep, "NEEDS_GH_AUTH", "the deferred stage lost the GitHub-auth verdict")
	})

	t.Run(s+"deferred_result_reaches_the_agent_when_the_digest_cannot_print_it", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1619
		w := newWorld(t)
		o := w.opts()
		o.Forge = func() error { time.Sleep(8 * time.Second); return errors.New("gh auth: unreachable") }
		digest(t, mechDeferred, o)
		got(impl.Deferred(w.ws, 60*time.Second))(t, mechDeferred)
		ws, err := wake.Load(w.epic)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, x := range ws {
			if strings.Contains(x.Note, "startup-forge") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("a result the digest could not print reached the queue %d times, want 1", n)
		}
	})

	t.Run(s+"read_only_session_declares_skipped_network_checks", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1639
		w := newWorld(t)
		got(impl.Acquire(w.ws, "leader-other", func(string) bool { return true }))(t, mechLease)
		o := w.opts()
		probed := false
		o.Forge = func() error { probed = true; return nil }
		d := digest(t, mechDeferred, o)
		contains(t, d.Text, "READ-ONLY SESSION", "fixture did not refuse the lease")
		contains(t, d.Text, "skipped (read-only session) - GitHub authentication", "read-only session did not declare skipped checks")
		got(impl.Deferred(w.ws, time.Second))(t, mechDeferred)
		if probed {
			t.Error("a read-only session started the deferred stage it has no authority for")
		}
	})

	t.Run(s+"backlog_compact_tasks_axi_omits_bodies_and_keeps_metadata", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1726
		w := newWorld(t)
		w.write("BACKLOG.md", longBacklog(3))
		d := digest(t, mechBacklog, w.opts())
		contains(t, d.Text, "compact backlog listing (done rows omitted; every in-epic, held, and blocked row shown in full;", "compact listing header missing")
		contains(t, d.Text, "| B-10 | wave | Compact startup digest | in-epic demo item 1 |", "in-epic row or its status lost")
		contains(t, d.Text, "| B-11 | wave | Held queued work | open (hold: captain choice pending) |", "held row lost")
		contains(t, d.Text, "| B-12 | wave | Follow compact startup | open (blocked-by: B-10) |", "blocked-by metadata lost")
		contains(t, d.Text, "| B-102 | wave | Plain queued item 3 | open |", "a queued row inside the bound was dropped")
		notContains(t, d.Text, "OVERSIZED-BODY-LINE", "digest leaked a row's evidence body")
		notContains(t, d.Text, "DONE-ROW-LINE", "digest listed a fixed row")
		notContains(t, d.Text, "WONTFIX-ROW-LINE", "digest listed a wontfix row")
		contains(t, d.Text, "Full rows and evidence remain on demand: BACKLOG.md", "full-body pointer missing")
	})

	t.Run(s+"backlog_queued_bound_discloses_its_remainder", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1786
		w := newWorld(t)
		w.write("BACKLOG.md", longBacklog(7))
		o := w.opts()
		o.QueuedLimit = 3
		d := digest(t, mechBacklog, o)
		contains(t, d.Text, "| B-102 | wave | Plain queued item 3 | open |", "bound dropped a row inside its limit")
		notContains(t, d.Text, "Plain queued item 4", "bound did not bound the open listing")
		contains(t, d.Text, "(shown 1 in-epic, 2 held or blocked, 3 of 7 other open row(s); 2 closed row(s) omitted)", "bound did not report what it showed")
		contains(t, d.Text, "(4 more open - read BACKLOG.md)", "bound did not disclose an exact remainder")
		contains(t, d.Text, "Held queued work", "bound swallowed a held row")
		contains(t, d.Text, "Follow compact startup", "bound swallowed a blocked row")
		contains(t, d.Text, "Compact startup digest", "bound swallowed an in-epic row")
	})

	t.Run(s+"backlog_compact_manual_backend_skips_indented_bodies", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1819
		// A cox row's "body" is its evidence prose below the table (longBacklog's "## Evidence"), not an indented line.
		w := newWorld(t)
		w.write("BACKLOG.md", longBacklog(25))
		o := w.opts()
		o.QueuedLimit = 4
		d := digest(t, mechBacklog, o)
		contains(t, d.Text, "Plain queued item 4", "an open row inside the bound was dropped")
		notContains(t, d.Text, "Plain queued item 5", "the open listing was not bounded")
		contains(t, d.Text, "(shown 1 in-epic, 2 held or blocked, 4 of 25 other open row(s); 2 closed row(s) omitted)", "bound accounting missing")
		contains(t, d.Text, "(21 more open - read BACKLOG.md)", "exact remainder not disclosed")
		notContains(t, d.Text, "OVERSIZED-BODY-LINE", "digest leaked an evidence body")
		notContains(t, d.Text, "DONE-ROW-LINE", "digest listed a closed row")
		if n := got(impl.BacklogOpenRows(w.ws))(t, mechBacklog); n != 28 {
			t.Errorf("BacklogOpenRows = %d, want 28 (1 in-epic + 2 held/blocked + 25 open)", n)
		}
	})

	t.Run(s+"runtime_bound_truncates_loudly_and_exits_zero", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1934
		w := newWorld(t)
		marker := filepath.Join(t.TempDir(), "hung")
		o := w.opts()
		o.Timeout = 3 * time.Second
		// a TERM-resistant grandchild: sh stays the parent, perl carries the marker in its argv for pgrep
		o.StageCmd = map[string][]string{"doctor": {"sh", "-c", `perl -e '$SIG{TERM}="IGNORE"; sleep 600' ` + marker + `; true`}}
		d := digest(t, mechBound, o) // err == nil is the exit-0 guarantee
		if !d.Truncated {
			t.Error("a truncated digest did not report itself truncated")
		}
		contains(t, d.Text, hLease, "the truncated digest lost a stage that had completed")
		contains(t, d.Text, "STARTUP TRUNCATED - SESSION START HIT ITS RUNTIME BOUND", "no truncation banner")
		contains(t, d.Text, `stopped during the "doctor" stage`, "banner did not name the incomplete stage")
		contains(t, d.Text, "RECONCILE these stages", "banner did not say what to reconcile")
		contains(t, d.Text, "wake-queue supervision-instructions read-once fleet-state forge-checks notes next-step", "banner did not list every stage that never ran")
		if lineOf(d.Text, hNext) != 0 {
			t.Error("a truncated digest claimed its closing reminder")
		}
		time.Sleep(time.Second)
		if out, _ := exec.Command("pgrep", "-f", marker).Output(); len(strings.TrimSpace(string(out))) > 0 {
			t.Errorf("the runtime bound left hung subprocess(es): %s", out)
		}
	})

	t.Run(s+"portable_timeout_escalates_term_resistant_process", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:1984
		code := got(impl.RunBounded(time.Second, "perl", "-e", `$SIG{TERM}="IGNORE"; sleep 600`))(t, mechBound)
		if code != 124 {
			t.Errorf("TERM-resistant child: exit %d, want 124", code)
		}
		code = got(impl.RunBounded(2*time.Second, "sh", "-c", "exit 137"))(t, mechBound)
		if code != 137 {
			t.Errorf("natural exit 137 reported as %d", code)
		}
	})

	t.Run(s+"runtime_bound_leaves_a_healthy_digest_untouched", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2016
		w := newWorld(t)
		d := digest(t, mechBound, w.opts())
		notContains(t, d.Text, "STARTUP TRUNCATED - SESSION START HIT ITS", "an in-time digest reported itself truncated")
		contains(t, d.Text, hNext, "an in-time digest lost its closing reminder")
		if d.Truncated {
			t.Error("Truncated set on a healthy digest")
		}
	})

	t.Run(s+"reemit_skips_startup_sweeps_but_keeps_the_wake_drain", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2103
		w := newWorld(t)
		w.wake("task-r", wake.KindWorkerDone, "done: queued after startup")
		o := w.opts()
		o.Forge = func() error { return nil }
		digest(t, mechReemit, o)
		got(impl.Deferred(w.ws, 15*time.Second))(t, mechReemit)
		gen := w.wake("task-r", wake.KindWorkerDone, "done: queued after the re-emit too")
		o.Reemit, o.Source = true, "compact"
		probed := false
		o.Forge = func() error { probed = true; return nil }
		d := digest(t, mechReemit, o)
		contains(t, d.Text, "SESSION START (CONTEXT RE-EMIT)", "re-emit did not label itself")
		if probed {
			t.Error("re-emit repeated a startup sweep")
		}
		contains(t, d.Text, "done: queued after the re-emit too", "re-emit did not drain the wake queue")
		queueUntouched(t, w, gen) // drain shows, the leader acks
		contains(t, d.Text, fmt.Sprintf("cox wake ack-through %d", gen), "re-emit omitted the generation-bound ack")
		for _, h := range []string{hNotes, hFleet, hNext} {
			contains(t, d.Text, h, "re-emit dropped a section")
		}
	})

	t.Run(s+"agents_baseline_stays_at_true_start_and_reemits_on_every_drifted_pi_compact", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2146
		w := newWorld(t)
		w.write("AGENTS.md", "LEADER_TEST_INSTRUCTION=original\n")
		o := w.opts()
		o.Harness = "pi"
		digest(t, mechReemit, o)
		o.Reemit, o.Source = true, "compact"
		d := digest(t, mechReemit, o)
		notContains(t, d.Text, "CURRENT AGENTS.md - INSTRUCTION REFRESH", "a no-drift compact refreshed")
		w.write("AGENTS.md", "LEADER_TEST_INSTRUCTION=updated\n")
		o.Reemit, o.Source = false, "resume"
		d = digest(t, mechReemit, o)
		notContains(t, d.Text, "CURRENT AGENTS.md - INSTRUCTION REFRESH", "a context-preserving resume refreshed")
		o.Reemit, o.Source = true, "compact"
		for i := 0; i < 2; i++ {
			d = digest(t, mechReemit, o)
			contains(t, d.Text, "CURRENT AGENTS.md - INSTRUCTION REFRESH", "a drifted compact did not refresh")
			contains(t, d.Text, "LEADER_TEST_INSTRUCTION=updated", "replacement instructions not emitted")
			before(t, d.Text, "CURRENT AGENTS.md - INSTRUCTION REFRESH", hFleet)
		}
		o.Source = "clear"
		d = digest(t, mechReemit, o)
		notContains(t, d.Text, "CURRENT AGENTS.md - INSTRUCTION REFRESH", "a clear rebuild refreshed against the old baseline")
	})

	t.Run(s+"read_only_pi_compact_refreshes_against_its_own_session_identity", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2232
		w := newWorld(t)
		w.write("AGENTS.md", "READ_ONLY_AGENTS=original\n")
		o := w.opts()
		o.Harness, o.LeaderID = "pi", "leader-ro"
		digest(t, mechReemit, o) // true start for this identity, which records its baseline
		// a competing leader takes the lease over (firstmate rewrites .lock to a live holder)
		takeover, err := impl.Acquire(w.ws, "leader-other", func(id string) bool { return id != "leader-ro" })
		if got(takeover, err)(t, mechLease); !takeover {
			t.Fatal("fixture: the competing leader did not take the lease")
		}
		w.write("AGENTS.md", "READ_ONLY_AGENTS=current\n")
		o.Reemit, o.Source = true, "compact"
		d := digest(t, mechReemit, o)
		contains(t, d.Text, "READ-ONLY SESSION", "a competing live leader did not force read-only")
		contains(t, d.Text, "READ_ONLY_AGENTS=current", "read-only compact did not refresh against its own identity")
	})

	t.Run(s+"codex_unreachable_reset_sources_do_not_claim_instruction_refresh", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2267
		w := newWorld(t)
		o := w.opts()
		o.Harness = "codex"
		d := digest(t, mechReemit, o)
		contains(t, d.Text, "leader harness: codex", "codex tier not selected")
		w.write("AGENTS.md", "CODEX_AGENTS=updated\n")
		o.Reemit = true
		for _, src := range []string{"clear", "compact"} {
			o.Source = src
			d = digest(t, mechReemit, o)
			notContains(t, d.Text, "CURRENT AGENTS.md - INSTRUCTION REFRESH", "codex "+src+" claimed an unavailable refresh channel")
		}
	})

	t.Run(s+"agents_baseline_requires_sha256_and_successful_completion", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2294
		// The sha256-tool-failure half is n/a (Go's crypto/sha256 cannot be absent); the completion half is ported.
		w := newWorld(t)
		w.write("AGENTS.md", "AGENTS_SHA_TEST=original\n")
		o := w.opts()
		o.Harness = "pi"
		o.Timeout = 2 * time.Second
		o.StageCmd = map[string][]string{"next-step": {"sleep", "30"}} // completion never publishes
		if d := digest(t, mechReemit, o); !d.Truncated {
			t.Fatal("fixture did not truncate before completion")
		}
		w.write("AGENTS.md", "AGENTS_SHA_TEST=updated\n")
		o.StageCmd, o.Timeout, o.Reemit, o.Source = nil, 0, true, "compact"
		d := digest(t, mechReemit, o)
		// no baseline was recorded, so a compact must refresh conservatively
		contains(t, d.Text, "AGENTS_SHA_TEST=updated", "a missing baseline did not refresh the instructions on compact")
	})

	t.Run(s+"reemit_keeps_repair_ownership_with_the_lock_holder", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2333
		w := newWorld(t)
		o := w.opts()
		digest(t, mechReemit, o) // this session holds the lease
		o.Reemit, o.Source = true, "compact"
		d := digest(t, mechReemit, o)
		notContains(t, d.Text, "READ-ONLY SESSION", "re-emit dropped the holder's lease")
		notContains(t, d.Text, "must leave repair work to the session holding the leader lease", "holder lost repair ownership")
		o.LeaderID = "leader-other"
		d = digest(t, mechReemit, o)
		contains(t, d.Text, "READ-ONLY SESSION", "a non-holder re-emit was not read-only")
		contains(t, d.Text, "must leave repair work to the session holding the leader lease", "non-holder claimed repair ownership")
	})

	t.Run(s+"fleet_digest_empty_fleet", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2374
		w := newWorld(t)
		d := digest(t, mechDigest, w.opts())
		contains(t, between(d.Text, hFleet, hNotes), "(none)", "empty fleet did not report (none)")
	})

	t.Run(s+"supervision_block_exactly_one_and_pi_diagnostic", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2474
		w := newWorld(t)
		o := w.opts()
		o.Harness = "pi"
		d := digest(t, mechPiLoaded, o)
		if n := strings.Count(d.Text, hHarness+" - "); n != 1 {
			t.Errorf("want exactly one supervision block, got %d", n)
		}
		contains(t, d.Text, hHarness+" - leader harness: pi", "pi supervision block missing")
		contains(t, d.Text, "PI_LEADER_EXTENSION: not loaded", "pi extension load diagnostic missing")
		before(t, d.Text, hWake, hHarness+" - leader harness: pi")
		before(t, d.Text, hHarness+" - leader harness: pi", hNotes)
	})

	t.Run(s+"pi_diagnostic_rejects_stale_loaded_marker", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2524
		w := newWorld(t)
		w.write(".cox/pi-leader-extension-loaded", "stale-extension-version\n"+fmt.Sprint(os.Getpid())+"\n")
		o := w.opts()
		o.Harness = "pi"
		d := digest(t, mechPiLoaded, o)
		contains(t, d.Text, "PI_LEADER_EXTENSION: not loaded", "pi diagnostic trusted a stale loaded marker")
	})

	t.Run(s+"pi_diagnostic_rejects_handoff_generation_marker", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2551
		w := newWorld(t)
		w.write(".cox/pi-leader-extension-loaded", pi.ExtensionHash()+"\n"+fmt.Sprint(os.Getpid())+"\ngeneration=1 phase=handoff\n")
		o := w.opts()
		o.Harness = "pi"
		d := digest(t, mechPiLoaded, o)
		contains(t, d.Text, "PI_LEADER_EXTENSION: not loaded", "pi diagnostic trusted a handoff-generation marker")
	})

	t.Run(s+"pi_diagnostic_accepts_prelock_loaded_marker", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2580
		w := newWorld(t)
		w.write(".cox/pi-leader-extension-loaded", pi.ExtensionHash()+"\n"+fmt.Sprint(os.Getpid())+"\n")
		o := w.opts()
		o.Harness = "pi"
		d := digest(t, mechPiLoaded, o)
		notContains(t, d.Text, "PI_LEADER_EXTENSION: not loaded", "pi diagnostic rejected a current marker written before the lease")
	})

	t.Run(s+"pi_diagnostic_rejects_missing_turnend_guard_marker", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2650
		w := newWorld(t)
		w.write(".cox/pi-leader-extension-loaded", pi.ExtensionHash()+"\n"+fmt.Sprint(os.Getpid())+"\nturnend=absent\n")
		o := w.opts()
		o.Harness = "pi"
		d := digest(t, mechPiLoaded, o)
		contains(t, d.Text, "PI_LEADER_EXTENSION: not loaded", "pi diagnostic trusted a session without the turn-end guard")
	})

	t.Run(s+"pi_diagnostic_rejects_previous_session_loaded_marker", func(t *testing.T) {
		// fm: tests/fm-session-start.test.sh:2675
		w := newWorld(t)
		w.write(".cox/pi-leader-extension-loaded", pi.ExtensionHash()+"\n999999\n")
		o := w.opts()
		o.Harness = "pi"
		d := digest(t, mechPiLoaded, o)
		contains(t, d.Text, "PI_LEADER_EXTENSION: not loaded", "pi diagnostic trusted a marker from a previous pi process")
	})
}

// longBacklog is a cox BACKLOG.md whose closed, in-epic, held, blocked and open rows are distinguishable; the *-LINE
// markers make a leak unmistakable. It mirrors write_long_body_backlog (fm-session-start.test.sh:1700).
func longBacklog(open int) string {
	var b strings.Builder
	b.WriteString("# Coxswain product backlog (cross-epic)\n\n| Id | Source | Gap | Status |\n|---|---|---|---|\n")
	b.WriteString("| B-10 | wave | Compact startup digest | in-epic demo item 1 |\n")
	b.WriteString("| B-11 | wave | Held queued work | open (hold: captain choice pending) |\n")
	b.WriteString("| B-12 | wave | Follow compact startup | open (blocked-by: B-10) |\n")
	for i := 0; i < open; i++ {
		fmt.Fprintf(&b, "| B-%d | wave | Plain queued item %d | open |\n", 100+i, i+1)
	}
	b.WriteString("| B-01 | wave | DONE-ROW-LINE already landed | fixed PR #1 |\n")
	b.WriteString("| B-02 | wave | WONTFIX-ROW-LINE declined | wontfix (not needed) |\n")
	b.WriteString("\n## Evidence\n\nB-10: OVERSIZED-BODY-LINE the digest must never print evidence prose.\n")
	return b.String()
}

// --- notes fixtures ------------------------------------------------------------------------------------------------

const notesHeader = "<!-- memory tiers: see docs/handoff.md -->\n"

// Memory file paths, workspace-relative (captain ruling 2026-09-24; firstmate data/*.md).
const (
	captainRel   = "cox/notes/captain.md"
	sharedRel    = "cox/notes/captain-shared.md"
	learningsRel = "cox/notes/learnings.md"
	archiveRel   = "cox/notes/memory-archive.md"
)

var notesFiles = []string{captainRel, sharedRel, learningsRel}

// notes writes captain.md and learnings.md, each with the one-line header pointer (stow SKILL.md:48).
func (w *world) notes(captain, learnings []string) {
	for _, f := range []struct {
		rel, title string
		lines      []string
	}{{captainRel, "Captain", captain}, {learningsRel, "Learnings", learnings}} {
		var b strings.Builder
		b.WriteString(notesHeader + "# " + f.title + "\n\n")
		for _, l := range f.lines {
			b.WriteString(l + "\n")
		}
		w.write(f.rel, b.String())
	}
}

// notesTotal is the reference total: the three memory files' estimates summed (docs/configuration.md:261).
func (w *world) notesTotal() int {
	n := 0
	for _, rel := range notesFiles {
		if b := w.read(rel); b != "" {
			n += estimate(b)
		}
	}
	return n
}

func (w *world) read(rel string) string {
	w.t.Helper()
	b, err := os.ReadFile(w.path(rel))
	if err != nil && !os.IsNotExist(err) {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *world) transition(story string, from, to state.State) {
	w.t.Helper()
	ev := state.Event{Epic: "demo", Story: story, Attempt: 1, Actor: state.Worker, From: from, To: to}
	if err := state.Append(w.epic, ev); err != nil {
		w.t.Fatal(err)
	}
}

// estimate is the reference startup-memory estimate: ceil(UTF-8 bytes / 3) (docs/configuration.md:268).
func estimate(s string) int { return (len(s) + 2) / 3 }

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

// --- fm-stow-cascade -----------------------------------------------------------------------------------------------

func TestPortStowCascade(t *testing.T) {
	const s = "FM/fm-stow-cascade/"

	t.Run(s+"budget_is_enforced_per_home_and_never_summed", func(t *testing.T) {
		// fm: tests/fm-stow-cascade.test.sh:125
		over, within := newWorld(t), newWorld(t)
		for _, w := range []*world{over, within} {
			w.write(notesBudgetRel, "10\n")
		}
		over.notes(nil, []string{"- " + strings.Repeat("x", 60) + " <!--a:2026-09-20-->"})
		// within holds only its learnings file (the one-file variant overwrote the whole file here)
		within.write(learningsRel, "- ok\n") // 5 bytes -> 2 tokens
		ro := got(impl.Budget(over.ws))(t, mechBudget)
		rw := got(impl.Budget(within.ws))(t, mechBudget)
		if ro.Budget != 10 || rw.Budget != 10 {
			t.Errorf("a workspace did not report its own allowance: %d, %d", ro.Budget, rw.Budget)
		}
		if want := over.notesTotal(); ro.Total != want {
			t.Errorf("over-budget total %d is not its own files (%d)", ro.Total, want)
		}
		if want := within.notesTotal(); rw.Total != want {
			t.Errorf("within-budget total %d is not its own files (%d)", rw.Total, want)
		}
		if ro.Status != "over-budget" || rw.Status != "within-budget" {
			t.Errorf("classification not per workspace: over=%q within=%q", ro.Status, rw.Status)
		}
	})

	t.Run(s+"receipt_facts_are_complete_and_show_before_and_after", func(t *testing.T) {
		// fm: tests/fm-stow-cascade.test.sh:254
		w := newWorld(t)
		w.write(notesBudgetRel, "45\n")
		w.notes(nil, []string{
			"- an old learning nobody exercised " + strings.Repeat("y", 60) + " <!--a:2026-01-01-->",
			"- a current learning <!--a:2026-09-20-->",
		})
		r := got(impl.Curate(w.ws, day("2026-09-24"), nil))(t, mechCurate)
		if r.Before.Status != "over-budget" {
			t.Errorf("the over-budget workspace was not surfaced before curation: %q", r.Before.Status)
		}
		if r.After.Status != "within-budget" || r.After.Total >= r.Before.Total {
			t.Errorf("the after pass did not reflect curation: before=%d after=%d %q", r.Before.Total, r.After.Total, r.After.Status)
		}
		for _, sec := range []string{"captain.md", "captain-shared.md", "learnings.md"} {
			if len(r.Actions[sec]) == 0 {
				t.Errorf("receipt lacks an action for file %s", sec)
			}
		}
		if r.Before.Budget != 45 || r.After.Budget != 45 {
			t.Error("receipt lacks the effective budget before and after")
		}
	})
}

// --- stow skill: tiers, decay, budget, archive, migration, receipt --------------------------------------------------

func TestPortStowSkill(t *testing.T) {
	const s = "FM/skill-stow/"
	now := day("2026-09-24")

	t.Run(s+"markers_name_their_tier", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:21
		for _, c := range []struct{ line, tier, date string }{
			{"- fact <!--a:2026-09-01-->", "aging", "2026-09-01"},
			{"- fact <!--p:2026-09-20-->", "perishable", "2026-09-20"},
			{"- fact <!--P-->", "pinned", ""},
			{"- fact <!--g-->", "grace", ""},
		} {
			e := got(impl.Classify("learnings.md", c.line, now, false))(t, mechTiers)
			if e.Tier != c.tier || e.Reinforced != c.date {
				t.Errorf("%q -> %+v, want tier %s date %q", c.line, e, c.tier, c.date)
			}
		}
	})

	t.Run(s+"pass_counter_marker_absent_means_zero", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:23
		e := got(impl.Classify("learnings.md", "- fact <!--a:2026-09-20/6-->", now, true))(t, mechTiers)
		if e.Passes != 6 {
			t.Errorf("counter /6 read as %d", e.Passes)
		}
		e = got(impl.Classify("learnings.md", "- fact <!--a:2026-09-20-->", now, true))(t, mechTiers)
		if e.Passes != 0 {
			t.Errorf("absent /N read as %d, want 0", e.Passes)
		}
	})

	t.Run(s+"pinned_is_exempt_from_decay", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:37
		e := got(impl.Classify("learnings.md", "- ancient but pinned <!--P-->", now.AddDate(5, 0, 0), true))(t, mechTiers)
		if e.Stale {
			t.Error("a pinned entry read a clock")
		}
	})

	t.Run(s+"aging_is_stale_at_30_days", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:38
		for _, c := range []struct {
			age   int
			stale bool
		}{{agingDays - 1, false}, {agingDays, true}} {
			line := "- fact <!--a:" + now.AddDate(0, 0, -c.age).Format("2006-01-02") + "-->"
			if e := got(impl.Classify("learnings.md", line, now, false))(t, mechTiers); e.Stale != c.stale {
				t.Errorf("aging age %dd: stale=%v, want %v", c.age, e.Stale, c.stale)
			}
		}
	})

	t.Run(s+"perishable_is_stale_at_7_days", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:39
		for _, c := range []struct {
			age   int
			stale bool
		}{{perishableDays - 1, false}, {perishableDays, true}} {
			line := "- fact until B-10 lands <!--p:" + now.AddDate(0, 0, -c.age).Format("2006-01-02") + "-->"
			if e := got(impl.Classify("learnings.md", line, now, false))(t, mechTiers); e.Stale != c.stale {
				t.Errorf("perishable age %dd: stale=%v, want %v", c.age, e.Stale, c.stale)
			}
		}
	})

	t.Run(s+"tier_defaults_are_section_scoped", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:45
		if e := got(impl.Classify("captain.md", "- the captain merges", now, false))(t, mechTiers); e.Tier != "pinned" {
			t.Errorf("unmarked captain.md entry tier %q, want pinned", e.Tier)
		}
		if e := got(impl.Classify("learnings.md", "- an unmarked learning", now, false))(t, mechTiers); e.Tier != "aging" {
			t.Errorf("unmarked learnings.md entry tier %q, want aging", e.Tier)
		}
	})

	t.Run(s+"marker_and_header_bytes_count_toward_the_budget", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:47
		w := newWorld(t)
		w.notes([]string{"- pinned"}, []string{"- fact <!--a:2026-09-20-->"})
		r := got(impl.Budget(w.ws))(t, mechBudget)
		for _, rel := range []string{captainRel, learningsRel} {
			if want := estimate(w.read(rel)); r.Files[rel] != want {
				t.Errorf("%s estimate %d, want %d (markers and header counted)", rel, r.Files[rel], want)
			}
		}
	})

	t.Run(s+"header_pointer_is_one_line_and_added_once", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:48
		w := newWorld(t)
		w.write(captainRel, "# Captain\n\n- pinned\n")
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		if n := strings.Count(w.read(captainRel), "memory tiers:"); n != 1 {
			t.Errorf("header pointer present %d times after two passes, want 1", n)
		}
	})

	t.Run(s+"unmarked_entry_takes_its_default_tier_never_destructive", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:53
		w := newWorld(t)
		w.notes([]string{"- the captain merges"}, nil)
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		contains(t, w.read(captainRel), "- the captain merges\n", "an unmarked pinned-default entry was changed")
	})

	t.Run(s+"pass_horizon_absent_ignores_counters", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:61
		w := newWorld(t)
		w.notes(nil, []string{"- fact <!--a:2026-09-20/99-->", "- other <!--a:2026-09-20-->"})
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		n := w.read(learningsRel)
		contains(t, n, "- fact <!--a:2026-09-20/99-->", "a counter was read or advanced without the flag")
		contains(t, n, "- other <!--a:2026-09-20-->", "a counter was written without the flag")
	})

	t.Run(s+"pass_horizon_aging_stale_at_10_passes", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:70
		for _, c := range []struct {
			passes int
			stale  bool
		}{{agingPasses - 1, false}, {agingPasses, true}} {
			line := fmt.Sprintf("- fact <!--a:2026-09-20/%d-->", c.passes)
			if e := got(impl.Classify("learnings.md", line, now, true))(t, mechTiers); e.Stale != c.stale {
				t.Errorf("aging %d passes: stale=%v, want %v", c.passes, e.Stale, c.stale)
			}
		}
	})

	t.Run(s+"pass_horizon_perishable_stale_at_3_passes", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:71
		for _, c := range []struct {
			passes int
			stale  bool
		}{{perishablePasses - 1, false}, {perishablePasses, true}} {
			line := fmt.Sprintf("- fact until B-10 lands <!--p:2026-09-23/%d-->", c.passes)
			if e := got(impl.Classify("learnings.md", line, now, true))(t, mechTiers); e.Stale != c.stale {
				t.Errorf("perishable %d passes: stale=%v, want %v", c.passes, e.Stale, c.stale)
			}
		}
	})

	t.Run(s+"pass_horizon_reinforcement_clears_the_counter", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:72
		w := newWorld(t)
		w.write("cox/notes-pass-horizon", "")
		w.notes(nil, []string{"- exercised <!--a:2026-09-01/7-->", "- idle <!--a:2026-09-20/2-->"})
		got(impl.Curate(w.ws, now, []string{"- exercised"}))(t, mechCurate)
		n := w.read(learningsRel)
		contains(t, n, "- exercised <!--a:2026-09-24-->", "reinforcement did not refresh the date and clear the counter")
		contains(t, n, "- idle <!--a:2026-09-20/3-->", "the pass tick did not advance an unreinforced counter")
	})

	t.Run(s+"pass_horizon_removal_leaves_counters_in_place", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:74
		w := newWorld(t)
		w.write("cox/notes-pass-horizon", "")
		w.notes(nil, []string{"- idle <!--a:2026-09-20/5-->"})
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		contains(t, w.read(learningsRel), "- idle <!--a:2026-09-20/6-->", "fixture: the opted-in pass did not tick")
		if err := os.Remove(w.path("cox/notes-pass-horizon")); err != nil {
			t.Fatal(err)
		}
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		contains(t, w.read(learningsRel), "- idle <!--a:2026-09-20/6-->", "a counter was advanced or rewritten after the flag was removed")
	})

	t.Run(s+"rejected_budget_setting_is_an_exception_not_a_default", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:84
		w := newWorld(t)
		w.write(notesBudgetRel, "abc\n")
		w.notes(nil, nil)
		r := got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		if len(r.Exceptions) == 0 {
			t.Error("a rejected budget setting was not reported as an exception")
		}
		if r.ResetSafe {
			t.Error("a pass with a rejected setting claimed reset-safe")
		}
	})

	t.Run(s+"absent_notes_are_not_manufactured", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:87
		w := newWorld(t)
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		for _, rel := range notesFiles {
			if _, err := os.Stat(w.path(rel)); !os.IsNotExist(err) {
				t.Errorf("a pass manufactured an absent %s", rel)
			}
		}
	})

	t.Run(s+"new_entries_are_stamped_with_today_and_tier", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:101
		w := newWorld(t)
		w.notes(nil, []string{"- a brand new learning"})
		got(impl.Curate(w.ws, now, []string{"- a brand new learning"}))(t, mechCurate)
		contains(t, w.read(learningsRel), "- a brand new learning <!--a:2026-09-24-->", "a new entry was not stamped")
	})

	t.Run(s+"pass_tick_increments_unreinforced_counters", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:103
		w := newWorld(t)
		w.write("cox/notes-pass-horizon", "")
		w.notes(nil, []string{"- idle <!--a:2026-09-20-->"})
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		contains(t, w.read(learningsRel), "- idle <!--a:2026-09-20/1-->", "the pass tick did not increment")
	})

	t.Run(s+"stale_unreinforced_entry_is_archived_not_kept", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:104
		w := newWorld(t)
		w.notes(nil, []string{"- stale learning <!--a:2026-08-01-->", "- stale but re-proved <!--a:2026-08-01-->"})
		got(impl.Curate(w.ws, now, []string{"- stale but re-proved"}))(t, mechCurate)
		n := w.read(learningsRel)
		notContains(t, n, "- stale learning", "a stale unreinforced entry was kept by inertia")
		contains(t, n, "- stale but re-proved <!--a:2026-09-24-->", "a re-validated stale entry was not refreshed")
		contains(t, w.read(archiveRel), "stale learning", "the stale entry was not archived")
	})

	t.Run(s+"budget_eviction_takes_oldest_reinforced_aging_first", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:114
		w := newWorld(t)
		w.notes([]string{"- pinned fact"}, []string{
			"- older " + strings.Repeat("o", 90) + " <!--a:2026-09-01-->",
			"- newer " + strings.Repeat("n", 90) + " <!--a:2026-09-20-->",
			"- grace " + strings.Repeat("g", 90), // unmarked legacy: takes <!--g--> this pass, ineligible for eviction
		})
		w.write(notesBudgetRel, fmt.Sprintf("%d\n", w.notesTotal()-20))
		r := got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		n := w.read(learningsRel)
		notContains(t, n, "- older", "eviction did not take the oldest-reinforced aging entry first")
		contains(t, n, "- newer", "eviction took a newer entry before the oldest")
		contains(t, n, "- grace "+strings.Repeat("g", 90)+" <!--g-->", "eviction took a grace entry, which is ineligible")
		contains(t, w.read(archiveRel), "[archived: budget oldest-first]", "eviction reason missing")
		if r.After.Status != "within-budget" {
			t.Errorf("after status %q", r.After.Status)
		}
	})

	t.Run(s+"convergence_precondition_skips_futile_eviction", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:115
		w := newWorld(t)
		w.notes([]string{"- pinned " + strings.Repeat("p", 300)}, []string{"- small <!--a:2026-09-20-->"})
		w.write(notesBudgetRel, "10\n")
		r := got(impl.Curate(w.ws, now, []string{"- small"}))(t, mechCurate)
		contains(t, w.read(learningsRel), "- small", "eviction archived knowledge that could not close the gap")
		if r.Decision == "" {
			t.Error("the pinned-floor shortfall did not open a captain decision")
		}
		contains(t, r.Decision, "raise", "the decision does not offer raising the budget")
	})

	t.Run(s+"pinned_is_never_moved_automatically", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:116
		w := newWorld(t)
		w.notes([]string{"- pinned " + strings.Repeat("p", 300)}, []string{"- fact <!--P-->"})
		w.write(notesBudgetRel, "10\n")
		got(impl.Curate(w.ws, now.AddDate(3, 0, 0), nil))(t, mechCurate)
		n := w.read(captainRel) + w.read(learningsRel)
		contains(t, n, "- pinned "+strings.Repeat("p", 300), "an automatic process moved a pinned Captain entry")
		contains(t, n, "- fact <!--P-->", "an automatic process moved a pinned Learnings entry")
	})

	t.Run(s+"over_budget_never_ends_as_an_accepted_exception", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:126
		w := newWorld(t)
		w.notes([]string{"- pinned " + strings.Repeat("p", 300)}, nil)
		w.write(notesBudgetRel, "10\n")
		r := got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		if r.After.Status == "over-budget" && r.Decision == "" {
			t.Error("the pass ended over budget without a captain decision")
		}
		if r.ResetSafe {
			t.Error("an over-budget pass claimed reset-safe")
		}
	})

	t.Run(s+"archive_is_a_move_with_provenance_under_a_dated_heading", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:134
		w := newWorld(t)
		w.notes(nil, []string{"- stale learning <!--a:2026-08-01-->"})
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		a := w.read(archiveRel)
		contains(t, a, "## 2026-09-24 notes pass", "archive lacks the dated pass heading")
		contains(t, a, "- (from learnings.md, tier: aging, reinforced: 2026-08-01) stale learning [archived: unreinforced 54d]", "archive line lacks provenance")
	})

	t.Run(s+"archive_is_never_counted_by_the_budget", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:134
		w := newWorld(t)
		w.notes(nil, nil)
		w.write(archiveRel, strings.Repeat("archived line\n", 500))
		r := got(impl.Budget(w.ws))(t, mechBudget)
		if _, ok := r.Files[archiveRel]; ok || r.Total != w.notesTotal() {
			t.Errorf("the cold tier was counted: %+v", r)
		}
	})

	t.Run(s+"archive_counter_reason_only_when_the_pass_horizon_caused_it", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:136
		w := newWorld(t)
		w.write("cox/notes-pass-horizon", "")
		w.notes(nil, []string{"- by passes <!--a:2026-09-20/9-->", "- by days <!--a:2026-08-01/2-->"})
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		a := w.read(archiveRel)
		contains(t, a, "by passes [archived: unreinforced 10p]", "a pass-horizon archival lacks its counter reason")
		contains(t, a, "by days [archived: unreinforced 54d]", "a wall-clock archival carried the counter")
	})

	t.Run(s+"unmarked_captain_entry_stays_pinned_through_migration", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:252
		w := newWorld(t)
		w.notes([]string{"- unmarked preference"}, nil)
		got(impl.Curate(w.ws, now.AddDate(1, 0, 0), nil))(t, mechCurate)
		contains(t, w.read(captainRel), "- unmarked preference\n", "an unmarked captain.md entry was migrated or aged")
	})

	t.Run(s+"unevidenced_unmarked_learning_consumes_one_grace_cycle", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:254
		w := newWorld(t)
		w.notes(nil, []string{"- legacy learning"})
		got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		n := w.read(learningsRel)
		contains(t, n, "- legacy learning <!--g-->", "an unevidenced legacy learning did not take the grace marker")
		if strings.Contains(w.read(archiveRel), "legacy learning") {
			t.Error("a legacy learning was archived in its first grace pass")
		}
	})

	t.Run(s+"grace_entry_resolves_on_the_next_pass", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:255
		w := newWorld(t)
		w.notes(nil, []string{"- confirmed legacy <!--g-->", "- unconfirmed legacy <!--g-->"})
		got(impl.Curate(w.ws, now, []string{"- confirmed legacy"}))(t, mechCurate)
		contains(t, w.read(learningsRel), "- confirmed legacy <!--a:2026-09-24-->", "a confirmed grace entry did not get its dated marker")
		contains(t, w.read(archiveRel), "unconfirmed legacy [archived: legacy-unvalidated]", "an unconfirmed grace entry was not archived")
	})

	t.Run(s+"receipt_reports_budget_before_and_after", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:262
		w := newWorld(t)
		w.notes([]string{"- pref"}, []string{"- fact <!--a:2026-09-20-->"})
		before := w.notesTotal()
		r := got(impl.Curate(w.ws, now, []string{"- fact"}))(t, mechCurate)
		if r.Before.Total != before || r.After.Total != w.notesTotal() || r.Before.Budget != defaultNotesBudget {
			t.Errorf("receipt budget facts wrong: %+v", r)
		}
	})

	t.Run(s+"receipt_actions_use_the_fixed_vocabulary", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:263
		w := newWorld(t)
		w.notes([]string{"- pref"}, []string{"- legacy"})
		r := got(impl.Curate(w.ws, now, nil))(t, mechCurate)
		ok := map[string]bool{"unchanged": true, "added": true, "rewritten": true, "pruned": true, "routed": true, "archived": true, "proposed-offload": true}
		for sec, acts := range r.Actions {
			for _, a := range acts {
				if !ok[a] {
					t.Errorf("section %s action %q is outside the vocabulary", sec, a)
				}
			}
		}
		if acts := r.Actions["learnings.md"]; len(acts) == 0 || acts[0] != "rewritten" {
			t.Errorf("adding a grace marker must be 'rewritten', got %v", acts)
		}
	})

	t.Run(s+"reset_safe_only_within_budget_without_exception", func(t *testing.T) {
		// fm: .agents/skills/stow/SKILL.md:268
		w := newWorld(t)
		w.notes([]string{"- pref"}, nil)
		if r := got(impl.Curate(w.ws, now, nil))(t, mechCurate); !r.ResetSafe {
			t.Error("a clean within-budget pass was not reset-safe")
		}
		w.write(notesBudgetRel, "1\n")
		if r := got(impl.Curate(w.ws, now, nil))(t, mechCurate); r.ResetSafe {
			t.Error("an over-budget pass claimed reset-safe")
		}
	})
}

// --- bearings skill: the four-section fleet digest ------------------------------------------------------------------

func TestPortBearingsSkill(t *testing.T) {
	const s = "FM/skill-bearings/"
	sections := []string{"Captain's Call", "Recently Landed", "Underway", "Charted Next"}

	t.Run(s+"digest_is_operationally_read_only", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:21
		w := newWorld(t)
		w.story("s1")
		w.transition("s1", state.Submitted, state.Working)
		w.write("BACKLOG.md", longBacklog(2))
		snap := map[string]string{}
		for _, rel := range []string{"BACKLOG.md", "epics/demo/stories/s1.md", "epics/demo/.cox/events.jsonl"} {
			snap[rel] = w.read(rel)
		}
		digest(t, mechFour, w.opts())
		for rel, was := range snap {
			if w.read(rel) != was {
				t.Errorf("the digest mutated %s", rel)
			}
		}
	})

	t.Run(s+"one_deterministic_fleet_state_source", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:39
		w := newWorld(t)
		w.story("s1")
		w.transition("s1", state.Submitted, state.Working)
		st := got(impl.StoryStates(w.epic))(t, mechFour)
		d := digest(t, mechFour, w.opts())
		for _, x := range st {
			contains(t, d.Text, x.Story+": "+x.State, "the digest's fleet row disagrees with cox state")
		}
	})

	t.Run(s+"four_sections_in_order", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:148
		w := newWorld(t)
		d := digest(t, mechFour, w.opts())
		for i := 1; i < len(sections); i++ {
			before(t, d.Text, sections[i-1], sections[i])
		}
	})

	t.Run(s+"captains_call_holds_only_captain_actions", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:150
		w := newWorld(t)
		w.story("asker")
		w.transition("asker", state.Working, state.InputRequired)
		w.write("epics/demo/questions/asker/q001.md", "---\nid: q001\nstory: asker\n---\nwhich library?\n")
		w.story("busy")
		w.transition("busy", state.Submitted, state.Working)
		d := digest(t, mechFour, w.opts())
		cc := section(d.Text, "Captain's Call")
		contains(t, cc, "asker", "an open question is missing from Captain's Call")
		notContains(t, cc, "busy", "a working story leaked into Captain's Call")
	})

	t.Run(s+"recently_landed_renders_the_current_baseline", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:156
		w := newWorld(t)
		w.story("landed")
		w.transition("landed", state.Working, state.Completed)
		for i := 0; i < 2; i++ { // a repeat still renders the baseline, never a delta
			contains(t, section(digest(t, mechFour, w.opts()).Text, "Recently Landed"), "landed", "a completion was dropped")
		}
	})

	t.Run(s+"underway_one_line_per_working_story", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:158
		w := newWorld(t)
		w.story("busy")
		w.transition("busy", state.Submitted, state.Working)
		if n := strings.Count(section(digest(t, mechFour, w.opts()).Text, "Underway"), "busy"); n != 1 {
			t.Errorf("working story appears %d times in Underway, want 1", n)
		}
	})

	t.Run(s+"charted_next_holds_queued_and_parked_work", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:160
		w := newWorld(t)
		w.story("queued")
		w.transition("queued", "", state.Submitted)
		w.story("parked")
		w.transition("parked", state.Working, state.Parked)
		cn := section(digest(t, mechFour, w.opts()).Text, "Charted Next")
		contains(t, cn, "queued", "a submitted story is missing from Charted Next")
		contains(t, cn, "parked", "a parked story is missing from Charted Next")
	})

	t.Run(s+"every_section_renders_its_empty_state", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:165
		d := digest(t, mechFour, newWorld(t).opts())
		for _, e := range []string{"Nothing needs your action right now", "No recent completions are in the current baseline.", "Nothing is underway.", "Nothing is queued."} {
			contains(t, d.Text, e, "an empty section did not render its empty-state sentence")
		}
	})

	t.Run(s+"pr_appears_as_a_full_url", func(t *testing.T) {
		// fm: .agents/skills/bearings/SKILL.md:175
		w := newWorld(t)
		w.story("pr")
		_, err := wake.Append(w.epic, wake.Wake{Epic: "demo", Story: "pr", Kind: wake.KindPRReady, Note: "PR ready",
			Evidence: map[string]any{"pr": "https://github.com/acme/repo/pull/7"}})
		if err != nil {
			t.Fatal(err)
		}
		d := digest(t, mechFour, w.opts())
		contains(t, d.Text, "https://github.com/acme/repo/pull/7", "a PR was not rendered as its full URL")
		if strings.Contains(d.Text, "#7") && strings.Index(d.Text, "#7") < strings.Index(d.Text, "https://github.com/acme/repo/pull/7") {
			t.Error("a bare #number preceded the full URL")
		}
	})
}

// --- docs/configuration.md: Captain Preferences, Startup memory budget ---------------------------------------------

func TestPortConfigurationDoc(t *testing.T) {
	const s = "FM/doc-configuration/"

	t.Run(s+"captain_preferences_print_in_the_digest", func(t *testing.T) {
		// fm: docs/configuration.md:248
		w := newWorld(t)
		w.notes([]string{"- review before merge"}, []string{"- a learning <!--a:2026-09-20-->"})
		d := digest(t, mechDigest, w.opts())
		nt := between(d.Text, hNotes, hNext)
		contains(t, nt, "- review before merge", "captain preferences missing from the digest")
		if strings.Index(nt, captainRel) > strings.Index(nt, learningsRel) {
			t.Error("captain preferences must print before learnings")
		}
	})

	t.Run(s+"memory_files_count_together", func(t *testing.T) {
		// fm: docs/configuration.md:261
		w := newWorld(t)
		w.notes([]string{"- a"}, []string{"- b <!--a:2026-09-20-->"})
		r := got(impl.Budget(w.ws))(t, mechBudget)
		sum := 0
		for _, n := range r.Files {
			sum += n
		}
		if r.Total != sum || r.Total != w.notesTotal() {
			t.Errorf("total %d is not the sum of the memory files (%d)", r.Total, sum)
		}
	})

	t.Run(s+"default_budget_is_materialized_when_absent", func(t *testing.T) {
		// fm: docs/configuration.md:262
		w := newWorld(t)
		w.notes(nil, nil)
		got(impl.Curate(w.ws, day("2026-09-24"), nil))(t, mechBudget)
		if got := w.read(notesBudgetRel); got != fmt.Sprintf("%d\n", defaultNotesBudget) {
			t.Errorf("default budget file = %q, want %d materialized", got, defaultNotesBudget)
		}
	})

	t.Run(s+"a_valid_value_selects_the_allowance", func(t *testing.T) {
		// fm: docs/configuration.md:263
		w := newWorld(t)
		w.write(notesBudgetRel, "1234\n")
		if r := got(impl.Budget(w.ws))(t, mechBudget); r.Budget != 1234 {
			t.Errorf("budget %d, want 1234", r.Budget)
		}
	})

	t.Run(s+"malformed_values_are_rejected_not_defaulted", func(t *testing.T) {
		// fm: docs/configuration.md:265
		for _, v := range []string{"0\n", "-5\n", "abc\n", "10", "10\n11\n", "10\n\n", " 10\n", "+10\n"} {
			w := newWorld(t)
			w.write(notesBudgetRel, v)
			_, err := impl.Budget(w.ws)
			var ni notImplementedErr
			if errors.As(err, &ni) {
				notImplemented(t, mechBudget)
			}
			if err == nil {
				t.Errorf("budget value %q was accepted", v)
			}
		}
	})

	t.Run(s+"unsafe_budget_files_are_rejected", func(t *testing.T) {
		// fm: docs/configuration.md:266
		cases := map[string]func(w *world){
			"symlink": func(w *world) {
				w.write("elsewhere", "10\n")
				_ = os.Symlink(w.path("elsewhere"), w.path(notesBudgetRel))
			},
			"hardlink": func(w *world) {
				w.write("elsewhere", "10\n")
				_ = os.Link(w.path("elsewhere"), w.path(notesBudgetRel))
			},
			"fifo": func(w *world) { _ = exec.Command("mkfifo", w.path(notesBudgetRel)).Run() },
			"symlinked config dir": func(w *world) {
				w.write("realcox/notes-budget", "10\n")
				_ = os.Remove(w.path("cox"))
				_ = os.Symlink(w.path("realcox"), w.path("cox"))
			},
		}
		for name, mk := range cases {
			w := newWorld(t)
			if err := os.MkdirAll(w.path("cox"), 0o755); err != nil {
				t.Fatal(err)
			}
			mk(w)
			_, err := impl.Budget(w.ws)
			var ni notImplementedErr
			if errors.As(err, &ni) {
				notImplemented(t, mechBudget)
			}
			if err == nil {
				t.Errorf("%s budget file was accepted", name)
			}
		}
	})

	t.Run(s+"report_accounts_absent_and_empty_distinctly", func(t *testing.T) {
		// fm: docs/configuration.md:267
		w := newWorld(t)
		r := got(impl.Budget(w.ws))(t, mechBudget)
		if _, ok := r.Files[learningsRel]; ok || r.Total != 0 {
			t.Errorf("an absent learnings.md was counted: %+v", r)
		}
		w.write(learningsRel, "")
		r = got(impl.Budget(w.ws))(t, mechBudget)
		if n, ok := r.Files[learningsRel]; !ok || n != 0 {
			t.Errorf("an empty learnings.md must be present with 0, got %v %v", n, ok)
		}
	})

	t.Run(s+"estimate_is_ceil_utf8_bytes_over_3_per_file", func(t *testing.T) {
		// fm: docs/configuration.md:268
		w := newWorld(t)
		w.write(learningsRel, "- é\n") // 5 UTF-8 bytes, 4 runes
		r := got(impl.Budget(w.ws))(t, mechBudget)
		if r.Files[learningsRel] != 2 {
			t.Errorf("estimate %d, want ceil(5/3) = 2 (bytes, not runes)", r.Files[learningsRel])
		}
	})
}
