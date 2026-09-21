// Package orca is the default Backend adapter (decision 0002). It wraps the exact `orca` CLI commands v1 uses in
// bin/, always with --json, parses each response into a typed struct, and returns a real error whenever Orca reports
// one - it never swallows a failure onto the data path (F02, the root cause 0001 cites). Observed CLI behavior is
// recorded in docs/adapters/orca.md.
//
// The command runner is injectable so the parsing and liveness mapping are unit-tested without Orca; live contract
// tests run behind the `orca` build tag.
package orca

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// Client is an Orca backend bound to one orchestration run (needed for worker and mailbox operations). Plane selects
// how the client drives Orca (ADR 0012): "orchestration" (default) uses task-create/worker-start and the orchestration
// mailbox; "terminal" uses worktree + terminal only, and cox owns the whole handoff.
type Client struct {
	Run            string
	From           string // optional --from handle for mailbox sends
	Plane          string // "orchestration" | "terminal"; "" behaves as orchestration
	Epic           string // epic dir, so ringReady/Composer can consult the harness-owned busy record before the UI signal (DESIGN wave-3)
	LaunchConfirmS int    // terminal-plane spawn confirm window in seconds (policy backend.orca.launch_confirm_s); 0 => 60
	run            func(args ...string) ([]byte, error)
	git            func(args ...string) ([]byte, error)
	confirmPoll    time.Duration // poll interval for confirmLaunch; 0 => 1s (tests set a small value)
}

// New returns a Client that shells out to the real `orca` and `git` binaries.
func New(runID string) *Client {
	return &Client{Run: runID, run: execOrca, git: execGit}
}

// terminalPlane reports whether this client drives Orca at the terminal plane (ADR 0012).
func (c *Client) terminalPlane() bool { return c.Plane == "terminal" }

func execOrca(args ...string) ([]byte, error) {
	return exec.Command("orca", args...).Output()
}

func execGit(args ...string) ([]byte, error) {
	return exec.Command("git", args...).Output()
}

// envelope is the common `orca ... --json` response shape.
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
}

