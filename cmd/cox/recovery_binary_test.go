package main

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/wake"
	"github.com/nphattai/coxswain/internal/watch"
)

// TestRecoveryEpisodeBinary is leader-findings 20 against the real binary: a recovery episode opens only when the
// watcher was really down. A live watcher plus an attached Stop cycle that delivers a wake leaves the next
// `cox wake drain` without a WAKE_ACK_REQUIRED recovery line; a killed watcher opens exactly one episode and one
// acknowledgement retires it.
func TestRecoveryEpisodeBinary(t *testing.T) {
	bin := fmCoxBin(t)
	epic := fmEpic(t, "s1")
	fmRetiredEpisode(t, epic) // an earlier real episode, handled and acked
	watcher, done := fmRealWatcher(t, epic)
	fmBeacon(t, epic, 0)
	if code, out := fmWait(t, epic, 10, func(i int) {
		if i == 1 {
			seedWake(t, epic, wake.KindWorkerDone)
		}
	}); code != 2 {
		t.Fatalf("the attached cycle did not deliver the wake: %d %q", code, out)
	}
	if !watch.Healthy(epic, time.Now(), 0) {
		t.Fatal("the watcher is not healthy after the delivered wake; the live-watcher leg proves nothing")
	}
	through, gen, out := binDrain(t, bin, epic)
	if gen != "" || strings.Contains(out, "WAKE_ACK_REQUIRED") {
		t.Fatalf("a drain on a live watcher printed a recovery acknowledgement %q: %q", gen, out)
	}
	if !strings.Contains(out, "worker_done") {
		t.Fatalf("the drain did not present the delivered wake: %q", out)
	}
	w, _ := wake.Drain(epic, true)
	through = strconv.Itoa(w[len(w)-1].Gen)
	if b, err := exec.Command(bin, "wake", "ack-through", through, "--epic", epic).CombinedOutput(); err != nil {
		t.Fatalf("plain ack-through failed: %v %q", err, b)
	}

	// The watcher dies without its close (SIGKILL): the next drain with a queued wake opens one episode.
	_ = watcher.Process.Signal(syscall.SIGKILL)
	<-done
	seedWake(t, epic, wake.KindStuck)
	through, gen, out = binDrain(t, bin, epic)
	if gen == "" || strings.Count(out, "WAKE_ACK_REQUIRED") != 1 {
		t.Fatalf("a drain after the watcher died must print exactly one recovery acknowledgement: %q", out)
	}
	if _, again, out := binDrain(t, bin, epic); again != gen {
		t.Fatalf("a second drain opened another episode (%q, want %q): %q", again, gen, out)
	}
	if b, err := exec.Command(bin, "wake", "ack-through", through, "--epic", epic, "--recovery-generation", gen).CombinedOutput(); err != nil || strings.Contains(string(b), "newer recovery episode") {
		t.Fatalf("the printed acknowledgement did not retire the episode: %v %q", err, b)
	}
	if tok := fmMarker(epic); tok != "acked:handling:"+gen {
		t.Fatalf("the episode was not retired by one acknowledgement: %q", tok)
	}
	if _, g, out := binDrain(t, bin, epic); g != "" || strings.Contains(out, "WAKE_ACK_REQUIRED") {
		t.Fatalf("the retired episode was presented again: %q", out)
	}
}

// binDrain runs the real `cox wake drain --epic epic` and returns its WAKE_ACK_REQUIRED pair (empty when absent).
func binDrain(t *testing.T, bin, epic string) (through, gen, out string) {
	t.Helper()
	b, err := exec.Command(bin, "wake", "drain", "--epic", epic).CombinedOutput()
	if err != nil {
		t.Fatalf("cox wake drain: %v %q", err, b)
	}
	out = string(b)
	if m := regexp.MustCompile(`ack-through (\d+) --epic \S+ --recovery-generation (\S+)`).FindStringSubmatch(out); m != nil {
		return m[1], m[2], out
	}
	return "", "", out
}
