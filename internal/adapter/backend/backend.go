// Package backend defines the one Backend interface the coxswain core talks to (decision 0002). The core never
// imports a concrete backend (Orca, herdr); it depends only on this interface, which the fake adapter satisfies
// in tests. Concrete adapters live in subpackages (orca/, herdr/) and import this package, never the reverse.
package backend

import (
	"errors"
	"strings"
)

// ErrAgentPromptBlocked is returned by Send when the backend refused to type the doorbell because the worker's pane is
// waiting on a local permission/approval prompt (Orca agent_prompt_blocked, or a "waiting" agents[] state). It is not a
// transient failure: the watcher treats it as skipped:permission - it bumps the ring ladder (so an undeliverable steer
// still escalates to stuck after ringMax) and notes the reason, rather than deferring silently forever (M14).
var ErrAgentPromptBlocked = errors.New("agent prompt blocked (worker waiting on a local permission prompt)")

// Worktree is a verified checkout: WorktreeCreate returns it only after the path exists on the branch (decision
// 0002, F02). Both fields are always set on success; a caller never gets a Worktree with an empty Path.
type Worktree struct {
	Path   string
	Branch string
}

// HarnessSpec names the harness a worker runs under. The core is model-agnostic (decision 0008): it passes what
// policy chose and never hard-codes a harness. Model and Effort are optional. LaunchFlags are the approval/autonomy
// flags policy chose for this harness (e.g. claude --permission-mode bypassPermissions), typed after the model and
// before the prompt; the caller resolves them from policy so the backend stays harness-agnostic.
type HarnessSpec struct {
	Name        string   // e.g. "claude", "codex"
	Model       string   // optional, e.g. "claude-opus-4-8"
	Effort      string   // optional, harness-specific
	LaunchFlags []string // optional, policy approval flags (harness.launch.<name>)
}

// Brief is the instruction payload delivered to a worker on Spawn. StoryPath is the file the worker reads in full;
// Text is an optional inline doorbell/prompt. The story file is the source of truth, not the inline text (F09: the
// same path exists on every machine, a pasted document stalls TUIs). Arena marks a terminal-mode arena role launch: an
// arena role writes only its report, in its own worktree (collected later), so it gets no writable roots (no --add-dir)
// even under a workspace-write sandbox (ADR 0013, M13 finding).
type Brief struct {
	StoryPath string
	Text      string
	Arena     bool
	// Worktree is the worker's checkout path, set at spawn on the terminal plane. It is not an instruction; the launch
	// composer uses it to resolve the git common dir a codex worker needs writable to commit in a linked worktree (M14).
	Worktree string
}

// Session identifies one spawned worker for the adapter that made it. Kind and the ids are adapter-specific; the
// core treats a Session as opaque and only hands it back to the same backend.
type Session struct {
	Kind   string // "orca" | "herdr"
	ID     string // dispatch id (orca) or pane id (herdr)
	Handle string // terminal handle (orca) or session:pane (herdr)
}

// Liveness is what a Probe can confirm. Unknown is the zero value so a probe error or an unreadable response is
// never mistaken for a confident answer: the resolver keeps the event-log state and never infers "gone" (F08, P5).
type Liveness int

const (
	// Unknown: the probe failed, timed out, or returned something we cannot classify. Keep monitoring; do not transition.
	Unknown Liveness = iota
	// Alive: the backend confirmed the worker session is live.
	Alive
	// Settled: the backend confirmed the worker session has ended (succeeded, failed, closed).
	Settled
)

func (l Liveness) String() string {
	switch l {
	case Alive:
		return "alive"
	case Settled:
		return "settled"
	default:
		return "unknown"
	}
}

// Worker is one dispatch the backend knows about, as reported by a run-scoped listing. State is the backend's own
// dispatch/worker state string (Orca: ready | running | succeeded | failed | abandoned), lower-cased; the caller
// classifies it (cox migrate keeps a session only for ready|running). Handle is the worker's terminal handle when known.
type Worker struct {
	Dispatch string
	State    string
	Handle   string
}

// Alive reports whether the worker state is one a session should be kept for (the dispatch is still ready or running).
func (w Worker) Alive() bool {
	s := strings.ToLower(strings.TrimSpace(w.State))
	return s == "ready" || s == "running"
}

// Backend is the whole surface the core needs from a runtime. Small on purpose (decision 0002): a community tmux
// backend can be added by satisfying this interface without touching the core.
type Backend interface {
	// WorktreeCreate returns a verified path and branch or an error; it never falls back to a shared checkout (F02).
	WorktreeCreate(repo, branch, base string) (Worktree, error)
	// WorktreeRemove removes the checkout. It must never delete a git branch (F01); an adapter whose underlying
	// command deletes branches documents that ceiling in its capability card and guards against it.
	WorktreeRemove(wt Worktree) error
	Spawn(wt Worktree, h HarnessSpec, brief Brief) (Session, error)
	// Send is a doorbell only: it nudges the worker, it is not the durable steer channel. It returns rang=true only
	// when it actually delivered the nudge to a live terminal (a busy/pending composer, or no handle, is not a ring),
	// so the caller re-rings or escalates on what was really delivered.
	Send(s Session, text string) (rang bool, err error)
	Interrupt(s Session) error
	// Stop returns confirmed=true only when the adapter confirmed the stop; an unconfirmed stop keeps ownership
	// (event.v1 pending_external, F03/F04/F05).
	Stop(s Session) (confirmed bool, err error)
	// Probe reports liveness. A failed probe returns Unknown and a non-nil error; it never returns Settled on doubt.
	Probe(s Session) (Liveness, error)
	// Composer classifies the worker terminal's input state: "empty" | "pending" | "busy" | "unknown". It never errors
	// on doubt (a backend that cannot read the composer returns "unknown"), so a caller treats only "empty" as idle.
	Composer(s Session) (string, error)
	// WorkerList returns every dispatch in the backend's run with its state, so a caller can tell a live dispatch from a
	// settled one without one Probe per story. A backend with no run listing runs reduced (herdr) and returns an error.
	WorkerList() ([]Worker, error)
	Mail() Mailbox
}

// Composer states returned by Backend.Composer.
const (
	ComposerEmpty   = "empty"   // the worker is idle at a bare prompt
	ComposerPending = "pending" // the worker has typed but not sent
	ComposerBusy    = "busy"    // the worker is mid-turn
	ComposerBlocked = "blocked" // the worker is waiting on a local prompt (approval or input) it cannot answer itself
	ComposerUnknown = "unknown" // the backend cannot tell (never treat as idle)
)

// Message is one inter-agent message read from the mailbox. Payload is the raw payload as delivered (Orca sends it
// as a JSON string); the caller parses it. Kept structured end to end so empty fields never collapse (F10).
type Message struct {
	ID        string
	From      string
	Subject   string
	Body      string
	Type      string
	Payload   string
	CreatedAt string
	Read      bool
}

// Mailbox is the steer/status channel. Check must not consume mail: it reads without acknowledging, so a caller
// that only wants to see the queue does not silently mark it read. Ack is the only call that acknowledges, and it
// acknowledges the delivery id Check returned.
type Mailbox interface {
	Send(to, subject, body string) error
	Check() (msgs []Message, deliveryID string, err error)
	Reply(msgID, body string) error
	Ack(deliveryID string) error
}
