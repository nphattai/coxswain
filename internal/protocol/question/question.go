// Package question is the file-based question/reply channel for the terminal plane (ADR 0012, arena round 1): a worker
// allocates a durable question id under <epic>/questions/<story>/qNNN.md, the leader writes qNNN.answer.md, and the
// worker polls for the answer and moves the pair into handled/. Ids are allocated under a per-story flock and files are
// published by atomic rename from a unique temp name, so concurrent writers never lose or reuse an id (the same
// discipline as the inbox). No Orca message id appears anywhere on this plane; both planes share this layout, so a
// story migrated between planes keeps its question history.
package question

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Schema is the record schema id every question and answer carries in its header.
const Schema = "coxswain.question.v1"

// idRe matches an allocated question id qNNN (at least three digits).
var idRe = regexp.MustCompile(`^q[0-9]{3,}$`)

// CanonID returns the allocated form of a question id typed by a worker or the leader. Ids are case-insensitive (B-61):
// Alloc always writes lower-case qNNN, but a worker that retypes it as Q001 addresses the same question.
func CanonID(id string) (string, error) {
	c := strings.ToLower(strings.TrimSpace(id))
	if !idRe.MatchString(c) {
		return "", fmt.Errorf("not a question id: %q (want qNNN)", id)
	}
	return c, nil
}

// Dir returns <epic>/questions/<story>.
func Dir(epicDir, story string) string {
	return filepath.Join(epicDir, "questions", story)
}

// questionPath / answerPath name the on-disk files for an id. The first answer is qNNN.answer.md; a second reply
// (--again) is qNNN.answer.<n>.md so an earlier answer is never overwritten.
func questionPath(epicDir, story, id string) string {
	return filepath.Join(Dir(epicDir, story), id+".md")
}

func answerPath(epicDir, story, id string, n int) string {
	if n <= 1 {
		return filepath.Join(Dir(epicDir, story), id+".answer.md")
	}
	return filepath.Join(Dir(epicDir, story), fmt.Sprintf("%s.answer.%d.md", id, n))
}

// Alloc allocates the next question id for a story, writes qNNN.md with the body, and returns the id. It takes the
// per-story lock, scans existing ids (in both the live dir and handled/, so an id is never reused), allocates the next,
// and publishes the file atomically.
func Alloc(epicDir, story, body string) (string, error) {
	d := Dir(epicDir, story)
	if err := os.MkdirAll(filepath.Join(d, "handled"), 0o755); err != nil {
		return "", fmt.Errorf("create questions dir: %w", err)
	}
	unlock, err := lock(d)
	if err != nil {
		return "", err
	}
	defer unlock()

	seq, err := nextSeq(d)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("q%03d", seq)
	body = strings.TrimRight(body, "\n")
	content := fmt.Sprintf("---\nschema: %s\nid: %s\nstory: %s\nat: %s\n---\n%s\n", Schema, id, story, nowUTC(), body)
	if err := writeAtomic(d, questionPath(epicDir, story, id), []byte(content)); err != nil {
		return "", err
	}
	return id, nil
}

// Answer writes the leader's reply for an id. It refuses an unknown id and an already-answered id unless again is set,
// in which case the reply is written as qNNN.answer.<n>.md. It returns the path written.
func Answer(epicDir, story, id, answer string, again bool) (string, error) {
	id, err := CanonID(id)
	if err != nil {
		return "", err
	}
	d := Dir(epicDir, story)
	unlock, err := lock(d)
	if err != nil {
		return "", err
	}
	defer unlock()

	if !existsEither(epicDir, story, id) {
		return "", fmt.Errorf("unknown question %s for story %s", id, story)
	}
	n := answerCount(epicDir, story, id)
	if n > 0 && !again {
		return "", fmt.Errorf("%s is already answered (pass --again to add another reply)", id)
	}
	answer = strings.TrimRight(answer, "\n")
	content := fmt.Sprintf("---\nschema: %s\nid: %s\nstory: %s\nat: %s\n---\n%s\n", Schema, id, story, nowUTC(), answer)
	path := answerPath(epicDir, story, id, n+1)
	if err := writeAtomic(d, path, []byte(content)); err != nil {
		return "", err
	}
	return path, nil
}

