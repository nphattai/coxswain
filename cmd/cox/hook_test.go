package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

func noSleep(time.Duration) {}

func seedWake(t *testing.T, epic string, k wake.Kind) {
	t.Helper()
	if _, err := wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: "s", Kind: k, Note: string(k)}); err != nil {
		t.Fatal(err)
	}
}

func seedEvent(t *testing.T, epic string, from, to state.State) {
	t.Helper()
	if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: "s", Attempt: 1, Actor: state.Leader, From: from, To: to, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
}

func promptJSON(prompt string) *strings.Reader {
	return strings.NewReader(`{"prompt":` + strconv.Quote(prompt) + `}`)
}

// The Orca terminal doorbell with no wake pending is suppressed (exit 2, stderr reason), so the stale bell costs no turn.
func TestPromptDrainSuppressesEmptyDoorbell(t *testing.T) {
	epic := t.TempDir()
	var out, errW bytes.Buffer
	in := promptJSON("You have 3 orchestration messages. Run `orca orchestration check --run r` to read them.")
	code := runPromptDrainAll([]string{epic}, "claude", in, &out, &errW)
	if code != 2 {
		t.Fatalf("empty doorbell must exit 2, got %d", code)
	}
	if !bytes.Contains(errW.Bytes(), []byte("no wake pending - suppressed")) {
		t.Fatalf("expected the suppressed stderr line, got %q", errW.String())
	}
	if out.Len() != 0 {
		t.Fatalf("suppressed doorbell must print no context, got %q", out.String())
	}
}

// Orca types its doorbell with a leading newline; the suppression must still fire (A12), or every stale bell costs a turn.
func TestPromptDrainSuppressesDoorbellWithLeadingNewline(t *testing.T) {
	epic := t.TempDir()
	var out, errW bytes.Buffer
	in := promptJSON("\nYou have 1 orchestration message. Run `orca orchestration check --run r` to read them.")
	code := runPromptDrainAll([]string{epic}, "claude", in, &out, &errW)
	if code != 2 {
		t.Fatalf("leading-newline doorbell must exit 2, got %d", code)
	}
	if !bytes.Contains(errW.Bytes(), []byte("no wake pending - suppressed")) {
		t.Fatalf("expected the suppressed stderr line, got %q", errW.String())
	}
}

