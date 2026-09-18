package wake

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ControlDir mirrors state.ControlDir; the queue lives beside the event log.
const ControlDir = ".cox"

// Wake is one coxswain.wake.v1 record.
type Wake struct {
	Schema   string         `json:"schema"`
	Gen      int            `json:"gen"`
	TS       string         `json:"ts"`
	Epic     string         `json:"epic"`
	Story    string         `json:"story"`
	Kind     Kind           `json:"kind"`
	Note     string         `json:"note,omitempty"`
	Full     string         `json:"full,omitempty"` // untruncated note, present only when it differs from Note; drain --full prints it
	Evidence map[string]any `json:"evidence,omitempty"`
	Acked    bool           `json:"acked,omitempty"`
}

func queuePath(epicDir string) string { return filepath.Join(epicDir, ControlDir, "wake.jsonl") }
func ackPath(epicDir string) string   { return filepath.Join(epicDir, ControlDir, "wake.ack") }
func lockPath(epicDir string) string  { return filepath.Join(epicDir, ControlDir, "wake.lock") }

// Append assigns the next generation (last gen + 1, read under an exclusive lock) and writes the wake as one line to
// <epic>/.cox/wake.jsonl. It fsyncs before releasing the lock so a concurrent Append never reuses a gen. Schema and TS
// default when empty; the caller fills epic, story, kind, and optional note/evidence. The assigned gen is returned.
func Append(epicDir string, w Wake) (int, error) {
	if w.Kind == KindHeartbeat {
		return 0, fmt.Errorf("wake: heartbeat is not a queue kind")
	}
	if w.Schema == "" {
		w.Schema = Schema
	}
	if w.TS == "" {
		w.TS = time.Now().UTC().Format(time.RFC3339)
	}
	dir := filepath.Join(epicDir, ControlDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create control dir: %w", err)
	}

	unlock, err := lock(epicDir)
	if err != nil {
		return 0, err
	}
	defer unlock()

	last, err := maxGen(epicDir)
	if err != nil {
		return 0, err
	}
	w.Gen = last + 1

	line, err := json.Marshal(w)
	if err != nil {
		return 0, fmt.Errorf("marshal wake: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(queuePath(epicDir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open wake queue: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return 0, fmt.Errorf("write wake: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("fsync wake queue: %w", err)
	}
	return w.Gen, nil
}

// Load reads every wake in the queue in file order. A missing queue is not an error.
func Load(epicDir string) ([]Wake, error) {
	f, err := os.Open(queuePath(epicDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open wake queue: %w", err)
	}
	defer f.Close()
	var out []Wake
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var w Wake
		if err := json.Unmarshal([]byte(raw), &w); err != nil {
			return nil, fmt.Errorf("corrupt wake at line %d: %w", n, err)
		}
		if w.Schema != Schema {
			continue // forward compatibility: skip unknown-schema lines
		}
		out = append(out, w)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read wake queue: %w", err)
	}
	return out, nil
}

// Drain returns the unacked wakes (gen > the acked generation) in gen order. peek is advisory: Drain never changes ack
// state (AckThrough is the only consume). It is kept so callers document intent and to mirror v1's drain --peek.
func Drain(epicDir string, peek bool) ([]Wake, error) {
	acked, err := Acked(epicDir)
	if err != nil {
		return nil, err
	}
	all, err := Load(epicDir)
	if err != nil {
		return nil, err
	}
	var out []Wake
	for _, w := range all {
		if w.Gen > acked {
			out = append(out, w)
		}
	}
	return out, nil
}

// Acked returns the last acknowledged generation (0 if none).
func Acked(epicDir string) (int, error) {
	b, err := os.ReadFile(ackPath(epicDir))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read wake ack: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse wake ack: %w", err)
	}
	return n, nil
}

// AckThrough marks every wake with gen <= gen read. It is idempotent and monotonic: acking through a lower gen than
// already recorded is a no-op.
func AckThrough(epicDir string, gen int) error {
	dir := filepath.Join(epicDir, ControlDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create control dir: %w", err)
	}
	unlock, err := lock(epicDir)
	if err != nil {
		return err
	}
	defer unlock()

	cur, err := Acked(epicDir)
	if err != nil {
		return err
	}
	if gen <= cur {
		return nil
	}
	tmp := ackPath(epicDir) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(gen)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write wake ack: %w", err)
	}
	if err := os.Rename(tmp, ackPath(epicDir)); err != nil {
		return fmt.Errorf("publish wake ack: %w", err)
	}
	return nil
}

// Wait blocks until an unacked wake exists or max elapses, polling every poll interval. Polling (not fsnotify) is
// mandatory: fsnotify misses events under symlinked dirs on macOS (M2 risk). It returns the unacked wakes and
// timedOut=false as soon as any exist (including ones already queued when called), or nil and timedOut=true at the
// deadline. It never changes ack state.
func Wait(epicDir string, max, poll time.Duration) (wakes []Wake, timedOut bool, err error) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	deadline := time.Now().Add(max)
	for {
		w, err := Drain(epicDir, true)
		if err != nil {
			return nil, false, err
		}
		if len(w) > 0 {
			return w, false, nil
		}
		if !time.Now().Before(deadline) {
			return nil, true, nil
		}
		remaining := time.Until(deadline)
		if remaining < poll {
			time.Sleep(remaining)
		} else {
			time.Sleep(poll)
		}
	}
}

// maxGen returns the highest gen in the queue (0 if empty). Caller holds the lock.
func maxGen(epicDir string) (int, error) {
	all, err := Load(epicDir)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, w := range all {
		if w.Gen > max {
			max = w.Gen
		}
	}
	return max, nil
}

func lock(epicDir string) (func(), error) {
	f, err := os.OpenFile(lockPath(epicDir), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open wake lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire wake lock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
