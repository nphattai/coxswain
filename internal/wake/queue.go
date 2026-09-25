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

	"github.com/nphattai/coxswain/internal/state"
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
func hwmPath(epicDir string) string   { return filepath.Join(epicDir, ControlDir, "wake.hwm") }

// LockPath is the queue lock file, <epic>/.cox/wake.lock. Whoever holds it has written its pid there ("pid=N"), so a
// waiter that gives up can name the holder (the bearings deferred worker's failed record, firstmate 5842d42).
func LockPath(epicDir string) string { return filepath.Join(epicDir, ControlDir, "wake.lock") }

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

// Load reads every usable wake in the queue in file order. A missing queue is not an error. One unusable row never
// wedges the queue: it is skipped here and retired by the next non-peek drain (fm-wake-drain.sh:90).
func Load(epicDir string) ([]Wake, error) {
	rows, err := scan(epicDir)
	if err != nil {
		return nil, err
	}
	var out []Wake
	for _, r := range rows {
		if r.usable && r.wake.Schema == Schema {
			out = append(out, r.wake)
		}
	}
	return out, nil
}

// row is one raw queue line and its parse.
type row struct {
	raw    string
	wake   Wake
	usable bool
}

// scan reads every nonblank queue line. A row is usable when it parses and carries a positive gen, the one field a
// drain presents and an acknowledgement names. A row with no schema tag is a legacy row, adopted as coxswain.wake.v1
// (fm-wake-queue legacy_generationless_wake_is_adopted); a row with another explicit schema is kept but not loaded
// (forward compatibility).
func scan(epicDir string) ([]row, error) {
	f, err := os.Open(queuePath(epicDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open wake queue: %w", err)
	}
	defer f.Close()
	var out []row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		r := row{raw: raw}
		if err := json.Unmarshal([]byte(raw), &r.wake); err == nil && r.wake.Gen > 0 {
			r.usable = true
			if r.wake.Schema == "" {
				r.wake.Schema = Schema
			}
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read wake queue: %w", err)
	}
	return out, nil
}

// DrainResult is one drain: the unacked wakes to present, the unusable rows this drain retired (or could not), and
// why retirement failed. A failed retirement is reported, never fatal: the usable rows stay presentable.
type DrainResult struct {
	Wakes     []Wake
	Retired   []string
	RetireErr error
}

// Drain returns the unacked wakes (gen > the acked generation) in gen order, obvious duplicates collapsed. A drain
// never consumes (the acknowledgement is the only consume); a non-peek drain also retires unusable rows.
func Drain(epicDir string, peek bool) ([]Wake, error) {
	r, err := DrainReport(epicDir, peek)
	return r.Wakes, err
}

// DrainReport is Drain with the retirement outcome, for the presenter.
func DrainReport(epicDir string, peek bool) (DrainResult, error) {
	var res DrainResult
	acked, err := Acked(epicDir)
	if err != nil {
		return res, err
	}
	rows, err := scan(epicDir)
	if err != nil {
		return res, err
	}
	var unacked []Wake
	for _, r := range rows {
		switch {
		case !r.usable:
			res.Retired = append(res.Retired, r.raw)
		case r.wake.Schema == Schema && r.wake.Gen > acked:
			unacked = append(unacked, r.wake)
		}
	}
	if len(res.Retired) > 0 {
		if peek {
			res.Retired = nil
		} else {
			res.RetireErr = retire(epicDir)
		}
	}
	res.Wakes = dedupe(unacked)
	return res, nil
}

// retire rewrites the queue without its unusable rows (fm-wake-drain.sh:90 retire_unconsumable_rows_locked). It takes
// the queue lock without waiting, so a live lock holder never strands the drain; a busy lock is a retry next drain.
func retire(epicDir string) error {
	unlock, err := tryLock(epicDir)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := scan(epicDir) // re-read under the lock: an append may have landed since
	if err != nil {
		return err
	}
	var keep strings.Builder
	for _, r := range rows {
		if r.usable {
			keep.WriteString(r.raw + "\n")
		}
	}
	tmp := queuePath(epicDir) + ".retire.tmp"
	if err := os.WriteFile(tmp, []byte(keep.String()), 0o644); err != nil {
		return fmt.Errorf("write retired queue: %w", err)
	}
	if err := os.Rename(tmp, queuePath(epicDir)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publish retired queue: %w", err)
	}
	return nil
}

// RetiredNotice renders the drain's report of retired rows for stderr (fm-wake-drain.sh:90): the first 20 rows, then a
// count of the rest; or, when the rewrite failed, that the rows remain and the drain continued.
func (r DrainResult) RetiredNotice(epicDir string) string {
	if len(r.Retired) == 0 {
		return ""
	}
	if r.RetireErr != nil {
		return fmt.Sprintf("wake drain: unusable queue row(s) could not be retired (check that %s is readable and %s is writable: %v); continuing with the rows that remain usable\n",
			queuePath(epicDir), filepath.Join(epicDir, ControlDir), r.RetireErr)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "wake drain: retired %d unusable queue row(s) that carried no sequence to present or acknowledge:\n", len(r.Retired))
	for i, raw := range r.Retired {
		if i == 20 {
			fmt.Fprintf(&b, "wake drain:   ... %d further unusable row(s) not shown\n", len(r.Retired)-20)
			break
		}
		fmt.Fprintf(&b, "wake drain:   %s\n", raw)
	}
	return b.String()
}

// text is the untruncated payload of a wake.
func (w Wake) text() string {
	if w.Full != "" {
		return w.Full
	}
	return w.Note
}

// extends reports that later equals earlier or extends it past a word boundary ("phase 2 building" ->
// "phase 2 building (turn ended)"), never a mere character prefix ("status-1" is not "status-10").
func extends(later, earlier string) bool {
	return later == earlier || strings.HasPrefix(later, earlier+" ")
}

// dedupe collapses obvious duplicates (fm-wake-queue drain_dedupes_obvious_duplicates): a later wake for the same story
// and kind whose payload equals or extends an earlier one replaces it, keeping the latest payload and gen. Distinct
// reports never collapse, so every unread status still surfaces (the translation of firstmate deduping raw signal
// rows while presenting unread status separately; leader ruling q001).
func dedupe(ws []Wake) []Wake {
	var out []Wake
	for _, w := range ws {
		for i := 0; i < len(out); i++ {
			if o := out[i]; o.Story == w.Story && o.Kind == w.Kind && extends(w.text(), o.text()) {
				out = append(out[:i], out[i+1:]...)
				i--
			}
		}
		out = append(out, w)
	}
	return out
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
	_, err := Ack(epicDir, gen)
	return err
}

// AckResult says what an acknowledgement consumed: the number of unacked wakes at or below the gen, and Current, the
// newest wake still unacked afterwards (0 when none).
type AckResult struct {
	Through  int
	Consumed int
	Current  int
}

// Notice is the stderr line for an acknowledgement that consumed nothing while a wake is still waiting
// (fm-wake-drain.sh:748): it names the exact command for the current wake instead of failing or staying silent.
func (r AckResult) Notice(epicDir string) string {
	if r.Consumed > 0 || r.Current == 0 {
		return ""
	}
	return fmt.Sprintf("wake drain: nothing was acknowledged through %d (no unacknowledged wake is at or below it); the current wake is row %d: run cox wake ack-through %d --epic %s after handling it\n",
		r.Through, r.Current, r.Current, epicDir)
}

// Ack is AckThrough with its result (see AckResult).
func Ack(epicDir string, gen int) (AckResult, error) {
	res := AckResult{Through: gen}
	dir := filepath.Join(epicDir, ControlDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, fmt.Errorf("create control dir: %w", err)
	}
	unlock, err := lock(epicDir)
	if err != nil {
		return res, err
	}
	defer unlock()

	cur, err := Acked(epicDir)
	if err != nil {
		return res, err
	}
	all, err := Load(epicDir)
	if err != nil {
		return res, err
	}
	named := false
	for _, w := range all {
		named = named || w.Gen == gen
		switch {
		case w.Gen <= cur:
		case w.Gen <= gen:
			res.Consumed++
		case w.Gen > res.Current:
			res.Current = w.Gen
		}
	}
	if named {
		if h, err := Handled(epicDir); err != nil {
			return res, err
		} else if gen > h {
			if err := state.AtomicWrite(handledPath(epicDir), []byte(strconv.Itoa(gen)+"\n"), 0o644); err != nil {
				return res, fmt.Errorf("write wake handled: %w", err)
			}
		}
	}
	if gen <= cur {
		return res, nil
	}
	tmp := ackPath(epicDir) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(gen)+"\n"), 0o644); err != nil {
		return res, fmt.Errorf("write wake ack: %w", err)
	}
	if err := os.Rename(tmp, ackPath(epicDir)); err != nil {
		return res, fmt.Errorf("publish wake ack: %w", err)
	}
	return res, nil
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

