package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/state"
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
	// COX_EPIC/COX_STORY are the worker-plane env the launch line exports; the terminal-plane worker's checkpoint hooks
	// (precompact, session-start) run with no flags and rely on them. Keeping them as the flag defaults preserves the
	// worker checkpoint path; a leader terminal sets neither, so leader hooks fall through to workspace discovery.
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory (optional; narrows to one epic instead of every active epic in the workspace)")
	story := fs.String("story", os.Getenv("COX_STORY"), "story id (optional; the workspace path uses the leader checkpoint)")
	worktree := fs.String("worktree", ".", "worktree path")
	harnessName := fs.String("harness", "claude", "invoking harness: claude | codex | pi (controls the block/continue signal; pi re-arms its own idle waiter, so stop-rewake never ticks)")
	// prompt-drain only: the turn was opened by a stop-rewake reopen, not a user prompt. Pi runs prompt-drain on every
	// turn, including the ones a reopen opens, so resetting the block budget there would reset it on every reopen and a
	// dead watcher would reopen forever (dogfood finding 8 / AC5). Claude never sets it: UserPromptSubmit is a user turn.
	reopenTurn := fs.Bool("reopen", false, "prompt-drain: this turn was opened by a stop-rewake reopen; keep the block budget")
	// stop-rewake only: false is the Pi waiter armed by the settle of the guard's own follow-up (firstmate's
	// once-per-logical-run latch, fm-primary-turnend-guard.ts): it waits for wakes without running the guard again.
	runGuard := fs.Bool("guard", true, "stop-rewake: run the turn-boundary watcher guard before waiting")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	switch name {
	case "prompt-drain":
		return hookPromptDrain(*epicDir, *harnessName, *reopenTurn)
	case "stop-rewake":
		return hookStopRewake(*epicDir, *harnessName, *runGuard)
	case "precompact":
		return hookLeaderCheckpoint("precompact", *epicDir, *story, *worktree, hookPreCompact)
	case "session-start":
		return hookLeaderCheckpoint("session-start", *epicDir, *story, *worktree, hookSessionStart)
	default:
		fmt.Fprintf(os.Stderr, "cox hook: unknown hook %q\n", name)
		return 2
	}
}

// leaderStory-scoped hooks and the wake hooks resolve the epics they act on the same way: an explicit --epic narrows to
// one, otherwise they walk up from the cwd to the workspace and act on every epic whose watcher is alive. This is the
// onboarding rule (DESIGN §3): leader hooks belong to the workspace, not to an epic, so a `cox hook` invocation reads no
// per-terminal epic env var. Returns the epic dirs and whether the cwd is inside a workspace at all.
func leaderEpics(epicDir string) (epics []string, inWorkspace bool) {
	if epicDir != "" {
		return []string{epicDir}, true
	}
	wsRoot, err := findWorkspaceRoot(".")
	if err != nil {
		return nil, false
	}
	return activeEpics(wsRoot), true
}

// activeEpics returns the epic dirs under a workspace (<ws>/<project>/epics/<slug>) whose .cox/watch.pid names a live
// process. A dead or absent watcher means no wakes are being delivered there, so the leader hooks skip it.
func activeEpics(wsRoot string) []string {
	var out []string
	seen := map[string]bool{}
	// Both the one-level (<ws>/<project>/epics/<slug>) and the two-level nested-project (<ws>/apps/foo/epics/<slug>)
	// layouts, matching cox epic new and allocatePorts; a one-level-only glob leaves nested projects with no hooks.
	for _, pat := range []string{
		filepath.Join(wsRoot, "*", "epics", "*"),
		filepath.Join(wsRoot, "*", "*", "epics", "*"),
	} {
		matches, _ := filepath.Glob(pat)
		for _, ep := range matches {
			if seen[ep] {
				continue
			}
			if info, err := os.Stat(ep); err != nil || !info.IsDir() {
				continue
			}
			if pid := readPid(watchPidPath(ep)); pid > 0 && processAlive(pid) {
				seen[ep] = true
				out = append(out, ep)
			}
		}
	}
	sort.Strings(out)
	return out
}

