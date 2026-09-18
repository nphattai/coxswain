// Package inbox is the steer channel (coxswain.inbox.v1): durable leader instructions to a running worker under
// <epic>/inbox/<story>/NNN.msg. Sequence numbers are allocated under a per-inbox flock and files are published by
// atomic rename from a unique temp name, so concurrent writers never lose or overwrite an instruction while reporting
// success (F06). The worker acks by moving the file into handled/; the move is the ack. The steer budget (5 per story)
// is checked inside the same locked section as sequence allocation.
package inbox

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Schema is the record schema id every steer carries.
const Schema = "coxswain.inbox.v1"

// DefaultBudget is the steer budget per story (fyi records never count).
const DefaultBudget = 5

// Urgency values.
const (
	Steer = "steer"
	FYI   = "fyi"
)

// KindReply marks an inbox record that carries a question answer (written by `cox reply`), not a fresh leader
// instruction. It rides as an extra `kind=reply` header (inbox.v1 compatible: an older reader ignores the unknown key),
// and the steer budget never counts it - a reply is an answer, not a steer (M14, dogfood: 7 replies wrongly consumed the
// 5-steer budget and refused a real steer).
const KindReply = "reply"

// ErrBudget is returned when a steer would exceed the per-story budget and no override was given. It carries the
// current steer count so the caller can report it.
type ErrBudget struct {
	Story  string
	Count  int
	Budget int
}

func (e *ErrBudget) Error() string {
	return fmt.Sprintf("steer budget exceeded for %s: %d of %d used (pass --override <reason> to force)", e.Story, e.Count, e.Budget)
}

// Record is one parsed steer. Seq comes from the filename, Story from the directory; the header carries schema, at,
// urgency and an optional override reason; Body is the verbatim instruction after the `--` separator.
type Record struct {
	Seq      int
	Story    string
	At       string
	Urgency  string
	Kind     string // "" for a steer/fyi, "reply" for a `cox reply` answer (kind=reply header); replies are budget-exempt
	Override string
	Body     string
	Path     string // absolute path to the .msg file
}

// Dir returns <epic>/inbox/<story>.
func Dir(epicDir, story string) string {
	return filepath.Join(epicDir, "inbox", story)
}

// Write publishes one steer record and returns its path. It takes the per-inbox lock, allocates the next sequence,
// enforces the steer budget inside the same critical section, writes to a uniquely named temp file, fsyncs, and
// renames into place. Any failure returns a real error; nothing is swallowed. urgency defaults to steer when empty.
// override, when non-empty, records `override=<reason>` in the header and bypasses the budget check.
func Write(epicDir, story, text, urgency, override string) (string, error) {
	return writeRecord(epicDir, story, text, urgency, "", override)
}

// WriteReply publishes a question answer as a budget-exempt inbox record (urgency=steer so the worker's re-ring ladder
// still delivers it, kind=reply so the steer budget never counts it). It is the terminal-plane `cox reply` durable
// channel; a reply is an answer, not a fresh instruction, so it needs no override and never consumes a steer (M14).
func WriteReply(epicDir, story, text string) (string, error) {
	return writeRecord(epicDir, story, text, Steer, KindReply, "")
}

func writeRecord(epicDir, story, text, urgency, kind, override string) (string, error) {
	if urgency == "" {
		urgency = Steer
	}
	if urgency != Steer && urgency != FYI {
		return "", fmt.Errorf("invalid urgency %q (want steer|fyi)", urgency)
	}
	d := Dir(epicDir, story)
	if err := os.MkdirAll(filepath.Join(d, "handled"), 0o755); err != nil {
		return "", fmt.Errorf("create inbox dir: %w", err)
	}

	unlock, err := lock(d)
	if err != nil {
		return "", err
	}
	defer unlock()

	recs, err := scan(d)
	if err != nil {
		return "", err
	}
	// Budget: count budget-bearing steer records only (handled or not); fyi and reply records never count. A reply
	// (kind=reply) is an answer, not a steer, so it is exempt from both the check and the count (M14).
	if urgency == Steer && kind != KindReply && override == "" {
		used := 0
		for _, r := range recs {
			if r.Urgency == Steer && r.Kind != KindReply {
				used++
			}
		}
		if used >= DefaultBudget {
			return "", &ErrBudget{Story: story, Count: used, Budget: DefaultBudget}
		}
	}

	seq := 1
	for _, r := range recs {
		if r.Seq >= seq {
			seq = r.Seq + 1
		}
	}

	rec := Record{Seq: seq, Story: story, At: nowUTC(), Urgency: urgency, Kind: kind, Override: override, Body: text}
	final := filepath.Join(d, fmt.Sprintf("%03d.msg", seq))
	tmp := filepath.Join(d, fmt.Sprintf("%03d.msg.%d.%s.tmp", seq, os.Getpid(), token()))
	if err := writeAtomic(tmp, final, rec.marshal()); err != nil {
		return "", err
	}
	return final, nil
}