// maxGen returns the highest gen ever assigned: of any usable row, whatever its schema, or recorded by a prune that
// removed the newest rows (wake.hwm), so a gen is never reused (0 if empty). Caller holds the lock.
func maxGen(epicDir string) (int, error) {
	rows, err := scan(epicDir)
	if err != nil {
		return 0, err
	}
	max := 0
	if b, err := os.ReadFile(hwmPath(epicDir)); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			max = n
		}
	}
	for _, r := range rows {
		if r.usable && r.wake.Gen > max {
			max = r.wake.Gen
		}
	}
	return max, nil
}

func lock(epicDir string) (func(), error) { return flock(epicDir, syscall.LOCK_EX) }

// tryLock takes the queue lock only when it is free (a busy lock is an error, never a wait).
func tryLock(epicDir string) (func(), error) { return flock(epicDir, syscall.LOCK_EX|syscall.LOCK_NB) }

func flock(epicDir string, how int) (func(), error) {
	f, err := os.OpenFile(LockPath(epicDir), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open wake lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire wake lock: %w", err)
	}
	// Record the holder while the lock is held (best effort: the flock, not this text, is the lock). A stale pid left by
	// a released holder is harmless: a reader only trusts it while the flock is busy.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte("pid="+strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// storyScopedKinds are the supervision rows that exist only because a story is live: its stale and probe escalations,
// its turn-end idle, its status and blocker reports, and its check output. A released story's rows of these kinds are
// noise (firstmate fm_wake_queue_prune_task drops the task's stale, signal and check rows at teardown, 7e0e60a). The
// leader's decision rows - question, input_required, pr_ready, worker_done - are never pruned.
var storyScopedKinds = map[Kind]bool{
	KindStale: true, KindUnknownProbe: true, KindIdleNoDone: true, KindStatus: true, KindStuck: true, KindCheck: true,
}

// PruneStory removes, under the queue lock, the unacked story-scoped rows (storyScopedKinds) of story, leaving every
// other story's rows and every decision row. Generations never move: the removed rows' highest gen is kept as the
// high-water mark, so a later Append never reuses a gen a drain already printed. It returns how many rows it removed.
func PruneStory(epicDir, story string) (int, error) {
	if story == "" {
		return 0, nil
	}
	unlock, err := lock(epicDir)
	if err != nil {
		return 0, err
	}
	defer unlock()
	acked, err := Acked(epicDir)
	if err != nil {
		return 0, err
	}
	rows, err := scan(epicDir)
	if err != nil {
		return 0, err
	}
	high, err := maxGen(epicDir)
	if err != nil {
		return 0, err
	}
	var keep strings.Builder
	removed := 0
	for _, r := range rows {
		if r.usable && r.wake.Story == story && r.wake.Gen > acked && storyScopedKinds[r.wake.Kind] {
			removed++
			continue
		}
		keep.WriteString(r.raw + "\n")
	}
	if removed == 0 {
		return 0, nil
	}
	if err := state.AtomicWrite(hwmPath(epicDir), []byte(strconv.Itoa(high)+"\n"), 0o644); err != nil {
		return 0, fmt.Errorf("write wake high-water mark: %w", err)
	}
	if err := state.AtomicWrite(queuePath(epicDir), []byte(keep.String()), 0o644); err != nil {
		return 0, fmt.Errorf("write pruned wake queue: %w", err)
	}
	return removed, nil
}
