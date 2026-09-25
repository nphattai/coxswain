package watch

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/wake"
)

// B-74 root cause: firstmate decides stale on a hash of the rendered pane (fm-watch.sh hash_pane, tail40), so a worker
// whose background agents keep rendering is never stale; busy/idle stays the harness busy record alone.

// staleRounds polls the rig every DefaultPoll for n StaleMin windows, calling render before each poll, and returns
// the stale-kind wakes raised for the story.
func staleRounds(r *portRig, windows int, render func(i int)) []wake.Wake {
	r.t.Helper()
	var out []wake.Wake
	polls := windows * int(r.w.staleMin()/DefaultPoll)
	for i := 0; i < polls; i++ {
		render(i)
		r.advance(DefaultPoll)
		for _, w := range r.tick() {
			if w.Story == portStory && (w.Kind == wake.KindStale || w.Kind == wake.KindUnknownProbe) {
				out = append(out, w)
			}
		}
	}
	return out
}

// pausedIdleRig is the B-74 shape: a live worker whose turn ended (busy record idle) after it posted a declared wait on
// its own background agents.
func pausedIdleRig(t *testing.T, line string) *portRig {
	t.Helper()
	r := newPortRig(t)
	r.liveness(backend.Alive)
	r.busySet(busy.Idle)
	if line != "" {
		r.mail("m1", "status", line)
	}
	r.pane("⏺ spawned 2 background agents")
	r.tick()
	return r
}

func TestStaleChangingPaneRaisesNoStale(t *testing.T) {
	for _, line := range []string{"paused: waiting on 2 background agents", ""} {
		r := pausedIdleRig(t, line)
		ws := staleRounds(r, 3, func(i int) { r.pane(fmt.Sprintf("⏺ agent 1 running… (%ds)", i)) })
		if len(ws) != 0 {
			t.Errorf("[%q] a pane that changes every poll raised %d stale wake(s) over 3 windows: %v", line, len(ws), kinds(ws))
		}
	}
}

func TestStaleFrozenPaneRaisesExactlyOne(t *testing.T) {
	r := pausedIdleRig(t, "paused: waiting on 2 background agents")
	ws := staleRounds(r, 3, func(int) {})
	if len(ws) != 1 {
		t.Errorf("a frozen pane raised %d stale wake(s) over 3 windows, want exactly one: %v", len(ws), kinds(ws))
	}
}

// An unreadable screen keeps the last good pane hash: a transient read error neither restarts the quiet clock nor
// counts as a change (fm skips the window for that poll).
func TestStaleUnreadableScreenKeepsTheLastHash(t *testing.T) {
	r := pausedIdleRig(t, "")
	before := r.w.activitySig(portStory)
	r.b.FailNext("Screen", errors.New("terminal read failed"))
	if got := r.w.activitySig(portStory); got != before {
		t.Errorf("a failed screen read changed the signature: %q -> %q", before, got)
	}
	r.w.Backend = screenless{r.b}
	ws := staleRounds(r, 1, func(i int) { r.pane(fmt.Sprintf("unseen %d", i)) })
	if len(ws) != 1 {
		t.Errorf("a frozen worker behind an unreadable screen raised %d stale wake(s), want one: %v", len(ws), kinds(ws))
	}
}

// screenless is a backend whose screen can never be read.
type screenless struct{ *fake.Backend }

func (screenless) Screen(backend.Session) ([]string, error) { return nil, errors.New("no screen") }

// screenComposer is a backend whose own composer classifier reads the screen (as Orca's text classifier does), so a
// busy/idle reading that leaked the pane would change with the rows.
type screenComposer struct{ *fake.Backend }

func (b screenComposer) Composer(backend.Session) (string, error) {
	switch rows := strings.Join(b.ScreenRows, "\n"); {
	case strings.Contains(rows, "Working"):
		return backend.ComposerBusy, nil
	case strings.Contains(rows, "proceed?"):
		return backend.ComposerBlocked, nil
	}
	return backend.ComposerEmpty, nil
}

// The pane is a staleness signal only, never a busy/idle source (fm-busy-lib.sh header): once the harness has reported
// busy or idle, busyNow, crewClass and composerState follow that record whatever the screen shows, and busyNow/crewClass
// never read the screen. (A dispatch seed no harness hook has touched yet, or a harness with no busy record, consults
// the backend's own composer - firstmate's launch-prompt backstop and harness-scoped fallbacks over tail40 - which is
// unchanged here and not the stale pane hash.)
func TestPaneIsNeverABusySource(t *testing.T) {
	screens := [][]string{{"❯ "}, {"✻ Working… (12s · esc to interrupt)"}, {"Do you want to proceed?", "❯ 1. Yes"}}
	for _, st := range []string{busy.Busy, busy.Idle} {
		r := newPortRig(t)
		r.w.Backend = screenComposer{r.b}
		r.liveness(backend.Alive)
		gen, err := busy.Arm(r.epic, portStory, "claude", []string{"dispatch", "claude-hook", "recovery"})
		must(t, err)
		must(t, busy.Apply(r.epic, portStory, st, gen, "claude-hook", "hook"))
		r.w.Sessions = LoadSessions(r.epic)
		sess := r.w.Sessions[portStory]
		var want string
		for i, rows := range screens {
			r.pane(rows...)
			r.b.Calls = nil
			got := fmt.Sprintf("busyNow=%v crew=%s", r.w.busyNow(portStory), r.w.crewClass(portStory))
			for _, c := range r.b.Calls {
				if c == "Screen" {
					t.Errorf("[busy %q] busyNow/crewClass read the screen", st)
				}
			}
			got += " composer=" + r.w.composerState(portStory, sess)
			if i == 0 {
				want = got
			} else if got != want {
				t.Errorf("[busy %q] screen %q changed the busy/idle reading: %s, want %s", st, rows, got, want)
			}
		}
	}
}
