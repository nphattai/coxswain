// Package busy is the harness-owned busy-state record: idle/busy is a fact the HARNESS reports (captain ruling
// 2026-09-21, wave 3), never something a backend infers from a UI. One record per story at
// <epic>/.cox/sessions/<story>.busy.json holds {schema, state, gen, seq, ts, source, event}. The design copies
// Firstmate's semantic busy-state contract (references/firstmate/bin/fm-busy-lib.sh): a gen token minted at Arm binds
// one incarnation, every Apply must present the current gen (a stale gen is rejected so a hook that outlives its
// incarnation fails closed), and seq advances under a writer lock so an out-of-order Apply can never regress a newer
// record. Arm also stamps the story's harness and the sources that harness trusts (from its capability card), so a
// source the card does not list is rejected on Apply and read as Unknown (DESIGN wave-2 item 6). Read returns the
// current state, or Unknown when the record is absent, unreadable, malformed, or written by an untrusted source - never
// a guess. The gen reaches the harness through the launch env (COX_BUSY_GEN); any harness hook mutates the record through
// `cox busy arm|apply|read`.
package busy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// controlDir is the per-epic control directory (mirrors state.ControlDir; not imported to avoid an import cycle, since
// state imports the backend package and the backend package consults this one).
const controlDir = ".cox"

// Schema is the record version. A record with any other schema is treated as malformed (Read -> Unknown).
const Schema = "busy.v1"

// The three states a worker or leader can be in. Unknown is never treated as idle by a consumer.
const (
	Busy    = "busy"
	Idle    = "idle"
	Unknown = "unknown"
)

// lockStale is how long a lock dir may sit before a new writer breaks it (a holder that died mid-write). Small on
// purpose: every write is a couple of syscalls, so a live holder never holds it this long. // ponytail: fixed 5s stale
// window matching fm-busy-event.sh FM_BUSY_LOCK_STALE_SECS; tune only if a real writer is observed to exceed it.
const lockStale = 5 * time.Second

const (
	lockTries = 40                    // mkdir attempts before the stale check runs
	lockSleep = 50 * time.Millisecond // between attempts (~2s total before the stale break)
)

// Record is one busy-state line. It is written atomically (temp + rename) under the writer lock.
type Record struct {
	Schema  string   `json:"schema"`
	State   string   `json:"state"`             // busy | idle | unknown
	Gen     string   `json:"gen"`               // the incarnation token minted at Arm; an Apply with a different gen is rejected
	Seq     int      `json:"seq"`               // strictly increasing per gen, advanced under the lock
	TS      int64    `json:"ts"`                // unix seconds of this write
	Source  string   `json:"source"`            // who wrote it (dispatch, claude-hook, pi-ext, ...)
	Event   string   `json:"event"`             // the lifecycle event (launch-brief, agent_start, agent_settled, ...)
	Harness string   `json:"harness,omitempty"` // the story's harness, stamped at Arm - names the trust table (below) and the Apply error
	Sources []string `json:"sources,omitempty"` // the sources trusted for this incarnation's harness, stamped at Arm from its capability card
}

// trusted reports whether src is one of the sources this record's harness trusts. An empty table trusts nothing (a
// legacy record armed before the trust table, or an unarmed one), so it fails closed: a source it cannot vouch for is
// never allowed to classify or mutate the record.
func (r Record) trusted(src string) bool {
	for _, s := range r.Sources {
		if s == src {
			return true
		}
	}
	return false
}

// Path is the record file for a story: <epic>/.cox/sessions/<story>.busy.json (beside the session file).
func Path(epic, story string) string {
	return filepath.Join(epic, controlDir, "sessions", story+".busy.json")
}