// allEpics enumerates every epic control dir under a workspace regardless of watcher liveness. Unlike activeEpics it
// keeps an epic whose watcher has died, so the turn-boundary guard (item 1) can see - and restart - the very watcher
// whose death would otherwise hide the epic from every leader hook.
func allEpics(wsRoot string) []string {
	var out []string
	seen := map[string]bool{}
	for _, pat := range []string{
		filepath.Join(wsRoot, "*", "epics", "*"),
		filepath.Join(wsRoot, "*", "*", "epics", "*"),
	} {
		matches, _ := filepath.Glob(pat)
		for _, ep := range matches {
			if seen[ep] {
				continue
			}
			if info, err := os.Stat(ep); err != nil || !info.IsDir() {
				continue
			}
			seen[ep] = true
			out = append(out, ep)
		}
	}
	sort.Strings(out)
	return out
}

// guardEpics returns the led epics with at least one open story (working or input_required), regardless of watcher
// liveness, so the turn-boundary guard restarts a dead watcher for an epic that still has work (item 1). An explicit
// --epic narrows to it; otherwise it walks up to the workspace. Leader-filtered so a worker worktree never guards the
// leader's epics.
func guardEpics(epicDir string) []string {
	var eps []string
	if epicDir != "" {
		eps = []string{epicDir}
	} else if wsRoot, err := findWorkspaceRoot("."); err == nil {
		eps = allEpics(wsRoot)
	} else {
		return nil
	}
	eps = filterLeaderEpics(eps)
	var out []string
	for _, ep := range eps {
		if open, err := watch.OpenStories(ep); err == nil && len(open) > 0 {
			out = append(out, ep)
		}
	}
	return out
}

// outsideWorkspace prints one line to stderr and returns exit 0: a hook fired in a terminal that leads nothing must be
// silent-but-visible, never crash the session (DESIGN §3).
func outsideWorkspace(hook string) int {
	fmt.Fprintf(os.Stderr, "cox hook %s: not inside a cox workspace (no cox/workspace.json above %s); nothing to do\n", hook, mustCwd())
	return 0
}

func mustCwd() string {
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return "."
}

// hookLeaderCheckpoint runs a checkpoint hook (precompact or session-start). An explicit --epic with an explicit --story
// narrows to that one checkpoint. Otherwise it resolves the workspace and runs the hook for the leader checkpoint
// (_leader) of every active epic, best-effort: one epic's checkpoint failure must not crash the leader's turn. Outside a
// workspace it prints one line and exits 0.
func hookLeaderCheckpoint(name, epicDir, story, worktree string, fn func(epicDir, story, worktree string) int) int {
	if epicDir != "" && story != "" {
		return fn(epicDir, story, worktree)
	}
	epics, in := leaderEpics(epicDir)
	if !in {
		return outsideWorkspace(name)
	}
	// On a leader resume/compact, restart a dead watcher for every led epic with an open story before injecting the
	// checkpoint, so the resumed leader is not blind (item 1). session-start cannot block a turn, so a watcher it cannot
	// take over is surfaced as the repair line in the session context.
	if name == "session-start" {
		guardWatchersSessionStart(guardEpics(epicDir), os.Stdout, os.Stderr)
	}
	st := story
	if st == "" {
		st = leaderStory
	}
	for _, ep := range filterLeaderEpics(epics) {
		_ = fn(ep, st, worktree)
	}
	return 0
}

// guardWatchersSessionStart restarts a dead watcher for every led epic with an open story when the leader session
// resumes or compacts (item 1). Unlike the Stop guard it cannot block, so a watcher it cannot take over (a wedged one,
// or a launch error) is surfaced as the repair line in the session context (out); a restart prints a note to errw.
func guardWatchersSessionStart(epics []string, out, errw io.Writer) {
	for _, ep := range epics {
		if watcherHealthy(ep, time.Now()) {
			continue
		}
		if err := launchWatcher(ep); err == nil {
			fmt.Fprintf(errw, "cox: watcher for %s was not alive; restarted it (cox watch --epic %s)\n", filepath.Base(ep), ep)
			continue
		}
		fmt.Fprintf(out, "Watcher for %s is not alive; run: cox watch --epic %s --replace\n", filepath.Base(ep), ep)
	}
}

