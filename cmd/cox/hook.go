package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/wake"
	"github.com/nphattai/coxswain/internal/watch"
)

// leaderStory is the pseudo-story id a leader session sets as COX_STORY, so its checkpoint hooks are guarded by the
// leader-terminal check rather than treated as a worker story.
const leaderStory = "_leader"

// cmdHook implements `cox hook <name>`. The Claude Code shims are three-line execs into these handlers, and Codex has
// no hooks (its discipline lives in AGENTS.md), so all hook logic is here where it is testable.
func cmdHook(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox hook prompt-drain|stop-rewake|precompact|session-start")
		return 2
	}
	name := args[0]
	fs := flag.NewFlagSet("hook "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	story := fs.String("story", os.Getenv("COX_STORY"), "story id")
	worktree := fs.String("worktree", ".", "worktree path")
	harnessName := fs.String("harness", "claude", "invoking harness: claude | codex (controls the block/continue signal)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	switch name {
	case "prompt-drain":
		return hookPromptDrain(*epicDir, *harnessName)
	case "stop-rewake":
		return hookStopRewake(*epicDir, *harnessName)
	case "precompact":
		return hookPreCompact(*epicDir, *story, *worktree)
	case "session-start":
		return hookSessionStart(*epicDir, *story, *worktree)
	default:
		fmt.Fprintf(os.Stderr, "cox hook: unknown hook %q\n", name)
		return 2
	}
}

// orcaDoorbellRe matches Orca's terminal doorbell prompt ("You have N orchestration messages. Run `orca orchestration
// check` ..."). When that prompt fires but cox has no wake pending, the mail was already drained into the wake queue and
// acked by the watcher, so the doorbell is stale and there is nothing for the leader to do.
var orcaDoorbellRe = regexp.MustCompile(`^You have [0-9]+ orchestration messages?\. Run .orca orchestration check`)

// hookPromptDrain (UserPromptSubmit): attach the unacked wakes of the led epic to the leader's turn as context. stdout
// becomes context; it never acks (the leader's turn drains explicitly). When there are no wakes and the submitted prompt
// is Orca's empty doorbell, it exits 2 to block the prompt (a UserPromptSubmit exit 2 erases the prompt and starts no
// turn; stderr is shown to the user) so the stale bell does not cost the leader a turn.
func hookPromptDrain(epicDir, harnessName string) int {
	if epicDir != "" && notLeaderTerminal(epicDir) {
		return 0 // a non-leader terminal must not drain the leader's wake queue
	}
	return runPromptDrain(epicDir, harnessName, os.Stdin, os.Stdout, os.Stderr)
}

// codexBlock emits codex's stdout block decision, which continues/reopens a turn (Stop) or discards a stale prompt
// (UserPromptSubmit). Claude uses exit 2 for the same effect; codex reads this JSON instead (verified shapes, codex-cli
// 0.154, docs/adapters/codex.md).
func codexBlock(out io.Writer, reason string) {
	_ = json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": reason})
}

// notLeaderTerminal reports whether this terminal is demonstrably NOT the epic's leader: <epic>/.cox/leader records a
// handle (story dispatch writes it) that differs from this terminal's ORCA_TERMINAL_HANDLE. When .cox/leader is absent
// the leader is unknown and it returns false (keep today's behavior). This stops the leader-only wake hooks from firing
// in a worker session whose worktree happens to carry the repo's own .claude/settings.json pointing at the leader epic.
func notLeaderTerminal(epicDir string) bool {
	leader := readLeader(epicDir)
	if leader == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv("ORCA_TERMINAL_HANDLE")) != leader
}

func runPromptDrain(epicDir, harnessName string, in io.Reader, out, errW io.Writer) int {
	prompt := hookPrompt(in)
	if epicDir == "" {
		return 0 // this terminal leads nothing; silent
	}
	wakes, err := wake.Drain(epicDir, true)
	if err != nil {
		return 0
	}
	if len(wakes) == 0 {
		// Orca types its doorbell with a leading newline ("\nYou have 1 orchestration message. Run ..."), so the ^-anchored
		// regex never matches the raw prompt; trim first (A12) or every stale bell costs the leader a turn.
		if orcaDoorbellRe.MatchString(strings.TrimSpace(prompt)) {
			const reason = "Orca doorbell, no wake pending - suppressed"
			if harnessName == "codex" {
				codexBlock(out, reason) // codex discards the prompt on a block decision (stdout), no exit-2
				return 0
			}
			fmt.Fprintln(errW, "cox: "+reason)
			return 2
		}
		return 0
	}
	fmt.Fprintf(out, "Watcher wakes for %s since your last turn:\n", filepath.Base(epicDir))
	printWakesTo(out, wakes)
	return 0
}