// tokenValid is the conservative charset shared by gen, source, and event (mirrors fm_busy_token_valid). Anything else
// is malformed, so a record can never smuggle a newline or a shell metacharacter into a field.
func tokenValid(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func stateValid(s string) bool { return s == Busy || s == Idle || s == Unknown }

// Arm mints a fresh incarnation gen, seeds the record at seq=1 with state=busy (the launch prompt is a submitted turn),
// and returns the gen so the caller can thread it into the launch env (COX_BUSY_GEN). It stamps the story's harness and
// the sources that harness trusts (from its capability card, `dispatch` among them) so Apply and Read enforce the trust
// table without re-consulting the card at every event. Arming again replaces the prior incarnation: an Apply carrying
// the old gen is stale from then on.
func Arm(epic, story, harnessName string, sources []string) (string, error) {
	gen, err := mintGen()
	if err != nil {
		return "", err
	}
	rec := Record{
		Schema: Schema, State: Busy, Gen: gen, Seq: 1, TS: time.Now().Unix(),
		Source: "dispatch", Event: "launch-brief", Harness: harnessName, Sources: sources,
	}
	if err := withLock(Path(epic, story), func() error { return write(Path(epic, story), rec) }); err != nil {
		return "", err
	}
	return gen, nil
}

// Apply appends one lifecycle event: it validates gen against the armed record, checks the source against the record's
// trust table, advances seq under the lock, and atomically replaces the record (carrying the harness and trust table
// forward). A stale gen (a hook that outlived its incarnation), an unarmed story, or a source the harness does not trust
// are each rejected. All fail closed so a late, wrong-incarnation, or wrong-harness event can never mutate the live state.
func Apply(epic, story, s, gen, source, event string) error {
	if !stateValid(s) {
		return fmt.Errorf("busy apply: invalid state %q", s)
	}
	if !tokenValid(gen) {
		return fmt.Errorf("busy apply: invalid gen")
	}
	if !tokenValid(source) || !tokenValid(event) {
		return fmt.Errorf("busy apply: invalid source/event")
	}
	path := Path(epic, story)
	return withLock(path, func() error {
		cur, err := load(path)
		if err != nil {
			return fmt.Errorf("busy apply: %s not armed: %w", story, err)
		}
		if cur.Gen != gen {
			return fmt.Errorf("busy apply: stale gen for %s (event rejected)", story)
		}
		if !cur.trusted(source) {
			return fmt.Errorf("busy apply: source %q not trusted for harness %q", source, cur.Harness)
		}
		return write(path, Record{
			Schema: Schema, State: s, Gen: gen, Seq: cur.Seq + 1,
			TS: time.Now().Unix(), Source: source, Event: event, Harness: cur.Harness, Sources: cur.Sources,
		})
	})
}

// Retire removes the record under the writer lock, but only when the presented gen matches the armed incarnation, so a
// SessionEnd hook (or a leader release) retires its OWN incarnation and never clobbers a newer one that a re-arm minted
// in between (fail closed, DESIGN wave-2 item 6). An absent record is already retired (no error); a gen mismatch keeps
// the record and returns an error the caller may treat as best-effort.
func Retire(epic, story, gen string) error {
	if !tokenValid(gen) {
		return fmt.Errorf("busy retire: invalid gen")
	}
	path := Path(epic, story)
	return withLock(path, func() error {
		cur, err := load(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // already gone
			}
			return fmt.Errorf("busy retire: %s: %w", story, err)
		}
		if cur.Gen != gen {
			return fmt.Errorf("busy retire: stale gen for %s (record kept)", story)
		}
		return os.Remove(path)
	})
}

// Read returns the current busy state (Busy | Idle | Unknown). An absent, unreadable, or malformed record is Unknown,
// and so is a record whose current source is not one the harness trusts (a stray writer, or a record armed for a
// different harness) - never a guess. A consumer can safely fall back to its own signal on Unknown and never mistake
// doubt, or an untrusted writer, for idle.
func Read(epic, story string) string {
	rec, err := load(Path(epic, story))
	if err != nil || !stateValid(rec.State) || !rec.trusted(rec.Source) {
		return Unknown
	}
	return rec.State
}

// ReadRecord returns the full record for observability (`cox busy read --json`). ok is false when absent/unreadable.
func ReadRecord(epic, story string) (Record, bool) {
	rec, err := load(Path(epic, story))
	if err != nil {
		return Record{}, false
	}
	return rec, true
}

func mintGen() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("busy: mint gen: %w", err)
	}
	return fmt.Sprintf("g%d.%s", time.Now().UnixNano(), hex.EncodeToString(b[:])), nil
}

// load reads and validates a record. A wrong schema or an out-of-range field is an error, so a corrupt file reads as
// Unknown rather than a fabricated state.
func load(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, err
	}
	if rec.Schema != Schema || !stateValid(rec.State) || !tokenValid(rec.Gen) || rec.Seq < 1 {
		return Record{}, fmt.Errorf("busy: malformed record %s", path)
	}
	return rec, nil
}

// write atomically replaces the record (temp in the same dir + rename), 0600 so the file never leaks to other users.
func write(path string, rec Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// withLock serializes writers on a mkdir lock (portable across macOS and Linux, and atomic even on NFS). A holder that
// died mid-write leaves the dir behind; a new writer breaks it once it is older than lockStale, so a crash never wedges
// the record forever.
func withLock(recPath string, fn func() error) error {
	lock := recPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return err
	}
	for tries := 0; ; tries++ {
		if err := os.Mkdir(lock, 0o700); err == nil {
			break
		}
		if tries >= lockTries {
			if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) >= lockStale {
				_ = os.RemoveAll(lock)
				if err := os.Mkdir(lock, 0o700); err == nil {
					break
				}
			}
			return fmt.Errorf("busy: lock timeout for %s", strings.TrimSuffix(filepath.Base(recPath), ".busy.json"))
		}
		time.Sleep(lockSleep)
	}
	defer os.RemoveAll(lock)
	return fn()
}
