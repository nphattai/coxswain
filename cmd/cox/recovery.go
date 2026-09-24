package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/wake"
)

// The watcher-down recovery episode, ported from firstmate bin/fm-wake-lib.sh fm_recovery_marker_* and
// docs/watcher-continuity.md "Recovery episode acknowledgement" (pinned 1e0e773). One episode is one generation of
// <epic>/.cox/watcher-down, a one-line token <pending|announced|acked>:<handling|downtime>:<generation>:
//
//   - a watcher close and a stale-lock reclaim publish downtime BEFORE the lock is cleared, reusing a pending or
//     announced generation (an outstanding acknowledgement stays valid) and minting a new one otherwise;
//   - a watcher start (arm check) announces a pending downtime generation once, with firstmate's
//     "check: rearm-resurface" wake, quarantines a malformed marker once, and waits on an announced or handling one;
//   - `cox wake drain` begins handling and prints the generation-bound acknowledgement; `cox wake ack-through <gen>
//     --recovery-generation <g>` consumes the rows through <gen> and retires the episode only when <g> is still its
//     generation - a moved generation consumes the rows it names and says to re-run the drain.

var recoveryTokenRe = regexp.MustCompile(`^(pending|announced|acked):(handling|downtime):([A-Za-z0-9._-]+)$`)

func recoveryMarker(epicDir string) string { return coxPath(epicDir, "watcher-down") }

// recoveryToken is a parsed marker token.
type recoveryToken struct{ status, kind, generation string }

func (t recoveryToken) String() string { return t.status + ":" + t.kind + ":" + t.generation }

