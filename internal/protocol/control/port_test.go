//go:build port

// Port tests (wave 1, cox-supervision-port-busy-wake): firstmate's control-plane suites fm-control, fm-control-relaunch
// and fm-control-herdr-smoke translated case by case against cox's control channel (control.Controller Interrupt/Park/
// Relaunch/Reconcile, the `cox control` and `cox story resume` verbs) and the harness-owned busy record. Firstmate
// pinned at 1e0e773 (references/firstmate, read only). Every case is t.Run("FM/<suite>/<case>") with a
// `// fm: tests/<file>:<line>` citation. A failure names its cox mechanism as red[<mechanism>] or
// notImplemented[<mechanism>]; red is the deliverable (DESIGN translation contract).
//
// Name map (firstmate -> cox): fm-control.sh <id> <verb> -> `cox control <story> <verb>` / control.Controller; exit ->
// park (Stop = terminal close, ADR 0012; no typed exit command); interrupt key -> Backend.Interrupt for a card with
// BackendInterrupt=true, else the inbox interrupt record + the harness extension; the task record (state/<id>.meta) ->
// the story file, the event log, the session and worktree records; relaunch -> Controller.Relaunch behind `cox control
// relaunch` and `cox story resume [--harness]`; a harness switch -> a reroute resume; the turn-end token -> the busy gen;
// reclaim -> Reconcile; the verified agent-state classifier -> Backend.Probe.
package control_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/protocol/control"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/state"
)

// notImplemented fails a case whose firstmate mechanism cox does not have, naming the gap (contract rule 3).
func notImplemented(t *testing.T, mechanism, detail string) {
	t.Helper()
	t.Fatalf("notImplemented[%s]: %s", mechanism, detail)
}

// red records a behaviour that differs from the firstmate case, tagged with the cox mechanism for the red list.
func red(t *testing.T, mechanism, format string, args ...any) {
	t.Helper()
	t.Errorf("red[%s]: %s", mechanism, fmt.Sprintf(format, args...))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const story = "s1"

var sess = backend.Session{Kind: "fake", ID: "ctx_s1", Handle: "term_s1", Story: story}

// epicWith is a temp epic with story s1 (harness h) working at attempt 1.
func epicWith(t *testing.T, h string) string {
	t.Helper()
	epic := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(epic, "stories"), 0o755))
	must(t, os.WriteFile(filepath.Join(epic, "stories", story+".md"), []byte("---\nid: s1\nharness: "+h+"\n---\nbody\n"), 0o644))
	must(t, state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true}))
	return epic
}

func ctl(epic string, b backend.Backend, h string) *control.Controller {
	c := &control.Controller{EpicDir: epic, Backend: b, ParkWait: 50 * time.Millisecond, PollInterval: 5 * time.Millisecond, ExitWait: 50 * time.Millisecond, Warn: &bytes.Buffer{}} // fm FM_CONTROL_EXIT_WAIT=0.05
	if a, ok := registry.Adapter(h); ok {
		c.Harness = a
	}
	return c
}

func snap(t *testing.T, epic string) *state.StorySnap {
	t.Helper()
	events, _, err := state.Load(epic)
	must(t, err)
	return state.Fold(events).Stories[story]
}

func called(b *fake.Backend, op string) int {
	n := 0
	for _, c := range b.Calls {
		if c == op {
			n++
		}
	}
	return n
}

// freshCheckpoint makes park's idle fast path hold (empty composer + a checkpoint newer than the last event), so a park
// case exercises the stop postconditions rather than the checkpoint wait.
func freshCheckpoint(t *testing.T, epic string, b *fake.Backend) {
	t.Helper()
	b.ComposerState = backend.ComposerEmpty
	dir := filepath.Join(epic, "handoffs")
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.WriteFile(filepath.Join(dir, story+".md"), []byte("---\nschema: coxswain.checkpoint.v1\nstory: s1\nattempt: 1\nhead: 0000000000\nbase: origin/epic/e@000\nwritten_at: 2099-01-01T00:00:00Z\nreason: park\n---\n## Next action\ngo\n"), 0o644))
}

// --- the real binary, for the verb-level cases ------------------------------------------------------------------------

var (
	coxOnce sync.Once
	coxPath string
	coxErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if coxPath != "" {
		_ = os.RemoveAll(filepath.Dir(coxPath))
	}
	os.Exit(code)
}

func coxBin(t *testing.T) string {
	t.Helper()
	coxOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cox-port-control-")
		if err != nil {
			coxErr = err
			return
		}
		coxPath = filepath.Join(dir, "cox")
		cmd := exec.Command("go", "build", "-o", coxPath, "./cmd/cox")
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			coxErr = fmt.Errorf("go build cox: %v\n%s", err, out)
		}
	})
	if coxErr != nil {
		t.Fatal(coxErr)
	}
	return coxPath
}

func repoRoot() string { r, _ := filepath.Abs(filepath.Join("..", "..", "..")); return r }

// fakeOrca logs every call and answers the ones a launch and a control verb make; the typed launch line fails so no
// harness starts (the cmd/cox/launch_model_test.go shape).
const fakeOrca = `#!/bin/sh
echo "$@" >> '%LOG%'
case "$1 $2" in
'terminal create') echo '{"ok":true,"result":{"terminal":{"handle":"h1"}}}' ;;
'terminal close') echo '{"ok":true,"result":{}}' ;;
'terminal show')
  if [ "$FAKE_ORCA_PRIOR" = exited ]; then echo '{"ok":true,"result":{"terminal":{"exitCause":"exited"}}}'; else echo '{"ok":false,"error":{"message":"fake orca"}}'; exit 1; fi ;;
*) echo '{"ok":false,"error":{"message":"fake orca"}}'; exit 1 ;;
esac
`

type ws struct {
	epic, wt, log string
	env           []string
}

