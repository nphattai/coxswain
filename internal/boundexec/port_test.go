// Port tests for internal/boundexec: firstmate's exec-style bound (bin/fm-timeout-lib.sh fm_exec_timed, commit
// ac2ed3b), translated case by case from tests/fm-timeout-lib.test.sh@a8572f6 (epic cox-refresh, story
// cox-refresh-doctor). Firstmate names map to cox names as follows: fm_exec_timed -> boundexec.Run, fm_timed_out ->
// boundexec.TimedOut, the bounding process (perl watchdog) -> the cox process calling Run. The grace is cox's fixed
// boundexec.Grace, not a per-call argument, so the timing bounds below are Grace-relative.
package boundexec_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/boundexec"
)

// helperEnv makes this test binary act as a bounding process: it runs the command in helperArgs under a 60s bound and
// exits with Run's status (the forwarding case signals it from outside).
const helperEnv = "BOUNDEXEC_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		cmd := exec.Command("bash", "-c", os.Getenv("BOUNDEXEC_TEST_SCRIPT"), "_", os.Getenv("BOUNDEXEC_TEST_PID"), os.Getenv("BOUNDEXEC_TEST_TERM"))
		code, err := boundexec.Run(context.Background(), 60*time.Second, cmd)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// runWithin runs boundexec.Run but fails the test (instead of hanging it) when Run itself outlasts limit.
func runWithin(t *testing.T, limit, bound time.Duration, cmd *exec.Cmd) (int, time.Duration) {
	t.Helper()
	type res struct {
		code int
		err  error
	}
	started := time.Now()
	ch := make(chan res, 1)
	go func() {
		code, err := boundexec.Run(context.Background(), bound, cmd)
		ch <- res{code, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Run: %v", r.err)
		}
		return r.code, time.Since(started)
	case <-time.After(limit):
		t.Fatalf("Run was still waiting %s after a %s bound", limit, bound)
	}
	return 0, 0
}

// waitFile waits for a non-empty file, as the shell suite's wait_for_file does.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	for i := 0; i < 500; i++ {
		if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

// gone reports whether the pid in file is no longer a live process (a short settle for the kernel to reap it).
func gone(t *testing.T, file string) bool {
	t.Helper()
	pid, err := strconv.Atoi(waitFile(t, file))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL) // do not leak it past the test
	return false
}