// call runs an orca command and returns its result payload, or an error if the process failed, the output was not
// valid JSON, or Orca reported ok=false. The raw JSON is never assumed valid; a parse failure is a real error.
func (c *Client) call(args ...string) (json.RawMessage, error) {
	out, err := c.run(args...)
	if err != nil {
		return nil, fmt.Errorf("orca %s: %w", strings.Join(args, " "), err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("orca %s: invalid JSON: %w", strings.Join(args, " "), err)
	}
	if !env.OK {
		msg := env.Error.Message
		if msg == "" {
			msg = "ok=false with no error message"
		}
		return nil, fmt.Errorf("orca %s: %s", strings.Join(args, " "), msg)
	}
	return env.Result, nil
}

// mutate runs an Orca mutation that requires the coordinator terminal to be bound to this client's run. A coordinator
// terminal binds one run at a time (run-use); if it is bound elsewhere - e.g. a live E2E rebound the leader terminal
// to another run - the mutation fails with consumer_fenced. mutate reads the current binding, rebinds to c.Run only
// when it differs, runs the mutation, then restores the previous binding so the leader's terminal is left as it was.
// Reads never call this and never pay the extra round-trips. It guards the four mutations the brief names: task-create,
// worker-start, worker-stop, and reply.
//
// ponytail: the rebind is best-effort and racy if something else rebinds the same terminal concurrently while cox runs;
// cox runs synchronously in that terminal, so within one command the binding is stable. Recorded in docs/adapters/orca.md.
func (c *Client) mutate(args ...string) (json.RawMessage, error) {
	restore, err := c.ensureBound()
	if err != nil {
		return nil, err
	}
	if restore != nil {
		defer restore()
	}
	return c.call(args...)
}

// ensureBound rebinds the coordinator terminal to c.Run when it is bound elsewhere, returning a restore func that
// rebinds it back to the previous run (nil when no rebind happened or there was nothing to restore to). A blank c.Run
// (no run configured) is a no-op.
func (c *Client) ensureBound() (func(), error) {
	if c.terminalPlane() {
		return nil, nil // no run binding on the terminal plane (ADR 0012 B5): no orchestration mutation runs
	}
	if c.Run == "" {
		return nil, nil
	}
	cur := c.currentRun()
	if cur == c.Run {
		return nil, nil // already bound to our run
	}
	if _, err := c.call("orchestration", "run-use", "--id", c.Run, "--json"); err != nil {
		return nil, fmt.Errorf("bind coordinator terminal to run %s: %w", c.Run, err)
	}
	if cur == "" {
		return nil, nil // the terminal was unbound; leave it bound to our run
	}
	return func() { _, _ = c.call("orchestration", "run-use", "--id", cur, "--json") }, nil
}

// currentRun returns the run id the coordinator terminal is bound to, or "" when it is unbound or the read fails
// (an unreadable binding is treated as "not ours", so mutate rebinds rather than assuming it is already correct).
func (c *Client) currentRun() string {
	res, err := c.call("orchestration", "run-current", "--json")
	if err != nil {
		return ""
	}
	var r struct {
		Run *struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(res, &r); err != nil || r.Run == nil {
		return ""
	}
	return r.Run.ID
}

// WorktreeCreate creates an Orca-managed worktree for a story branch cut from base, and returns the path and branch
// Orca reports. It never falls back (F02): a failed create is returned as an error, and worktree.Ensure re-verifies
// the branch on disk before any worker is placed. The repo is addressed by name for a registered repo, or by path for
// a repo Orca has not named (on this machine `orca repo show --repo path:<p>` returns name:null for the coxswain repo);
// see repoSelector.
func (c *Client) WorktreeCreate(repo, branch, base string) (backend.Worktree, error) {
	res, err := c.call("worktree", "create",
		"--repo", repoSelector(repo),
		"--name", branch,
		"--base-branch", base,
		"--no-parent", "--json")
	if err != nil {
		return backend.Worktree{}, err
	}
	var r struct {
		Worktree struct {
			Path   string `json:"path"`
			Branch string `json:"branch"`
		} `json:"worktree"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return backend.Worktree{}, fmt.Errorf("parse worktree create result: %w", err)
	}
	path := r.Worktree.Path
	// Orca derives the branch name from the worktree name: it prefixes the repo's git username and flattens slashes,
	// so a requested `epic/e2e-1` becomes `nphattai/epic-e2e-1` (observed live; docs/adapters/orca.md). The rest of cox
	// addresses branches by their canonical name (epic/<slug>, story/<id>), so put the worktree on the canonical name.
	// If that branch does not exist yet, rename the mangled branch to it. If it already exists (a parked story whose
	// worktree was removed without deleting the branch, F01, then resumed into a fresh worktree), git branch -m refuses
	// (exit 128), so switch to the existing branch instead. Either way worktree.Ensure re-verifies the result.
	// ponytail: leaving the orphaned mangled branch behind is the ceiling - never delete it (F01); operators prune the
	// nphattai/<...> leftovers by hand (docs/adapters/orca.md).
	if path != "" && branch != "" {
		if cur, err := c.git("-C", path, "branch", "--show-current"); err == nil {
			if strings.TrimSpace(string(cur)) != branch {
				if _, err := c.git("-C", path, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
					if _, err := c.git("-C", path, "switch", branch); err != nil {
						return backend.Worktree{}, fmt.Errorf("switch worktree to existing branch %s: %w", branch, err)
					}
					// A switch lands on the existing branch at its OLD HEAD (M5: the adversary reviewed d770a2a while the
					// pack said cd1e383). Bring the branch to the requested base, but only when it carries no unmerged work
					// (A11): reset --hard is safe when base..branch is empty; otherwise refuse rather than discard commits.
					if err := c.resetReusedBranch(path, branch, base); err != nil {
						return backend.Worktree{}, err
					}
				} else if _, err := c.git("-C", path, "branch", "-m", branch); err != nil {
					return backend.Worktree{}, fmt.Errorf("rename worktree branch to %s: %w", branch, err)
				}
			}
		}
	}
	return backend.Worktree{Path: path, Branch: branch}, nil
}

// resetReusedBranch brings a just-switched-onto existing branch to base. It resets --hard to base only when the branch
// has no commits missing from base (base..branch is empty), so a stale reused branch is refreshed to the pack's HEAD
// without ever discarding unmerged work; a branch that is ahead of base is refused with guidance (F01: the branch is
// never deleted here either).
func (c *Client) resetReusedBranch(path, branch, base string) error {
	if base == "" {
		return nil // no base to reset to
	}
	ahead, err := c.git("-C", path, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return fmt.Errorf("check unmerged commits on %s vs base %s: %w", branch, base, err)
	}
	if n := strings.TrimSpace(string(ahead)); n != "0" {
		return fmt.Errorf("existing branch %s has %s commit(s) not in base %s; not resetting (would discard work) - merge or remove the branch, or remove its worktree, then retry", branch, n, base)
	}
	if _, err := c.git("-C", path, "reset", "--hard", base); err != nil {
		return fmt.Errorf("reset reused branch %s to base %s: %w", branch, base, err)
	}
	return nil
}

// WorktreeRemove detaches the worktree from its branch, then asks Orca to remove it. `orca worktree rm` deletes the
// checked-out branch, but with HEAD detached there is no checked-out branch to delete, so the branch survives - this is
// v1's proven sequence (bin/epic-close.sh close_wt) and satisfies the never-delete-a-branch contract (F01). If the
// detach fails, `rm` is NOT called: removing a still-attached worktree is exactly what would destroy the branch.
func (c *Client) WorktreeRemove(wt backend.Worktree) error {
	if wt.Path == "" {
		return fmt.Errorf("orca WorktreeRemove: empty worktree path")
	}
	if _, err := c.git("-C", wt.Path, "switch", "--detach"); err != nil {
		return fmt.Errorf("orca WorktreeRemove: detach %s before rm (rm skipped to protect the branch, F01): %w", wt.Path, err)
	}
	_, err := c.call("worktree", "rm", "--worktree", "path:"+wt.Path, "--force", "--json")
	return err
}

// Spawn creates a task from the brief and starts one supervised worker in the worktree. The returned Session carries
// the dispatch id used by Probe/Stop/Send.
func (c *Client) Spawn(wt backend.Worktree, h backend.HarnessSpec, brief backend.Brief) (backend.Session, error) {
	if c.terminalPlane() {
		return c.spawnTerminal(wt, h, brief)
	}
	if c.Run == "" {
		return backend.Session{}, fmt.Errorf("orca Spawn: no run id configured")
	}
	spec := brief.Text
	if spec == "" {
		spec = backend.PromptFromBrief(brief)
	}
	taskRes, err := c.mutate("orchestration", "task-create", "--run", c.Run, "--spec", spec, "--json")
	if err != nil {
		return backend.Session{}, err
	}
	var tr struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(taskRes, &tr); err != nil {
		return backend.Session{}, fmt.Errorf("parse task-create result: %w", err)
	}
	args := []string{"orchestration", "worker-start", "--run", c.Run, "--task", tr.Task.ID,
		"--worktree", "path:" + wt.Path, "--agent", h.Name}
	if h.Model != "" {
		args = append(args, "--model", h.Model)
	}
	if h.Effort != "" {
		args = append(args, "--effort", h.Effort)
	}
	args = append(args, "--json")
	// worker-start only acknowledges the request (result is {stage:"input_accepted"}); it does NOT return the dispatch
	// id or terminal handle. Resolve them the way v1 does (bin/dispatch.sh, bin/interrupt.sh).
	if _, err := c.mutate(args...); err != nil {
		return backend.Session{}, err
	}
	dispatchID, err := c.dispatchForTask(tr.Task.ID)
	if err != nil {
		return backend.Session{}, err
	}
	return backend.Session{Kind: "orca", ID: dispatchID, Handle: c.terminalHandle(dispatchID), Story: backend.StoryFromPath(brief.StoryPath)}, nil
}

// workerRow is one entry of `orchestration worker-list --json` .result.workers[]. workerState is the dispatch's own
// state (ready | running | succeeded | failed | abandoned); agentTerminalHandle is its terminal.
type workerRow struct {
	DispatchID string `json:"dispatchId"`
	TaskID     string `json:"taskId"`
	State      string `json:"workerState"`
	Handle     string `json:"agentTerminalHandle"`
}

// workerRows lists the run's workers. A missing run id is a real error (the listing is run-scoped).
func (c *Client) workerRows() ([]workerRow, error) {
	if c.Run == "" {
		return nil, fmt.Errorf("orca worker-list: no run id configured")
	}
	res, err := c.call("orchestration", "worker-list", "--run", c.Run, "--json")
	if err != nil {
		return nil, err
	}
	var r struct {
		Workers []workerRow `json:"workers"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, fmt.Errorf("parse worker-list result: %w", err)
	}
	return r.Workers, nil
}

// WorkerList reports every dispatch in the run with its state and terminal handle. cox migrate uses it to keep a
// session only for a dispatch Orca still reports as ready|running.
func (c *Client) WorkerList() ([]backend.Worker, error) {
	rows, err := c.workerRows()
	if err != nil {
		return nil, err
	}
	out := make([]backend.Worker, 0, len(rows))
	for _, w := range rows {
		out = append(out, backend.Worker{Dispatch: w.DispatchID, State: w.State, Handle: w.Handle})
	}
	return out, nil
}

// Terminals lists Orca's terminals (`terminal list`, run-independent) so the duplicate-leader check can count the
// connected leader-harness terminals in a workspace root. It reads the fields observed live on Orca 1.4.197: handle,
// worktreePath, connected, and agentIdentity (absent when the terminal runs no agent). Recorded as a ceiling in
// docs/adapters/orca.md - re-verify the shape on an Orca upgrade.
func (c *Client) Terminals() ([]backend.Terminal, error) {
	res, err := c.call("terminal", "list", "--json")
	if err != nil {
		return nil, err
	}
	var r struct {
		Terminals []struct {
			Handle        string `json:"handle"`
			WorktreePath  string `json:"worktreePath"`
			Connected     bool   `json:"connected"`
			AgentIdentity string `json:"agentIdentity"`
		} `json:"terminals"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, fmt.Errorf("parse terminal list result: %w", err)
	}
	out := make([]backend.Terminal, 0, len(r.Terminals))
	for _, t := range r.Terminals {
		out = append(out, backend.Terminal{Handle: t.Handle, WorktreePath: t.WorktreePath, Harness: t.AgentIdentity, Connected: t.Connected})
	}
	return out, nil
}

// dispatchForTask resolves the dispatch id of a just-started worker by listing the run's workers and taking the last
// entry whose taskId matches (v1 bin/dispatch.sh line 104). A task with no worker is a real error: the caller must not
// end up with an empty dispatch id.
func (c *Client) dispatchForTask(taskID string) (string, error) {
	rows, err := c.workerRows()
	if err != nil {
		return "", err
	}
	dispatch := ""
	for _, w := range rows {
		if w.TaskID == taskID && w.DispatchID != "" {
			dispatch = w.DispatchID // last match wins
		}
	}
	if dispatch == "" {
		return "", fmt.Errorf("orca Spawn: no dispatch found for task %s in run %s", taskID, c.Run)
	}
	return dispatch, nil
}

// terminalHandle looks up the worker's terminal handle via worker-show (v1 bin/interrupt.sh). The handle is best-effort:
// an empty handle or a failed lookup returns "" rather than failing the spawn, since Send/Stop/Probe address the
// dispatch id, not the handle (only Interrupt needs it).
func (c *Client) terminalHandle(dispatchID string) string {
	res, err := c.call("orchestration", "worker-show", "--dispatch", dispatchID, "--json")
	if err != nil {
		return ""
	}
	var r struct {
		Terminal struct {
			Handle string `json:"handle"`
		} `json:"terminal"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return ""
	}
	return r.Terminal.Handle
}

// Send is a doorbell to the worker. When the Session carries a terminal handle it knocks on the terminal the way v1
// does (bin/send.sh -> bin/composer.sh), typing the doorbell only when the composer is empty so it never clobbers a
// half-typed line or a busy turn; it returns rang=true only on a real delivery. Orchestration mail (the old path) is a
// message the worker sees only on its own next check, so it wakes no one; it is used only as a fallback when there is
// no handle to ring, and never counts as a ring.
func (c *Client) Send(s backend.Session, text string) (bool, error) {
	if s.Handle != "" {
		return c.ringTerminal(s.Handle, s.Story, text)
	}
	if c.terminalPlane() {
		return false, nil // no handle to ring and no orchestration mail on this plane; nothing delivered
	}
	args := []string{"orchestration", "send", "--to", "dispatch:" + s.ID,
		"--type", "status", "--subject", "doorbell", "--body", text, "--json"}
	if c.From != "" {
		args = append(args, "--from", c.From)
	}
	_, err := c.call(args...)
	return false, err // mail is not a terminal ring
}

// ringTerminal types the doorbell into the terminal only when the worker is ready to receive it. It consults Orca's
// structured agents[] state first (ringReady), because the text composer classifier does not recognize the codex footer
// and would leave a codex worker un-rung forever (M14). A busy/pending/unknown composer is left alone (rang=false) so
// the watcher defers; a pane blocked on a local permission prompt returns backend.ErrAgentPromptBlocked so the watcher
// escalates instead of deferring silently.
func (c *Client) ringTerminal(handle, story, text string) (bool, error) {
	ready, err := c.ringReady(handle, story)
	if err != nil {
		return false, err // skipped:permission (ErrAgentPromptBlocked), surfaced to the ladder
	}
	if !ready {
		return false, nil // skipped:busy|pending|unknown
	}
	if err := c.typeDoorbell(handle, text); err != nil {
		return false, err
	}
	return true, nil
}

// ringReady decides whether the doorbell may be typed. It consults the harness-owned busy record FIRST (DESIGN wave-3
// item 3): idle -> ready, busy -> not ready; only when the harness reports no state (unknown/absent) does it fall back to
// Orca's structured agents[] state (M14: idle|done -> ready, working -> busy, waiting -> ErrAgentPromptBlocked) and then
// the text composer classification (empty -> ready), which is all the orchestration plane ever uses.
func (c *Client) ringReady(handle, story string) (bool, error) {
	if cs, ok := backend.BusyComposer(c.Epic, story); ok {
		return cs == backend.ComposerEmpty, nil // harness idle -> ring; harness busy -> skip
	}
	if c.terminalPlane() {
		if st, found, err := c.agentStateForHandle(handle); err == nil && found {
			switch strings.ToLower(strings.TrimSpace(st)) {
			case "idle", "done":
				return true, nil
			case "working":
				return false, nil
			case "waiting":
				return false, backend.ErrAgentPromptBlocked
			}
			// unrecognized structured state: fall through to text classification
		}
	}
	return c.composerState(handle) == backend.ComposerEmpty, nil
}

// typeDoorbell types text into the terminal composer and presses enter. It parses the envelope error code so Orca's
// agent_prompt_blocked refusal (the pane is waiting on a local permission prompt) is returned as
// backend.ErrAgentPromptBlocked rather than a generic error, letting the watcher escalate a permission-blocked worker.
func (c *Client) typeDoorbell(handle, text string) error {
	out, runErr := c.run("terminal", "send", "--terminal", handle, "--text", text, "--enter", "--json")
	if len(out) == 0 {
		if runErr != nil {
			return fmt.Errorf("orca terminal send %s: %w", handle, runErr)
		}
		return fmt.Errorf("orca terminal send %s: empty response", handle)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("orca terminal send %s: invalid JSON: %w", handle, err)
	}
	if !env.OK {
		if env.Error.Code == "agent_prompt_blocked" {
			return backend.ErrAgentPromptBlocked
		}
		msg := env.Error.Message
		if msg == "" {
			msg = "ok=false with no error message"
		}
		return fmt.Errorf("orca terminal send %s: %s", handle, msg)
	}
	return nil
}

// Composer exposes the composer classification for the watcher (idle_no_done and blocked detection). A session with no
// handle, or an unreadable terminal, is "unknown", never "empty", so an idle check never fires on doubt. On the terminal
// plane it first consults the structured agents[] state: a "waiting" agent (blocked on a local approval/input prompt it
// cannot answer) is reported as "blocked", which the watcher's blocked pass turns into a stuck wake. Text classification
// (bin/composer.sh) does not recognize a codex approval prompt, so the structured state is the reliable signal. It never
// errors; if the agent state cannot be read it falls back to the text tail. Ringing uses the private composerState, so
// this does not change the doorbell path.
func (c *Client) Composer(s backend.Session) (string, error) {
	// Consult the harness-owned busy record first (DESIGN wave-3 item 3): idle -> empty, busy -> busy. Only when the
	// harness reports no state does it fall back to the structured agents[] state and the text classifier below.
	if cs, ok := backend.BusyComposer(c.Epic, s.Story); ok {
		return cs, nil
	}
	if s.Handle == "" {
		return backend.ComposerUnknown, nil
	}
	if c.terminalPlane() {
		if st, found, err := c.agentStateForHandle(s.Handle); err == nil && found &&
			strings.EqualFold(strings.TrimSpace(st), "waiting") {
			return backend.ComposerBlocked, nil
		}
	}
	return c.composerState(s.Handle), nil
}

// composerState classifies a worker terminal's composer from a bounded `terminal read` tail: empty | pending | busy |
// unknown (a faithful port of v1 bin/composer.sh). A read error is unknown (defer), never a false ring. It passes
// --screen so the tail is the rendered screen rows (the prompt and status line) rather than the raw stream tail, which
// is truncated and classifies as unknown, so the doorbell would never ring an empty composer.
func (c *Client) composerState(handle string) string {
	tail, err := c.screenTail(handle)
	if err != nil {
		return "unknown"
	}
	return classifyComposer(tail)
}

// screenTail reads a terminal's rendered screen rows (the prompt and status line), the same `terminal read --screen`
// call Composer classifies. It is shared with Screen so the blocked-worker dialog capture reads the exact rows the
// composer classifier sees.
func (c *Client) screenTail(handle string) ([]string, error) {
	res, err := c.call("terminal", "read", "--terminal", handle, "--screen", "--json")
	if err != nil {
		return nil, err
	}
	var r struct {
		Terminal struct {
			Tail []string `json:"tail"`
		} `json:"terminal"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	return r.Terminal.Tail, nil
}

// Screen returns the worker terminal's rendered screen rows, so the watcher can put a blocked worker's visible prompt in
// the stuck wake. An empty handle or an unreadable read is an error (the caller omits the dialog, never fails on it).
func (c *Client) Screen(s backend.Session) ([]string, error) {
	if s.Handle == "" {
		return nil, fmt.Errorf("orca Screen: no terminal handle")
	}
	return c.screenTail(s.Handle)
}

var (
	// spinnerRe: a Claude spinner row (glyph, optional dashes, a capitalized word + ellipsis) or a compaction row.
	spinnerRe = regexp.MustCompile(`[✳✶✻✽✢✺✹✸✷·]─*[A-Z][a-z]+…|Compacting conversation`)
	// statusRe: the harness status line (claude model name, context counter, reset-time hint; or the codex status line,
	// which carries "Context NN% used" / "weekly NN% left" - fixture from a live codex run, M14).
	statusRe = regexp.MustCompile(`(Opus|Sonnet|Haiku) [0-9]|\| [0-9]+k \([0-9]+%\)|[0-9]+% → [0-9]{2}:[0-9]{2}|Context [0-9]+% used|weekly [0-9]+% left`)
	// boxRe: a transcript box-drawing/marker row, which sits directly above the status line on an empty composer.
	boxRe = regexp.MustCompile(`^[─╭╰│⎿⏺]`)
)

// isComposerFooter reports whether a row is a trailing UI footer (the permissions/agents hint) that sits below the
// status line and must be dropped before the status line is read as the last row.
func isComposerFooter(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "⏵⏵") || strings.Contains(l, "shift+tab to cycle")
}

// classifyComposer ports bin/composer.sh: strip trailing whitespace, drop blank lines, keep the last 6, then read the
// prompt row. A bare `❯` (or a status line with only a transcript row above it) is empty; `❯ <text>` is pending; a
// spinner row is busy; anything else is unknown and the caller defers.
func classifyComposer(tail []string) string {
	var lines []string
	for _, l := range tail {
		l = strings.TrimRight(l, " \t")
		if l != "" {
			lines = append(lines, l)
		}
	}
	// Drop trailing footer rows (the "bypass permissions on (shift+tab to cycle)" hint current Claude Code renders under
	// the status line) before taking the last six, or the status line is not the last row and the result is unknown.
	for len(lines) > 0 && isComposerFooter(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	if len(lines) == 0 {
		return "unknown"
	}
	for _, l := range lines {
		if spinnerRe.MatchString(l) {
			return "busy"
		}
	}
	last := lines[len(lines)-1]
	prev := ""
	if len(lines) >= 2 {
		prev = lines[len(lines)-2]
	}
	switch {
	case isEmptyPrompt(last):
		return "empty"
	case isPendingPrompt(last):
		return "pending"
	}
	if statusRe.MatchString(last) {
		switch {
		case prev == "" || isEmptyPrompt(prev):
			return "empty"
		case isPendingPrompt(prev):
			return "pending"
		case statusRe.MatchString(prev) || boxRe.MatchString(prev):
			return "empty"
		default:
			return "pending"
		}
	}
	return "unknown"
}

// codexPlaceholder is the codex composer's idle placeholder row (the codex analog of claude's bare `❯`). When it shows,
// the composer is empty; once the worker types, the placeholder is replaced (a `› <text>` row is pending). Fixture from
// a live codex run, M14.
const codexPlaceholder = "› Ask Codex to do anything"

// isEmptyPrompt reports whether a row is an idle composer prompt: claude's bare `❯` or the codex placeholder.
func isEmptyPrompt(l string) bool {
	return l == "❯" || strings.TrimSpace(l) == codexPlaceholder
}

// isPendingPrompt reports whether a row is a composer with typed-but-unsent text: claude `❯ <text>` or codex `› <text>`
// where the text is not the placeholder.
func isPendingPrompt(l string) bool {
	if strings.HasPrefix(l, "❯ ") {
		return true
	}
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "› ") && t != codexPlaceholder
}

// Interrupt breaks a worker out of a runaway turn by sending an interrupt to its terminal, the way v1 does
// (bin/interrupt.sh): `orca terminal send --terminal <handle> --interrupt --json`. It addresses the terminal handle, so
// a Session with no handle is a real error rather than a silent no-op.
func (c *Client) Interrupt(s backend.Session) error {
	if s.Handle == "" {
		return fmt.Errorf("orca Interrupt: no terminal handle")
	}
	_, err := c.call("terminal", "send", "--terminal", s.Handle, "--interrupt", "--json")
	return err
}

// Stop fences the worker dispatch. confirmed is true only when Orca reports ok; an error leaves confirmed false so
// the caller keeps ownership (pending_external, F03/F04/F05).
func (c *Client) Stop(s backend.Session) (bool, error) {
	if c.terminalPlane() {
		return c.stopTerminal(s)
	}
	_, err := c.mutate("orchestration", "worker-stop", "--dispatch", s.ID, "--json")
	if err != nil {
		return false, err
	}
	return true, nil
}

// Probe maps `orchestration worker-read` to liveness. A call error or invalid JSON is Unknown with an error (never
// inferred as gone, F08). A successful read with liveness "live" is Alive; a successful read reporting any other
// terminal/settled liveness is Settled; a successful read with no recognizable liveness is Unknown with an error.
func (c *Client) Probe(s backend.Session) (backend.Liveness, error) {
	if c.terminalPlane() {
		return c.probeTerminal(s)
	}
	res, err := c.call("orchestration", "worker-read", "--dispatch", s.ID, "--json")
	if err != nil {
		return backend.Unknown, err
	}
	var r struct {
		Status struct {
			Liveness string `json:"liveness"`
		} `json:"status"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return backend.Unknown, fmt.Errorf("parse worker-read result: %w", err)
	}
	return mapLiveness(r.Status.Liveness)
}

// mapLiveness turns Orca's liveness string into a Liveness. Empty/unrecognized on an otherwise-ok read is Unknown
// (with an error) rather than a guessed Settled, keeping F08's "never infer gone" rule.
func mapLiveness(s string) (backend.Liveness, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "live", "alive", "running":
		return backend.Alive, nil
	case "settled", "closed", "done", "completed", "failed", "stopped", "exited":
		return backend.Settled, nil
	case "":
		return backend.Unknown, fmt.Errorf("worker-read returned no liveness")
	default:
		return backend.Unknown, fmt.Errorf("worker-read returned unrecognized liveness %q", s)
	}
}

// Mail returns the mailbox: the reduced terminal-plane mailbox (no orchestration mail; the watcher wakes only from
// wake.jsonl) or the orchestration mailbox bound to this client's run.
func (c *Client) Mail() backend.Mailbox {
	if c.terminalPlane() {
		return terminalMailbox{}
	}
	return &mailbox{c: c}
}

func stripRef(b string) string {
	return strings.TrimPrefix(b, "refs/heads/")
}

// repoSelector turns a repo reference into an `orca --repo` selector. An absolute path (leading `/`) addresses a repo
// by path, which is required for a repo Orca has not registered under a name (on this machine `orca repo show --repo
// path:<p>` returns name:null for the coxswain checkout); anything else is treated as a registered repo name. Epic
// `repos` files and workspace.yaml therefore accept either a name or an absolute path.
func repoSelector(repo string) string {
	if strings.HasPrefix(repo, "/") {
		return "path:" + repo
	}
	return "name:" + repo
}
