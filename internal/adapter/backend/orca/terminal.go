package orca

// Terminal plane (ADR 0012): cox drives Orca through worktrees and terminals only - no orchestration task-create,
// worker-start, mailbox, or worker-stop. Spawn creates a terminal in the worktree and types the harness launch;
// liveness comes from `worktree ps` agents[] correlated by pane key; Stop closes the terminal. The command shapes here
// are the ones observed live on this machine and recorded in docs/adapters/orca.md.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// SessionKindTerminal marks a Session spawned at the terminal plane, so a caller can tell it apart from an
// orchestration dispatch. Both ID and Handle are the terminal handle (there is no dispatch id on this plane).
const SessionKindTerminal = "orca-terminal"

// spawnConfirmWait bounds the Spawn launch-confirmation window. COX_SPAWN_CONFIRM (a Go duration) wins, else the policy
// value (Client.LaunchConfirmS, backend.orca.launch_confirm_s), else 60s. The 60s default is up from the original 20s:
// a cold harness start registered its agents[] entry but had not yet rendered a busy composer within 20s, so the
// composer-only window warned on workers that were in fact running (M10b, docs/adapters/orca.md live fact).
func (c *Client) spawnConfirmWait() time.Duration {
	if v := strings.TrimSpace(os.Getenv("COX_SPAWN_CONFIRM")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	if c.LaunchConfirmS > 0 {
		return time.Duration(c.LaunchConfirmS) * time.Second
	}
	return 60 * time.Second
}

// confirmPollInterval is how often confirmLaunch re-checks; tests set a small value so no real second elapses.
func (c *Client) confirmPollInterval() time.Duration {
	if c.confirmPoll > 0 {
		return c.confirmPoll
	}
	return time.Second
}

// spawnTerminal is Spawn on the terminal plane. It creates a terminal in the worktree, types the harness launch command
// (carrying COX_EPIC/COX_STORY/COX_PLANE so the worker reports through cox, not Orca), and waits up to spawnConfirmWait
// for the composer to go busy. It returns the terminal-handle Session in every case: the handle is durable, and a
// worker that never reaches busy is caught by the watcher's liveness (ADR 0012), so tearing a possibly-slow worker down
// here would be worse than letting liveness own detection.
//
// ponytail: an unconfirmed busy is logged, not fatal - the durable handle plus watcher liveness is the recovery path,
// so there is no separate pending_external signal threaded out of Spawn (the Backend interface returns only
// (Session, error)). Recorded in docs/adapters/orca.md.
func (c *Client) spawnTerminal(wt backend.Worktree, h backend.HarnessSpec, brief backend.Brief) (backend.Session, error) {
	if wt.Path == "" {
		return backend.Session{}, fmt.Errorf("orca spawnTerminal: empty worktree path")
	}
	title := backend.StoryFromPath(brief.StoryPath)
	if title == "" {
		title = "cox worker"
	}
	res, err := c.call("terminal", "create", "--worktree", "path:"+wt.Path, "--title", title, "--json")
	if err != nil {
		return backend.Session{}, err
	}
	var cr struct {
		Terminal struct {
			Handle string `json:"handle"`
		} `json:"terminal"`
	}
	if err := json.Unmarshal(res, &cr); err != nil {
		return backend.Session{}, fmt.Errorf("parse terminal create result: %w", err)
	}
	handle := cr.Terminal.Handle
	if handle == "" {
		return backend.Session{}, fmt.Errorf("orca spawnTerminal: terminal create returned no handle")
	}
	sess := backend.Session{Kind: SessionKindTerminal, ID: handle, Handle: handle, Story: backend.StoryFromPath(brief.StoryPath)}

	brief.Worktree = wt.Path // so the launch composer can grant the git common dir writable for a codex worker (M14)
	line := backend.LaunchLine(h, brief)
	if _, err := c.call("terminal", "send", "--terminal", handle, "--text", line, "--enter", "--json"); err != nil {
		// The command did not land; close the just-created terminal so it is not orphaned, then fail.
		_, _ = c.call("terminal", "close", "--terminal", handle, "--json")
		return backend.Session{}, fmt.Errorf("orca spawnTerminal: type launch command: %w", err)
	}
	if !c.confirmLaunch(sess) {
		fmt.Fprintf(os.Stderr, "cox: warning: %s launch not confirmed within %s (terminal %s); liveness will own detection\n",
			h.Name, c.spawnConfirmWait(), handle)
	}
	return sess, nil
}

// confirmLaunch polls until the launch is confirmed or the window elapses. A launch is confirmed when Orca reports an
// agents[] entry for the worker's pane (any state - the harness registered with Orca) OR the composer has gone busy.
// The agents[] signal catches a cold harness start that registered but had not yet rendered a busy composer, which the
// old composer-only 20s window missed and warned on (M10b). It returns true on the first confirmation.
func (c *Client) confirmLaunch(s backend.Session) bool {
	deadline := time.Now().Add(c.spawnConfirmWait())
	for {
		if _, found, err := c.agentStateForHandle(s.Handle); err == nil && found {
			return true
		}
		if c.composerState(s.Handle) == backend.ComposerBusy {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(c.confirmPollInterval())
	}
}

// probeTerminal is Probe on the terminal plane. It reads `terminal show` for the endpoint verdict, then correlates the
// worker's pane against Orca's structured `worktree ps` agents[] model:
//   - a stale/closed handle, or a disconnected/exited terminal, is Settled (the worker is confidently gone);
//   - a connected terminal whose pane has an agents[] entry: working|idle -> Alive, dead|exited -> Settled;
//   - a connected terminal with no agents[] entry (or an unreadable ps) is Unknown, never gone (ADR 0012, decision 3:
//     absence of a structured entry is unknown).
//
// Composer classification (the F7/F10 fallback) stays in Composer(); Probe does not read the composer.
func (c *Client) probeTerminal(s backend.Session) (backend.Liveness, error) {
	if s.Handle == "" {
		return backend.Unknown, fmt.Errorf("orca probeTerminal: no terminal handle")
	}
	show, stale, err := c.terminalShow(s.Handle)
	if err != nil {
		return backend.Unknown, err
	}
	if stale || show.gone() {
		return backend.Settled, nil
	}
	paneKey := show.TabID + ":" + show.LeafID
	if show.TabID == "" || show.LeafID == "" {
		return backend.Unknown, nil // cannot correlate a pane; defer rather than guess
	}
	agentState, found, err := c.psAgentState(paneKey)
	if err != nil {
		return backend.Unknown, err
	}
	if !found {
		return backend.Unknown, nil // no structured entry: unknown, never gone
	}
	return mapAgentState(agentState), nil
}

// stopTerminal is Stop on the terminal plane: it closes the worker terminal, then confirms the close by probing. A
// confirmed stop needs the endpoint to read as gone (terminal show missing/disconnected, or the pane's agents[] entry
// settled) - probeTerminal already folds both into Settled. An unconfirmed close returns confirmed=false so the caller
// keeps ownership (pending_external, F03/F04/F05); the close command is still issued.
func (c *Client) stopTerminal(s backend.Session) (bool, error) {
	if s.Handle == "" {
		return false, fmt.Errorf("orca stopTerminal: no terminal handle")
	}
	if _, err := c.call("terminal", "close", "--terminal", s.Handle, "--json"); err != nil {
		return false, err
	}
	live, err := c.probeTerminal(s)
	if err != nil {
		return false, nil // close issued but liveness unreadable; keep ownership
	}
	return live == backend.Settled, nil
}

// termShow is the subset of `terminal show` .result.terminal cox reads. Connected is a pointer so an absent field is
// distinguished from an explicit false; ExitCause is present only for an exited endpoint.
type termShow struct {
	Connected *bool           `json:"connected"`
	ExitCause json.RawMessage `json:"exitCause"`
	TabID     string          `json:"tabId"`
	LeafID    string          `json:"leafId"`
}

// gone reports whether the terminal show describes a confidently-ended endpoint: an exit cause is recorded, or it is
// explicitly not connected.
func (t termShow) gone() bool {
	if len(t.ExitCause) > 0 && string(t.ExitCause) != "null" {
		return true
	}
	return t.Connected != nil && !*t.Connected
}

// terminalShow runs `terminal show` and parses the envelope even on a non-zero exit, so a stale-handle body (ok=false
// with code terminal_handle_stale / tab_not_found) is reported as stale=true (a gone endpoint) rather than a transient
// error. An empty read or a transient ok=false is a real error (Unknown at the caller).
func (c *Client) terminalShow(handle string) (show termShow, stale bool, err error) {
	out, runErr := c.run("terminal", "show", "--terminal", handle, "--json")
	if len(out) == 0 {
		if runErr != nil {
			return termShow{}, false, fmt.Errorf("orca terminal show %s: %w", handle, runErr)
		}
		return termShow{}, false, fmt.Errorf("orca terminal show %s: empty response", handle)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminal termShow `json:"terminal"`
		} `json:"result"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(out, &env); jsonErr != nil {
		return termShow{}, false, fmt.Errorf("orca terminal show %s: invalid JSON: %w", handle, jsonErr)
	}
	if !env.OK {
		switch env.Error.Code {
		case "terminal_handle_stale", "tab_not_found":
			return termShow{}, true, nil // the endpoint is gone
		default:
			msg := env.Error.Message
			if msg == "" {
				msg = "ok=false with no error message"
			}
			return termShow{}, false, fmt.Errorf("orca terminal show %s: %s", handle, msg)
		}
	}
	return env.Result.Terminal, false, nil
}

// psAgentState scans `worktree ps` agents[] across every worktree for the pane and returns its state. found=false means
// no entry matched the pane (the caller treats that as Unknown, never gone). An unreadable ps is a real error.
func (c *Client) psAgentState(paneKey string) (state string, found bool, err error) {
	res, err := c.call("worktree", "ps", "--json")
	if err != nil {
		return "", false, err
	}
	var r struct {
		Worktrees []struct {
			Agents []struct {
				PaneKey string `json:"paneKey"`
				State   string `json:"state"`
			} `json:"agents"`
		} `json:"worktrees"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", false, fmt.Errorf("parse worktree ps result: %w", err)
	}
	for _, w := range r.Worktrees {
		for _, a := range w.Agents {
			if a.PaneKey == paneKey {
				return a.State, true, nil
			}
		}
	}
	return "", false, nil
}

// mapAgentState maps an Orca agents[] state to liveness (ADR 0012 B3): working|idle|waiting -> Alive, dead|exited ->
// Settled, anything else (done, empty, unrecognized) -> Unknown, so a doubtful state never claims alive or infers gone.
// waiting is Alive: a worker blocked on a local prompt (approval or input) is very much alive - the watcher's blocked
// pass, not liveness, is what surfaces the stuck prompt (M10b).
func mapAgentState(state string) backend.Liveness {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "working", "idle", "waiting":
		return backend.Alive
	case "dead", "exited", "gone":
		return backend.Settled
	default:
		return backend.Unknown
	}
}