// workspaceCase builds a workspace with story s1 (frontmatter extra), a git worktree recorded for it, a worker session,
// and a logging fake orca on PATH, like the cmd/cox launch fixture.
func workspaceCase(t *testing.T, frontmatter string) ws {
	t.Helper()
	root := t.TempDir()
	tpl, err := os.ReadFile(filepath.Join(repoRoot(), "templates", "policy.json"))
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(root, "cox"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "cox", "workspace.json"), []byte(`{"schema":"coxswain.workspace.v1"}`), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "cox", "policy.json"), tpl, 0o644))
	c := ws{epic: filepath.Join(root, "epics", "e1"), wt: t.TempDir()}
	must(t, os.MkdirAll(filepath.Join(c.epic, "stories"), 0o755))
	must(t, os.WriteFile(filepath.Join(c.epic, "stories", "s1.md"), []byte("---\nid: s1\n"+frontmatter+"---\nbody\n"), 0o644))
	must(t, state.Append(c.epic, state.Event{Epic: "e1", Story: story, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true}))
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}, {"checkout", "-q", "-b", "story/s1"}} {
		if out, err := exec.Command("git", append([]string{"-C", c.wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	must(t, os.MkdirAll(filepath.Dir(state.WorktreePath(c.epic, story)), 0o755))
	must(t, os.WriteFile(state.WorktreePath(c.epic, story), []byte(c.wt), 0o644))
	sd := filepath.Join(c.epic, state.ControlDir, "sessions")
	must(t, os.MkdirAll(sd, 0o755))
	b, _ := json.Marshal(backend.Session{Kind: "orca", ID: "term_old", Handle: "term_old", Story: story})
	must(t, os.WriteFile(filepath.Join(sd, story+".json"), b, 0o644))
	bin := t.TempDir()
	c.log = filepath.Join(bin, "orca.log")
	must(t, os.WriteFile(filepath.Join(bin, "orca"), []byte(strings.ReplaceAll(fakeOrca, "%LOG%", c.log)), 0o755))
	c.env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "ORCA_RUN_ID=run-fake",
		"COX_PLANE=terminal", "ORCA_TERMINAL_HANDLE=")
	return c
}

func (c ws) cox(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(coxBin(t), args...)
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run cox: %v", err)
	}
	return string(out), code
}

// priorExited makes the story's recorded terminal read as exited (fm alive_as zsh: no agent at the endpoint), so a
// relaunch or resume needs no stop and reaches its launch line.
func (c *ws) priorExited() { c.env = append(c.env, "FAKE_ORCA_PRIOR=exited") }

func (c ws) calls(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(c.log)
	return string(b)
}

// launchedModel is the --model of the launch line typed into the new terminal ("" when none reached orca).
func (c ws) launchLine(t *testing.T) string {
	t.Helper()
	for _, l := range strings.Split(c.calls(t), "\n") {
		if strings.HasPrefix(l, "terminal send") && strings.Contains(l, "COX_STORY") {
			return l
		}
	}
	return ""
}

func TestPortControl(t *testing.T) {
	// n/a exit_types_each_harness_verified_command fm:tests/fm-control.test.sh:271 - cox stops a worker by closing its terminal (ADR 0012), never by typing a per-harness exit command

	// fm: tests/fm-control.test.sh:291 (every cox card gets its own verified delivery: the backend keystroke where the
	// card says it aborts the turn, the inbox interrupt record where it does not)
	t.Run("FM/fm-control/interrupt_sends_each_harness_verified_key", func(t *testing.T) {
		for _, h := range registry.Names() {
			epic := epicWith(t, h)
			b := fake.New()
			if err := ctl(epic, b, h).Interrupt(story, sess); err != nil {
				red(t, "control.interrupt", "%s interrupt failed: %v", h, err)
				continue
			}
			recs, _ := inbox.List(epic, story)
			viaInbox := false
			for _, r := range recs {
				viaInbox = viaInbox || r.Kind == inbox.KindInterrupt
			}
			if want := !registry.Card(h).BackendInterrupt; viaInbox != want {
				red(t, "control.interrupt", "%s: interrupt record written=%v, want %v (card BackendInterrupt=%v)", h, viaInbox, want, registry.Card(h).BackendInterrupt)
			}
		}
	})

	// n/a devin_interrupt_invalidates_busy fm:tests/fm-control.test.sh:327 - devin has no cox harness card
	// n/a devin_idle_interrupt_sends_one_press fm:tests/fm-control.test.sh:346 - devin has no cox harness card
	// n/a devin_exit_after_turn_ended_types_quit_once fm:tests/fm-control.test.sh:363 - devin has no cox harness card
	// n/a devin_interrupt_dismisses_revert_picker fm:tests/fm-control.test.sh:377 - devin has no cox harness card
	// n/a devin_stuck_picker_refuses_and_exit_types_nothing fm:tests/fm-control.test.sh:390 - devin has no cox harness card

	// fm: tests/fm-control.test.sh:410 (cox records canonical harness names; the translated half is "never guess")
	t.Run("FM/fm-control/harness_family_resolution", func(t *testing.T) {
		for _, h := range []string{"claude", "codex", "pi"} {
			if _, ok := registry.Adapter(h); !ok {
				red(t, "control.harness-resolution", "%s does not resolve to its adapter", h)
			}
		}
		for _, h := range []string{"someagent", "", "pid", "claudex"} {
			if _, ok := registry.Adapter(h); ok {
				red(t, "control.harness-resolution", "%q was guessed into an adapter", h)
			}
		}
	})

	// n/a prefixed_recorded_harness_reaches_each_control_verb fm:tests/fm-control.test.sh:437 - grok has no cox card and cox records canonical harness names, never a launch-command basename
	// n/a opencode_interrupts_twice_and_others_once fm:tests/fm-control.test.sh:461 - opencode has no cox harness card

	// fm: tests/fm-control.test.sh:480 (`cox control` resolves an unknown harness to registry.Default() instead of refusing)
	t.Run("FM/fm-control/unverified_harness_is_refused", func(t *testing.T) {
		c := workspaceCase(t, "harness: someagent\n")
		out, code := c.cox(t, "control", story, "interrupt", "--epic", c.epic)
		if code == 0 || strings.Contains(c.calls(t), "--interrupt") || strings.Contains(c.calls(t), "terminal send") {
			red(t, "control.harness-resolution", "a harness with no verified control mechanics was not refused (exit %d, orca calls %q, out %q)", code, c.calls(t), out)
		}
	})

	// n/a backend_key_capability_matrix fm:tests/fm-control.test.sh:494 - tmux/herdr/zellij/cmux send-key surfaces; cox backends deliver interrupts through their own verb
	// n/a harness_kind_capability fm:tests/fm-control.test.sh:518 - per task-kind capability (secondmate), firstmate-only

	// fm: tests/fm-control.test.sh:537 (a harness whose turn the backend keystroke cannot abort gets no keystroke at all)
	t.Run("FM/fm-control/orca_refuses_an_escape_harness_interrupt", func(t *testing.T) {
		epic := epicWith(t, "pi")
		b := fake.New()
		_ = ctl(epic, b, "pi").Interrupt(story, sess)
		if n := called(b, "Interrupt"); n > 0 {
			red(t, "control.interrupt", "a BackendInterrupt=false harness still received the backend keystroke %d time(s); a key the harness does not honour must not be sent", n)
		}
	})

	// fm: tests/fm-control.test.sh:554 (a stop the backend cannot prove refuses both stop verbs)
	t.Run("FM/fm-control/unverified_state_backends_refuse_stop_verbs", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.StopConfirmed = false
		b.Liveness = backend.Unknown
		freshCheckpoint(t, epic, b)
		if err := ctl(epic, b, "claude").Park(story, t.TempDir(), sess); err == nil && snap(t, epic).State == state.Parked {
			red(t, "control.stop-postcondition", "park completed with an unconfirmed stop and an unknown probe (abandon-only)")
		}
		epic2 := epicWith(t, "claude")
		b2 := fake.New()
		b2.StopConfirmed = false
		b2.Liveness = backend.Unknown
		if _, err := ctl(epic2, b2, "claude").Relaunch(story, t.TempDir(), "note", sess, backend.HarnessSpec{Name: "claude"}, nil); err == nil {
			red(t, "control.stop-postcondition", "relaunch spawned after a prior stop it could not prove")
		}
	})

	// n/a state_verified_backends_are_exactly_tmux_and_herdr fm:tests/fm-control.test.sh:585 - firstmate's backend list; cox gates on Probe per backend
	// n/a window_label_is_refused_with_the_exact_id fm:tests/fm-control.test.sh:598 - tmux window labels; cox control takes only a story id
	// n/a explicit_endpoint_is_refused fm:tests/fm-control.test.sh:610 - cox control has no endpoint argument

	// fm: tests/fm-control.test.sh:622
	t.Run("FM/fm-control/unknown_task_is_refused", func(t *testing.T) {
		epic := t.TempDir()
		b := fake.New()
		if err := ctl(epic, b, "claude").Interrupt("ghost", backend.Session{ID: "x", Story: "ghost"}); err == nil || called(b, "Interrupt") > 0 {
			red(t, "control.scoping", "an unrecorded story was interrupted (backend calls %v)", b.Calls)
		}
	})

	// fm: tests/fm-control.test.sh:632
	t.Run("FM/fm-control/record_bound_to_another_task_is_refused", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		other := backend.Session{Kind: "fake", ID: "ctx_s2", Handle: "term_s2", Story: "s2"}
		if err := ctl(epic, b, "claude").Interrupt(story, other); err == nil || called(b, "Interrupt") > 0 {
			red(t, "control.scoping", "a session bound to s2 was interrupted as s1 (backend calls %v)", b.Calls)
		}
	})

	// n/a remote_secondmate_is_refused_by_placement fm:tests/fm-control.test.sh:653 - remote secondmates, firstmate-only

	// fm: tests/fm-control.test.sh:692
	t.Run("FM/fm-control/interrupt_and_exit_lock_before_task_state_resolution", func(t *testing.T) {
		for _, verb := range []string{"interrupt", "park"} {
			epic := epicWith(t, "claude")
			b := fake.New()
			b.Liveness = backend.Alive
			freshCheckpoint(t, epic, b)
			release := holdLock(t, epic)
			// The session now names another story: a verb that resolved state before locking would refuse on that.
			other := backend.Session{Kind: "fake", ID: "ctx_s1", Handle: "term_s1", Story: "other"}
			var err error
			if verb == "interrupt" {
				err = ctl(epic, b, "claude").Interrupt(story, other)
			} else {
				err = ctl(epic, b, "claude").Park(story, t.TempDir(), other)
			}
			release()
			if err == nil || !strings.Contains(err.Error(), "another lifecycle action is already running") {
				red(t, "control.per-story-lock", "%s did not refuse a held lifecycle lock before reading task state: %v", verb, err)
			}
			if len(b.Calls) > 0 {
				red(t, "control.per-story-lock", "contended %s reached the backend: %v", verb, b.Calls)
			}
		}
	})

	// fm: tests/fm-control.test.sh:724
	t.Run("FM/fm-control/verb_allowlist_is_closed", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\n")
		for _, verb := range []string{"send", "keys", "clear", "exit"} {
			if _, code := c.cox(t, "control", story, verb, "--epic", c.epic); code == 0 {
				red(t, "control.verbs", "verb %q was accepted", verb)
			}
		}
		if strings.Contains(c.calls(t), "terminal send") {
			red(t, "control.verbs", "a refused verb still reached the terminal: %q", c.calls(t))
		}
	})

	// fm: tests/fm-control.test.sh:744
	t.Run("FM/fm-control/resume_is_refused_with_its_reason", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\n")
		out, code := c.cox(t, "control", story, "resume", "--epic", c.epic)
		if code == 0 || !strings.Contains(out, "cox story resume") {
			red(t, "control.verbs", "resume refused without naming the reason and the alternative `cox story resume` (exit %d, %q)", code, strings.TrimSpace(out))
		}
	})

	// fm: tests/fm-control.test.sh:756
	t.Run("FM/fm-control/relaunch_only_flags_are_rejected_on_other_verbs", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\n")
		// A refusal happens before any effect: the interrupt must never reach the terminal.
		if _, code := c.cox(t, "control", story, "interrupt", "--note", "progress", "--epic", c.epic); code == 0 || strings.Contains(c.calls(t), "--interrupt") {
			red(t, "control.verbs", "--note (relaunch only) was accepted on interrupt (exit %d, orca calls %q)", code, c.calls(t))
		}
	})

	// fm: tests/fm-control.test.sh:769
	t.Run("FM/fm-control/already_stopped_exit_is_idempotent", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		freshCheckpoint(t, epic, b.Backend)
		must(t, ctl(epic, b, "claude").Park(story, t.TempDir(), sess))
		stops := called(b.Backend, "Stop")
		err := ctl(epic, b, "claude").Park(story, t.TempDir(), sess)
		if err != nil || called(b.Backend, "Stop") != stops {
			red(t, "control.park", "parking an already-parked story is not idempotent success without a second stop (err %v, stops %d->%d)", err, stops, called(b.Backend, "Stop"))
		}
	})

	// fm: tests/fm-control.test.sh:781
	t.Run("FM/fm-control/missing_tmux_endpoint_refuses_rather_than_claiming_a_stop", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.FailNext("Stop", errors.New("terminal not found"))
		b.Liveness = backend.Unknown
		freshCheckpoint(t, epic, b)
		if err := ctl(epic, b, "claude").Park(story, t.TempDir(), sess); err == nil || snap(t, epic).State == state.Parked {
			red(t, "control.stop-postcondition", "an unprovable endpoint was claimed stopped")
		}
	})

	// fm: tests/fm-control.test.sh:800
	t.Run("FM/fm-control/interrupt_refuses_when_no_agent_runs", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.Liveness = backend.Settled
		if err := ctl(epic, b, "claude").Interrupt(story, sess); err == nil || called(b, "Interrupt") > 0 {
			red(t, "control.interrupt", "interrupt keyed a terminal whose agent is not running (probe settled, calls %v)", b.Calls)
		}
	})

	// fm: tests/fm-control.test.sh:812
	t.Run("FM/fm-control/ambiguous_endpoint_refuses", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.Liveness = backend.Unknown
		b.StopConfirmed = true
		freshCheckpoint(t, epic, b)
		if err := ctl(epic, b, "claude").Park(story, t.TempDir(), sess); err == nil || called(b, "Stop") > 0 {
			red(t, "control.stop-postcondition", "park stopped an endpoint whose agent could not be attributed (probe unknown, calls %v)", b.Calls)
		}
	})

	// n/a busy_agent_is_interrupted_before_the_exit_command fm:tests/fm-control.test.sh:824 - cox park closes the terminal; there is no typed exit command for a busy composer to swallow

	// fm: tests/fm-control.test.sh:841
	t.Run("FM/fm-control/idle_agent_is_not_interrupted", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		freshCheckpoint(t, epic, b.Backend)
		must(t, ctl(epic, b, "claude").Park(story, t.TempDir(), sess))
		if called(b.Backend, "Interrupt") > 0 {
			red(t, "control.park", "an idle worker was interrupted before its stop")
		}
	})

	// fm: tests/fm-control.test.sh:856
	t.Run("FM/fm-control/interrupt_without_acknowledgement_preserves_busy_state", func(t *testing.T) {
		epic := epicWith(t, "claude")
		gen, err := busy.Arm(epic, story, "claude", registry.Card("claude").BusySources)
		must(t, err)
		must(t, busy.Apply(epic, story, busy.Busy, gen, "claude-hook", "prompt"))
		must(t, ctl(epic, fake.New(), "claude").Interrupt(story, sess))
		if got := busy.Read(epic, story); got != busy.Busy {
			red(t, "control.interrupt", "an unacknowledged interrupt changed the observed busy state to %q", got)
		}
		if ev := snap(t, epic).LastEvent.Evidence; ev["confirmed"] == true {
			red(t, "control.interrupt", "an unacknowledged interrupt was recorded as confirmed: %v", ev)
		}
	})

	// n/a muse_interrupt_confirms_adapter_acknowledgement fm:tests/fm-control.test.sh:875 - muse has no cox harness card

	// fm: tests/fm-control.test.sh:895
	t.Run("FM/fm-control/interrupt_revalidates_agent_after_acknowledgement_wait", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := &diesOnInterrupt{Backend: fake.New()}
		b.Liveness = backend.Alive
		err := ctl(epic, b, "claude").Interrupt(story, sess)
		if err == nil || !strings.Contains(err.Error(), "after its interrupt") {
			red(t, "control.interrupt-postcondition", "interrupt did not fail when the agent stopped during delivery: %v", err)
		}
		if ev := snap(t, epic).LastEvent.Evidence; ev["verified"] != nil {
			red(t, "control.interrupt-postcondition", "a stale pre-delivery liveness proof was published: %v", ev)
		}
	})

	// fm: tests/fm-control.test.sh:918
	t.Run("FM/fm-control/exit_accepts_agent_stopped_by_busy_interrupt", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.FailNext("Stop", errors.New("terminal already gone"))
		b.Liveness = backend.Settled
		freshCheckpoint(t, epic, b)
		if err := ctl(epic, b, "claude").Park(story, t.TempDir(), sess); err != nil || snap(t, epic).State != state.Parked {
			red(t, "control.park", "a worker already stopped did not satisfy the park postcondition: %v", err)
		}
	})

	// fm: tests/fm-control.test.sh:938
	t.Run("FM/fm-control/agent_that_does_not_stop_fails_closed", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.StopConfirmed = false
		b.Liveness = backend.Alive
		freshCheckpoint(t, epic, b)
		if err := ctl(epic, b, "claude").Park(story, t.TempDir(), sess); err == nil && snap(t, epic).State == state.Parked {
			red(t, "control.stop-postcondition", "a worker that did not stop (probe alive) was recorded parked (abandon-only park)")
		}
	})

	// n/a grok_interrupt_without_acknowledgement_reports_unconfirmed fm:tests/fm-control.test.sh:961 - grok has no cox harness card
	// n/a grok_idle_footer_does_not_confirm_cancellation fm:tests/fm-control.test.sh:974 - grok has no cox harness card
	// n/a secondmate_control_command_carries_no_marker fm:tests/fm-control.test.sh:990 - secondmates and from-firstmate markers, firstmate-only
	// n/a fm_send_still_marks_the_same_secondmate_task fm:tests/fm-control.test.sh:1014 - secondmates and fm-send markers, firstmate-only
}

