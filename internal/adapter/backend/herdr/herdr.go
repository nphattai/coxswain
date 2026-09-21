// Package herdr is the optional Backend adapter (decision 0002), completed to full capability in M10 (ADR 0012). herdr
// is an agent-native terminal runtime with native per-pane agent state. Task worktrees are plain git worktrees (herdr
// does not own them), created under the workspace's worktree base; a worker runs in a herdr pane the adapter opens in
// that worktree and into which it types the harness launch. Liveness comes from herdr's native agent state.
//
// Command shapes are the ones firstmate drives (bin/backends/herdr.sh) against herdr, and the JSON field names
// (result.workspace.workspace_id, result.panes[].pane_id, result.agent.agent_status) are the ones firstmate reads;
// see docs/adapters/herdr.md, which records the herdr version and which methods are verified live vs mapped. The
// command runner and git runner are injectable so the mapping is unit-tested with a fake herdr CLI and a real temp
// repo, no herdr install required.
package herdr

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// SessionKind marks a herdr Session. ID is the pane id; Handle is the herdr session name.
const SessionKind = "herdr"

// Client is a herdr backend for one named session. WorktreeBase is where git worktrees are created (from the
// workspace's cox/workspace.json worktree_base); a blank base makes WorktreeCreate a real error rather than writing to
// an unexpected path.
type Client struct {
	Session      string
	WorktreeBase string
	run          func(args ...string) ([]byte, error) // raw herdr exec
	git          func(args ...string) ([]byte, error)
}

// New returns a Client that shells out to the real `herdr` and `git` binaries for the given session.
func New(session string) *Client {
	return &Client{Session: session, run: execHerdr, git: execGit}
}

func execHerdr(args ...string) ([]byte, error) { return exec.Command("herdr", args...).Output() }
func execGit(args ...string) ([]byte, error)   { return exec.Command("git", args...).Output() }

// herdr runs a herdr subcommand with the session bound as the leading global option (herdr --session <s> <subcommand>),
// the shape firstmate uses. herdr emits JSON on stdout by default.
func (c *Client) herdr(args ...string) ([]byte, error) {
	return c.run(append([]string{"--session", c.Session}, args...)...)
}

// envelope is herdr's common response shape.
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call runs a herdr subcommand and returns its result payload, or a real error (process failure, invalid JSON, or
// ok=false). A structured ok=false body on a non-zero exit is surfaced with its code so callers can classify it.
func (c *Client) call(args ...string) (json.RawMessage, error) {
	out, runErr := c.herdr(args...)
	var env envelope
	if len(out) > 0 && json.Unmarshal(out, &env) == nil {
		if env.OK {
			return env.Result, nil
		}
		msg := env.Error.Message
		if msg == "" {
			msg = env.Error.Code
		}
		if msg == "" {
			msg = "ok=false with no error"
		}
		return nil, fmt.Errorf("herdr %s: %s", strings.Join(args, " "), msg)
	}
	if runErr != nil {
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args, " "), runErr)
	}
	return nil, fmt.Errorf("herdr %s: unparseable response", strings.Join(args, " "))
}

// WorktreeCreate creates a plain git worktree for the story branch under the workspace's worktree base (herdr does not
// own worktrees). A new branch is cut from base; an existing branch is reused (A11): it is attached and reset --hard to
// base only when it carries no commits missing from base, otherwise the create is refused rather than discarding work.
// It never deletes a branch (F01).
func (c *Client) WorktreeCreate(repo, branch, base string) (backend.Worktree, error) {
	if c.WorktreeBase == "" {
		return backend.Worktree{}, fmt.Errorf("herdr WorktreeCreate: no worktree base configured (workspace.json worktree_base)")
	}
	if repo == "" || branch == "" {
		return backend.Worktree{}, fmt.Errorf("herdr WorktreeCreate: repo and branch are required")
	}
	path := filepath.Join(c.WorktreeBase, strings.ReplaceAll(branch, "/", "-"))
	exists := false
	if _, err := c.git("-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		exists = true
	}
	if exists {
		if _, err := c.git("-C", repo, "worktree", "add", path, branch); err != nil {
			return backend.Worktree{}, fmt.Errorf("herdr worktree add (reuse %s): %w", branch, err)
		}
		if err := c.resetReusedBranch(repo, path, branch, base); err != nil {
			return backend.Worktree{}, err
		}
	} else {
		if _, err := c.git("-C", repo, "worktree", "add", "-b", branch, path, base); err != nil {
			return backend.Worktree{}, fmt.Errorf("herdr worktree add -b %s: %w", branch, err)
		}
	}
	return backend.Worktree{Path: path, Branch: branch}, nil
}

// resetReusedBranch brings a reused branch to base only when base..branch is empty (no unmerged work); a branch ahead
// of base is refused, never reset (F01: discarding commits is the data loss F01 guards against).
func (c *Client) resetReusedBranch(repo, path, branch, base string) error {
	if base == "" {
		return nil
	}
	ahead, err := c.git("-C", path, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return fmt.Errorf("check unmerged commits on %s vs base %s: %w", branch, base, err)
	}
	if n := strings.TrimSpace(string(ahead)); n != "0" {
		return fmt.Errorf("existing branch %s has %s commit(s) not in base %s; not resetting (would discard work)", branch, n, base)
	}
	if _, err := c.git("-C", path, "reset", "--hard", base); err != nil {
		return fmt.Errorf("reset reused branch %s to base %s: %w", branch, base, err)
	}
	return nil
}

