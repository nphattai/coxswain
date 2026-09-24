package bearings

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	// DefaultTimeout bounds the whole digest (FM_SESSION_START_TIMEOUT default, bin/fm-session-start.sh:278).
	DefaultTimeout = 120 * time.Second
	// killGrace is the pause between TERM and KILL to a timed-out process group (bin/fm-timeout-lib.sh:64).
	killGrace = 200 * time.Millisecond
	// timeoutExit is the exit status of a run the bound stopped, as timeout(1) reports it.
	timeoutExit = 124
)

// groupKill sends TERM to a process group, waits killGrace, then KILLs whatever resisted.
func groupKill(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(killGrace)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// exitCode maps a finished command to its shell exit status (128+signal for a signalled child).
func exitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	return 0, err
}

// RunBounded runs argv in its own process group under a hard deadline: on expiry the whole group gets TERM, then KILL
// after a short grace, and the result is exit 124; a natural exit status passes through (fm_run_timed).
func RunBounded(timeout time.Duration, argv ...string) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("bearings: RunBounded needs a command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err)
	case <-time.After(timeout):
		groupKill(cmd.Process.Pid)
		<-done
		return timeoutExit, nil
	}
}

// stageProcs tracks the process groups a running digest's stages started, so the runtime bound can reap them.
type stageProcs struct {
	mu    sync.Mutex
	pgids map[int]bool
	dead  bool
}

// run starts argv in its own group and waits; after the bound fired it starts nothing.
func (s *stageProcs) run(argv []string) {
	if len(argv) == 0 {
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return
	}
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return
	}
	if s.pgids == nil {
		s.pgids = map[int]bool{}
	}
	s.pgids[cmd.Process.Pid] = true
	s.mu.Unlock()
	_ = cmd.Wait()
	s.mu.Lock()
	delete(s.pgids, cmd.Process.Pid)
	s.mu.Unlock()
}

// reap kills every live stage group and refuses new ones.
func (s *stageProcs) reap() {
	s.mu.Lock()
	s.dead = true
	var ids []int
	for id := range s.pgids {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		groupKill(id)
	}
}
