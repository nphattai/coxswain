package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LeaderRecord is the versioned form of <epic>/.cox/leader (DESIGN wave-2 item 7): the leader terminal handle plus the
// pid and timestamp of the process that bound it, so a reader can tell a stale binding from a live one. It is written by
// temp + rename. Readers accept both this JSON form and the legacy plain-handle text (a file written before this change,
// or by an older cox), so an in-flight epic is never blocked by the format change.
type LeaderRecord struct {
	Handle string `json:"handle"`
	PID    int    `json:"pid"`
	TS     string `json:"ts"`
}

// leaderPath is <epic>/.cox/leader.
func leaderPath(epicDir string) string { return filepath.Join(epicDir, ControlDir, "leader") }

// LeaderHandle returns the recorded leader terminal handle, or "" when the file is absent or unreadable. It is the ONE
// reader every consumer (hooks, watcher, close, doctor) routes through, so the JSON/legacy parsing lives in one place
// and can never drift between them. A JSON record yields its handle; a legacy plain-text file yields its trimmed body.
func LeaderHandle(epicDir string) string {
	rec, ok := ReadLeaderRecord(epicDir)
	if !ok {
		return ""
	}
	return rec.Handle
}

// ReadLeaderRecord returns the full leader record and whether one was read. A legacy plain-handle file is returned as a
// record carrying only the handle (pid 0, empty ts), so a caller that wants the pid/ts still works and one that wants
// only the handle is unaffected.
func ReadLeaderRecord(epicDir string) (LeaderRecord, bool) {
	b, err := os.ReadFile(leaderPath(epicDir))
	if err != nil {
		return LeaderRecord{}, false
	}
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" {
		return LeaderRecord{}, false
	}
	// A JSON object is the versioned form; anything else is a legacy plain handle.
	if strings.HasPrefix(trimmed, "{") {
		var rec LeaderRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err == nil && rec.Handle != "" {
			return rec, true
		}
		// Malformed JSON with no usable handle: treat as unreadable rather than guessing.
		return LeaderRecord{}, false
	}
	return LeaderRecord{Handle: trimmed}, true
}

// WriteLeader records the leader terminal handle as a JSON LeaderRecord (handle + this process's pid + now), by temp +
// rename so a reader never sees a half-written file. It is the ONE writer, so every producer emits the same JSON shape.
func WriteLeader(epicDir, handle string) error {
	rec := LeaderRecord{Handle: handle, PID: os.Getpid(), TS: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dir := filepath.Join(epicDir, ControlDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return AtomicWrite(leaderPath(epicDir), append(data, '\n'), 0o644)
}

// AtomicWrite writes data to a unique temp file in the destination's directory, then renames it onto path, so a reader
// never observes a partial write and a lower-attempt writer can be dropped before the rename (DESIGN wave-2 item 7). It
// is exported so the runtime-record writers (session, worktree, leader) share one atomic-write implementation.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
