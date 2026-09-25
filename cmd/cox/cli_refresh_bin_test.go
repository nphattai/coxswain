package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/forge/github"
	"github.com/nphattai/coxswain/internal/bearings"
	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/protocol/report"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// B-54 against the real binary: a Stop waiter (one per terminal: it holds the terminal's single-waiter lock) started in a
// workspace with no active epic keeps waiting and rewakes
// (exit 2) on a wake in an epic whose watcher started after the waiter did. Before the fix the epic set was resolved
// once, so the waiter returned at once (exit 0) and the new epic's wake waited for the leader's next turn.
func TestStopRewakeBinarySeesEpicOpenedMidWait(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	cmd := exec.Command(fmCoxBin(t), "hook", "stop-rewake", "--harness", "claude")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "REWAKE_MAX_WAIT=30", "REWAKE_POLL=1", "TMPDIR="+t.TempDir(), "ORCA_TERMINAL_HANDLE=term-b54")
	cmd.Stdin = strings.NewReader(`{"session_id":"b54"}`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("waiter ended before any epic existed (err %v): the epic set was frozen at start\n%s", err, stderr.String())
	case <-time.After(1500 * time.Millisecond):
	}
	epic := filepath.Join(root, "app", "epics", "late")
	if err := os.MkdirAll(filepath.Join(epic, controlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(watchPidPath(epic), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wake.Append(epic, wake.Wake{Epic: "late", Story: "s", Kind: wake.KindWorkerDone, Note: "done"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 {
			t.Fatalf("waiter exit = %v, want exit 2 on the late epic's wake\n%s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "Watcher wake while idle") {
			t.Errorf("rewake text missing:\n%s", stderr.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("waiter never saw the epic opened mid-wait\n%s", stderr.String())
	}
}

// runCox runs the real built cox binary in dir with HOME pinned (TestMain already dropped the worker env) and returns
// stdout, stderr and the exit code.
func runCox(t *testing.T, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(fmCoxBin(t), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"HOME=" + t.TempDir()}, env...)...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run cox %v: %v", args, err)
	}
	return so.String(), se.String(), code
}

// B-56 against the real binary: a story resumed to attempt 3 whose handoff was written at attempt 1. precompact must
// stamp the current attempt, so session-start injects the checkpoint instead of refusing a wrong-attempt one (F07).
func TestPreCompactBinaryStampsCurrentAttempt(t *testing.T) {
	epic := t.TempDir()
	wt := t.TempDir()
	gitInitRepo(t, wt)
	for _, e := range []state.Event{
		{Story: "s", Attempt: 1, From: state.Submitted, To: state.Working},
		{Story: "s", Attempt: 1, From: state.Working, To: state.Failed},
		{Story: "s", Attempt: 2, From: state.Submitted, To: state.Working},
		{Story: "s", Attempt: 2, From: state.Working, To: state.Failed},
		{Story: "s", Attempt: 3, From: state.Submitted, To: state.Working},
	} {
		e.Epic, e.Actor, e.ExternalConfirmed = filepath.Base(epic), state.Leader, true
		if err := state.Append(epic, e); err != nil {
			t.Fatal(err)
		}
	}
	p := checkpoint.Path(epic, "s")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "---\nschema: " + checkpoint.Schema + "\nstory: s\nattempt: 1\nhead: x\nbase: y\nwritten_at: 2026-09-01T00:00:00Z\nreason: precompact-auto\n---\n\nBody kept.\n"
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, se, code := runCox(t, wt, nil, "hook", "precompact", "--epic", epic, "--story", "s", "--worktree", wt); code != 0 {
		t.Fatalf("precompact exit %d: %s", code, se)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "\nattempt: 3\n") || !strings.Contains(string(b), "Body kept.") {
		t.Fatalf("refreshed checkpoint did not stamp attempt 3 (or lost its body):\n%s", b)
	}
	so, se, code := runCox(t, wt, nil, "hook", "session-start", "--epic", epic, "--story", "s", "--worktree", wt)
	if code != 0 || !strings.Contains(so, "Body kept.") {
		t.Fatalf("session-start did not inject the refreshed checkpoint (exit %d)\nstdout %s\nstderr %s", code, so, se)
	}
}

// Review H2: with no terminal handle (no single-waiter lock) or under codex (Stop may block), a Stop waiter with no
// active epic still returns at once instead of idling for REWAKE_MAX_WAIT.
func TestStopRewakeBinaryNoEpicReturnsAtOnceWithoutALock(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	for _, c := range []struct{ harness, handle string }{{"claude", ""}, {"codex", "term-codex"}} {
		began := time.Now()
		cmd := exec.Command(fmCoxBin(t), "hook", "stop-rewake", "--harness", c.harness)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "REWAKE_MAX_WAIT=30", "REWAKE_POLL=1", "TMPDIR="+t.TempDir(), "ORCA_TERMINAL_HANDLE="+c.handle)
		cmd.Stdin = strings.NewReader(`{}`)
		out, err := cmd.CombinedOutput()
		if err != nil || time.Since(began) > 10*time.Second {
			t.Errorf("%s (handle %q): waiter with no epic took %s, err %v\n%s", c.harness, c.handle, time.Since(began), err, out)
		}
	}
}

// B-50 / B-71a against the real binary: `cox story dispatch --epic <relative>` from the workspace root resolves the
// epic once at parse time, so the worker's launch line (its COX_EPIC and the story path it reads from inside its own
// worktree) carries the absolute epic dir. Before the fix both carried "epics/e1". The watcher dispatch starts takes
// the same parsed value (startWatcher(*epicDir, ...)).
func TestDispatchBinaryAbsolutizesRelativeEpic(t *testing.T) {
	epic, _, log := launchFixture(t, "harness: pi\nrepo: r\n")
	ws := filepath.Dir(filepath.Dir(epic))
	t.Cleanup(func() {
		if pid := readPid(watchPidPath(epic)); pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})
	_, se, _ := runCox(t, ws, nil, "story", "dispatch", "s1", "--epic", filepath.Join("epics", "e1"), "--allow-unsandboxed")
	b, _ := os.ReadFile(log)
	var launch string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "terminal send") {
			launch = l
		}
	}
	if launch == "" {
		t.Fatalf("no launch line reached orca\nstderr: %s\norca calls:\n%s", se, b)
	}
	real, err := filepath.EvalSymlinks(epic) // the binary's cwd is the resolved path (/private/var on macOS)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COX_EPIC='" + real + "'", "story file " + real + "/stories/s1.md"} {
		if !strings.Contains(launch, want) {
			t.Errorf("launch line lacks %q (absolute epic): %s", want, launch)
		}
	}
}