// agentStateForHandle resolves the raw Orca agents[] state string for a terminal handle: terminal show gives the pane
// key, worktree ps gives the pane's state. found=false when the terminal is stale/gone or has no agents[] entry. It is
// how Composer detects a "waiting" (blocked-on-a-prompt) worker on the terminal plane.
func (c *Client) agentStateForHandle(handle string) (state string, found bool, err error) {
	show, stale, err := c.terminalShow(handle)
	if err != nil {
		return "", false, err
	}
	if stale || show.TabID == "" || show.LeafID == "" {
		return "", false, nil
	}
	return c.psAgentState(show.TabID + ":" + show.LeafID)
}

// terminalMailbox is the reduced mailbox for the terminal plane (ADR 0012 B5): there is no orchestration mailbox, so
// Check returns nothing and Ack is a no-op (the watcher's only wake source is wake.jsonl, written by `cox story
// report`). Send is a no-op nil so a status path that still calls Mail().Send does not error; Reply is refused, since a
// reply on this plane goes through the file-based `cox reply`, not a mailbox.
type terminalMailbox struct{}

func (terminalMailbox) Send(to, subject, body string) error { return nil }
func (terminalMailbox) Check() ([]backend.Message, string, error) {
	return nil, "", nil // no mailbox: the watcher ignores mail on this plane
}
func (terminalMailbox) Reply(msgID, body string) error {
	return fmt.Errorf("orca terminal plane has no mailbox reply; use file-based cox reply")
}
func (terminalMailbox) Ack(deliveryID string) error { return nil }