// orcaDoorbellRe matches Orca's terminal doorbell prompt ("You have N orchestration messages. Run `orca orchestration
// check` ..."). When that prompt fires but cox has no wake pending, the mail was already drained into the wake queue and
// acked by the watcher, so the doorbell is stale and there is nothing for the leader to do.
var orcaDoorbellRe = regexp.MustCompile(`^You have [0-9]+ orchestration messages?\. Run .orca orchestration check`)

// hookPromptDrain (UserPromptSubmit): attach the unacked wakes of every active epic in the workspace to the leader's
// turn as context. stdout becomes context; it never acks (the leader's turn drains explicitly). When no epic has a wake
// and the submitted prompt is Orca's empty doorbell, it exits 2 to block the prompt (a UserPromptSubmit exit 2 erases
// the prompt and starts no turn; stderr is shown to the user) so the stale bell does not cost the leader a turn.
// Outside a workspace it prints one line and exits 0.
func hookPromptDrain(epicDir, harnessName string, reopenTurn bool) int {
	epics, in := leaderEpics(epicDir)
	if !in {
		return outsideWorkspace("prompt-drain")
	}
	// The turn-boundary block budget is NOT reset by a prompt (firstmate: only positive watcher recovery clears the
	// budget, the failure notice and the alarm together; supersedes ADR 0014's per-turn reset). --reopen stays accepted
	// for the Pi extension that passes it.
	_ = reopenTurn
	return runPromptDrainAll(filterLeaderEpics(epics), harnessName, os.Stdin, os.Stdout, os.Stderr)
}

// filterLeaderEpics drops any epic this terminal is demonstrably not the leader of, so a worker worktree that happens to
// carry the leader epic's settings never drains the leader's wake queue.
func filterLeaderEpics(epics []string) []string {
	out := make([]string, 0, len(epics))
	for _, ep := range epics {
		if !notLeaderTerminal(ep) {
			out = append(out, ep)
		}
	}
	return out
}

// codexBlock emits codex's stdout block decision, which continues/reopens a turn (Stop) or discards a stale prompt
// (UserPromptSubmit). Claude uses exit 2 for the same effect; codex reads this JSON instead (verified shapes, codex-cli
// 0.154, docs/adapters/codex.md).
func codexBlock(out io.Writer, reason string) {
	_ = json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": reason})
}

// notLeaderTerminal reports whether this terminal is demonstrably NOT the epic's leader. It is the negation of
// leaderTerminal, whose rebind side effect keeps a restarted leader from orphaning the epic (finding 4).
func notLeaderTerminal(epicDir string) bool {
	isLeader, _ := leaderTerminal(epicDir)
	return !isLeader
}

// leaderTerminal reports whether THIS terminal is the epic's leader, and whether it re-bound <epic>/.cox/leader in the
// process. The leader is identified by what survives a harness restart, not by one Orca pty handle:
//   - .cox/leader absent  -> leader unknown, treat this terminal as leader (keep today's behavior).
//   - recorded handle == this terminal's ORCA_TERMINAL_HANDLE -> leader, no rebind.
//   - recorded handle differs but is no longer live (Backend.Probe) AND this terminal runs in the epic's workspace ->
//     this terminal is the leader; re-bind .cox/leader to ORCA_TERMINAL_HANDLE so the watcher rings it next tick.
//   - otherwise -> not the leader.
//
// With no live backend (no run) or no ORCA_TERMINAL_HANDLE, liveness cannot be probed, so it falls back to plain handle
// equality and never re-binds. prompt-drain, stop-rewake and session-start all reach this through filterLeaderEpics /
// notLeaderTerminal, so a restarted leader re-binds on its first turn (finding 4).
func leaderTerminal(epicDir string) (isLeader bool, rebound bool) {
	recorded := readLeader(epicDir)
	if recorded == "" {
		return true, false // leader unknown; do not gate the hooks out
	}
	handle := strings.TrimSpace(os.Getenv("ORCA_TERMINAL_HANDLE"))
	if handle != "" && handle == recorded {
		return true, false
	}
	if handle == "" {
		return false, false // no handle to identify or re-bind to
	}
	live, canProbe := probeLeaderHandle(epicDir, recorded)
	if !canProbe {
		return false, false // no backend (no run): equality only, and equality already failed above
	}
	if live {
		return false, false // a different, still-live leader owns the epic; do not steal it
	}
	if !sameWorkspaceAsEpic(epicDir) {
		return false, false // recorded handle is dead, but we are not in the epic's workspace
	}
	// Restart case: the recorded leader handle died and this terminal leads the epic's workspace. Re-bind to it.
	_ = state.WriteLeader(epicDir, handle)
	return true, true
}