// WorktreeRemove detaches the worktree HEAD, then removes the git worktree from the main checkout. git worktree remove
// never deletes the branch, so F01 holds. The remove runs from the main worktree (not the target, which git refuses to
// remove from its own cwd).
func (c *Client) WorktreeRemove(wt backend.Worktree) error {
	if wt.Path == "" {
		return fmt.Errorf("herdr WorktreeRemove: empty worktree path")
	}
	if _, err := c.git("-C", wt.Path, "switch", "--detach"); err != nil {
		return fmt.Errorf("herdr WorktreeRemove: detach %s: %w", wt.Path, err)
	}
	main, err := c.mainWorktree(wt.Path)
	if err != nil {
		return err
	}
	if _, err := c.git("-C", main, "worktree", "remove", "--force", wt.Path); err != nil {
		return fmt.Errorf("herdr worktree remove %s: %w", wt.Path, err)
	}
	return nil
}

// mainWorktree resolves the main worktree directory of the repo a worktree belongs to (the parent of its common git
// dir), so worktree remove can run from a directory other than the one being removed.
func (c *Client) mainWorktree(wtPath string) (string, error) {
	out, err := c.git("-C", wtPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve main worktree for %s: %w", wtPath, err)
	}
	return filepath.Dir(strings.TrimSpace(string(out))), nil
}

// Spawn opens a herdr workspace in the worktree, types the harness launch into its pane, and returns the pane Session.
// It creates a per-worker workspace (the isolation unit) with the worktree as cwd, resolves its pane, then types the
// cox launch line (COX_EPIC/COX_STORY/COX_PLANE + harness command) followed by Enter.
//
// ponytail: a per-worker workspace is left behind when its pane is closed (Stop closes the pane, not the workspace) -
// the ceiling herdr's own presentation cleanup would remove; recorded in docs/adapters/herdr.md.
func (c *Client) Spawn(wt backend.Worktree, h backend.HarnessSpec, brief backend.Brief) (backend.Session, error) {
	if wt.Path == "" {
		return backend.Session{}, fmt.Errorf("herdr Spawn: empty worktree path")
	}
	label := backend.StoryFromPath(brief.StoryPath)
	if label == "" {
		label = "cox worker"
	}
	res, err := c.call("workspace", "create", "--cwd", wt.Path, "--label", label, "--no-focus")
	if err != nil {
		return backend.Session{}, err
	}
	var wsr struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(res, &wsr); err != nil {
		return backend.Session{}, fmt.Errorf("parse workspace create result: %w", err)
	}
	ws := wsr.Workspace.WorkspaceID
	if ws == "" {
		return backend.Session{}, fmt.Errorf("herdr Spawn: workspace create returned no workspace_id")
	}
	pane, err := c.firstPane(ws)
	if err != nil {
		return backend.Session{}, err
	}
	sess := backend.Session{Kind: SessionKind, ID: pane, Handle: c.Session}
	brief.Worktree = wt.Path // so the launch composer can grant the git common dir writable for a codex worker (M14)
	line := backend.LaunchLine(h, brief)
	if _, err := c.call("pane", "send-text", pane, line); err != nil {
		return backend.Session{}, fmt.Errorf("herdr Spawn: type launch command: %w", err)
	}
	if _, err := c.call("pane", "send-keys", pane, "Enter"); err != nil {
		return backend.Session{}, fmt.Errorf("herdr Spawn: submit launch command: %w", err)
	}
	return sess, nil
}

