// Package checkpoint is what a worker leaves for its own future self so a restart or compaction is a non-event
// (coxswain.checkpoint.v1). It lives at <epic>/handoffs/<id>.md as markdown with YAML frontmatter; the frontmatter is
// the machine contract. Facts fills the computable part (head, dirty, unhandled inbox, PR, CI); Inject prints the
// checkpoint and refuses a wrong-attempt one, warning when its head differs from the current HEAD (F07).
package checkpoint

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Schema is the checkpoint frontmatter schema id.
const Schema = "coxswain.checkpoint.v1"

// validReasons are the coxswain.checkpoint.v1 reason enum values.
var validReasons = map[string]bool{
	"phase-end": true, "plan-compact": true, "compact-now": true, "park": true, "precompact-auto": true,
	"migrated-v1": true, // a v1 handoff wrapped into a checkpoint by cox migrate (M9)
}

// Frontmatter is the machine-validated header of a checkpoint file.
type Frontmatter struct {
	Schema    string
	Story     string
	Attempt   int
	Head      string
	Base      string
	WrittenAt string
	Reason    string
}

// Path returns <epic>/handoffs/<id>.md.
func Path(epicDir, story string) string {
	return filepath.Join(epicDir, "handoffs", story+".md")
}

// HeadMatches reports whether two commit shas name the same commit, tolerating short/long forms: the shorter side must
// be a prefix of the longer and at least 7 hex chars. So a checkpoint that recorded a short sha still matches a full
// `rev-parse HEAD` (and the reverse), while a genuinely different head fails the prefix test. An empty or "unknown"
// sha never matches.
func HeadMatches(a, b string) bool {
	if a == "" || b == "" || a == "unknown" || b == "unknown" {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 7 && strings.HasPrefix(b, a)
}

// Validate checks the frontmatter against coxswain.checkpoint.v1: required fields present, schema id exact, attempt
// >= 1, reason in the enum. It returns the first violation.
func (fm Frontmatter) Validate() error {
	if fm.Schema != Schema {
		return fmt.Errorf("schema is %q, want %q", fm.Schema, Schema)
	}
	if fm.Story == "" {
		return fmt.Errorf("missing story")
	}
	if fm.Attempt < 1 {
		return fmt.Errorf("attempt is %d, want >= 1", fm.Attempt)
	}
	if fm.Head == "" {
		return fmt.Errorf("missing head")
	}
	if fm.Base == "" {
		return fmt.Errorf("missing base")
	}
	if fm.WrittenAt == "" {
		return fmt.Errorf("missing written_at")
	}
	if !validReasons[fm.Reason] {
		return fmt.Errorf("reason %q not in enum (phase-end|plan-compact|compact-now|park|precompact-auto|migrated-v1)", fm.Reason)
	}
	return nil
}

// Parse reads a checkpoint file and returns its frontmatter and the markdown body below the closing `---`. It is a
// hard error when the file has no `---` frontmatter block: a checkpoint without the machine contract is not a
// checkpoint.
func Parse(path string) (Frontmatter, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return Frontmatter{}, "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Frontmatter{}, "", fmt.Errorf("%s: missing frontmatter (first line must be ---)", path)
	}
	var fm Frontmatter
	closed := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "schema":
			fm.Schema = v
		case "story":
			fm.Story = v
		case "attempt":
			fm.Attempt, _ = strconv.Atoi(v)
		case "head":
			fm.Head = v
		case "base":
			fm.Base = v
		case "written_at":
			fm.WrittenAt = v
		case "reason":
			fm.Reason = v
		}
	}
	if !closed {
		return Frontmatter{}, "", fmt.Errorf("%s: frontmatter not closed with ---", path)
	}
	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return Frontmatter{}, "", fmt.Errorf("read checkpoint: %w", err)
	}
	return fm, body.String(), nil
}

// Injection is the result of preparing a checkpoint for a resuming session.
type Injection struct {
	Text  string // the full text to print into the session (stale warning first, then checkpoint, then Read first)
	Stale bool   // head differed from the current HEAD
}

// ErrWrongAttempt is returned by Inject when the checkpoint belongs to a different attempt; the caller exits 1 and
// never injects it (F07: a wrong-attempt checkpoint is never injected silently).
type ErrWrongAttempt struct {
	Want int
	Got  int
}

func (e *ErrWrongAttempt) Error() string {
	return fmt.Sprintf("checkpoint is for attempt %d, current attempt is %d; refusing to inject", e.Got, e.Want)
}

// Inject prepares the checkpoint at <epic>/handoffs/<story>.md for a session resuming as currentAttempt at
// currentHead. It rejects a checkpoint whose attempt differs (ErrWrongAttempt). When the checkpoint's head differs
// from currentHead the first line of the output is a CHECKPOINT STALE warning. The output then carries the checkpoint
// and the story's "Read first" block, matching v1 hook-session-compact. A missing checkpoint is not an error: the
// output says so and carries only the Read first block, so a first attempt still gets its story context.
func Inject(epicDir, story string, currentAttempt int, currentHead string) (Injection, error) {
	var out strings.Builder
	path := Path(epicDir, story)

	fm, body, err := Parse(path)
	if os.IsNotExist(err) {
		fmt.Fprintf(&out, "No checkpoint at %s yet (first attempt or none written). Start from the story.\n", path)
		out.WriteString(readFirst(epicDir, story))
		return Injection{Text: out.String()}, nil
	}
	if err != nil {
		return Injection{}, err
	}
	if err := fm.Validate(); err != nil {
		return Injection{}, fmt.Errorf("invalid checkpoint %s: %w", path, err)
	}
	if fm.Attempt != currentAttempt {
		return Injection{}, &ErrWrongAttempt{Want: currentAttempt, Got: fm.Attempt}
	}

	stale := currentHead != "" && !HeadMatches(fm.Head, currentHead)
	if stale {
		fmt.Fprintf(&out, "CHECKPOINT STALE: head %s, HEAD hiện tại %s. Re-verify the Current state and Verified outcomes sections before trusting them.\n\n", fm.Head, currentHead)
	}
	fmt.Fprintf(&out, "Checkpoint for story %s (attempt %d, reason %s), from %s:\n\n", fm.Story, fm.Attempt, fm.Reason, path)
	out.WriteString("---\n")
	fmt.Fprintf(&out, "schema: %s\nstory: %s\nattempt: %d\nhead: %s\nbase: %s\nwritten_at: %s\nreason: %s\n", fm.Schema, fm.Story, fm.Attempt, fm.Head, fm.Base, fm.WrittenAt, fm.Reason)
	out.WriteString("---\n")
	out.WriteString(body)
	out.WriteString(readFirst(epicDir, story))
	return Injection{Text: out.String(), Stale: stale}, nil
}

// readFirst returns the story's "## Read first" section (through the next "## " heading), or empty when absent.
func readFirst(epicDir, story string) string {
	f, err := os.Open(filepath.Join(epicDir, "stories", story+".md"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	in := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			if in {
				break // next section ends Read first
			}
			if strings.HasPrefix(line, "## Read first") {
				in = true
				b.WriteString("\n== Read first (from the story):\n")
				continue
			}
		}
		if in {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