// probeLeaderHandle reports whether the epic's recorded leader handle is live (Probe == Alive) and whether it could be
// probed at all (canProbe=false when there is no live backend, i.e. no run). It is a package var so a test can inject a
// fake prober without a real Orca. A probe error or any non-Alive liveness counts as not live.
var probeLeaderHandle = func(epicDir, handle string) (live bool, canProbe bool) {
	b, _ := newBackend(epicDir)
	if b == nil {
		return false, false
	}
	l, err := b.Probe(backend.Session{Kind: "orca", Handle: handle})
	return err == nil && l == backend.Alive, true
}

// sameWorkspaceAsEpic reports whether this terminal's cwd and the epic dir resolve to the same workspace root (the
// nearest ancestor holding cox/workspace.json). This is the restart-surviving identity: a leader that restarts is still
// running in the same workspace even though Orca hands it a new pty handle.
func sameWorkspaceAsEpic(epicDir string) bool {
	cwdWs, err1 := findWorkspaceRoot(".")
	epicWs, err2 := findWorkspaceRoot(epicDir)
	return err1 == nil && err2 == nil && cwdWs == epicWs
}

// runPromptDrain drains one epic's unacked wakes and attaches them as context; it is the single-epic core. It never
// acks (peek) and returns the wakes it printed so the caller can decide the empty-doorbell suppression across all epics.
func runPromptDrain(epicDir string, out io.Writer) int {
	wakes, err := wake.Drain(epicDir, true)
	if err != nil || len(wakes) == 0 {
		return 0
	}
	// Name the epic dir, not just the slug: the leader acks with `--epic <dir>`, and a slug alone made a Pi leader guess
	// the workspace root, so its ack landed nowhere and the wakes came back (dogfood F-8).
	abs, err := filepath.Abs(epicDir)
	if err != nil {
		abs = epicDir
	}
	fmt.Fprintf(out, "Watcher wakes for %s (--epic %s) since your last turn:\n", filepath.Base(epicDir), abs)
	printWakesTo(out, wakes)
	return len(wakes)
}

// runPromptDrainAll attaches the unacked wakes of every epic to the leader's turn. When no epic has a wake and the
// submitted prompt is Orca's stale doorbell, it suppresses the prompt (exit 2 for claude, a stdout block decision for
// codex) so the bell costs no turn; every other empty prompt is a silent exit 0.
func runPromptDrainAll(epics []string, harnessName string, in io.Reader, out, errW io.Writer) int {
	prompt := hookPrompt(in)
	total := 0
	for _, ep := range epics {
		total += runPromptDrain(ep, out)
	}
	total += warnDuplicateLeaders(epics, out)
	if total == 0 {
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
	}
	return 0
}

// warnDuplicateLeaders prints one warning line (to the leader's turn context) per workspace where more than one
// connected leader-harness terminal runs in the workspace root: two leaders drive one epic, so a wake may reach the
// wrong one (item 4). It returns the number of warnings printed, so a warning keeps the turn from being suppressed as an
// empty doorbell. Deduped by workspace root, since several epics share one leader.
func warnDuplicateLeaders(epics []string, out io.Writer) int {
	seen := map[string]bool{}
	warned := 0
	for _, ep := range epics {
		wsRoot, err := findWorkspaceRoot(ep)
		if err != nil || seen[wsRoot] {
			continue
		}
		seen[wsRoot] = true
		if handles := duplicateLeaderHandles(ep, wsRoot); len(handles) > 1 {
			fmt.Fprintf(out, "cox: duplicate leader - %d connected leader terminals in %s (%s); keep .cox/leader %s and close the rest\n",
				len(handles), wsRoot, strings.Join(handles, ", "), orNone(readLeader(ep)))
			warned++
		}
	}
	return warned
}