func TestPortTimeoutLib(t *testing.T) {
	const s = "FM/fm-timeout-lib/"

	t.Run(s+"passes_the_command_status_and_output_through", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:46@a8572f6
		var out, errOut bytes.Buffer
		cmd := exec.Command("bash", "-c", "echo to-stdout; echo to-stderr >&2; exit 7")
		cmd.Stdout, cmd.Stderr = &out, &errOut
		code, _ := runWithin(t, 10*time.Second, 5*time.Second, cmd)
		if code != 7 {
			t.Errorf("the bound did not pass the command's own status through (code=%d)", code)
		}
		if !strings.Contains(out.String(), "to-stdout") || !strings.Contains(errOut.String(), "to-stderr") {
			t.Errorf("the bound lost the command's output: stdout=%q stderr=%q", out.String(), errOut.String())
		}
	})

	t.Run(s+"term_ends_a_cooperative_command_at_the_bound", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:57@a8572f6
		pidFile := filepath.Join(t.TempDir(), "pid")
		cmd := exec.Command("bash", "-c", `echo $$ > "$1"; exec sleep 30`, "_", pidFile)
		code, took := runWithin(t, 10*time.Second, time.Second, cmd)
		if code != boundexec.ExitTimeout {
			t.Errorf("an expired bound did not report 124 (code=%d)", code)
		}
		if took < time.Second {
			t.Errorf("the bound fired before it elapsed (%s)", took)
		}
		if took >= time.Second+boundexec.Grace+time.Second {
			t.Errorf("a TERM-honoring command took %s: TERM was not sent at the bound", took)
		}
		if !gone(t, pidFile) {
			t.Error("the bounded command outlived its bound")
		}
	})

	t.Run(s+"kill_ends_a_term_ignoring_command_after_the_grace", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:74@a8572f6
		pidFile := filepath.Join(t.TempDir(), "pid")
		cmd := exec.Command("bash", "-c", `trap "" TERM; echo $$ > "$1"; exec sleep 30`, "_", pidFile)
		code, took := runWithin(t, 10*time.Second, time.Second, cmd)
		if code != boundexec.ExitTimeout {
			t.Errorf("a KILL-forced expiry did not report 124 (code=%d)", code)
		}
		if took < time.Second+boundexec.Grace {
			t.Errorf("a TERM-ignoring command ended before bound plus grace (%s): the grace was skipped", took)
		}
		if took >= time.Second+boundexec.Grace+2*time.Second {
			t.Errorf("a TERM-ignoring command was not killed after the grace (%s)", took)
		}
		if !gone(t, pidFile) {
			t.Error("the TERM-ignoring command survived the KILL")
		}
	})

	// n/a the_bound_replaces_the_calling_shell fm:tests/fm-timeout-lib.test.sh:92 - shell exec semantics; the Go caller
	// is never replaced, it waits on its child, which the forwarding case below covers.

	t.Run(s+"a_descendant_holding_the_output_cannot_outlast_the_bound", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:114@a8572f6
		pidFile := filepath.Join(t.TempDir(), "pid")
		var out bytes.Buffer
		cmd := exec.Command("bash", "-c", `( trap "" TERM; exec sleep 30 ) &
echo $! > "$1"
wait`, "_", pidFile)
		cmd.Stdout = &out // captured output: the descendant inherits the pipe
		code, took := runWithin(t, 10*time.Second, time.Second, cmd)
		if code != boundexec.ExitTimeout {
			t.Errorf("an expired bound did not report 124 (code=%d)", code)
		}
		if took >= time.Second+boundexec.Grace+2*time.Second {
			t.Errorf("a TERM-ignoring descendant held the captured output for %s past a 1s bound", took)
		}
		if !gone(t, pidFile) {
			t.Error("the TERM-ignoring descendant survived the bound")
		}
	})

	t.Run(s+"a_signal_to_the_bounding_process_reaches_the_command", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:138@a8572f6
		// Cox deviation: after forwarding, the cox process re-raises the signal on itself so it still ends the way that
		// signal intends (firstmate's watchdog instead exits with the command's status); the command's own exit is
		// what the forwarded TERM produced.
		dir := t.TempDir()
		pidFile, termFile := filepath.Join(dir, "pid"), filepath.Join(dir, "term")
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		helper := exec.Command(self, "-test.run=^$")
		helper.Env = append(os.Environ(), helperEnv+"=1",
			"BOUNDEXEC_TEST_SCRIPT="+`trap 'echo forwarded > "$2"; exit 3' TERM
echo $$ > "$1"
while :; do sleep 0.1; done`,
			"BOUNDEXEC_TEST_PID="+pidFile, "BOUNDEXEC_TEST_TERM="+termFile)
		if err := helper.Start(); err != nil {
			t.Fatal(err)
		}
		waitFile(t, pidFile)
		if err := helper.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("could not signal the bounding process: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- helper.Wait() }()
		select {
		case err = <-done:
		case <-time.After(10 * time.Second):
			_ = helper.Process.Kill()
			t.Fatal("the bounding process did not end 10s after its TERM")
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) || !ee.Sys().(syscall.WaitStatus).Signaled() || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Errorf("the bounding process did not end by the TERM it received: %v", err)
		}
		if b, _ := os.ReadFile(termFile); strings.TrimSpace(string(b)) != "forwarded" {
			t.Error("the TERM never reached the bounded command")
		}
		if !gone(t, pidFile) {
			t.Error("the bounded command outlived the bounding process")
		}
	})

	// n/a perl_is_preferred_over_timeout fm:tests/fm-timeout-lib.test.sh:165 - mechanism choice between perl and GNU
	// timeout; Go bounds the process group itself and needs neither.

	t.Run(s+"refuses_rather_than_running_unbounded", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:180@a8572f6
		ran := filepath.Join(t.TempDir(), "ran")
		_, err := boundexec.Run(context.Background(), 0, exec.Command("bash", "-c", `: > "$1"`, "_", ran))
		if err == nil {
			t.Error("Run ran with nothing to bound it")
		}
		if _, statErr := os.Stat(ran); statErr == nil {
			t.Error("the command ran although nothing bounded it")
		}
	})

	t.Run(s+"rejects_malformed_bounds_before_running_anything", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:192@a8572f6
		ran := filepath.Join(t.TempDir(), "ran")
		for _, bound := range []time.Duration{0, -time.Second} {
			if _, err := boundexec.Run(context.Background(), bound, exec.Command("bash", "-c", `: > "$1"`, "_", ran)); err == nil {
				t.Errorf("bound %s was not rejected", bound)
			}
		}
		if _, err := boundexec.Run(context.Background(), time.Second, nil); err == nil {
			t.Error("a call with no command was not rejected")
		}
		if _, statErr := os.Stat(ran); statErr == nil {
			t.Error("a rejected bound still ran the command")
		}
	})

	// n/a gnu_timeout_kills_a_term_ignoring_command_after_the_grace fm:tests/fm-timeout-lib.test.sh:210 - GNU timeout
	// fallback mechanism; kill_ends_a_term_ignoring_command_after_the_grace covers the behaviour.

	t.Run(s+"timed_out_names_exactly_the_bound_statuses", func(t *testing.T) {
		// fm: tests/fm-timeout-lib.test.sh:233@a8572f6
		for _, code := range []int{124, 137, 0, 1, 125, 127, 143} {
			want := code == 124 || code == 137
			if got := boundexec.TimedOut(code); got != want {
				t.Errorf("TimedOut(%d) = %v, want %v", code, got, want)
			}
		}
	})
}