// Adapters #43 follow-up (a) against the real binary: `cox reply s Q001` (an upper-cased id) writes its inbox record as
// "answer to q001:", the form the watcher's replyAnswerRe reads, so the reply is marked consumed.
func TestReplyBinaryCanonicalisesQuestionID(t *testing.T) {
	epic := t.TempDir()
	if _, err := report.Question(epic, "m10", 1, "which port range?"); err != nil {
		t.Fatal(err)
	}
	so, se, code := runCox(t, epic, []string{"COX_PLANE=terminal"}, "reply", "m10", "Q001", "use 41000-41099", "--epic", epic)
	if code != 0 {
		t.Fatalf("cox reply exit %d: %s %s", code, so, se)
	}
	if !strings.Contains(so, "answered m10/q001") {
		t.Errorf("reply echoed the raw id: %q", so)
	}
	entries, _ := os.ReadDir(filepath.Join(epic, "inbox", "m10"))
	var body string
	for _, e := range entries {
		if !e.IsDir() {
			b, _ := os.ReadFile(filepath.Join(epic, "inbox", "m10", e.Name()))
			body += string(b)
		}
	}
	if !strings.Contains(body, "answer to q001: use 41000-41099") {
		t.Errorf("inbox record does not carry the canonical id:\n%s", body)
	}
}

// Adapters #43 follow-up (c) against the real binary: with no run yet, dispatch's `orca orchestration run-create`
// failing with an ok=false envelope surfaces Orca's code and message, not a bare "exit status 1".
func TestDispatchBinarySurfacesRunCreateEnvelope(t *testing.T) {
	epic, _, log := launchFixture(t, "harness: pi\nrepo: r\n")
	mustWrite(t, filepath.Join(filepath.Dir(log), "orca"), "#!/bin/sh\necho '{\"ok\":false,\"error\":{\"code\":\"runtime_unavailable\",\"message\":\"Orca is not running\"}}'\nexit 1\n")
	_, se, code := runCox(t, epic, []string{"ORCA_RUN_ID="}, "story", "dispatch", "s1", "--epic", epic, "--allow-unsandboxed")
	if code == 0 {
		t.Fatalf("dispatch with a failing run-create exited 0: %s", se)
	}
	if !strings.Contains(se, "Orca is not running (runtime_unavailable)") || strings.Contains(se, "exit status") {
		t.Errorf("run-create failure did not surface the orca envelope: %q", se)
	}
}