func relaunch(t *testing.T, epic string, b backend.Backend, wt, note string, prior backend.Session) error {
	t.Helper()
	_, err := ctl(epic, b, "claude").Relaunch(story, wt, note, prior, backend.HarnessSpec{Name: "claude"}, nil)
	return err
}

func TestPortControlRelaunch(t *testing.T) {
	// fm: tests/fm-control-relaunch.test.sh:365 (cox replaces the endpoint by design - close then spawn - so the
	// translated invariant is one live agent in the same worktree and branch at attempt+1)
	t.Run("FM/fm-control-relaunch/same_harness_relaunch_keeps_identity_and_reuses_the_endpoint", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		must(t, relaunch(t, epic, b, gitWT(t), "phase 2 half done", sess))
		s := snap(t, epic)
		if s.State != state.Working || s.Attempt != 2 || called(b.Backend, "Stop") != 1 || called(b.Backend, "Spawn") != 1 {
			red(t, "control.relaunch", "want one stop + one spawn to working attempt 2, got %s attempt %d calls %v", s.State, s.Attempt, b.Calls)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:390
	t.Run("FM/fm-control-relaunch/relaunch_refuses_before_exit_when_the_composer_holds_pending_text", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		b.ComposerState = backend.ComposerPending
		if err := relaunch(t, epic, b, gitWT(t), "note", sess); err == nil || !strings.Contains(err.Error(), "pending text") || called(b.Backend, "Stop") > 0 {
			red(t, "control.relaunch-preflight", "relaunch stopped a worker whose composer holds pending text (calls %v)", b.Calls)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:408
	t.Run("FM/fm-control-relaunch/relaunch_refuses_before_exit_when_the_composer_state_is_unproven", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		b.FailNext("Composer", errors.New("unreadable"))
		if err := relaunch(t, epic, b, gitWT(t), "note", sess); err == nil || !strings.Contains(err.Error(), "not proven empty") || called(b.Backend, "Stop") > 0 {
			red(t, "control.relaunch-preflight", "relaunch stopped a worker whose composer could not be read (calls %v)", b.Calls)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:428
	t.Run("FM/fm-control-relaunch/relaunch_from_linked_home_preserves_recorded_worktree", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := &spawnRecorder{Backend: fake.New()}
		wt := gitWT(t)
		must(t, relaunch(t, epic, b, wt, "note", backend.Session{}))
		if b.wt.Path != wt || b.wt.Branch != "story/"+story {
			red(t, "control.relaunch", "replacement spawned in %+v, want the recorded worktree %s on story/%s", b.wt, wt, story)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:466
	t.Run("FM/fm-control-relaunch/relaunch_preserves_durable_task_metadata", func(t *testing.T) {
		epic := epicWith(t, "claude")
		before, _ := os.ReadFile(filepath.Join(epic, "stories", story+".md"))
		must(t, relaunch(t, epic, fake.New(), gitWT(t), "note", backend.Session{}))
		after, _ := os.ReadFile(filepath.Join(epic, "stories", story+".md"))
		if !bytes.Equal(before, after) {
			red(t, "control.relaunch", "relaunch rewrote the story record")
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:490
	t.Run("FM/fm-control-relaunch/relaunch_serializes_concurrent_durable_metadata_publication", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := &gatedSpawn{Backend: fake.New(), entered: make(chan struct{}), release: make(chan struct{})}
		done := make(chan error, 1)
		go func() { done <- relaunch(t, epic, b, gitWT(t), "continue after publication", backend.Session{}) }()
		<-b.entered // the relaunch holds the lock and is mid-launch
		written := make(chan error, 1)
		go func() {
			// A concurrent durable writer serializes on the same lock instead of interleaving with the relaunch.
			rel, err := control.LockWait(epic, story, 5*time.Second)
			if err == nil {
				err = state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: 2, Actor: state.Leader,
					From: state.Working, To: state.Working, Evidence: map[string]any{"x_request": "request-28"}, ExternalConfirmed: true})
				rel()
			}
			written <- err
		}()
		select {
		case <-written:
			red(t, "control.per-story-lock", "a durable metadata writer was not blocked during relaunch delivery")
		case <-time.After(100 * time.Millisecond):
		}
		close(b.release)
		if err := <-done; err != nil {
			red(t, "control.per-story-lock", "relaunch should complete before the serialized publication: %v", err)
		}
		if err := <-written; err != nil {
			red(t, "control.per-story-lock", "the concurrent publication did not resume after the relaunch committed: %v", err)
		}
		events, _, err := state.Load(epic)
		must(t, err)
		working, published := -1, -1
		for i, e := range events {
			if e.To == state.Working && e.Evidence["verb"] == nil && e.Evidence["dispatch"] != nil {
				working = i
			}
			if e.Evidence["x_request"] == "request-28" {
				published = i
			}
		}
		if working < 0 || published < working {
			red(t, "control.per-story-lock", "relaunch and the concurrent publication interleaved (working at %d, publication at %d)", working, published)
		}
	})

	// n/a disabled_relaunch_clears_prior_trace_context fm:tests/fm-control-relaunch.test.sh:564 - firstmate tracing context

	// fm: tests/fm-control-relaunch.test.sh:584
	t.Run("FM/fm-control-relaunch/relaunch_appends_the_progress_note_to_the_instructions", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := &spawnRecorder{Backend: fake.New()}
		must(t, relaunch(t, epic, b, gitWT(t), "phase 2 half done", backend.Session{}))
		if !strings.Contains(b.brief.Text, "phase 2 half done") || !strings.HasSuffix(b.brief.StoryPath, story+".md") {
			red(t, "control.relaunch", "the replacement's instructions lack the progress note or story: %+v", b.brief)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:611
	t.Run("FM/fm-control-relaunch/relaunch_requires_a_note_for_a_ship_task", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		if err := relaunch(t, epic, b, t.TempDir(), "", sess); err == nil {
			red(t, "control.relaunch-preflight", "a relaunch with no progress note was accepted (the replacement starts blind)")
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:628 (a reroute resume leaves the old harness's worker hooks behind)
	t.Run("FM/fm-control-relaunch/harness_switch_moves_the_record_and_clears_prior_wiring", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\n")
		c.priorExited()
		c.cox(t, "story", "resume", story, "--epic", c.epic, "--allow-unsandboxed")
		if _, err := os.Stat(filepath.Join(c.wt, ".claude", "settings.local.json")); err != nil {
			t.Fatalf("setup: claude dispatch wrote no worker hooks")
		}
		c.cox(t, "story", "resume", story, "--epic", c.epic, "--harness", "codex", "--allow-unsandboxed")
		if b, _ := os.ReadFile(filepath.Join(c.wt, ".claude", "settings.local.json")); strings.Contains(string(b), "busy apply") {
			red(t, "control.relaunch-wiring", "switching to codex left claude's busy hooks wired in the worktree")
		}
		if rec, ok := busy.ReadRecord(c.epic, story); ok && rec.Harness == "claude" {
			red(t, "control.relaunch-wiring", "switching to codex left the claude-armed busy record live (%s %s)", rec.State, rec.Source)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:648
	t.Run("FM/fm-control-relaunch/harness_switch_does_not_carry_the_old_profile_axes", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\nmodel: claude-opus-4-8\n")
		c.priorExited()
		c.cox(t, "story", "resume", story, "--epic", c.epic, "--harness", "codex", "--allow-unsandboxed")
		if l := c.launchLine(t); strings.Contains(l, "claude-opus-4-8") {
			red(t, "control.relaunch", "a harness switch carried the old model: %q", l)
		}
	})

	// n/a harness_switch_resolves_a_prefixed_recorded_harness fm:tests/fm-control-relaunch.test.sh:665 - cox records canonical harness names
	// n/a prefixed_recorded_harness_requires_explicit_replacement fm:tests/fm-control-relaunch.test.sh:692 - cox records canonical harness names

	// fm: tests/fm-control-relaunch.test.sh:723
	t.Run("FM/fm-control-relaunch/same_harness_relaunch_keeps_the_profile_axes", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\nmodel: claude-opus-4-8\n")
		c.priorExited()
		c.cox(t, "control", story, "relaunch", "--note", "resume", "--epic", c.epic, "--allow-unsandboxed")
		if l := c.launchLine(t); !strings.Contains(l, "claude-opus-4-8") {
			red(t, "control.relaunch", "a same-harness relaunch dropped the recorded model: %q", l)
		}
	})

	// n/a native_ultra_relaunch_preserves_profile_and_rejects_before_stop fm:tests/fm-control-relaunch.test.sh:737 - native Ultra profile, firstmate-only

	// fm: tests/fm-control-relaunch.test.sh:762
	t.Run("FM/fm-control-relaunch/explicit_model_wins_over_the_recorded_one", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\nmodel: claude-opus-4-8\n")
		c.priorExited()
		c.cox(t, "story", "resume", story, "--epic", c.epic, "--model", "claude-sonnet-5", "--allow-unsandboxed")
		if l := c.launchLine(t); !strings.Contains(l, "claude-sonnet-5") {
			red(t, "control.relaunch", "the explicit model did not win: %q", l)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:773
	t.Run("FM/fm-control-relaunch/relaunch_onto_an_unverified_harness_is_refused", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\n")
		if _, code := c.cox(t, "story", "resume", story, "--epic", c.epic, "--harness", "someagent", "--allow-unsandboxed"); code == 0 || strings.Contains(c.calls(t), "terminal close") {
			red(t, "control.harness-resolution", "a reroute onto an unverified harness was not refused before the stop (exit %d, calls %q)", code, c.calls(t))
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:784 (the retired incarnation's token is the busy gen: a re-arm revokes it)
	t.Run("FM/fm-control-relaunch/prior_harness_turnend_registry_entry_is_cleared", func(t *testing.T) {
		epic := epicWith(t, "claude")
		old, err := busy.Arm(epic, story, "claude", registry.Card("claude").BusySources)
		must(t, err)
		_, err = busy.Arm(epic, story, "claude", registry.Card("claude").BusySources) // the relaunch re-arm
		must(t, err)
		if err := busy.Apply(epic, story, busy.Idle, old, "claude-hook", "stop"); err == nil {
			red(t, "control.relaunch-wiring", "the retired incarnation's gen still mutates the record")
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:800
	t.Run("FM/fm-control-relaunch/wiring_removal_failure_refuses_before_replacement_arm", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		c := ctl(epic, b, "claude")
		armed := false
		c.Unwire = func() error { return errors.New("permission denied: .claude/settings.local.json") }
		c.Arm = func() (string, error) { armed = true; return "g1.2.3", nil }
		_, err := c.Relaunch(story, gitWT(t), "retry after wiring cleanup", sess, backend.HarnessSpec{Name: "claude"}, nil)
		if err == nil || !strings.Contains(err.Error(), "could not retire claude wiring") {
			red(t, "control.relaunch-wiring", "an undeletable prior hook did not fail closed naming the wiring cleanup: %v", err)
		}
		if armed || called(b.Backend, "Spawn") > 0 {
			red(t, "control.relaunch-wiring", "the replacement was armed or launched after wiring cleanup failed (armed %v, calls %v)", armed, b.Calls)
		}
		if s := snap(t, epic); s.State != state.PendingExternal {
			red(t, "control.relaunch-wiring", "the partial launch failure was not recorded: state %s", s.State)
		}
	})

	// n/a turnend_auth_paths_are_owned_by_the_control_adapter fm:tests/fm-control-relaunch.test.sh:824 - firstmate library ownership of turn-end registry paths
	// n/a secondmate_relaunch_picks_up_the_configured_harness_pin fm:tests/fm-control-relaunch.test.sh:845 - secondmates, firstmate-only
	// n/a secondmate_relaunch_ignores_invalid_configured_effort_before_stop fm:tests/fm-control-relaunch.test.sh:885 - secondmates, firstmate-only
	// n/a secondmate_relaunch_onto_a_crewmate_only_adapter_refuses_before_stop fm:tests/fm-control-relaunch.test.sh:927 - secondmates, firstmate-only
	// n/a explicit_secondmate_harness_ignores_configured_profile_axes fm:tests/fm-control-relaunch.test.sh:963 - secondmates, firstmate-only

	// fm: tests/fm-control-relaunch.test.sh:1000
	t.Run("FM/fm-control-relaunch/ship_relaunch_ignores_the_crew_harness_config", func(t *testing.T) {
		c := workspaceCase(t, "harness: codex\n")
		c.priorExited()
		c.cox(t, "control", story, "relaunch", "--note", "resume", "--epic", c.epic, "--allow-unsandboxed")
		if l := c.launchLine(t); !strings.Contains(l, "'codex'") {
			red(t, "control.relaunch", "relaunch did not keep the recorded harness codex: %q", l)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1014
	t.Run("FM/fm-control-relaunch/spawn_relaunch_without_a_harness_reuses_the_recorded_one", func(t *testing.T) {
		c := workspaceCase(t, "harness: codex\n")
		c.priorExited()
		c.cox(t, "story", "resume", story, "--epic", c.epic, "--allow-unsandboxed")
		if l := c.launchLine(t); !strings.Contains(l, "'codex'") {
			red(t, "control.relaunch", "resume without --harness did not reuse the recorded codex: %q", l)
		}
	})

	// n/a promoted_scout_relaunch_receives_the_current_delivery_contract fm:tests/fm-control-relaunch.test.sh:1028 - scout promotion, firstmate-only
	// n/a prefixed_prior_harness_wiring_is_still_retired fm:tests/fm-control-relaunch.test.sh:1098 - cox records canonical harness names
	// n/a muse_session_binding_is_retired_on_a_harness_switch fm:tests/fm-control-relaunch.test.sh:1122 - muse has no cox harness card
	// n/a cursor_session_binding_is_retired_on_a_harness_switch fm:tests/fm-control-relaunch.test.sh:1138 - cursor has no cox harness card

	// fm: tests/fm-control-relaunch.test.sh:1153
	t.Run("FM/fm-control-relaunch/missing_worktree_refuses_before_stopping_anything", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.StopConfirmed = true
		err := relaunch(t, epic, b, filepath.Join(t.TempDir(), "gone"), "note", sess)
		if err == nil || called(b, "Stop") > 0 {
			red(t, "control.relaunch-preflight", "relaunch with no local copy was not refused before the stop (err %v, calls %v)", err, b.Calls)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1166
	t.Run("FM/fm-control-relaunch/missing_instructions_refuse_before_stopping_anything", func(t *testing.T) {
		epic := epicWith(t, "claude")
		must(t, os.Remove(filepath.Join(epic, "stories", story+".md")))
		b := fake.New()
		if err := relaunch(t, epic, b, t.TempDir(), "note", sess); err == nil || called(b, "Stop") > 0 || called(b, "Spawn") > 0 {
			red(t, "control.relaunch-preflight", "relaunch with no story file was not refused before touching the worker (err %v, calls %v)", err, b.Calls)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1178
	t.Run("FM/fm-control-relaunch/checkpoint_refusal_leaves_the_record_byte_identical", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		wt := gitWT(t)
		before, _ := os.ReadFile(state.EventsPath(epic))
		must(t, os.RemoveAll(filepath.Join(wt, ".git")))
		if err := relaunch(t, epic, b, wt, "x", sess); err == nil {
			red(t, "control.relaunch-checkpoint", "a relaunch from a checkout with no git metadata was not refused")
		}
		if after, _ := os.ReadFile(state.EventsPath(epic)); !bytes.Equal(before, after) {
			red(t, "control.relaunch-checkpoint", "a refused relaunch changed the durable record")
		}
		if called(b.Backend, "Stop") > 0 {
			red(t, "control.relaunch-checkpoint", "a refused relaunch stopped the agent")
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1190
	t.Run("FM/fm-control-relaunch/checkpoint_refuses_uninspectable_head_and_status", func(t *testing.T) {
		realGit, err := exec.LookPath("git")
		must(t, err)
		bin := t.TempDir()
		must(t, os.WriteFile(filepath.Join(bin, "git"), []byte(`#!/bin/sh
for a in "$@"; do
  case "$FAKE_GIT_FAILURE:$a" in head:--verify|head:symbolic-ref|status:status) exit 128 ;; esac
done
exec '`+realGit+`' "$@"
`), 0o755))
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		for failure, want := range map[string]string{"head": "HEAD cannot be inspected", "status": "status cannot be inspected"} {
			epic := epicWith(t, "claude")
			b := alive(true)
			wt := gitWT(t)
			t.Setenv("FAKE_GIT_FAILURE", failure)
			err := relaunch(t, epic, b, wt, "x", sess)
			t.Setenv("FAKE_GIT_FAILURE", "")
			if err == nil || !strings.Contains(err.Error(), want) {
				red(t, "control.relaunch-checkpoint", "an uninspectable %s did not refuse naming the failed proof: %v", failure, err)
			}
			if called(b.Backend, "Stop") > 0 {
				red(t, "control.relaunch-checkpoint", "%s inspection failure stopped the agent", failure)
			}
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1216
	t.Run("FM/fm-control-relaunch/launch_failure_keeps_the_prior_record_and_reports_it", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(true)
		b.FailNext("Spawn", errors.New("launch failed"))
		err := relaunch(t, epic, b, gitWT(t), "note", sess)
		if err == nil || !strings.Contains(err.Error(), "pending_external") || snap(t, epic).State != state.PendingExternal {
			red(t, "control.relaunch", "a launch failure after the stop did not report and keep the real state: %v %s", err, snap(t, epic).State)
		}
	})

	// n/a prepublication_failure_keeps_concurrent_durable_metadata fm:tests/fm-control-relaunch.test.sh:1239 - meta publication rollback; cox's durable record is the append-only event log
	// n/a post_publication_launch_failure_keeps_the_new_record fm:tests/fm-control-relaunch.test.sh:1272 - meta publication rollback; cox's durable record is the append-only event log

	// fm: tests/fm-control-relaunch.test.sh:1289
	t.Run("FM/fm-control-relaunch/stop_transport_failure_reconciles_a_dead_agent", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(false)
		b.FailNext("Stop", errors.New("transport error"))
		if err := relaunch(t, epic, b, gitWT(t), "note", sess); err == nil || called(b.Backend, "Spawn") > 0 || indexOf(b.Calls, "Probe", indexOf(b.Calls, "Stop", 0)) < 0 {
			red(t, "control.stop-postcondition", "a failed prior stop was never reconciled against the agent's real state before spawning a second agent (err %v, calls %v)", err, b.Calls)
		}
	})

	// n/a complete_journal_failure_rolls_back_from_durable_phase fm:tests/fm-control-relaunch.test.sh:1307 - relaunch journal, firstmate-only

	// fm: tests/fm-control-relaunch.test.sh:1330 (a relaunch that aborts before its replacement is live retires the
	// busy record armed for it)
	t.Run("FM/fm-control-relaunch/prepublication_abort_retires_replacement_wiring_and_busy_state", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\n")
		c.priorExited()
		out, code := c.cox(t, "control", story, "relaunch", "--note", "resume", "--epic", c.epic, "--allow-unsandboxed")
		if code == 0 {
			t.Fatalf("setup: the fake launch was expected to fail: %s", out)
		}
		if got := busy.Read(c.epic, story); got == busy.Busy {
			red(t, "control.relaunch-abort", "the aborted relaunch left its replacement's busy record armed busy (a worker that never launched reads busy)")
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1353
	t.Run("FM/fm-control-relaunch/journal_records_the_checkpoint_it_proved", func(t *testing.T) {
		epic := epicWith(t, "claude")
		wt := gitWT(t)
		must(t, os.WriteFile(filepath.Join(wt, "uncommitted.txt"), []byte("scratch\n"), 0o644))
		head, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
		must(t, err)
		must(t, relaunch(t, epic, alive(true), wt, "keeping the scratch file", sess))
		ev := snap(t, epic).LastEvent.Evidence
		if ev["worktree_head"] != strings.TrimSpace(string(head)) {
			red(t, "control.relaunch-checkpoint", "the checkpoint should record the head it preserved: %v", ev["worktree_head"])
		}
		if ev["worktree_dirty"] != "yes" {
			red(t, "control.relaunch-checkpoint", "the checkpoint should record that uncommitted work was present: %v", ev["worktree_dirty"])
		}
		if _, err := os.Stat(filepath.Join(wt, "uncommitted.txt")); err != nil {
			red(t, "control.relaunch-checkpoint", "uncommitted work must survive a relaunch")
		}
	})

	// n/a secondmate_relaunch_checkpoints_child_work_and_spares_the_charter fm:tests/fm-control-relaunch.test.sh:1370 - secondmates, firstmate-only
	// n/a secondmate_relaunch_refuses_an_unmarked_home fm:tests/fm-control-relaunch.test.sh:1413 - secondmates, firstmate-only
	// n/a secondmate_checkpoint_refuses_unreadable_child_state fm:tests/fm-control-relaunch.test.sh:1441 - secondmates, firstmate-only
	// n/a secondmate_checkpoint_ignores_a_vanished_scratch_find_walk fm:tests/fm-control-relaunch.test.sh:1484 - secondmates, firstmate-only

	// fm: tests/fm-control-relaunch.test.sh:1536
	t.Run("FM/fm-control-relaunch/concurrent_relaunch_is_refused", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := fake.New()
		b.Liveness = backend.Alive
		release := holdLock(t, epic)
		err := relaunch(t, epic, b, t.TempDir(), "concurrent", sess)
		release()
		if err == nil || !strings.Contains(err.Error(), "another lifecycle action is already running") {
			red(t, "control.per-story-lock", "a second concurrent control action was not refused: %v", err)
		}
		if called(b, "Stop") > 0 || called(b, "Spawn") > 0 {
			red(t, "control.per-story-lock", "a refused concurrent relaunch touched the agent: %v", b.Calls)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1568
	t.Run("FM/fm-control-relaunch/direct_spawn_relaunch_participates_in_the_lifecycle_lock", func(t *testing.T) {
		c := workspaceCase(t, "harness: claude\n")
		release := holdLock(t, c.epic)
		out, code := c.cox(t, "story", "resume", story, "--epic", c.epic, "--allow-unsandboxed")
		release()
		if code == 0 || !strings.Contains(out, "another lifecycle action is already running") {
			red(t, "control.per-story-lock", "cox story resume did not refuse a held lifecycle lock (exit %d, %q)", code, strings.TrimSpace(out))
		}
		if c.launchLine(t) != "" {
			red(t, "control.per-story-lock", "a contended resume delivered launch bytes: %q", c.calls(t))
		}
	})

	// n/a promotion_participates_in_the_lifecycle_lock_before_metadata_resolution fm:tests/fm-control-relaunch.test.sh:1596 - scout promotion, firstmate-only

	// fm: tests/fm-control-relaunch.test.sh:1625
	t.Run("FM/fm-control-relaunch/spawn_relaunch_refuses_a_live_agent", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := alive(false)
		if err := relaunch(t, epic, b, gitWT(t), "note", sess); err == nil || called(b.Backend, "Spawn") > 0 {
			red(t, "control.stop-postcondition", "relaunch spawned a second agent while the prior one is alive and unconfirmed stopped (calls %v)", b.Calls)
		}
	})

	// n/a spawn_relaunch_refuses_a_symlinked_task_record_before_inspection fm:tests/fm-control-relaunch.test.sh:1636 - firstmate state/<id>.meta symlink handling

	// fm: tests/fm-control-relaunch.test.sh:1663
	t.Run("FM/fm-control-relaunch/spawn_relaunch_keeps_its_early_meta_lock_continuous", func(t *testing.T) {
		epic := epicWith(t, "claude")
		b := &lockObserver{agent: alive(true), lock: control.LockPath(epic, story)}
		must(t, relaunch(t, epic, b, gitWT(t), "note", sess))
		if !b.started {
			red(t, "control.per-story-lock", "test did not observe the relaunch-held lifecycle lock")
		}
		if b.recreated {
			red(t, "control.per-story-lock", "relaunch released or recreated its already-held lifecycle lock (%v)", b.Calls)
		}
	})

	// fm: tests/fm-control-relaunch.test.sh:1693 (a story whose close is authoritative is never relaunched)
	t.Run("FM/fm-control-relaunch/spawn_relaunch_refuses_a_pending_authoritative_close", func(t *testing.T) {
		epic := epicWith(t, "claude")
		must(t, state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: 1, Actor: state.Leader,
			From: state.Working, To: state.Completed, ExternalConfirmed: true}))
		b := fake.New()
		if err := relaunch(t, epic, b, t.TempDir(), "note", sess); err == nil || called(b, "Spawn") > 0 {
			red(t, "control.relaunch-preflight", "a completed story was relaunched (calls %v)", b.Calls)
		}
	})

	// n/a spawn_relaunch_refuses_contradicting_flags fm:tests/fm-control-relaunch.test.sh:1719 - cox relaunch takes no identity-axis flags to contradict

	// fm: tests/fm-control-relaunch.test.sh:1736
	t.Run("FM/fm-control-relaunch/spawn_relaunch_refuses_an_unrecorded_task", func(t *testing.T) {
		epic := t.TempDir()
		must(t, os.MkdirAll(filepath.Join(epic, "stories"), 0o755))
		must(t, os.WriteFile(filepath.Join(epic, "stories", story+".md"), []byte("---\nid: s1\n---\nbody\n"), 0o644))
		b := fake.New()
		if err := relaunch(t, epic, b, "", "note", backend.Session{}); err == nil || called(b, "Spawn") > 0 {
			red(t, "control.relaunch-preflight", "a story never dispatched (no events, no worktree) was relaunched (calls %v)", b.Calls)
		}
	})

	// n/a spawn_relaunch_refuses_a_pane_outside_the_worktree fm:tests/fm-control-relaunch.test.sh:1746 - tmux pane cwd drift; the spawn worktree case is relaunch_from_linked_home_preserves_recorded_worktree
	// n/a tmux_refuses_a_window_missing_from_its_session fm:tests/fm-control-relaunch.test.sh:1807 - tmux backend, firstmate-only
	// n/a tmux_refuses_a_session_that_cannot_be_found fm:tests/fm-control-relaunch.test.sh:1816 - tmux backend, firstmate-only
	// n/a tmux_refuses_when_the_server_is_gone fm:tests/fm-control-relaunch.test.sh:1828 - tmux backend, firstmate-only

	// fm: tests/fm-control-relaunch.test.sh:1839 (reclaim -> Reconcile: an unclassifiable endpoint is never adopted)
	t.Run("FM/fm-control-relaunch/reclaim_refuses_an_unreadable_endpoint", func(t *testing.T) {
		epic := epicWith(t, "claude")
		must(t, state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: 2, Actor: state.Leader,
			From: state.Working, To: state.PendingExternal, Evidence: map[string]any{"intended_to": "working", "verb": "relaunch"}}))
		b := fake.New()
		b.Liveness = backend.Unknown
		if err := ctl(epic, b, "claude").Reconcile(story, sess); err == nil || snap(t, epic).State != state.PendingExternal {
			red(t, "control.reconcile", "an unclassifiable endpoint was adopted: %v %s", err, snap(t, epic).State)
		}
	})

	// n/a herdr_reclaim_adopts_a_pane_that_outlived_its_server fm:tests/fm-control-relaunch.test.sh:2022 - herdr, firstmate-only (DESIGN rule 5)
	// n/a herdr_exit_reports_already_stopped_when_the_pane_outlived_its_server fm:tests/fm-control-relaunch.test.sh:2059 - herdr, firstmate-only
	// n/a herdr_rebind_stays_in_the_recorded_session fm:tests/fm-control-relaunch.test.sh:2078 - herdr, firstmate-only
	// n/a herdr_reclaim_refuses_an_agent_that_came_back fm:tests/fm-control-relaunch.test.sh:2106 - herdr, firstmate-only
	// n/a herdr_reclaim_keeps_the_task_whole fm:tests/fm-control-relaunch.test.sh:2129 - herdr, firstmate-only
	// n/a herdr_rebind_failure_from_a_plain_shell_names_the_real_cause fm:tests/fm-control-relaunch.test.sh:2179 - herdr, firstmate-only
	// n/a herdr_reclaim_of_a_secondmate_names_its_own_owner fm:tests/fm-control-relaunch.test.sh:2203 - herdr and secondmates, firstmate-only
	// n/a relaunch_reverifies_an_already_in_flight_item_instead_of_rewriting_it fm:tests/fm-control-relaunch.test.sh:2220 - tasks-axi backlog items, firstmate-only
	// n/a relaunch_moves_a_drifted_item_back_in_flight fm:tests/fm-control-relaunch.test.sh:2238 - tasks-axi backlog items, firstmate-only
}

// fm-control-herdr-smoke drives a real herdr binary; herdr is a firstmate-only surface (DESIGN rule 5).
// n/a herdr_exit_on_a_pane_with_no_registered_agent_is_idempotent fm:tests/fm-control-herdr-smoke.test.sh:117 - real herdr binary
// n/a herdr_gone_session_reads_recoverable fm:tests/fm-control-herdr-smoke.test.sh:155 - real herdr binary
// n/a herdr_drifted_shell_returns_to_its_worktree fm:tests/fm-control-herdr-smoke.test.sh:194 - real herdr binary
// n/a herdr_interrupt_refuses_with_no_registered_agent fm:tests/fm-control-herdr-smoke.test.sh:203 - real herdr binary
// n/a herdr_interrupt_delivers_and_agent_survives fm:tests/fm-control-herdr-smoke.test.sh:249 - real herdr binary
// n/a herdr_no_verb_removed_the_endpoint_or_copy fm:tests/fm-control-herdr-smoke.test.sh:254 - real herdr binary
// n/a herdr_stale_registration_reads_dead fm:tests/fm-control-herdr-smoke.test.sh:282 - real herdr binary
// n/a herdr_exit_on_a_stale_registration_is_idempotent fm:tests/fm-control-herdr-smoke.test.sh:289 - real herdr binary
// n/a herdr_stale_registration_no_longer_blocks_relaunch fm:tests/fm-control-herdr-smoke.test.sh:308 - real herdr binary
// n/a herdr_unproven_composer_fails_closed fm:tests/fm-control-herdr-smoke.test.sh:325 - real herdr binary

// agent is the fake backend with a live agent at the story's endpoint (fm alive_as claude): Probe reads Alive until a
// Stop kills it (kills), and the composer reads empty.
type agent struct {
	*fake.Backend
	kills bool
}

func alive(kills bool) *agent {
	b := fake.New()
	b.Liveness = backend.Alive
	b.ComposerState = backend.ComposerEmpty
	b.StopConfirmed = kills
	return &agent{Backend: b, kills: kills}
}

func (a *agent) Stop(s backend.Session) (bool, error) {
	ok, err := a.Backend.Stop(s)
	if a.kills {
		a.Liveness = backend.Settled
	}
	return ok, err
}

// gitWT is the story's recorded local copy: a git worktree on story/s1 with one commit (fm add_ship_task).
func gitWT(t *testing.T) string {
	t.Helper()
	wt := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}, {"checkout", "-q", "-b", "story/s1"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return wt
}

// holdLock takes the story's lifecycle lock the way a running control action holds it (fm: a live holder through the
// same lock library) and returns its release.
func holdLock(t *testing.T, epic string) func() {
	t.Helper()
	release, err := control.LockWait(epic, story, 0)
	must(t, err)
	return release
}

// diesOnInterrupt is a backend whose agent stops while its interrupt is delivered (fm FM_FAKE_MUSE_DISAPPEAR_BEFORE_ACK).
type diesOnInterrupt struct{ *fake.Backend }

func (d *diesOnInterrupt) Interrupt(s backend.Session) error {
	err := d.Backend.Interrupt(s)
	d.Liveness = backend.Settled
	return err
}

// gatedSpawn blocks the relaunch's launch until released, so a concurrent writer can be observed against it.
type gatedSpawn struct {
	*fake.Backend
	entered, release chan struct{}
}

func (g *gatedSpawn) Spawn(wt backend.Worktree, h backend.HarnessSpec, b backend.Brief) (backend.Session, error) {
	close(g.entered)
	<-g.release
	return g.Backend.Spawn(wt, h, b)
}

// lockObserver checks at every backend call of a relaunch that the lifecycle lock it saw first is still the same one
// (fm: a sentinel dropped inside the held lock dir must survive to the launch).
type lockObserver struct {
	*agent
	lock               string
	started, recreated bool
}

func (o *lockObserver) observe() {
	sentinel := filepath.Join(o.lock, "continuity-sentinel")
	if !o.started {
		if os.WriteFile(sentinel, nil, 0o600) == nil {
			o.started = true
		}
		return
	}
	if _, err := os.Stat(sentinel); err != nil {
		o.recreated = true
	}
}

func (o *lockObserver) Probe(s backend.Session) (backend.Liveness, error) {
	o.observe()
	return o.agent.Probe(s)
}
func (o *lockObserver) Stop(s backend.Session) (bool, error) { o.observe(); return o.agent.Stop(s) }
func (o *lockObserver) Spawn(wt backend.Worktree, h backend.HarnessSpec, b backend.Brief) (backend.Session, error) {
	o.observe()
	return o.agent.Spawn(wt, h, b)
}

// indexOf is the first index of op in calls at or after from, or -1.
func indexOf(calls []string, op string, from int) int {
	if from < 0 {
		return -1
	}
	for i := from; i < len(calls); i++ {
		if calls[i] == op {
			return i
		}
	}
	return -1
}

// spawnRecorder records the worktree and brief a relaunch spawns with.
type spawnRecorder struct {
	*fake.Backend
	wt    backend.Worktree
	brief backend.Brief
}

func (s *spawnRecorder) Spawn(wt backend.Worktree, h backend.HarnessSpec, b backend.Brief) (backend.Session, error) {
	s.wt, s.brief = wt, b
	return s.Backend.Spawn(wt, h, b)
}
