package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/watch"
)

// The watcher cycle-exit ledger, ported from firstmate bin/fm-watch-arm.sh cycle_log_append /
// cycle_mark_predecessor_successor (docs/watcher-continuity.md: one tab-separated record per observed cycle, bounded by
// FM_WATCH_CYCLE_LOG_MAX_BYTES 262144 and FM_WATCH_CYCLE_LOG_KEEP_LINES 1000). Cox has no arm process that owns the
// watcher, so the records come from the two places that observe a cycle end: the watcher itself (origin=started: its
// signal, nonzero or clean exit) and the stop-rewake waiter attached to it (origin=attached: the delivered wake, or the
// watcher gone mid-wait). A new watcher links itself as the successor of the last unlinked record. Diagnostic evidence,
// never a supervision dependency: every write is bounded and best-effort.

// cycleLogCap is a positive integer knob with its default (fm-watch-arm.sh:84-88: empty, non-numeric or 0 => default).
func cycleLogCap(name string, def int) int {
	v := os.Getenv(name)
	if strings.Trim(v, "0123456789") != "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return def
}

// cycleLogMaxBytes / cycleLogKeepLines are FM_WATCH_CYCLE_LOG_MAX_BYTES / FM_WATCH_CYCLE_LOG_KEEP_LINES.
func cycleLogMaxBytes() int  { return cycleLogCap("COX_WATCH_CYCLE_LOG_MAX_BYTES", 262144) }
func cycleLogKeepLines() int { return cycleLogCap("COX_WATCH_CYCLE_LOG_KEEP_LINES", 1000) }

func cycleLogPath(epicDir string) string { return coxPath(epicDir, "watch-cycle-exits.log") }

// cycleRecord is one cycle's close.
type cycleRecord struct {
	armPid, watcherPid    int
	origin                string // started | attached
	startedAt             time.Time
	exitCode, signal      string
	reason                string
	lockBefore, successor string
}

func cycleField(s string) string {
	s = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// lockSnapshot is firstmate's "pid:<p>|identity:<id>" view of the watcher lock.
func lockSnapshot(epicDir string) string {
	pid, identity := watch.ReadPid(epicDir)
	p, id := "none", "none"
	if pid > 0 {
		p = fmt.Sprint(pid)
	}
	if identity != "" {
		id = identity
	}
	return cycleField("pid:" + p + "|identity:" + id)
}

// withCycleLog runs fn under the ledger lock (bounded: a contended ledger drops the record).
func withCycleLog(epicDir string, fn func()) {
	lock := coxPath(epicDir, "watch-cycle-exits.lock")
	for i := 0; ; i++ {
		if _, err := tryLock(lock, nil); err == nil {
			break
		} else if i >= 20 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer releaseLock(lock)
	fn()
}

// appendCycle appends one record and trims the ledger to its caps, keeping only complete records.
func appendCycle(epicDir string, r cycleRecord) {
	withCycleLog(epicDir, func() {
		beacon := int(pathAge(beaconPath(epicDir)).Seconds())
		if !exists(beaconPath(epicDir)) {
			beacon = 999999
		}
		line := fmt.Sprintf("arm_pid=%d\twatcher_pid=%d\torigin=%s\tstarted_at=%d\tended_at=%d\texit_code=%s\tsignal=%s\treason=%s\tbeacon_age=%d\tlock_before=%s\tlock_after=%s\tsuccessor=%s\n",
			r.armPid, r.watcherPid, cycleField(r.origin), r.startedAt.Unix(), time.Now().Unix(), cycleField(r.exitCode), cycleField(r.signal),
			cycleField(r.reason), beacon, cycleField(r.lockBefore), lockSnapshot(epicDir), cycleField(nonEmpty(r.successor, "none")))
		f, err := os.OpenFile(cycleLogPath(epicDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = f.WriteString(line)
		_ = f.Close()
		b, err := os.ReadFile(cycleLogPath(epicDir))
		max, keep := cycleLogMaxBytes(), cycleLogKeepLines()
		if err != nil || len(b) < max {
			return
		}
		// tail -n KEEP | tail -c MAX, then only complete records.
		lines := strings.SplitAfter(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(lines) > keep {
			lines = lines[len(lines)-keep:]
		}
		kept := strings.Join(lines, "") + "\n"
		for len(kept) > max {
			i := strings.IndexByte(kept, '\n')
			if i < 0 {
				kept = ""
				break
			}
			kept = kept[i+1:]
		}
		_ = writeFileAtomic(cycleLogPath(epicDir), []byte(kept))
	})
}

// linkCycleSuccessor is cycle_mark_predecessor_successor: the last record still at successor=none now names successor.
func linkCycleSuccessor(epicDir, successor string) {
	withCycleLog(epicDir, func() {
		b, err := os.ReadFile(cycleLogPath(epicDir))
		if err != nil {
			return
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.HasSuffix(lines[i], "\tsuccessor=none") {
				lines[i] = strings.TrimSuffix(lines[i], "none") + cycleField(successor)
				_ = writeFileAtomic(cycleLogPath(epicDir), []byte(strings.Join(lines, "\n")+"\n"))
				return
			}
		}
	})
}
