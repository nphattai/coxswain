package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lockStale is how long a lock dir with no readable owner may sit before a new taker breaks it (a holder that died
// between mkdir and writing its pid). A lock whose owner pid is dead is broken at once.
const lockStale = 5 * time.Second

// LockPath is the story's lifecycle lock (fm state/.control-<id>.lock): <epic>/.cox/sessions/.control-<story>.lock.
// Every control verb holds it from before it resolves the story's state to its last write, so two lifecycle actions on
// one story serialize instead of interleaving (fm-control.sh:311).
func LockPath(epic, story string) string {
	return filepath.Join(epic, ".cox", "sessions", ".control-"+story+".lock")
}

// ErrLocked is the refusal a verb returns when another lifecycle action holds the story's lock.
var ErrLocked = errors.New("another lifecycle action is already running")

// tryLock takes the story's lifecycle lock or refuses at once (fm_lock_try_acquire).
func tryLock(epic, story string) (func(), error) {
	release, ok, err := acquire(epic, story)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w for story %s", ErrLocked, story)
	}
	return release, nil
}

// LockWait takes the story's lifecycle lock, waiting up to timeout for a running action to finish (fm_lock_acquire_wait).
// A durable writer that must not interleave with a relaunch (a record publication) uses it; it returns the release.
func LockWait(epic, story string, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)
	for {
		release, ok, err := acquire(epic, story)
		if err != nil {
			return nil, err
		}
		if ok {
			return release, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("%w for story %s (waited %s)", ErrLocked, story, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// acquire makes one attempt: mkdir the lock dir and record this process as its owner. A lock whose owner pid is no
// longer running, or that has no owner after lockStale, is broken first, so a crashed holder never wedges the story.
func acquire(epic, story string) (release func(), ok bool, err error) {
	lock := LockPath(epic, story)
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return nil, false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.Mkdir(lock, 0o700); err == nil {
			if err := os.WriteFile(filepath.Join(lock, "owner"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
				_ = os.RemoveAll(lock)
				return nil, false, err
			}
			return func() { _ = os.RemoveAll(lock) }, true, nil
		} else if !os.IsExist(err) {
			return nil, false, err
		}
		if !stale(lock) {
			return nil, false, nil
		}
		_ = os.RemoveAll(lock)
	}
	return nil, false, nil
}

// stale reports whether a held lock's owner is provably gone: its recorded pid is not running, or it recorded no pid
// within lockStale.
func stale(lock string) bool {
	b, err := os.ReadFile(filepath.Join(lock, "owner"))
	if err != nil {
		info, serr := os.Stat(lock)
		return serr == nil && time.Since(info.ModTime()) >= lockStale
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return true
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