// Wait polls for the answer to an id, prints nothing itself, and on arrival moves the question and every answer file
// into handled/ and returns the answer body. It backs off from 1s up to 10s. On timeout it returns timedOut=true and
// leaves the files in place so the worker can retry after checkpointing. An unknown id is an error up front.
func Wait(epicDir, story, id string, max time.Duration) (answer string, timedOut bool, err error) {
	id, err = CanonID(id)
	if err != nil {
		return "", false, err
	}
	if !existsEither(epicDir, story, id) {
		return "", false, fmt.Errorf("unknown question %s for story %s", id, story)
	}
	deadline := time.Now().Add(max)
	backoff := time.Second
	for {
		if body, ok, err := readAnswer(epicDir, story, id); err != nil {
			return "", false, err
		} else if ok {
			if err := handle(epicDir, story, id); err != nil {
				return "", false, err
			}
			return body, false, nil
		}
		if !time.Now().Before(deadline) {
			return "", true, nil
		}
		remaining := time.Until(deadline)
		if remaining < backoff {
			time.Sleep(remaining)
		} else {
			time.Sleep(backoff)
		}
		if backoff < 10*time.Second {
			backoff += time.Second
		}
	}
}

// readAnswer returns the body of the first answer file for an id (stripping the header), or ok=false when none exists.
func readAnswer(epicDir, story, id string) (string, bool, error) {
	b, err := os.ReadFile(answerPath(epicDir, story, id, 1))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read answer %s: %w", id, err)
	}
	return body(string(b)), true, nil
}

// handle moves the question and all of its answer files into handled/ (the ack). A missing file is skipped.
func handle(epicDir, story, id string) error {
	d := Dir(epicDir, story)
	handledDir := filepath.Join(d, "handled")
	if err := os.MkdirAll(handledDir, 0o755); err != nil {
		return fmt.Errorf("create handled dir: %w", err)
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return fmt.Errorf("read questions dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == id+".md" || strings.HasPrefix(name, id+".answer") {
			if err := os.Rename(filepath.Join(d, name), filepath.Join(handledDir, name)); err != nil {
				return fmt.Errorf("move %s to handled: %w", name, err)
			}
		}
	}
	return nil
}

// existsEither reports whether the question file exists in the live dir or handled/.
func existsEither(epicDir, story, id string) bool {
	if _, err := os.Stat(questionPath(epicDir, story, id)); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(Dir(epicDir, story), "handled", id+".md"))
	return err == nil
}

// answerCount counts existing answer files for an id (live dir only; a handled question is not re-answered here).
func answerCount(epicDir, story, id string) int {
	entries, err := os.ReadDir(Dir(epicDir, story))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), id+".answer") {
			n++
		}
	}
	return n
}

// nextSeq returns the next question sequence for a story, one past the highest id seen in the live dir and handled/.
// Caller holds the lock.
func nextSeq(d string) (int, error) {
	max := 0
	for _, dir := range []string{d, filepath.Join(d, "handled")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, fmt.Errorf("read questions dir: %w", err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".md") || strings.Contains(name, ".answer") {
				continue
			}
			base := strings.TrimSuffix(name, ".md")
			if !idRe.MatchString(base) {
				continue
			}
			if n, err := strconv.Atoi(strings.TrimPrefix(base, "q")); err == nil && n > max {
				max = n
			}
		}
	}
	return max + 1, nil
}

// List returns the ids of unanswered (still-live) questions for a story in ascending order (for status rendering).
func List(epicDir, story string) ([]string, error) {
	entries, err := os.ReadDir(Dir(epicDir, story))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read questions dir: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".md")
		if strings.HasSuffix(e.Name(), ".md") && idRe.MatchString(base) {
			ids = append(ids, base)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// body returns the text after the `---` header block, or the whole string when there is no header.
func body(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return strings.TrimRight(s, "\n")
	}
	rest := s[len("---\n"):]
	if i := strings.Index(rest, "\n---\n"); i >= 0 {
		return strings.TrimRight(rest[i+len("\n---\n"):], "\n")
	}
	return strings.TrimRight(s, "\n")
}

// lock takes an exclusive flock on <dir>/.seq.lock.
func lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create questions dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".seq.lock"), os.O_CREATE|os.O_RDWR, 0o644)
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

// writeAtomic writes data to a unique temp file in dir, fsyncs, and renames onto final.
func writeAtomic(dir, final string, data []byte) error {
	tmp := filepath.Join(dir, fmt.Sprintf(".tmp.%d.%s", os.Getpid(), token()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publish: %w", err)
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
