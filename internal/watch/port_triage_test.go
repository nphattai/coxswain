//go:build port

// Port tests (wave 1, cox-supervision-port-triage): firstmate's wake triage translated case by case against cox's
// watcher passes. Firstmate pinned at 1e0e773 (references/firstmate, read only). Every case is
// t.Run("FM/<suite>/<case>") with a `// fm: path:line` citation and a `// cox:` mechanism tag; a case whose mechanism
// cox lacks calls notImplemented and fails. Red is the deliverable (DESIGN translation contract rules 1-8).
package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// notImplemented fails a case whose firstmate mechanism cox does not have, naming the gap (contract rule 3).
func notImplemented(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("not implemented in cox: %s", mechanism)
}

// portRig is one hermetic watcher under test: a temp epic, the repo's fake backend, and a mutable clock.
type portRig struct {
	t     *testing.T
	epic  string
	b     *fake.Backend
	mb    *fake.Mailbox
	clock time.Time
	w     *Watcher
}

const portStory = "s1"

// newPortRig builds an epic with story s1 in state working, a session for it (dispatch "ctx_s1"), and a leader handle.
// The clock starts at the real now so busy records (stamped with time.Now by the busy package) line up with it.
func newPortRig(t *testing.T) *portRig {
	t.Helper()
	epic := t.TempDir()
	r := &portRig{t: t, epic: epic, b: fake.New(), clock: time.Now()}
	r.mb = r.b.Mail().(*fake.Mailbox)
	r.w = &Watcher{EpicDir: epic, Backend: r.b, Now: func() time.Time { return r.clock }}
	portMust(t, state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: portStory, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true,
		Evidence: map[string]any{"dispatch": "ctx_" + portStory}}))
	portSession(t, epic, portStory, backend.Session{ID: "ctx_" + portStory, Handle: "term_" + portStory})
	portMust(t, os.WriteFile(filepath.Join(epic, state.ControlDir, "leader"), []byte("term_leader"), 0o644))
	return r
}

func portMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func portSession(t *testing.T, epic, story string, sess backend.Session) {
	t.Helper()
	dir := filepath.Join(epic, state.ControlDir, "sessions")
	portMust(t, os.MkdirAll(dir, 0o755))
	b, err := json.Marshal(sess)
	portMust(t, err)
	portMust(t, os.WriteFile(filepath.Join(dir, story+".json"), b, 0o644))
}

// busySet arms the story's busy record for a claude worker and applies state s through the trusted claude-hook source.
func (r *portRig) busySet(s string) {
	r.t.Helper()
	gen, err := busy.Arm(r.epic, portStory, "claude", []string{"dispatch", "claude-hook", "recovery"})
	portMust(r.t, err)
	if s != busy.Busy {
		portMust(r.t, busy.Apply(r.epic, portStory, s, gen, "claude-hook", "Stop"))
	}
}

// mail queues one worker message from the story's dispatch.
func (r *portRig) mail(id, typ, subject string) {
	r.mb.Queue = append(r.mb.Queue, backend.Message{ID: id, From: "dispatch:ctx_" + portStory, Type: typ, Subject: subject,
		Payload: `{"dispatchId":"ctx_` + portStory + `"}`})
}

// advance moves the fake clock forward.
func (r *portRig) advance(d time.Duration) { r.clock = r.clock.Add(d) }

// tick runs one watcher pass and returns the wakes appended by it.
func (r *portRig) tick() []wake.Wake {
	r.t.Helper()
	before, err := wake.Drain(r.epic, true)
	portMust(r.t, err)
	_, err = r.w.Tick()
	portMust(r.t, err)
	after, err := wake.Drain(r.epic, true)
	portMust(r.t, err)
	return after[len(before):]
}

// urgentFor reports whether any wake in ws is urgent for the story.
func urgentFor(ws []wake.Wake, story string) bool {
	for _, w := range ws {
		if w.Story == story && wake.IsUrgent(w.Kind) {
			return true
		}
	}
	return false
}

// composer sets what the backend's own composer classifier reports (used only when no busy record answers).
func (r *portRig) composer(cs string) { r.b.ComposerState = cs }

// liveness sets what a backend Probe reports.
func (r *portRig) liveness(l backend.Liveness) { r.b.Liveness = l }

// steer writes one leader steer into the story's durable inbox (its mtime is the real now; advance the clock to age it).
func (r *portRig) steer(text string) string {
	r.t.Helper()
	p, err := inbox.Write(r.epic, portStory, text, inbox.Steer, "")
	portMust(r.t, err)
	return p
}

// reply writes a question answer into the inbox (kind=reply), as `cox reply` does.
func (r *portRig) reply(text string) string {
	r.t.Helper()
	p, err := inbox.WriteReply(r.epic, portStory, text)
	portMust(r.t, err)
	return p
}

// heartbeat queues a heartbeat mail (phase optional), the liveness ping stalePass ages.
func (r *portRig) heartbeat(id string) {
	r.mb.Queue = append(r.mb.Queue, backend.Message{ID: id, From: "dispatch:ctx_" + portStory, Type: "heartbeat",
		Payload: `{"dispatchId":"ctx_` + portStory + `"}`})
}

// kinds lists the kinds of ws for failure messages.
func kinds(ws []wake.Wake) []wake.Kind {
	out := make([]wake.Kind, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Kind)
	}
	return out
}

// wantSurfaced asserts the tick raised an urgent wake for the story (firstmate: "surfaced" - queued and the watcher exits).
func wantSurfaced(t *testing.T, ws []wake.Wake, why string) {
	t.Helper()
	if !urgentFor(ws, portStory) {
		t.Errorf("want an urgent wake (%s), got %v", why, kinds(ws))
	}
}

// wantAbsorbed asserts the tick raised no wake for the story (firstmate: "absorbed" - no queue entry, no exit).
func wantAbsorbed(t *testing.T, ws []wake.Wake, why string) {
	t.Helper()
	for _, w := range ws {
		if w.Story == portStory {
			t.Errorf("want absorbed (%s), got wake %s: %s", why, w.Kind, w.Note)
		}
	}
}

func TestPortHarness(t *testing.T) {
	t.Run("Harness/smoke", func(t *testing.T) {
		r := newPortRig(t)
		if ws := r.tick(); len(ws) != 0 {
			t.Fatalf("empty epic tick appended %v", ws)
		}
	})
}
