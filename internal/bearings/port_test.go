//go:build port

// Port tests for the proposed internal/bearings package: firstmate's session-start digest and memory-curation
// contracts, translated case by case from firstmate@1e0e773 (epic cox-supervision-port, story
// cox-supervision-port-session). Cox has no session-start command yet, so every case runs against the local Bearings
// interface below through a notImplemented adapter and fails naming the gap; wave 2 swaps the adapter for the real
// package and removes the port tag from each case it turns green. Firstmate names map to cox names as follows: home ->
// workspace, fleet lock -> leader lease, bootstrap -> cox doctor, state/*.status -> story status wakes, data/backlog.md
// -> BACKLOG.md, data/captain.md + data/learnings.md -> NOTES.md.
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

// Notes constants. NOTES.md has two sections whose default tiers port firstmate's per-file defaults: "## Captain"
// (was data/captain.md, pinned) and "## Learnings" (was data/learnings.md, aging). The budget lives in cox/notes-budget.
const (
	notesBudgetRel     = "cox/notes-budget"
	defaultNotesBudget = 4000 // half the VISION 8,000-token session-start ceiling; the digest gets the other half
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

// Opts is one digest invocation.
type Opts struct {
	Workspace   string            // workspace root: BACKLOG.md, NOTES.md, AGENTS.md, epics/<slug>/
	LeaderID    string            // this session's leader identity (ORCA_TERMINAL_HANDLE in cox)
	Live        func(string) bool // is a leader identity live; the fake stands in for the backend probe
	Harness     string            // claude | codex | pi
	Source      string            // "" (true startup) | resume | compact | clear
	Reemit      bool              // re-print the digest without startup's mutating sweeps
	StatusTail  int               // 0 = defaultStatusTail
	QueuedLimit int               // 0 = defaultQueuedLimit
	Timeout     time.Duration     // runtime bound; 0 = the package default
	Forge       func() error      // the deferred forge/auth probe (gh); a fake may block or fail
	Endpoint    func(epic, story string) (alive bool, handle string)
	StateRead   func(epic, story string) (string, error) // the slow current-state read for an inactive story
	StageCmd    map[string][]string                      // test seam: an extra subprocess a named stage runs (hang injection)
}

// Digest is the printed session-start digest.
type Digest struct {
	Text      string
	ReadOnly  bool
	Truncated bool
}

// StoryState is one story's state as the digest's fleet section needs it.
type StoryState struct {
	Story         string
	State         string
	OpenQuestions []string
}

// BudgetReport is the startup-memory accounting for one workspace.
type BudgetReport struct {
	Budget int            // effective allowance, estimated tokens
	Files  map[string]int // file -> ceil(bytes/3); an absent file has no key
	Total  int
	Status string // within-budget | over-budget
}

// Bearings is the proposed internal/bearings surface: what one session-start command needs from cox's sources of
// truth (cox doctor, cox wake drain, cox state, questions/, BACKLOG.md, NOTES.md).
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
	// Classify reads one NOTES.md entry line under its section's default tier.
	Classify(section, line string, now time.Time, passHorizon bool) (Entry, error)
	// Curate runs the mechanical half of a notes pass: tick, decay, grace migration, budget eviction, archive, and the
	// receipt. reinforced lists the entries this session evidenced (the judgment half stays with the leader).
	Curate(ws string, now time.Time, reinforced []string) (Receipt, error)
}

// Entry is one classified notes entry.
type Entry struct {
	Tier       string // pinned | aging | perishable | grace
	Reinforced string // YYYY-MM-DD, "" for pinned and grace
	Passes     int    // unreinforced-pass counter (pass horizon only)
	Stale      bool
}

// Receipt is a notes pass's completion receipt (stow SKILL.md "Completion receipt").
type Receipt struct {
	Before, After BudgetReport
	Actions       map[string][]string // section -> unchanged | added | rewritten | pruned | routed | archived | proposed-offload
	Archived      []string            // archive lines written this pass
	Exceptions    []string
	Decision      string // the captain decision opened for an unresolved over-budget result, "" when none
	ResetSafe     bool
}

type notImplementedErr struct{ what string }

func (e notImplementedErr) Error() string { return "not implemented: " + e.what }

// notImplementedBearings is the wave 1 adapter: every method reports the gap.
type notImplementedBearings struct{}

func (notImplementedBearings) Digest(Opts) (Digest, error) {
	return Digest{}, notImplementedErr{"Digest"}
}
func (notImplementedBearings) Acquire(string, string, func(string) bool) (bool, error) {
	return false, notImplementedErr{"Acquire"}
}
func (notImplementedBearings) DoctorSummary(string) (string, error) {
	return "", notImplementedErr{"DoctorSummary"}
}
func (notImplementedBearings) WakeDrain(string) ([]wake.Wake, error) {
	return nil, notImplementedErr{"WakeDrain"}
}
func (notImplementedBearings) StoryStates(string) ([]StoryState, error) {
	return nil, notImplementedErr{"StoryStates"}
}
func (notImplementedBearings) BacklogOpenRows(string) (int, error) {
	return 0, notImplementedErr{"BacklogOpenRows"}
}
func (notImplementedBearings) Notes(string, int) (string, error) {
	return "", notImplementedErr{"Notes"}
}
func (notImplementedBearings) Budget(string) (BudgetReport, error) {
	return BudgetReport{}, notImplementedErr{"Budget"}
}
func (notImplementedBearings) Deferred(string, time.Duration) (string, error) {
	return "", notImplementedErr{"Deferred"}
}
func (notImplementedBearings) RunBounded(time.Duration, ...string) (int, error) {
	return 0, notImplementedErr{"RunBounded"}
}

func (notImplementedBearings) Classify(string, string, time.Time, bool) (Entry, error) {
	return Entry{}, notImplementedErr{"Classify"}
}
func (notImplementedBearings) Curate(string, time.Time, []string) (Receipt, error) {
	return Receipt{}, notImplementedErr{"Curate"}
}

var impl Bearings = notImplementedBearings{}

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
		w.write("NOTES.md", "") // present, empty; BACKLOG.md deliberately absent
		d := digest(t, mechDigest, w.opts())
		contains(t, d.Text, "NOTES.md", "digest did not label the notes section")
		contains(t, d.Text, "BACKLOG.md", "digest did not label the backlog section")
		if n := countLines(d.Text, "ABSENT"); n != 1 {
			t.Errorf("want exactly 1 ABSENT marker (BACKLOG.md), got %d", n)
		}
		contains(t, section(d.Text, "NOTES.md"), "(present, empty)", "empty-but-present NOTES.md not distinguished from ABSENT")
		w.write("NOTES.md", "- the captain merges; leaders never push main\n")
		w.write("BACKLOG.md", "| Id | Source | Gap | Status |\n|---|---|---|---|\n| B-01 | x | a gap | open |\n")
		d = digest(t, mechDigest, w.opts())
		contains(t, d.Text, "- the captain merges; leaders never push main", "digest did not print NOTES.md content")
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
		w.write("NOTES.md", "Notes that may be truncated away safely.\n")
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

func (w *world) transition(story string, from, to state.State) {
	w.t.Helper()
	ev := state.Event{Epic: "demo", Story: story, Attempt: 1, Actor: state.Worker, From: from, To: to}
	if err := state.Append(w.epic, ev); err != nil {
		w.t.Fatal(err)
	}
}