// readRecovery is fm_recovery_marker_read: a regular one-line file with a valid token, else false.
func readRecovery(epicDir string) (recoveryToken, bool) {
	p := recoveryMarker(epicDir)
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return recoveryToken{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil || strings.Count(string(b), "\n") != 1 {
		return recoveryToken{}, false
	}
	m := recoveryTokenRe.FindStringSubmatch(strings.TrimSuffix(string(b), "\n"))
	if m == nil {
		return recoveryToken{}, false
	}
	return recoveryToken{m[1], m[2], m[3]}, true
}

// withRecoveryLock runs fn under the marker lock (fm_lock_acquire_wait, bounded).
func withRecoveryLock(epicDir string, fn func() error) error {
	lock := recoveryMarker(epicDir) + ".lock"
	for i := 0; ; i++ {
		if _, err := tryLock(lock, nil); err == nil {
			break
		} else if i >= 250 {
			return fmt.Errorf("recovery state is locked: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer releaseLock(lock)
	return fn()
}

// writeRecovery is _fm_recovery_marker_write_locked: atomically replace the marker (a new generation when empty).
func writeRecovery(epicDir string, t recoveryToken) error {
	if t.generation == "" {
		t.generation = fmt.Sprintf("%d.%d.%d", os.Getpid(), time.Now().Unix(), time.Now().UnixNano()%1_000_000)
	}
	p := recoveryMarker(epicDir)
	if info, err := os.Lstat(p); err == nil && info.IsDir() {
		return fmt.Errorf("recovery marker %s is a directory", p)
	}
	tmp := fmt.Sprintf("%s.tmp.%d", p, os.Getpid())
	if err := os.WriteFile(tmp, []byte(t.String()+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// publishRecoveryDowntime is _fm_recovery_marker_publish downtime: a pending or announced episode keeps its generation
// and status; anything else (none, acked, malformed) starts a new pending generation.
func publishRecoveryDowntime(epicDir string) error {
	return withRecoveryLock(epicDir, func() error {
		t := recoveryToken{status: "pending", kind: "downtime"}
		if cur, ok := readRecovery(epicDir); ok && cur.status != "acked" {
			t.status, t.generation = cur.status, cur.generation
		}
		return writeRecovery(epicDir, t)
	})
}

// publishWatcherDowntime publishes the downtime of a stale watcher lock (pid) before the lock is cleared, and announces
// the episode at once when it is still pending (the reclaiming arm is the one that will re-surface it). A failed
// publication keeps the stale lock as the recovery evidence.
func publishWatcherDowntime(epicDir string, pid int) error {
	if err := publishRecoveryDowntime(epicDir); err != nil {
		return err
	}
	if err := watchStealHook(); err != nil {
		return err
	}
	_, err := recoveryArmCheck(epicDir, pid)
	return err
}

// watchStealHook runs between the downtime publication and the stale lock's removal; a test makes it fail to prove the
// publication lands first. nil error in production.
var watchStealHook = func() error { return nil }

// recoveryArmCheck is _fm_recovery_marker_arm_check at a watcher start (stalePid > 0: the lock it reclaimed): it reports
// whether this start re-surfaces the episode, appending firstmate's "check: rearm-resurface" wake when it does.
func recoveryArmCheck(epicDir string, stalePid int) (recover bool, err error) {
	err = withRecoveryLock(epicDir, func() error {
		p := recoveryMarker(epicDir)
		queued := false
		if w, err := wake.Drain(epicDir, true); err == nil && len(w) > 0 {
			queued = true
		}
		cur, ok := readRecovery(epicDir)
		switch {
		case !exists(p):
			if queued {
				recover = true
				return writeRecovery(epicDir, recoveryToken{status: "announced", kind: "downtime"})
			}
			return nil
		case !ok:
			// Malformed: quarantine it once, then open a fresh announced episode.
			q, err := os.MkdirTemp(filepath.Dir(p), filepath.Base(p)+".invalid.")
			if err != nil {
				return err
			}
			if err := os.Rename(p, filepath.Join(q, "marker")); err != nil {
				_ = os.Remove(q)
				return err
			}
			recover = true
			return writeRecovery(epicDir, recoveryToken{status: "announced", kind: "downtime"})
		case cur.status == "pending" && cur.kind == "downtime":
			recover = true
			return writeRecovery(epicDir, recoveryToken{status: "announced", kind: "downtime", generation: cur.generation})
		case cur.status == "acked" && queued:
			recover = true
			return writeRecovery(epicDir, recoveryToken{status: "announced", kind: "downtime"})
		}
		return nil
	})
	if err == nil && recover {
		note := "check: rearm-resurface - the watcher was down; drain the wakes queued during the downtime"
		if stalePid > 0 {
			note = fmt.Sprintf("check: rearm-resurface - the watcher (pid %d) was down; drain the wakes queued during the downtime", stalePid)
		}
		_, err = wake.Append(epicDir, wake.Wake{Epic: filepath.Base(epicDir), Kind: wake.KindStatus, Note: note})
	}
	return recover, err
}

// recoveryBeginHandling is _fm_recovery_marker_begin_handling: the drain moves a downtime episode to handling and
// returns its generation ("" when there is no episode to acknowledge).
func recoveryBeginHandling(epicDir string) (string, error) {
	var gen string
	err := withRecoveryLock(epicDir, func() error {
		cur, ok := readRecovery(epicDir)
		if !ok {
			return nil
		}
		gen = cur.generation
		if cur.status == "acked" || cur.kind == "handling" {
			return nil
		}
		return writeRecovery(epicDir, recoveryToken{status: cur.status, kind: "handling", generation: cur.generation})
	})
	return gen, err
}

// errRecoveryMoved reports an acknowledgement whose generation is no longer the marker's (fm return 3).
var errRecoveryMoved = errors.New("recovery generation moved")

// recoveryAck is _fm_recovery_marker_ack: retire the episode when gen is still its generation.
func recoveryAck(epicDir, gen string) error {
	return withRecoveryLock(epicDir, func() error {
		cur, ok := readRecovery(epicDir)
		if !ok || cur.generation != gen {
			return errRecoveryMoved
		}
		if cur.status == "acked" {
			return nil
		}
		return writeRecovery(epicDir, recoveryToken{status: "acked", kind: cur.kind, generation: cur.generation})
	})
}

// printRecoveryAck prints the drain's generation-bound acknowledgement after the presented wakes (WAKE_ACK_REQUIRED
// with --recovery-generation) when a recovery episode is open, beginning its handling.
func printRecoveryAck(epicDir string, errw *os.File) {
	gen, err := recoveryBeginHandling(epicDir)
	if err != nil || gen == "" {
		return
	}
	through := 0
	if w, err := wake.Drain(epicDir, true); err == nil {
		for _, k := range w {
			if k.Gen > through {
				through = k.Gen
			}
		}
	}
	fmt.Fprintf(errw, "WAKE_ACK_REQUIRED: after handling completes run cox wake ack-through %s --epic %s --recovery-generation %s\n",
		strconv.Itoa(through), epicDir, gen)
}

// recoveryReopenAnnounced is _fm_recovery_marker_reopen_announced: a non-successor watcher start turns an announced
// episode into a fresh pending generation, so a still-unacked recovery is presented once more.
func recoveryReopenAnnounced(epicDir string) error {
	return withRecoveryLock(epicDir, func() error {
		if cur, ok := readRecovery(epicDir); ok && cur.status == "announced" {
			return writeRecovery(epicDir, recoveryToken{status: "pending", kind: "downtime"})
		}
		return nil
	})
}