// hookStdinWait bounds the wait for the hook envelope on stdin. A harness writes the envelope at spawn, so it is there
// at once; a caller that leaves stdin an open pipe it never writes or closes (the Pi extension's execFile, dogfood F-4)
// must not hang the hook. Past the bound the hook proceeds with no prompt, which costs only the stale-doorbell
// suppression (that needs the envelope anyway). A var so a test can shorten it.
var hookStdinWait = time.Second

// hookPrompt reads the UserPromptSubmit hook stdin JSON and returns its `prompt` field, or "" when stdin is empty, a
// terminal, not the hook envelope, or silent past hookStdinWait (so a manual `cox hook prompt-drain` is harmless and an
// open, never-closed stdin pipe never blocks the hook).
func hookPrompt(in io.Reader) string {
	var p struct {
		Prompt string `json:"prompt"`
	}
	if json.Unmarshal(hookStdin(in), &p) == nil {
		return p.Prompt
	}
	return ""
}

// hookStdin reads a hook envelope from stdin, or nil when stdin is absent, a terminal, empty, or silent past
// hookStdinWait (an open, never-closed pipe never blocks the hook).
func hookStdin(in io.Reader) []byte {
	if in == nil {
		return nil
	}
	if f, ok := in.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return nil // an interactive terminal: no envelope will ever arrive
		}
	}
	got := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(in, 1<<20))
		got <- b
	}()
	select {
	case b := <-got:
		return b
	case <-time.After(hookStdinWait):
		return nil // ponytail: the reader goroutine is abandoned; the hook process exits right after
	}
}

// hookStopRewake (Stop, asyncRewake): while the leader is idle, wait for a watcher wake in the durable queue, then exit
// 2 to open a new turn without typing into the composer (v1 bin/hook-stop-rewake.sh, patched in M0). One waiter per
// terminal (a lock file); an urgent wake rewakes at once, routine wakes (status/stale/quiet/unknown_probe) batch up to
// WAKE_BATCH so several cost one turn; on MAX_WAIT with no wake, a still-open dispatched story ticks (exit 2) so the
// next turn re-arms the waiter, otherwise the leader stays idle (exit 0).
func hookStopRewake(epicDir, harnessName string, runGuard bool) int {
	epics, in := leaderEpics(epicDir)
	if !in {
		return outsideWorkspace("stop-rewake")
	}
	epics = filterLeaderEpics(epics)
	// The guard set (open-story epics regardless of watcher liveness) is computed before the single-waiter lock and the
	// len(epics)==0 return, so a dead-watcher epic - which activeEpics hides - is still guarded (item 1).
	var guard []string
	if runGuard {
		guard = guardEpics(epicDir)
	}
	handle := os.Getenv("ORCA_TERMINAL_HANDLE")
	payload := readStopPayload(os.Stdin)
	if handle != "" {
		lock := filepath.Join(tmpDir(), "cox-rewake-"+handle+".lock")
		if rewakeWaiterAlive(lock, guard) {
			return 0 // another waiter already runs for this terminal
		}
		if err := os.WriteFile(lock, []byte(fmt.Sprintf("%d\n%s\n", os.Getpid(), identityOf(os.Getpid()))), 0o644); err == nil {
			defer func() {
				if pid, _, _ := lockHolder(lock); pid == os.Getpid() {
					_ = os.Remove(lock)
				}
			}()
		}
	}
	return runStopRewake(rewakeCfg{
		epics:      epics,
		guardEpics: guard,
		harness:    harnessName,
		maxWait:    envSeconds("REWAKE_MAX_WAIT", 3300),
		batchMax:   envSeconds("WAKE_BATCH", 300),
		poll:       15 * time.Second,
		out:        os.Stderr,
		stdout:     os.Stdout,
		sleep:      time.Sleep,
		session:    nonEmpty(payload.SessionID, nonEmpty(handle, "unknown")),
		stopActive: payload.StopHookActive,
	})
}