// List returns the unhandled records of a story in ascending sequence order.
func List(epicDir, story string) ([]Record, error) {
	d := Dir(epicDir, story)
	recs, err := scanDir(d) // only NNN.msg directly under d, not handled/
	if err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Seq < recs[j].Seq })
	return recs, nil
}

// All returns every record of a story, handled and unhandled, in ascending sequence order. The watcher uses it to find
// the most recent steer regardless of whether the worker has already acked it.
func All(epicDir, story string) ([]Record, error) {
	recs, err := scan(Dir(epicDir, story))
	if err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Seq < recs[j].Seq })
	return recs, nil
}

// Handled moves a record into handled/. The move is the worker's acknowledgement (mv semantics kept from v1).
func Handled(rec Record) error {
	if rec.Path == "" {
		return errors.New("inbox: record has no path to move")
	}
	dst := filepath.Join(filepath.Dir(rec.Path), "handled", filepath.Base(rec.Path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create handled dir: %w", err)
	}
	if err := os.Rename(rec.Path, dst); err != nil {
		return fmt.Errorf("ack (move to handled): %w", err)
	}
	return nil
}

// Ack is the path-based wrapper the CLI uses: it loads the record at recPath and moves it to handled/.
func Ack(recPath string) error {
	rec, err := parseFile(recPath)
	if err != nil {
		return err
	}
	return Handled(rec)
}

// Doorbell is the constant instruction rung into a worker terminal when steers are waiting (ported from v1).
func Doorbell(inboxDir string) string {
	return fmt.Sprintf("Leader instruction waiting: list %s/*.msg and, in numeric order, read and act on each, then mv each handled file to %s/handled/. Then continue.", inboxDir, inboxDir)
}

// marshal renders the on-disk record: a key=value header, a `--` separator, then the verbatim body.
func (r Record) marshal() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "schema=%s\n", Schema)
	fmt.Fprintf(&b, "at=%s\n", r.At)
	fmt.Fprintf(&b, "urgency=%s\n", r.Urgency)
	if r.Kind != "" {
		fmt.Fprintf(&b, "kind=%s\n", r.Kind)
	}
	if r.Override != "" {
		fmt.Fprintf(&b, "override=%s\n", r.Override)
	}
	b.WriteString("--\n")
	b.WriteString(r.Body)
	if !strings.HasSuffix(r.Body, "\n") {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// scan reads every record under d and d/handled (used for sequence allocation and budget).
func scan(d string) ([]Record, error) {
	main, err := scanDir(d)
	if err != nil {
		return nil, err
	}
	handled, err := scanDir(filepath.Join(d, "handled"))
	if err != nil {
		return nil, err
	}
	return append(main, handled...), nil
}

// scanDir reads the NNN.msg records directly inside dir (not recursing). A missing dir yields no records.
func scanDir(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read inbox dir: %w", err)
	}
	var recs []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".msg") {
			continue
		}
		rec, err := parseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// parseFile reads and parses one record file. Seq comes from the filename; the header supplies the rest.
func parseFile(path string) (Record, error) {
	seq, err := seqFromName(filepath.Base(path))
	if err != nil {
		return Record{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Record{}, fmt.Errorf("open steer: %w", err)
	}
	defer f.Close()

	rec := Record{Seq: seq, Path: path, Story: filepath.Base(filepath.Dir(path))}
	if rec.Story == "handled" {
		rec.Story = filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	inBody := false
	var body strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if !inBody {
			if line == "--" {
				inBody = true
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "urgency":
				rec.Urgency = v
			case "kind":
				rec.Kind = v
			case "at":
				rec.At = v
			case "override":
				rec.Override = v
			}
			continue
		}
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(line)
	}
	if err := sc.Err(); err != nil {
		return Record{}, fmt.Errorf("read steer: %w", err)
	}
	rec.Body = body.String()
	if rec.Urgency == "" {
		rec.Urgency = Steer
	}
	return rec, nil
}

func seqFromName(name string) (int, error) {
	// NNN.msg (a temp is NNN.msg.<pid>.<tok>.tmp and is never parsed as a record).
	base := strings.TrimSuffix(name, ".msg")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 0, fmt.Errorf("not a sequence-numbered record: %q", name)
	}
	return n, nil
}

// lock takes an exclusive flock on <dir>/.seq.lock and returns an unlock func.
func lock(dir string) (func(), error) {
	lf := filepath.Join(dir, ".seq.lock")
	f, err := os.OpenFile(lf, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open seq lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire seq lock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// writeAtomic writes data to tmp, fsyncs, then renames onto final. A failure at any step returns a real error and
// leaves no record file (the temp is removed on failure).
func writeAtomic(tmp, final string, data []byte) error {
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open steer temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write steer temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync steer temp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close steer temp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publish steer: %w", err)
	}
	return nil
}

func token() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
