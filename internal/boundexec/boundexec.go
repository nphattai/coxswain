// Package boundexec is cox's one bounded external exec (firstmate bin/fm-timeout-lib.sh fm_exec_timed, ac2ed3b): a
// command runs in its own process group, the whole group gets TERM at the bound and KILL after a short grace, a
// descendant holding captured output cannot keep the caller waiting, and a TERM/INT/HUP to cox reaches every live
// bounded command before cox ends. Every external call on a supervision surface (doctor probes, bearings stages,
// lavish poll, quota-axi) goes through Run.
package boundexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	// Grace is the pause between TERM and KILL to a timed-out process group (bin/fm-timeout-lib.sh:91).
	Grace = 200 * time.Millisecond
	// ExitTimeout is the status of a run the bound stopped, as timeout(1) reports it.
	ExitTimeout = 124
	// ExitKilled is timeout(1)'s status when it had to KILL; TimedOut accepts it too (fm_timed_out).
	ExitKilled = 137
)

// ErrUnbounded refuses a run with no positive bound: nothing is started.
var ErrUnbounded = errors.New("boundexec: refusing to run without a positive bound")

// TimedOut reports whether an exit status is the bound's own (124 or 137, fm_timed_out).
func TimedOut(code int) bool { return code == ExitTimeout || code == ExitKilled }

// Run runs cmd (built by the caller with exec.Command; its Env, Stdin, Stdout and Stderr are kept) in its own process
// group under timeout and ctx. At the bound or when ctx ends the group gets TERM, then KILL after Grace, and the result
// is ExitTimeout. Otherwise the command's own status passes through (128+signal for a signalled child). err is only a
// refusal or a launch failure.
func Run(ctx context.Context, timeout time.Duration, cmd *exec.Cmd) (int, error) {
	if timeout <= 0 {
		return 0, ErrUnbounded
	}
	if cmd == nil || cmd.Path == "" {
		return 0, errors.New("boundexec: Run needs a command")
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if cmd.WaitDelay == 0 {
		// A descendant that left the group can still hold a captured pipe; Wait stops waiting for it after this.
		cmd.WaitDelay = 2 * Grace
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pgid := cmd.Process.Pid
	track(pgid)
	defer untrack(pgid) // idempotent
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		untrack(pgid) // before the wait: the forwarder waits for the live groups to end
		awaitReraise()
		return exitCode(cmd, err)
	case <-timer.C:
	case <-ctx.Done():
	}
	groupKill(pgid)
	<-done
	return ExitTimeout, nil
}

// groupKill sends TERM to a process group, waits Grace, then KILLs whatever resisted.
func groupKill(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(Grace)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// exitCode maps a finished command to its shell exit status. exec.ErrWaitDelay (the command exited but a descendant
// still held its output) is not a failure: the command's own status stands.
func exitCode(cmd *exec.Cmd, err error) (int, error) {
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return 0, err
		}
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ok && ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return cmd.ProcessState.ExitCode(), nil
}

// forwardSignals are the signals that end cox and so must first reach every live bounded command.
var forwardSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// live is the set of running bounded groups, and the signal channel installed while it is non-empty.
var live struct {
	sync.Mutex
	groups map[int]bool
	ch     chan os.Signal
	raised chan struct{} // set while a received signal is being forwarded and re-raised; closed a second after
}

// awaitReraise holds a run whose command ended on a forwarded signal until that signal ends cox, so the caller never
// races past it. When another handler in cox owns the signal, the process survives and the run returns after a bound.
func awaitReraise() {
	live.Lock()
	raised := live.raised
	live.Unlock()
	if raised == nil {
		return
	}
	<-raised
}

func track(pgid int) {
	live.Lock()
	defer live.Unlock()
	if live.groups == nil {
		live.groups = map[int]bool{}
	}
	live.groups[pgid] = true
	if live.ch == nil {
		live.ch = make(chan os.Signal, 1)
		signal.Notify(live.ch, forwardSignals...)
		go forward(live.ch)
	}
}

func untrack(pgid int) {
	live.Lock()
	defer live.Unlock()
	delete(live.groups, pgid)
	if len(live.groups) == 0 && live.ch != nil {
		signal.Stop(live.ch)
		close(live.ch)
		live.ch = nil
	}
}

// forward passes a received signal to every live group, gives them Grace to end on it (KILL after), then stops
// catching the signal and re-raises it so cox ends the way that signal intends.
func forward(ch chan os.Signal) {
	sig, ok := <-ch
	if !ok {
		return
	}
	s := sig.(syscall.Signal)
	raised := make(chan struct{})
	live.Lock()
	live.raised = raised
	live.Unlock()
	defer func() {
		time.Sleep(time.Second) // the re-raised signal normally ends cox inside this pause
		live.Lock()
		live.raised = nil
		live.Unlock()
		close(raised)
	}()
	for _, id := range groups() {
		_ = syscall.Kill(-id, s)
	}
	deadline := time.Now().Add(Grace)
	for len(groups()) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for _, id := range groups() {
		_ = syscall.Kill(-id, syscall.SIGKILL)
	}
	signal.Stop(ch)
	live.Lock()
	if live.ch == ch {
		live.ch = nil // a run that starts before the killed ones are reaped re-arms forwarding, not a stopped channel
	}
	live.Unlock()
	_ = syscall.Kill(os.Getpid(), s)
}

func groups() []int {
	live.Lock()
	defer live.Unlock()
	ids := make([]int, 0, len(live.groups))
	for id := range live.groups {
		ids = append(ids, id)
	}
	return ids
}

// KillLive ends every live bounded command group now: TERM, Grace, then KILL. A process that owns its own exit path (a
// signal handler that calls os.Exit, a hard backstop) calls it first, so a bounded command it started never outlives it
// when os.Exit wins the race with the signal forwarder (#47: a <=60s quota-axi orphaned by `cox watch` on TERM).
func KillLive() {
	ids := groups()
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		_ = syscall.Kill(-id, syscall.SIGTERM)
	}
	time.Sleep(Grace)
	for _, id := range ids {
		_ = syscall.Kill(-id, syscall.SIGKILL)
	}
}
