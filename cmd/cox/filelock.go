package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// File locks ported from firstmate bin/fm-wake-lib.sh fm_lock_try_acquire / fm_lock_try_create / fm_lock_release
// (pinned 1e0e773). Firstmate publishes a lock as a symlink to a private owner directory; cox's locks are plain files
// (other packages read .cox/watch.pid as a file), so the atomic publication is a hard link of a private owner file:
// link(2) either creates the lock with its full content or fails because the lock exists, so there is never an empty
// window a contender could mistake for a free lock. A lock file holds the owner pid on line 1, an optional role on
// line 2 (firstmate's role file: autoarm, terminal-check) and an optional pid identity on line 3 (its pid-identity file).

// lockMidAcquireGrace is firstmate's FM_LOCK_STALE_AFTER floor: a lock whose pid is empty or unparsable is a claim still
// being written and stays held for this long after its mtime, instead of being stolen at once.
var lockMidAcquireGrace = 2 * time.Second

// errLockHeld reports a lock another live (or mid-acquire) holder owns.
type errLockHeld struct{ pid int }

func (e errLockHeld) Error() string { return fmt.Sprintf("lock held by pid %d", e.pid) }

// lockHolder reads the pid and role a lock file records (pid 0 when empty or unparsable).
func lockHolder(path string) (pid int, role string, ok bool) {
	pid, role, _, ok = lockRecord(path)
	return pid, role, ok
}

// lockRecord reads a lock file's pid, role and recorded pid identity.
func lockRecord(path string) (pid int, role, identity string, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, "", "", false
	}
	lines := strings.SplitN(string(b), "\n", 4)
	pid, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
	if len(lines) > 1 {
		role = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		identity = strings.TrimSpace(lines[2])
	}
	return pid, role, identity, true
}

// lockCreate publishes path atomically with our pid (fm_lock_try_create). It fails when path exists.
func lockCreate(path string) error {
	owner, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".owner.*")
	if err != nil {
		return err
	}
	defer os.Remove(owner.Name())
	if _, err := fmt.Fprintf(owner, "%d\n", os.Getpid()); err != nil {
		owner.Close()
		return err
	}
	if err := owner.Close(); err != nil {
		return err
	}
	return os.Link(owner.Name(), path)
}

// lockMidAcquireFresh is fm_lock_mid_acquire_is_fresh: an unparsable pid younger than the grace is a claim in flight.
func lockMidAcquireFresh(path string, pid int) bool {
	if pid > 0 {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && time.Since(info.ModTime()) < lockMidAcquireGrace
}

// tryLock is fm_lock_try_acquire: a non-blocking claim of path for this process. A lock held by a live pid, or a fresh
// mid-acquire claim, is refused with errLockHeld; a lock this very process already records is reclaimed (an abandoned
// frame of our own); a dead or stale holder is stolen under the path+".steal" mutex, rechecking under the mutex that
// the same stale holder is still recorded, so concurrent stealers yield exactly one winner. beforeSteal (nil = none)
// runs under the steal mutex before the stale lock is removed, and a failure keeps the stale lock in place (the watcher
// lock publishes its downtime marker there). It returns the pid it recovered from (0 when none).
func tryLock(path string, beforeSteal func(stalePid int) error) (recovered int, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	if err := lockCreate(path); err == nil {
		return 0, nil
	} else if !errors.Is(err, os.ErrExist) {
		return 0, err
	}
	pid, _, _ := lockHolder(path)
	if pid == os.Getpid() {
		_ = os.Remove(path)
		if err := lockCreate(path); err != nil {
			held, _, _ := lockHolder(path)
			return 0, errLockHeld{held}
		}
		return 0, nil
	}
	if pid > 0 && processAlive(pid) || lockMidAcquireFresh(path, pid) {
		return 0, errLockHeld{pid}
	}
	steal := path + ".steal"
	if _, err := tryLock(steal, nil); err != nil {
		cur, _, _ := lockHolder(path)
		return 0, errLockHeld{cur}
	}
	defer releaseLock(steal)
	cur, _, ok := lockHolder(path)
	if ok && (cur > 0 && processAlive(cur) || lockMidAcquireFresh(path, cur) || cur != pid) {
		return 0, errLockHeld{cur}
	}
	if beforeSteal != nil {
		if err := beforeSteal(cur); err != nil {
			return 0, errLockHeld{cur}
		}
	}
	_ = os.Remove(path)
	if err := lockCreate(path); err != nil {
		held, _, _ := lockHolder(path)
		return 0, errLockHeld{held}
	}
	return cur, nil
}

// releaseLock is fm_lock_release: it removes path only while it still records this process, so a holder whose lock was
// reclaimed never deletes its successor's.
func releaseLock(path string) {
	if pid, _, _ := lockHolder(path); pid == os.Getpid() {
		_ = os.Remove(path)
	}
}

// setLockRole is fm_lock_set_role: it records role on a lock this process holds and reads it back.
func setLockRole(path, role string) bool {
	if pid, _, _ := lockHolder(path); pid != os.Getpid() {
		return false
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n%s\n", os.Getpid(), role)), 0o644); err != nil {
		return false
	}
	_, got, _ := lockHolder(path)
	return got == role
}