// firstPane returns the first pane id of a workspace (one worker per workspace, so the first pane is the worker's).
func (c *Client) firstPane(workspace string) (string, error) {
	res, err := c.call("pane", "list", "--workspace", workspace)
	if err != nil {
		return "", err
	}
	var r struct {
		Panes []struct {
			PaneID string `json:"pane_id"`
		} `json:"panes"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", fmt.Errorf("parse pane list result: %w", err)
	}
	if len(r.Panes) == 0 || r.Panes[0].PaneID == "" {
		return "", fmt.Errorf("herdr Spawn: workspace %s has no pane", workspace)
	}
	return r.Panes[0].PaneID, nil
}

// Send is a doorbell: it types text into the worker pane and submits it with Enter. A herdr pane is always writable, so
// a successful send is a real ring (rang=true); an empty session id is a real error rather than a silent no-op.
func (c *Client) Send(s backend.Session, text string) (bool, error) {
	if s.ID == "" {
		return false, fmt.Errorf("herdr Send: no pane id")
	}
	if _, err := c.call("pane", "send-text", s.ID, text); err != nil {
		return false, err
	}
	if _, err := c.call("pane", "send-keys", s.ID, "Enter"); err != nil {
		return false, err
	}
	return true, nil
}

// Interrupt sends Ctrl-C to the worker pane to break a runaway turn (firstmate's key name is C-c).
func (c *Client) Interrupt(s backend.Session) error {
	if s.ID == "" {
		return fmt.Errorf("herdr Interrupt: no pane id")
	}
	_, err := c.call("pane", "send-keys", s.ID, "C-c")
	return err
}

// Stop closes the worker pane and confirms by probing: a gone agent (agent_not_found / pane_not_found) reads as Settled
// and confirms. An unconfirmed close returns confirmed=false so the caller keeps ownership (F03/F04/F05).
func (c *Client) Stop(s backend.Session) (bool, error) {
	if s.ID == "" {
		return false, fmt.Errorf("herdr Stop: no pane id")
	}
	if _, err := c.call("pane", "close", s.ID); err != nil {
		return false, err
	}
	live, err := c.Probe(s)
	if err != nil {
		return false, nil // close issued but liveness unreadable; keep ownership
	}
	return live == backend.Settled, nil
}

// Composer is reduced: herdr's composer classification (bin/fm-composer-lib.sh) is UI-shape specific and not reproduced
// in cox, so it returns "unknown". The watcher treats only "empty" as idle, so idle_no_done never false-fires on herdr.
func (c *Client) Composer(s backend.Session) (string, error) {
	return backend.ComposerUnknown, nil
}

// Screen is not reproduced for herdr (its terminal read shape is UI-specific and not ported): it returns an error so
// the watcher records the stuck wake without a captured dialog rather than a wrong one.
func (c *Client) Screen(s backend.Session) ([]string, error) {
	return nil, fmt.Errorf("herdr: screen capture not supported")
}

// Terminals is not reproduced for herdr (no run-independent terminal listing): it returns an error, so the
// duplicate-leader check skips a herdr backend rather than inferring one.
func (c *Client) Terminals() ([]backend.Terminal, error) {
	return nil, fmt.Errorf("herdr: terminal listing not supported")
}

// Probe maps herdr's native agent state to liveness. A registered agent (any agent_status: working, idle, done,
// blocked) is Alive; a gone pane/agent (pane_not_found / agent_not_found) is Settled; an unreadable or unexpected
// response is Unknown with an error (never inferred as gone, F08). A native "idle" agent is still a live, registered
// agent - idleness is a semantic-busy question, not a liveness one.
func (c *Client) Probe(s backend.Session) (backend.Liveness, error) {
	out, err := c.herdr("agent", "get", s.ID)
	if err != nil {
		if code := errorCode(out); code != "" {
			return classify("", code)
		}
		return backend.Unknown, fmt.Errorf("herdr agent get: %w", err)
	}
	var r struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Result struct {
			Agent struct {
				Status string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return backend.Unknown, fmt.Errorf("herdr agent get: invalid JSON: %w", err)
	}
	if !r.OK {
		return classify("", r.Error.Code)
	}
	return classify(r.Result.Agent.Status, "")
}

// classify maps a herdr agent_status or error code to liveness.
func classify(status, code string) (backend.Liveness, error) {
	switch code {
	case "pane_not_found", "agent_not_found":
		return backend.Settled, nil
	case "":
		// fall through to status
	default:
		return backend.Unknown, fmt.Errorf("herdr agent get: unrecognized error code %q", code)
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "working", "idle", "done", "blocked":
		return backend.Alive, nil
	case "":
		return backend.Unknown, fmt.Errorf("herdr agent get: ok but no agent_status")
	default:
		return backend.Unknown, fmt.Errorf("herdr agent get: unrecognized agent_status %q", status)
	}
}

// errorCode best-effort extracts an error code from a structured herdr response emitted on a non-zero exit.
func errorCode(out []byte) string {
	var r struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &r) != nil {
		return ""
	}
	return r.Error.Code
}

// WorkerList is reduced: herdr has no run-scoped dispatch listing. It returns an error so a caller falls back to Probe
// rather than mistaking an empty list for "no live workers" (decision 0008: an unmet capability is explicit).
func (c *Client) WorkerList() ([]backend.Worker, error) {
	return nil, reduced("WorkerList", "herdr has no run-scoped dispatch listing")
}

// Mail is reduced: there is no orchestration mailbox on herdr. The watcher's wake source is wake.jsonl and steers use
// the durable inbox, so no mailbox is needed (this matches the terminal plane, ADR 0012).
func (c *Client) Mail() backend.Mailbox { return reducedMailbox{} }

func reduced(op, why string) error {
	return fmt.Errorf("herdr %s: reduced (%s), see docs/adapters/herdr.md", op, why)
}

type reducedMailbox struct{}

func (reducedMailbox) Send(to, subject, body string) error { return nil }
func (reducedMailbox) Check() ([]backend.Message, string, error) {
	return nil, "", nil // no mailbox: the watcher ignores mail on herdr
}
func (reducedMailbox) Reply(msgID, body string) error {
	return reduced("Mail.Reply", "no orchestration mailbox; use file-based cox reply")
}
func (reducedMailbox) Ack(deliveryID string) error { return nil }