// stopPayload is the Stop hook envelope fields the guard reads: the session the block budget is keyed on and the loop
// guard (firstmate reads `stopHookActive` first when it is a boolean, else `stop_hook_active`).
type stopPayload struct {
	SessionID      string
	StopHookActive bool
}

// readStopPayload reads the Stop envelope from stdin, bounded like hookPrompt; an absent or malformed envelope reads as
// the zero payload (no loop guard, the terminal handle as the session).
func readStopPayload(in io.Reader) stopPayload {
	raw := hookStdin(in)
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return stopPayload{}
	}
	var p stopPayload
	p.SessionID, _ = m["session_id"].(string)
	if v, ok := m["stopHookActive"].(bool); ok {
		p.StopHookActive = v
	} else if v, ok := m["stop_hook_active"].(bool); ok {
		p.StopHookActive = v
	}
	return p
}

// rewakeCfg is the stop-rewake loop's inputs, injected so the loop is unit-tested without real waiting (sleep is a
// no-op in tests; maxWait/batchMax/poll are durations, not env reads). epics is every active epic the leader waits on.
type rewakeCfg struct {
	epics      []string
	guardEpics []string // epics with open stories to run the turn-boundary watcher guard over, regardless of watcher liveness (item 1)
	harness    string   // "codex" reopens via a stdout block decision; anything else (claude) reopens via exit 2
	maxWait    time.Duration
	batchMax   time.Duration
	poll       time.Duration
	out        io.Writer // reopen/tick text sink for the exit-2 path (stderr)
	stdout     io.Writer // codex block-decision sink (stdout); defaults handled by the caller
	sleep      func(time.Duration)
	// launch restarts a dead epic watcher (the startWatcher-equivalent). nil => launchWatcher; a test injects a stub so
	// no real process is spawned and the refused path is exercised (item 1).
	launch func(epicDir string) error
	// session keys the Claude block budget (the Stop payload's session_id, else the terminal handle); "" = "unknown".
	session string
	// stopActive is the Stop payload's loop guard: in the default (codex, pi) mode a true value allows the stop.
	stopActive bool
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

// runStopRewake is the testable core: first guard every led epic's watcher (item 1), then peek every led epic's wake
// queue each poll up to maxWait and decide the tick.
func runStopRewake(cfg rewakeCfg) int {
	if code, proceed := cfg.guardWatchers(); !proceed {
		return code
	}
	if len(cfg.epics) == 0 {
		return 0 // nothing active to wait on
	}
	poll := cfg.poll
	if poll <= 0 {
		poll = 15 * time.Second
	}
	batch := time.Duration(0)
	for elapsed := time.Duration(0); elapsed < cfg.maxWait; elapsed += poll {
		wakes := drainAll(cfg.epics)
		if len(wakes) > 0 {
			if anyUrgent(wakes) || batch >= cfg.batchMax {
				msg := "Watcher wake while idle. Run `" + drainHint(cfg.epics) + "`, handle them, then ack-through:\n" + wakesText(wakes)
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
	// nothing open, stay silent (exit 0) so there is no idle churn between epics. Pi's extension re-arms its own waiter
	// on exit 0, so a tick would only cost it a model turn: exit 0.
	if cfg.harness == "pi" {
		return 0
	}
	open := 0
	for _, ep := range cfg.epics {
		if o, err := watch.OpenStories(ep); err == nil {
			open += len(o)
		}
	}
	if open > 0 {
		msg := fmt.Sprintf("Rewake tick: no watcher wake in %s and %d dispatched story(ies) still open. Nothing to handle - end this turn with one line and no tool calls so the Stop hook re-arms the waiter.\n", cfg.maxWait, open)
		return cfg.reopen(msg)
	}
	return 0
}

// guardWatchers is the turn-boundary guard (item 1, firstmate bin/fm-turnend-guard.sh + bin/fm-claude-stop-autoarm.sh):
// before the leader waits, every led epic that needs supervision must have a live, identity-matched watcher with a
// fresh beacon, or the turn would end blind. Claude (the default harness) runs firstmate's --claude contract per epic:
// the Stop auto-arm (restart, generation ledger, one failure notice) and then the cooperative guard (bounded block
// budget, the one verified attended fail-open). Codex and Pi run firstmate's default mode: the restart, then a block
// that a loop-guarded retry (stop_hook_active) always allows, so a turn is forced at most once. It returns
// (exit code, proceed): proceed=true means run the wait loop.
func (cfg rewakeCfg) guardWatchers() (code int, proceed bool) {
	claude := cfg.harness != "codex" && cfg.harness != "pi"
	session := nonEmpty(cfg.session, "unknown")
	var text strings.Builder
	var sys bytes.Buffer
	reopen, failOpen, loopGuarded := false, false, false
	for _, ep := range cfg.guardEpics {
		if watcherHealthy(ep, time.Now()) {
			if claude && !failureEpisodeReset(ep, false) {
				reopen = true // reset contention: continue silently, the episode state is preserved for the retry
			}
			continue
		}
		open := 0
		if o, err := watch.OpenStories(ep); err == nil {
			open = len(o)
		}
		if !claude {
			if cfg.launchWatcher(ep) == nil || watcherHealthy(ep, time.Now()) {
				continue
			}
			if cfg.stopActive {
				loopGuarded = true // the loop-guarded retry: never block twice in one turn; this Stop ends here
				continue
			}
			text.WriteString(blockText(ep, open, false))
			reopen = true
			continue
		}
		armCode, healthy := runClaudeAutoarm(ep, cfg.launchWatcher, &text)
		if healthy {
			reopen = reopen || armCode == 2
			continue
		}
		before := sys.Len()
		guardCode := runClaudeGuard(ep, session, open, &text, &sys)
		if sys.Len() > before && guardCode == 0 {
			failOpen = true // the one attended fail-open ends this turn; no automatic continuation defeats it
			continue
		}
		reopen = reopen || armCode == 2 || guardCode == 2
	}
	if sys.Len() > 0 {
		_, _ = cfg.stdout.Write(sys.Bytes())
	}
	if reopen {
		return cfg.reopen(text.String()), false
	}
	if failOpen || loopGuarded {
		return 0, false
	}
	return 0, true
}

// launchWatcher restarts a dead epic watcher the way dispatch's startWatcher does (detached `cox watch --epic <dir>`),
// returning an error when it cannot take over so the guard reopens with the repair line. A watcher pid that is still
// alive (a wedged watcher whose lasttick is stale) is NOT killed here - the auto-restart never runs --replace - so it is
// reported as a refusal and the leader is told to --replace it. cfg.launch overrides it in tests.
func (cfg rewakeCfg) launchWatcher(epicDir string) error {
	if cfg.launch != nil {
		return cfg.launch(epicDir)
	}
	return launchWatcher(epicDir)
}

// watcherConfirmWait bounds how long a restart waits for the new watcher's first fresh lasttick. A watcher ticks right
// after it starts (one pass: Orca probes), so a healthy restart confirms within a few seconds; a watcher that exits at
// once (no backend, a broken binary) is caught when it is reaped. A var so a test can shorten it.
var watcherConfirmWait = 10 * time.Second

// watcherExe is the binary a restart runs (`<self> watch --epic <dir>`). A var so a test can point it at a stub.
var watcherExe = os.Executable

// launchWatcher starts `cox watch --epic <dir>` detached and reports success only once the new watcher is CONFIRMED:
// its lasttick is written after the launch while it is still running. A watcher that exits at once - a fresh epic with
// no .cox/run, "watch needs a live backend" - or never ticks within watcherConfirmWait is an error, so the guard takes
// the repair/exit-2 path instead of reporting "restarted" and re-arming into the same dead restart (dogfood F-10).
func launchWatcher(epicDir string) error {
	if pid := readPid(watchPidPath(epicDir)); pid > 0 && processAlive(pid) {
		return fmt.Errorf("watcher pid %d is alive but not ticking; run cox watch --epic %s --replace", pid, epicDir)
	}
	self, err := watcherExe()
	if err != nil {
		self = "cox"
	}
	started := time.Now()
	cmd := exec.Command(self, "watch", "--epic", epicDir)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	tick := filepath.Join(epicDir, controlDir, "watch", "lasttick")
	deadline := time.After(watcherConfirmWait)
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("restarted watcher exited at once (%v); run cox watch --epic %s --replace to see why", err, epicDir)
		case <-deadline:
			return fmt.Errorf("restarted watcher did not tick within %s; run cox watch --epic %s --replace", watcherConfirmWait, epicDir)
		case <-time.After(100 * time.Millisecond):
			// ponytail: mtime granularity is 1s on some filesystems, so "after the launch" compares whole seconds.
			if info, err := os.Stat(tick); err == nil && !info.ModTime().Before(started.Truncate(time.Second)) {
				return nil // confirmed: the new watcher completed a pass after the launch and is still running
			}
		}
	}
}

// watcherHealthy is firstmate's PID-strict fm_watcher_healthy: watch.pid names a live process whose identity matches
// the .cox/watch.identity sidecar (an identityless or reused pid is not a watcher) and watch/lasttick is younger than
// the poll-derived grace max(300s, poll+60s) (fm_poll_derived_grace; supersedes ADR 0014's 3 x poll). A live watcher
// whose beacon has gone stale is wedged, exactly as a dead one is.
func watcherHealthy(epicDir string, now time.Time) bool { return watch.Healthy(epicDir, now, 0) }

// rewakeRepairMsg is the reopen text naming every blocked epic and the exact repair command.
func rewakeRepairMsg(blocked []string) string {
	var b strings.Builder
	for _, ep := range blocked {
		fmt.Fprintf(&b, "Watcher for %s is not alive; run: cox watch --epic %s --replace\n", filepath.Base(ep), ep)
	}
	return b.String()
}

// drainAll peeks every epic's unacked wakes and returns them merged (each wake carries its own epic name). A per-epic
// read error is skipped so one broken queue does not stop the leader waiting on the others.
func drainAll(epics []string) []wake.Wake {
	var all []wake.Wake
	for _, ep := range epics {
		if w, err := wake.Drain(ep, true); err == nil {
			all = append(all, w...)
		}
	}
	return all
}

// drainHint renders the `cox wake drain` command the reopen message points at: the exact --epic form for a single led
// epic, or a bare `cox wake drain --epic <each>` list when several are active.
func drainHint(epics []string) string {
	if len(epics) == 1 {
		return "cox wake drain --epic " + epics[0]
	}
	parts := make([]string, len(epics))
	for i, ep := range epics {
		parts[i] = "cox wake drain --epic " + ep
	}
	return strings.Join(parts, "; ")
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

// rewakeWaiterAlive reports whether the single-waiter lock names a live waiter still owning this terminal's wait: its
// pid is alive, the identity it recorded recomputes and matches (a reused or identityless pid is not a waiter), and it
// is not stuck - the lock and every guarded epic's beacon older than the grace prove a waiter hung over a dead watcher
// (firstmate fm_autoarm_claim_open). Anything else is reclaimed by this Stop, never trusted blind.
func rewakeWaiterAlive(lock string, guard []string) bool {
	pid, identity, _, ok := lockRecord(lock) // the waiter lock records "pid\nidentity"
	if !ok || pid <= 0 || !processAlive(pid) || identity == "" || identityOf(pid) != identity {
		return false
	}
	if pathAge(lock) < watch.DefaultGrace {
		return true
	}
	for _, ep := range guard {
		if pathAge(beaconPath(ep)) < watch.DefaultGrace {
			return true
		}
	}
	return len(guard) == 0
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
	if story == leaderStory {
		if _, err := os.Stat(checkpoint.Path(epicDir, story)); os.IsNotExist(err) {
			// A leader with no saved checkpoint has nothing to recover and no story to "start from": print nothing, so
			// a harness that turns session-start output into a message never gives a fresh leader an extra turn (F-6).
			return 0
		}
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
