package main

import (
	"github.com/nphattai/coxswain/internal/watch"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestClaimWatchPid covers the three pidfile outcomes: a clean claim, refusal when another live process holds the
// file, and --replace killing that process and taking over.
func TestClaimWatchPid(t *testing.T) {
	epic := t.TempDir()
	path := watchPidPath(epic)

	// Clean claim: no pidfile yet.
	release, err := claimWatchPid(epic, false)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if got := readPid(path); got != os.Getpid() {
		t.Fatalf("pidfile = %d, want our pid %d", got, os.Getpid())
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("release should remove the pidfile, stat err = %v", err)
	}

	// A live foreign process holds the file: refuse without --replace, take over with it.
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() { _ = sleep.Process.Kill() }()
	// Reap the child once it is signaled, so it does not linger as a zombie that signal-0 still reports alive.
	go func() { _ = sleep.Wait() }()
	if err := writeCoxFile(epic, "watch.pid", strconv.Itoa(sleep.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	// It is this epic's watcher: its identity is recorded, so --replace may signal it (a bare pid never is).
	if err := watch.RecordIdentity(epic, sleep.Process.Pid); err != nil {
		t.Fatal(err)
	}

	if _, err := claimWatchPid(epic, false); err == nil {
		t.Fatal("claim should refuse while a live process holds the pidfile")
	}
	if got := readPid(path); got != sleep.Process.Pid {
		t.Fatalf("refused claim must not overwrite the pidfile: got %d", got)
	}

	release, err = claimWatchPid(epic, true)
	if err != nil {
		t.Fatalf("replace claim: %v", err)
	}
	// The old process was killed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && processAlive(sleep.Process.Pid) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(sleep.Process.Pid) {
		t.Fatal("--replace should have killed the old watcher")
	}
	if got := readPid(path); got != os.Getpid() {
		t.Fatalf("after replace pidfile = %d, want our pid %d", got, os.Getpid())
	}
	release()
}

// TestClaimWatchPidStale: a pidfile naming a dead pid is silently taken over without --replace.
func TestClaimWatchPidStale(t *testing.T) {
	epic := t.TempDir()
	// A pid that is (almost certainly) not running: start then reap a child.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	if err := writeCoxFile(epic, "watch.pid", strconv.Itoa(dead.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	release, err := claimWatchPid(epic, false)
	if err != nil {
		t.Fatalf("stale pidfile should be claimable: %v", err)
	}
	defer release()
	if got := readPid(watchPidPath(epic)); got != os.Getpid() {
		t.Fatalf("stale claim pidfile = %d, want %d", got, os.Getpid())
	}
}
