package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// gitInitRepo makes a git repo with one commit, so checkpoint.Facts (which reads HEAD) works against it.
func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
}

// A terminal-plane worker's checkpoint hooks run with no flags and rely on the exported COX_EPIC/COX_STORY (the worker
// plane keeps that env). From a worktree outside any workspace, precompact must still write the worker checkpoint at the
// env epic - not walk up, find no workspace, and skip it (PR#3 review round 2, finding 1).
func TestCheckpointHooksUseWorkerEnvOutsideWorkspace(t *testing.T) {
	epic := t.TempDir()
	wt := t.TempDir()
	gitInitRepo(t, wt)
	t.Setenv("COX_EPIC", epic)
	t.Setenv("COX_STORY", "m1")
	t.Chdir(t.TempDir()) // cwd is outside any cox workspace
	if code := cmdHook([]string{"precompact", "--worktree", wt}); code != 0 {
		t.Fatalf("worker precompact exit %d (COX_EPIC/COX_STORY env fallback lost?)", code)
	}
	if _, err := os.Stat(checkpoint.Path(epic, "m1")); err != nil {
		t.Errorf("worker checkpoint not written from the COX_EPIC/COX_STORY env: %v", err)
	}
}

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

// activeEpics also finds epics under the two-level nested-project layout (<ws>/apps/foo/epics/<slug>), so nested
// projects still get leader hooks (PR#3 review finding 4).
func TestActiveEpicsNestedProject(t *testing.T) {
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"repos":[{"alias":"a","path":"/x","production":"main"}]}`)
	nested := filepath.Join(ws, "apps", "foo", "epics", "e")
	if err := os.MkdirAll(filepath.Join(nested, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(nested, ".cox", "watch.pid"), strconv.Itoa(os.Getpid()))
	got := activeEpics(ws)
	found := false
	for _, e := range got {
		if e == nested {
			found = true
		}
	}
	if !found {
		t.Fatalf("activeEpics did not find the nested-project epic: %v", got)
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

// After a leader harness restart, the recorded handle is dead and Orca hands the same pane a new handle. In the epic's
// workspace, leaderTerminal treats this terminal as the leader and re-binds .cox/leader to the new handle, so a restart
// never orphans the epic (finding 4). Outside the workspace, or while the recorded handle is still live, it does not.
func TestLeaderTerminalRebindsAfterRestart(t *testing.T) {
	// A workspace with an epic under it.
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "cox", "workspace.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	epic := filepath.Join(ws, "proj", "epics", "e1")
	if err := os.MkdirAll(filepath.Join(epic, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStaleLeader := func() {
		if err := os.WriteFile(filepath.Join(epic, ".cox", "leader"), []byte("term_old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ORCA_TERMINAL_HANDLE", "term_new")

	// A dead recorded handle + this terminal in the epic's workspace -> leader, and .cox/leader is re-bound to term_new.
	writeStaleLeader()
	t.Chdir(ws)
	restore := probeLeaderHandle
	t.Cleanup(func() { probeLeaderHandle = restore })
	probeLeaderHandle = func(string, string) (bool, bool) { return false, true } // recorded handle is dead

	isLeader, rebound := leaderTerminal(epic)
	if !isLeader || !rebound {
		t.Fatalf("dead handle in-workspace: isLeader=%v rebound=%v, want true/true", isLeader, rebound)
	}
	if got := readLeader(epic); got != "term_new" {
		t.Errorf(".cox/leader not re-bound: %q, want term_new", got)
	}
	// filterLeaderEpics (the prompt-drain / stop-rewake path) keeps the epic and drives the same rebind.
	writeStaleLeader()
	if got := filterLeaderEpics([]string{epic}); len(got) != 1 || got[0] != epic {
		t.Errorf("filterLeaderEpics must keep the restarted-leader epic: %v", got)
	}
	if got := readLeader(epic); got != "term_new" {
		t.Errorf("filterLeaderEpics must re-bind .cox/leader: %q", got)
	}

	// A still-live recorded handle -> a different leader owns the epic; do not steal it, do not rebind.
	writeStaleLeader()
	probeLeaderHandle = func(string, string) (bool, bool) { return true, true }
	if isLeader, rebound = leaderTerminal(epic); isLeader || rebound {
		t.Errorf("a live recorded handle must not be stolen: isLeader=%v rebound=%v", isLeader, rebound)
	}
	if got := readLeader(epic); got != "term_old" {
		t.Errorf("a live leader's .cox/leader must be untouched: %q", got)
	}

	// A dead handle but this terminal is NOT in the epic's workspace -> not the leader, no rebind.
	writeStaleLeader()
	probeLeaderHandle = func(string, string) (bool, bool) { return false, true }
	t.Chdir(t.TempDir()) // cwd no longer under the epic's workspace
	if isLeader, rebound = leaderTerminal(epic); isLeader || rebound {
		t.Errorf("dead handle out-of-workspace must not become leader: isLeader=%v rebound=%v", isLeader, rebound)
	}
	if got := readLeader(epic); got != "term_old" {
		t.Errorf("out-of-workspace must not re-bind: %q", got)
	}
}
