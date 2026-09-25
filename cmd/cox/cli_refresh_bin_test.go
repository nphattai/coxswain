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