// B-71a: every --epic (flag or the COX_EPIC default) is absolute after Parse; "" stays "" (not given).
func TestEpicFlagIsAbsoluteAtParse(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	wd, _ := os.Getwd()
	for _, c := range []struct {
		def  string
		args []string
		want string
	}{
		{"", []string{"--epic", "epics/e1"}, filepath.Join(wd, "epics", "e1")},
		{"epics/env", nil, filepath.Join(wd, "epics", "env")},
		{"", nil, ""},
		{"", []string{"--epic=/abs/e"}, "/abs/e"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		p := epicFlag(fs, c.def, "epic")
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if *p != c.want {
			t.Errorf("def %q args %v: --epic = %q, want %q", c.def, c.args, *p, c.want)
		}
	}
}

// doctorOn runs the real binary's `cox doctor` from inside workspace root (HOME and the scan roots pinned to temp dirs,
// so only this workspace is inspected) and returns stdout+stderr.
func doctorOn(t *testing.T, root string) string {
	t.Helper()
	so, se, _ := runCox(t, root, []string{"ORCA_WORKSPACES=", "COX_ROOTS="}, "doctor")
	return so + se
}

// B-04 against the real binary: `cox epic design --sign --no-arena --reason <ruling>` signs a lite epic with no arena
// synthesis (before: "cannot sign: no synthesis"), refuses without a ruling, and doctor then reports no signature
// divergence for the signed Status.
func TestEpicDesignBinarySignsNoArena(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	epic := filepath.Join(root, "app", "epics", "lite")
	mustWrite(t, filepath.Join(epic, "DESIGN.md"), "# lite\n\nStatus: active (signed 2026-09-25, no arena)\n")
	if _, se, code := runCox(t, root, nil, "epic", "design", "--sign", "--no-arena", "--epic", epic); code == 0 || !strings.Contains(se, "--reason") {
		t.Fatalf("no-arena sign without a ruling: exit %d %q, want a --reason refusal", code, se)
	}
	so, se, code := runCox(t, root, nil, "epic", "design", "--sign", "--no-arena", "--reason", "captain: lite, no arena", "--by", "captain", "--epic", epic)
	if code != 0 || !strings.Contains(so, "design_signed recorded (no arena") {
		t.Fatalf("no-arena sign: exit %d\n%s%s", code, so, se)
	}
	if out := doctorOn(t, root); strings.Contains(out, "signature lost") || strings.Contains(out, "ledger is signed but") {
		t.Errorf("doctor reports a signature divergence after a no-arena sign:\n%s", out)
	}
}

// B-46 against the real binary: an epic closed by hand before close wrote anything (Status "closed ... (previously:
// active, signed ...)", no design_signed in the ledger, no .cox.closed - the second-machine shape) is not reported as
// "signature lost".
func TestDoctorBinaryHandClosedEpicIsNotSignatureLost(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	epic := filepath.Join(root, "app", "epics", "old")
	mustWrite(t, filepath.Join(epic, "DESIGN.md"), "# old\n\nStatus: closed 2026-09-20 (previously: active, signed 2026-09-10)\n")
	if out := doctorOn(t, root); strings.Contains(out, "signature lost") {
		t.Errorf("doctor flags a hand-closed epic's signature as lost:\n%s", out)
	}
}

// B-06: a headless arena role that ran to its report folds to completed, not working (the turn-boundary guard and the
// stop waiter count a working role as open).
func TestCommitHeadlessCompletesTheRole(t *testing.T) {
	epic := t.TempDir()
	if err := commitHeadless(epic, "e", "arena-adversary", 1, "", map[string]any{"kind": "headless", "round": 1}); err != nil {
		t.Fatal(err)
	}
	if snap := foldStoryState(t, epic, "arena-adversary"); snap != state.Completed {
		t.Fatalf("headless role folded to %q, want completed", snap)
	}
}

// B-06 against the real binary: `cox arena close` completes a headless role an earlier run left working, with no live
// backend needed when no arena worktree is tracked.
func TestArenaCloseBinaryCompletesStuckHeadlessRole(t *testing.T) {
	epic := t.TempDir()
	for _, e := range []state.Event{
		{Story: "arena-adversary", Attempt: 1, From: state.Submitted, To: state.Working, Evidence: map[string]any{"kind": "headless", "round": 1}},
		{Story: "arena-reviewer", Attempt: 1, From: state.Submitted, To: state.Working, Evidence: map[string]any{"kind": "terminal"}},
	} {
		e.Epic, e.Actor, e.ExternalConfirmed = "e", state.Leader, true
		if err := state.Append(epic, e); err != nil {
			t.Fatal(err)
		}
	}
	so, se, code := runCox(t, epic, []string{"ORCA_RUN_ID="}, "arena", "close", "--round", "1", "--epic", epic)
	if code != 0 || !strings.Contains(so, "completed headless arena-adversary") {
		t.Fatalf("arena close: exit %d\n%s%s", code, so, se)
	}
	if got := foldStoryState(t, epic, "arena-adversary"); got != state.Completed {
		t.Errorf("stuck headless role folded to %q after close, want completed", got)
	}
	if got := foldStoryState(t, epic, "arena-reviewer"); got != state.Working {
		t.Errorf("a terminal role was touched by close: %q", got)
	}
}

func foldStoryState(t *testing.T, epic, story string) state.State {
	t.Helper()
	events, _, err := state.Load(epic)
	if err != nil {
		t.Fatal(err)
	}
	if s := state.Fold(events).Stories[story]; s != nil {
		return s.State
	}
	return ""
}

// Delta B (firstmate 7e0e60a) against the real binary: `cox story cancel` prunes the released story's queued
// supervision rows and leaves its decision rows and every other story's rows.
func TestStoryCancelBinaryPrunesItsWakes(t *testing.T) {
	epic := t.TempDir()
	for _, st := range []string{"s1", "s2"} {
		if err := state.Append(epic, state.Event{Epic: "e", Story: st, Attempt: 1, Actor: state.Leader, From: state.Submitted, To: state.Working, ExternalConfirmed: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []wake.Wake{
		{Story: "s1", Kind: wake.KindStale, Note: "stale: s1"},
		{Story: "s1", Kind: wake.KindIdleNoDone, Note: "idle: s1"},
		{Story: "s1", Kind: wake.KindQuestion, Note: "question: s1"},
		{Story: "s2", Kind: wake.KindStale, Note: "stale: s2"},
	} {
		w.Epic = "e"
		if _, err := wake.Append(epic, w); err != nil {
			t.Fatal(err)
		}
	}
	so, se, code := runCox(t, epic, []string{"ORCA_RUN_ID="}, "story", "cancel", "s1", "--reason", "superseded", "--epic", epic)
	if code != 0 {
		t.Fatalf("cancel exit %d\n%s%s", code, so, se)
	}
	ws, _ := wake.Drain(epic, true)
	var got []string
	for _, w := range ws {
		got = append(got, w.Note)
	}
	if strings.Join(got, "|") != "question: s1|stale: s2" {
		t.Errorf("after cancel the queue holds %v, want the s1 question and the s2 stale row only", got)
	}
}

// Delta F cmd half (firstmate 5842d42): the deferred worker's hard backstop leaves a failed record the next digest reads
// (why, and the rerun command) instead of exiting silently.
func TestDeferredBackstopWritesFailedRecord(t *testing.T) {
	ws := t.TempDir()
	if err := writeDeferredBackstop(ws, 630*time.Second); err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(bearings.DeferredFailed(ws), "\n")
	for _, want := range []string{"hard bound (10m30s)", "rerun: cox bearings deferred --root " + ws} {
		if !strings.Contains(lines, want) {
			t.Errorf("failed record lacks %q:\n%s", want, lines)
		}
	}
}

// Delta E, the cmd/cox site (firstmate ac2ed3b, fm-timeout-lib.test.sh:132 "a descendant holding the captured output
// must not keep the caller waiting"): a quota-axi whose background child keeps stdout open returns its version at once.
// Before, exec.CommandContext + Output waited for the child's EOF (here 20s; the ctx only killed the exited parent).
func TestQuotaAxiVersionDescendantHoldingStdout(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "quota-axi")
	mustWrite(t, bin, "#!/bin/sh\nsleep 20 &\necho 'quota-axi 1.2.3'\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	v := quotaAxiVersion(bin)
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("quotaAxiVersion took %s: a descendant holding stdout parked the read", took)
	}
	if v != "quota-axi 1.2.3" {
		t.Errorf("version = %q", v)
	}
}

// #47 follow-up against the real binary: doctor inside a valid v2 workspace no longer opens with "no coxswain
// installations found" (that list is only the legacy v1 kits), and with nothing found at all it says what it scanned.
func TestDoctorBinaryInstallationsLine(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	if out := doctorOn(t, root); strings.Contains(out, "no coxswain installations found") || strings.Contains(out, "no coxswain workspace found") {
		t.Errorf("doctor inside a v2 workspace reports none found:\n%s", out)
	}
	if out := doctorOn(t, t.TempDir()); !strings.Contains(out, "no coxswain workspace found under ") {
		t.Errorf("doctor with nothing to find does not say what it scanned:\n%s", out)
	}
}

// fakeOrcaRepos puts an orca first on PATH that knows only the checkouts listed in its registry file: `repo show`
// answers repo_not_found for any other path, `repo add` appends to the registry, and every call is logged. It returns
// the log path.
func fakeOrcaRepos(t *testing.T, registered ...string) string {
	t.Helper()
	bin := t.TempDir()
	reg, log := filepath.Join(bin, "registry"), filepath.Join(bin, "orca.log")
	mustWrite(t, reg, strings.Join(registered, "\n")+"\n")
	mustWrite(t, filepath.Join(bin, "orca"), `#!/bin/sh
echo "$@" >> '`+log+`'
case "$1 $2" in
'repo show') p=${4#path:}; if grep -qxF "$p" '`+reg+`'; then echo '{"ok":true,"result":{"repo":{}}}'; else echo '{"ok":false,"error":{"code":"repo_not_found","message":"repo_not_found"}}'; exit 1; fi ;;
'repo add') echo "$4" >> '`+reg+`'; echo '{"ok":true,"result":{"repo":{}}}' ;;
*) echo '{"ok":true,"result":{}}' ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "orca"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// gitCheckout is a temp git repo with one commit on main.
func gitCheckout(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	gitInitRepo(t, d)
	real, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// B-34b against the real binary: add-repo registers a checkout Orca does not know; a known one is left alone.
func TestWorkspaceAddRepoBinaryRegistersWithOrca(t *testing.T) {
	root := t.TempDir()
	known, fresh := gitCheckout(t), gitCheckout(t)
	coxInit(t, root, "--repo", "app="+known+":main")
	log := fakeOrcaRepos(t, known)
	so, se, code := runCox(t, root, nil, "workspace", "add-repo", "web="+fresh+":main", "--root", root)
	if code != 0 || !strings.Contains(so, "registered "+fresh+" with orca") {
		t.Fatalf("add-repo of an unregistered checkout: exit %d\n%s%s", code, so, se)
	}
	so, se, code = runCox(t, root, nil, "workspace", "add-repo", "api="+known+":main", "--root", root)
	if code != 0 || strings.Contains(so, "registered") {
		t.Errorf("add-repo of a registered checkout: exit %d\n%s%s", code, so, se)
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "repo add"); n != 1 {
		t.Errorf("orca repo add ran %d time(s), want once:\n%s", n, b)
	}
}

// B-34b against the real binary (leader ruling q001): `cox epic new` never registers; on a checkout Orca does not know
// it fails before creating anything, naming the exact `orca repo add` command, and doctor reports the same fix.
func TestEpicNewBinaryRefusesUnregisteredRepo(t *testing.T) {
	root := t.TempDir()
	repo := gitCheckout(t)
	coxInit(t, root, "--repo", "app="+repo+":main")
	log := fakeOrcaRepos(t)
	_, se, code := runCox(t, root, nil, "epic", "new", "proj", "e1", "--repo", "app", "--no-push", "--root", root)
	if code == 0 || !strings.Contains(se, "run: orca repo add --path "+repo) {
		t.Fatalf("epic new on an unregistered repo: exit %d, stderr %q", code, se)
	}
	if _, err := os.Stat(filepath.Join(root, "proj", "epics", "e1")); err == nil {
		t.Error("epic new created the epic dir before refusing")
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "repo add") || strings.Contains(string(b), "worktree create") {
		t.Errorf("epic new registered the repo or cut a worktree:\n%s", b)
	}
	if out := doctorOn(t, root); !strings.Contains(out, "repo app ("+repo+") is not registered with Orca; run: orca repo add --path "+repo) {
		t.Errorf("doctor does not report the unregistered repo with its fix:\n%s", out)
	}
}

// #46 follow-up (leader-findings 16): cmd/cox wires the watcher a forge over the epic's repo checkout, so its turn-end
// pass reads a story PR's pending CI instead of "CI state unknown"; an epic with no repos file keeps no forge.
func TestWatchForgeIsTheEpicRepo(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	epic := filepath.Join(root, "proj", "epics", "e1")
	mustWrite(t, filepath.Join(epic, "repos"), "app\n")
	f, ok := newWatchForge(epic).(*github.Client)
	if !ok || f == nil || f.Dir != filepath.Join(epic, "app") {
		t.Fatalf("watch forge = %#v, want a github client on %s", newWatchForge(epic), filepath.Join(epic, "app"))
	}
	if f := newWatchForge(t.TempDir()); f != nil {
		t.Errorf("an epic outside any workspace got a forge: %#v", f)
	}
}