// hookPrompt reads the UserPromptSubmit hook stdin JSON and returns its `prompt` field, or "" when stdin is empty or not
// the hook envelope (so a manual `cox hook prompt-drain` with no stdin is harmless).
func hookPrompt(in io.Reader) string {
	if in == nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(in, 1<<20))
	if err != nil || len(b) == 0 {
		return ""
	}
	var p struct {
		Prompt string `json:"prompt"`
	}
	if json.Unmarshal(b, &p) == nil {
		return p.Prompt
	}
	return ""
}

// hookStopRewake (Stop, asyncRewake): while the leader is idle, wait for a watcher wake in the durable queue, then exit
// 2 to open a new turn without typing into the composer (v1 bin/hook-stop-rewake.sh, patched in M0). One waiter per
// terminal (a lock file); an urgent wake rewakes at once, routine wakes (status/stale/quiet/unknown_probe) batch up to
// WAKE_BATCH so several cost one turn; on MAX_WAIT with no wake, a still-open dispatched story ticks (exit 2) so the
// next turn re-arms the waiter, otherwise the leader stays idle (exit 0).
func hookStopRewake(epicDir, harnessName string) int {
	if epicDir == "" {
		return 0
	}
	if notLeaderTerminal(epicDir) {
		return 0 // a non-leader terminal must not rewake on the leader's wake queue
	}
	if handle := os.Getenv("ORCA_TERMINAL_HANDLE"); handle != "" {
		lock := filepath.Join(tmpDir(), "cox-rewake-"+handle+".lock")
		if rewakeWaiterAlive(lock) {
			return 0 // another waiter already runs for this terminal
		}
		if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())), 0o644); err == nil {
			defer os.Remove(lock)
		}
	}
	return runStopRewake(rewakeCfg{
		epicDir:  epicDir,
		harness:  harnessName,
		maxWait:  envSeconds("REWAKE_MAX_WAIT", 3300),
		batchMax: envSeconds("WAKE_BATCH", 300),
		poll:     15 * time.Second,
		out:      os.Stderr,
		stdout:   os.Stdout,
		sleep:    time.Sleep,
	})
}

// rewakeCfg is the stop-rewake loop's inputs, injected so the loop is unit-tested without real waiting (sleep is a
// no-op in tests; maxWait/batchMax/poll are durations, not env reads).
type rewakeCfg struct {
	epicDir  string
	harness  string // "codex" reopens via a stdout block decision; anything else (claude) reopens via exit 2
	maxWait  time.Duration
	batchMax time.Duration
	poll     time.Duration
	out      io.Writer // reopen/tick text sink for the exit-2 path (stderr)
	stdout   io.Writer // codex block-decision sink (stdout); defaults handled by the caller
	sleep    func(time.Duration)
}

// reopen ends the idle wait by opening a new turn: codex reads a stdout block decision (exit 0), every other harness
// reads exit 2 with the message on the text sink. msg is the human-readable instruction shown either way.
func (cfg rewakeCfg) reopen(msg string) int {
	if cfg.harness == "codex" {
		codexBlock(cfg.stdout, strings.TrimRight(msg, "\n"))
		return 0
	}
	fmt.Fprint(cfg.out, msg)
	return 2
}

// runStopRewake is the testable core: peek the wake queue every poll up to maxWait, then decide the tick.
func runStopRewake(cfg rewakeCfg) int {
	poll := cfg.poll
	if poll <= 0 {
		poll = 15 * time.Second
	}
	batch := time.Duration(0)
	for elapsed := time.Duration(0); elapsed < cfg.maxWait; elapsed += poll {
		wakes, err := wake.Drain(cfg.epicDir, true)
		if err != nil {
			return 0
		}
		if len(wakes) > 0 {
			if anyUrgent(wakes) || batch >= cfg.batchMax {
				msg := fmt.Sprintf("Watcher wake while idle. Run `cox wake drain --epic %s`, handle them, then ack-through:\n", cfg.epicDir) + wakesText(wakes)
				return cfg.reopen(msg)
			}
			batch += poll
		} else {
			batch = 0
		}
		cfg.sleep(poll)
	}
	// MAX_WAIT reached with no rewake. A Stop hook cannot outlive its timeout, so while any led story is still
	// dispatched (working or input_required) start a minimal turn (exit 2) whose Stop re-arms a fresh waiter; with
	// nothing open, stay silent (exit 0) so there is no idle churn between epics.
	open, err := watch.OpenStories(cfg.epicDir)
	if err != nil {
		return 0
	}
	if len(open) > 0 {
		msg := fmt.Sprintf("Rewake tick: no watcher wake in %s and %d dispatched story(ies) still open. Nothing to handle - end this turn with one line and no tool calls so the Stop hook re-arms the waiter.\n", cfg.maxWait, len(open))
		return cfg.reopen(msg)
	}
	return 0
}

func anyUrgent(wakes []wake.Wake) bool {
	for _, w := range wakes {
		if wake.IsUrgent(w.Kind) {
			return true
		}
	}
	return false
}