// The same doorbell with a wake queued passes through and attaches the wake as context (exit 0).
func TestPromptDrainDoorbellWithWakePassesThrough(t *testing.T) {
	epic := t.TempDir()
	seedWake(t, epic, wake.KindWorkerDone)
	var out, errW bytes.Buffer
	in := promptJSON("You have 1 orchestration message. Run `orca orchestration check` to read it.")
	code := runPromptDrainAll([]string{epic}, "claude", in, &out, &errW)
	if code != 0 {
		t.Fatalf("doorbell with a wake must not block, got %d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("Watcher wakes")) {
		t.Fatalf("expected wake context on stdout, got %q", out.String())
	}
}

// A normal prompt with no wakes is silent and never blocks (only the Orca doorbell is suppressed).
func TestPromptDrainNormalPromptNoWake(t *testing.T) {
	epic := t.TempDir()
	var out, errW bytes.Buffer
	code := runPromptDrainAll([]string{epic}, "claude", promptJSON("fix the failing test"), &out, &errW)
	if code != 0 || out.Len() != 0 || errW.Len() != 0 {
		t.Fatalf("normal prompt must be silent exit 0, got code=%d out=%q err=%q", code, out.String(), errW.String())
	}
}

// MAX_WAIT=0: no wait, decide the tick from open stories. A parked story is not open -> no tick (exit 0).
func TestStopRewakeParkedNoTick(t *testing.T) {
	epic := t.TempDir()
	seedEvent(t, epic, state.Working, state.Parked)
	var out bytes.Buffer
	code := runStopRewake(rewakeCfg{epics: []string{epic}, maxWait: 0, batchMax: time.Second, poll: time.Second, out: &out, sleep: noSleep})
	if code != 0 {
		t.Fatalf("parked story must not tick, got exit %d (%q)", code, out.String())
	}
}

// MAX_WAIT=0 with a still-working story ticks (exit 2) so the next turn re-arms the waiter.
func TestStopRewakeWorkingTicks(t *testing.T) {
	epic := t.TempDir()
	seedEvent(t, epic, state.Submitted, state.Working)
	var out bytes.Buffer
	code := runStopRewake(rewakeCfg{epics: []string{epic}, maxWait: 0, batchMax: time.Second, poll: time.Second, out: &out, sleep: noSleep})
	if code != 2 {
		t.Fatalf("working story must tick, got exit %d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("Rewake tick")) {
		t.Fatalf("expected a Rewake tick line, got %q", out.String())
	}
}

// An urgent wake rewakes on the first peek, before any sleep.
func TestStopRewakeUrgentExitsAtOnce(t *testing.T) {
	epic := t.TempDir()
	seedWake(t, epic, wake.KindWorkerDone)
	slept := 0
	var out bytes.Buffer
	code := runStopRewake(rewakeCfg{epics: []string{epic}, maxWait: time.Second, batchMax: time.Hour, poll: time.Second, out: &out, sleep: func(time.Duration) { slept++ }})
	if code != 2 {
		t.Fatalf("urgent wake must exit 2, got %d", code)
	}
	if slept != 0 {
		t.Fatalf("urgent wake must not wait, slept %d times", slept)
	}
}

// A routine wake batches up to WAKE_BATCH, then rewakes once (not per wake).
func TestStopRewakeRoutineBatches(t *testing.T) {
	epic := t.TempDir()
	seedWake(t, epic, wake.KindStatus)
	var out bytes.Buffer
	// poll 1s, batchMax 2s: iters accumulate batch 0 ->1 ->2, third iter (batch>=2) rewakes.
	code := runStopRewake(rewakeCfg{epics: []string{epic}, maxWait: time.Minute, batchMax: 2 * time.Second, poll: time.Second, out: &out, sleep: noSleep})
	if code != 2 {
		t.Fatalf("batched routine wake must eventually rewake, got %d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("Watcher wake while idle")) {
		t.Fatalf("expected the wake-drain prompt, got %q", out.String())
	}
}

// A codex UserPromptSubmit payload (extra fields: session_id, turn_id, cwd, model, ...) still yields the prompt and,
// with a wake queued, attaches it as context (stdout) on exit 0 - the shared path works unchanged for codex.
func TestPromptDrainCodexPayloadPassesThrough(t *testing.T) {
	epic := t.TempDir()
	seedWake(t, epic, wake.KindWorkerDone)
	codexIn := strings.NewReader(`{"session_id":"s1","turn_id":"t1","cwd":"/w","model":"gpt-5.6-sol","hook_event_name":"UserPromptSubmit","prompt":"continue"}`)
	var out, errW bytes.Buffer
	if code := runPromptDrainAll([]string{epic}, "codex", codexIn, &out, &errW); code != 0 {
		t.Fatalf("codex prompt with a wake must exit 0, got %d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("Watcher wakes")) {
		t.Fatalf("expected wake context on stdout, got %q", out.String())
	}
}

// A stale doorbell under codex is suppressed with a stdout block decision (exit 0), not Claude's exit 2.
func TestPromptDrainCodexSuppressesDoorbellWithBlockDecision(t *testing.T) {
	epic := t.TempDir()
	in := promptJSON("You have 2 orchestration messages. Run `orca orchestration check` to read them.")
	var out, errW bytes.Buffer
	if code := runPromptDrainAll([]string{epic}, "codex", in, &out, &errW); code != 0 {
		t.Fatalf("codex suppression must exit 0 (block via stdout), got %d", code)
	}
	var dec map[string]string
	if err := json.Unmarshal(out.Bytes(), &dec); err != nil {
		t.Fatalf("codex must emit a JSON block decision on stdout, got %q (%v)", out.String(), err)
	}
	if dec["decision"] != "block" || !strings.Contains(dec["reason"], "no wake pending") {
		t.Fatalf("wrong block decision: %+v", dec)
	}
}

// stop-rewake under codex reopens the turn with a stdout block decision (exit 0) carrying the wake instructions.
func TestStopRewakeCodexReopensWithBlockDecision(t *testing.T) {
	epic := t.TempDir()
	seedWake(t, epic, wake.KindWorkerDone)
	var stderrOut, stdout bytes.Buffer
	code := runStopRewake(rewakeCfg{epics: []string{epic}, harness: "codex", maxWait: time.Second, batchMax: time.Hour, poll: time.Second, out: &stderrOut, stdout: &stdout, sleep: noSleep})
	if code != 0 {
		t.Fatalf("codex reopen must exit 0, got %d", code)
	}
	var dec map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &dec); err != nil {
		t.Fatalf("codex must emit a JSON block decision on stdout, got %q (%v)", stdout.String(), err)
	}
	if dec["decision"] != "block" || !strings.Contains(dec["reason"], "Watcher wake while idle") {
		t.Fatalf("wrong reopen decision: %+v", dec)
	}
	if stderrOut.Len() != 0 {
		t.Fatalf("codex reopen must not write the exit-2 text sink, got %q", stderrOut.String())
	}
}

// refreshFacts replaces a prior facts block instead of stacking, keeps the body, and refreshes written_at.
func TestRefreshFacts(t *testing.T) {
	base := "---\nschema: coxswain.checkpoint.v1\nstory: s\nwritten_at: 2026-09-15T00:00:00Z\nreason: x\n---\n## Next action\ngo\n"
	once := refreshFacts(base, "## Facts (máy tính)\n- head: aaa\n", "2026-09-16T10:00:00Z")
	if strings.Count(once, factsHeading) != 1 || !strings.Contains(once, "## Next action") {
		t.Fatalf("first refresh wrong:\n%s", once)
	}
	if strings.Contains(once, "2026-09-15T00:00:00Z") || !strings.Contains(once, "written_at: 2026-09-16T10:00:00Z") {
		t.Fatalf("written_at not refreshed:\n%s", once)
	}
	// A second refresh must not stack a second block.
	twice := refreshFacts(once, "## Facts (máy tính)\n- head: bbb\n", "2026-09-16T11:00:00Z")
	if strings.Count(twice, factsHeading) != 1 || !strings.Contains(twice, "head: bbb") || strings.Contains(twice, "head: aaa") {
		t.Fatalf("second refresh stacked or kept stale facts:\n%s", twice)
	}
}

// prompt-drain attaches the wakes of every active epic, each under its own epic name (AC 4: two-epic drain).
func TestPromptDrainAllAcrossEpics(t *testing.T) {
	e1 := t.TempDir()
	e2 := t.TempDir()
	seedWake(t, e1, wake.KindWorkerDone)
	seedWake(t, e2, wake.KindStatus)
	var out, errW bytes.Buffer
	if code := runPromptDrainAll([]string{e1, e2}, "claude", promptJSON("continue"), &out, &errW); code != 0 {
		t.Fatalf("multi-epic drain exit %d", code)
	}
	s := out.String()
	if !strings.Contains(s, filepath.Base(e1)) || !strings.Contains(s, filepath.Base(e2)) {
		t.Fatalf("expected both epic names in drain output:\n%s", s)
	}
}

// leaderEpics narrows to an explicit --epic, and otherwise returns only the epics with a live watcher under the
// workspace found by walking up from the cwd (AC 4: narrowing --epic, active-epic discovery).
func TestLeaderEpicsNarrowAndActive(t *testing.T) {
	eps, in := leaderEpics("/x/proj/epics/foo")
	if !in || len(eps) != 1 || eps[0] != "/x/proj/epics/foo" {
		t.Fatalf("narrowing: got %v,%v", eps, in)
	}

	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"repos":[{"alias":"a","path":"/x","production":"main"}]}`)
	live := filepath.Join(ws, "proj", "epics", "live")
	dead := filepath.Join(ws, "proj", "epics", "dead")
	if err := os.MkdirAll(filepath.Join(live, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dead, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(live, ".cox", "watch.pid"), strconv.Itoa(os.Getpid()))
	mustWrite(t, filepath.Join(dead, ".cox", "watch.pid"), "999999")
	got := activeEpics(ws)
	if len(got) == 1 && got[0] == dead {
		t.Skip("pid 999999 happens to be alive on this host")
	}
	if len(got) != 1 || got[0] != live {
		t.Fatalf("activeEpics = %v, want [%s]", got, live)
	}
}

// Outside a workspace, hook resolution reports not-in-workspace and outsideWorkspace exits 0 (AC 4).
func TestLeaderEpicsOutsideWorkspace(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, in := leaderEpics(""); in {
		t.Error("a temp dir with no cox/workspace.json above it must not resolve as inside a workspace")
	}
	if code := outsideWorkspace("prompt-drain"); code != 0 {
		t.Errorf("outsideWorkspace exit %d, want 0", code)
	}
}

// notLeaderTerminal suppresses the leader-only hooks in any terminal whose handle is not the recorded leader, and is a
// no-op guard when no leader is recorded.
func TestNotLeaderTerminal(t *testing.T) {
	epic := t.TempDir()
	// No .cox/leader: leader unknown, do not suppress.
	t.Setenv("ORCA_TERMINAL_HANDLE", "term_worker")
	if notLeaderTerminal(epic) {
		t.Error("with no recorded leader, should not suppress")
	}
	// Leader recorded, this terminal is a different handle: suppress.
	if err := os.MkdirAll(filepath.Join(epic, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, ".cox", "leader"), []byte("term_leader\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !notLeaderTerminal(epic) {
		t.Error("a non-leader handle should be suppressed")
	}
	// This terminal IS the leader: do not suppress.
	t.Setenv("ORCA_TERMINAL_HANDLE", "term_leader")
	if notLeaderTerminal(epic) {
		t.Error("the leader terminal must not be suppressed")
	}
}
