package env

import (
	"fmt"
	"syscall"
)

// ErrPortConflict is returned when a port cox wants is held by a process cox does not own. cox reports the foreign
// owner instead of killing it (F13: "an occupied port should report a conflict rather than kill an unrelated owner").
type ErrPortConflict struct {
	Port int
	PID  int
	Cmd  string
}

func (e *ErrPortConflict) Error() string {
	return fmt.Sprintf("port %d is held by pid %d (%s), which cox does not own; refusing to kill", e.Port, e.PID, e.Cmd)
}

// EnsurePortFree checks that port is free or already held by the pid we recorded as the backend. A foreign holder is an
// ErrPortConflict. Call it before starting the backend.
func (a *Allocator) EnsurePortFree(port int) error {
	pid, cmd, occupied, err := a.Ops.PortOwner(port)
	if err != nil {
		return err
	}
	if !occupied {
		return nil
	}
	res, err := loadResources(a.EpicDir)
	if err != nil {
		return err
	}
	if res.Backend != nil && res.Backend.PID == pid {
		return nil // our own backend already holds it
	}
	return &ErrPortConflict{Port: port, PID: pid, Cmd: cmd}
}

// RecordBackend records the pid and port of a backend process cox started, so a later stop kills only this pid.
func (a *Allocator) RecordBackend(pid, port int) error {
	res, err := loadResources(a.EpicDir)
	if err != nil {
		return err
	}
	res.Backend = &Backend{PID: pid, Port: port}
	return saveResources(a.EpicDir, res)
}

// StopBackend stops the backend cox owns. It verifies the port's current holder is our recorded pid before signaling: a
// port now held by a foreign pid is an ErrPortConflict and nothing is killed (F13). A stop that reaches a dead pid is
// fine (the ownership is cleared). It returns stopped=true only when it actually signaled or the process was already
// gone; ownership is cleared only then.
func (a *Allocator) StopBackend() (bool, error) {
	res, err := loadResources(a.EpicDir)
	if err != nil {
		return false, err
	}
	if res.Backend == nil || res.Backend.PID == 0 {
		return false, nil // nothing owned
	}
	pid, port := res.Backend.PID, res.Backend.Port
	// If something still holds the port, confirm it is our pid before killing.
	if port > 0 {
		owner, cmd, occupied, err := a.Ops.PortOwner(port)
		if err != nil {
			return false, err
		}
		if occupied && owner != pid {
			return false, &ErrPortConflict{Port: port, PID: owner, Cmd: cmd}
		}
	}
	// Signal 0 probes liveness; ESRCH means already gone.
	if err := a.Ops.Signal(pid, syscall.Signal(0)); err == nil {
		if err := a.Ops.Signal(pid, syscall.SIGTERM); err != nil {
			return false, fmt.Errorf("stop backend pid %d: %w", pid, err)
		}
	}
	res.Backend = nil
	if err := saveResources(a.EpicDir, res); err != nil {
		return false, err
	}
	return true, nil
}

// PromoteSnapshot rebuilds the epic's template database from a fresh clone of dbName, keeping the old template
// recoverable until the swap is confirmed (F04). It clones dbName into <tpl>_new, renames the live <tpl> aside to
// <tpl>_prev, renames <tpl>_new into <tpl>, and only then drops <tpl>_prev. If the final rename fails, it restores
// <tpl>_prev back to <tpl> and returns the error, so a failed promotion never destroys the working template.
func (a *Allocator) PromoteSnapshot(dbName, tpl string) error {
	newTpl := tpl + "_new"
	prev := tpl + "_prev"
	// Clean any stale scratch databases from a prior aborted promote.
	if err := a.Ops.DBDrop(newTpl); err != nil {
		return fmt.Errorf("clear stale %s: %w", newTpl, err)
	}
	if err := a.Ops.DBDrop(prev); err != nil {
		return fmt.Errorf("clear stale %s: %w", prev, err)
	}
	if err := a.Ops.DBCreate(newTpl, dbName); err != nil {
		return fmt.Errorf("clone %s into %s: %w", dbName, newTpl, err)
	}
	tplExists, err := a.Ops.DBExists(tpl)
	if err != nil {
		return err
	}
	if tplExists {
		if err := a.Ops.DBRename(tpl, prev); err != nil {
			return fmt.Errorf("set %s aside to %s: %w", tpl, prev, err)
		}
	}
	if err := a.Ops.DBRename(newTpl, tpl); err != nil {
		// Restore the previous template so the epic keeps a working snapshot.
		if tplExists {
			if rerr := a.Ops.DBRename(prev, tpl); rerr != nil {
				return fmt.Errorf("promote %s failed (%v) AND restore failed (%v): manual recovery needed; %s is intact", tpl, err, rerr, newTpl)
			}
		}
		return fmt.Errorf("promote %s failed, previous template restored: %w", tpl, err)
	}
	if tplExists {
		if err := a.Ops.DBDrop(prev); err != nil {
			return fmt.Errorf("promoted %s but could not drop %s: %w", tpl, prev, err)
		}
	}
	return nil
}