func envSeconds(name string, def int) time.Duration {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(def) * time.Second
}

func tmpDir() string {
	if d := os.Getenv("TMPDIR"); d != "" {
		return d
	}
	return "/tmp"
}

// rewakeWaiterAlive reports whether the lock file names a still-running process (v1's `kill -0` single-waiter guard).
func rewakeWaiterAlive(lock string) bool {
	b, err := os.ReadFile(lock)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// hookPreCompact (PreCompact): compute checkpoint facts and refresh them in the handoff so the post-compaction session
// reads a checkpoint with current facts. It replaces the previous facts block rather than appending another, so a
// checkpoint that compacts several times carries one block, not a stack of stale ones, and refreshes written_at. It
// seeds a minimal checkpoint from the template shape when the handoff is absent.
func hookPreCompact(epicDir, story, worktree string) int {
	if epicDir == "" || story == "" {
		return 0
	}
	if story == leaderStory && notLeaderTerminal(epicDir) {
		return 0 // the leader's own checkpoint hook, running in a non-leader terminal
	}
	facts, err := checkpoint.Facts(worktree, epicDir, story)
	if err != nil {
		return fail("%v", err)
	}
	path := checkpoint.Path(epicDir, story)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail("%v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var body string
	switch b, err := os.ReadFile(path); {
	case err == nil:
		body = refreshFacts(string(b), facts.Markdown(), now)
	case errors.Is(err, os.ErrNotExist):
		// No handoff yet: seed a minimal checkpoint so the facts have a home.
		fm := fmt.Sprintf("---\nschema: %s\nstory: %s\nattempt: %d\nhead: %s\nbase: %s\nwritten_at: %s\nreason: precompact-auto\n---\n",
			checkpoint.Schema, story, currentAttempt(epicDir, story), facts.Head, facts.Base, now)
		body = fm + "\n" + facts.Markdown()
	default:
		return fail("%v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(os.Stderr, "precompact: refreshed facts in %s\n", path)
	return 0
}

// factsHeading opens the machine-facts block a checkpoint carries (checkpoint.FactSet.Markdown).
const factsHeading = "## Facts (máy tính)"

// refreshFacts replaces every trailing facts block in a checkpoint with one fresh block and refreshes the frontmatter
// written_at. Facts are always written at the end, so everything from the first facts heading onward is prior facts
// blocks and is dropped; the checkpoint body above it is kept.
func refreshFacts(content, factsMD, writtenAt string) string {
	content = setWrittenAt(content, writtenAt)
	if i := strings.Index(content, factsHeading); i >= 0 {
		content = content[:i]
	}
	return strings.TrimRight(content, "\n") + "\n\n" + factsMD
}

// setWrittenAt replaces the written_at value inside the leading frontmatter block. A checkpoint with no written_at line
// is returned unchanged (the seed always writes one).
func setWrittenAt(content, writtenAt string) string {
	lines := strings.Split(content, "\n")
	inFM := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "---" {
			if inFM {
				break // end of frontmatter
			}
			inFM = true
			continue
		}
		if inFM && strings.HasPrefix(t, "written_at:") {
			lines[i] = "written_at: " + writtenAt
			break
		}
	}
	return strings.Join(lines, "\n")
}

// hookSessionStart (SessionStart compact|resume): inject the checkpoint (with the freshness check) into the new
// session. A wrong-attempt checkpoint exits 1 and is not injected (F07).
func hookSessionStart(epicDir, story, worktree string) int {
	if epicDir == "" || story == "" {
		return 0
	}
	if story == leaderStory && notLeaderTerminal(epicDir) {
		return 0 // the leader's own checkpoint hook, running in a non-leader terminal
	}
	head := gitHead(worktree)
	inj, err := checkpoint.Inject(epicDir, story, currentAttempt(epicDir, story), head)
	if err != nil {
		var wa *checkpoint.ErrWrongAttempt
		if errors.As(err, &wa) {
			fmt.Fprintln(os.Stderr, "session-start:", err)
			return 1
		}
		return fail("%v", err)
	}
	fmt.Print(inj.Text)
	return 0
}

// wakesText renders the wake list to a string (for embedding in a codex block-decision reason).
func wakesText(wakes []wake.Wake) string {
	var b strings.Builder
	printWakesTo(&b, wakes)
	return b.String()
}

func printWakesTo(w io.Writer, wakes []wake.Wake) {
	for _, k := range wakes {
		note := k.Note
		if note == "" {
			note = string(k.Kind)
		}
		fmt.Fprintf(w, "[gen %d] %s %s: %s\n", k.Gen, k.Kind, k.Story, note)
	}
}

// gitHead returns the full HEAD sha (not --short): Inject compares heads prefix-tolerantly, and a full sha keeps the
// freshness check unambiguous while still matching an older short-sha checkpoint.
func gitHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
