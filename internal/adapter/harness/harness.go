// Package harness is the model-agnostic seam between the core and whatever runs an agent (Claude Code, Codex, ...).
// The core never hard-codes a harness (decision 0008); it reads a harness's Capability card to decide how wakes are
// delivered (push vs pull), whether checkpoints are automatic, and whether telemetry is available. Concrete adapters
// live in subpackages (claude/, codex/, fake/) and satisfy the Harness interface here.
package harness

// Role is which side of the handoff an agent plays.
type Role string

const (
	RoleLeader Role = "leader"
	RoleWorker Role = "worker"
)

// WakeMode is how the leader receives watcher wakes.
type WakeMode string

const (
	// WakePush: the harness has hooks that open a turn when a wake is queued (Claude Code UserPromptSubmit/Stop).
	WakePush WakeMode = "push"
	// WakePull: no hooks; the leader drains at the start of every turn and blocks on `cox wake wait` when idle,
	// with a backend doorbell as the safety net (Codex and any new harness).
	WakePull WakeMode = "pull"
)

// CheckpointMode is whether checkpoints are written automatically at compaction or only by the worker on demand.
type CheckpointMode string

const (
	CheckpointAuto   CheckpointMode = "auto"   // a PreCompact/SessionStart hook runs `cox checkpoint facts|inject`
	CheckpointManual CheckpointMode = "manual" // the worker writes the checkpoint at each phase boundary itself
)

// Capability is the observable contract of a harness. docs/adapters/<name>.md mirrors this exactly and a test reads
// Card() and compares it to the doc, so the card never drifts from the documentation.
type Capability struct {
	Name         string   // "claude", "codex"
	Roles        []Role   // roles this harness can play
	Wake         WakeMode // push | pull
	Checkpoint   CheckpointMode
	Doorbell     bool   // backend Send can nudge a running session
	Interrupt    bool   // the harness can be interrupted mid-turn
	Telemetry    bool   // Telemetry() returns real token/turn counts (false => always Unknown)
	Sandbox      bool   // the harness sandboxes tool execution by default
	Instructions string // how Package() renders instructions for this harness
}

// Brief is the launch payload for a worker or leader. StoryPath is the file the agent reads in full; ContextPath is
// the leader's context pack; Note is an optional relaunch progress note. InjectCheckpoint asks the launch to read the
// checkpoint first (relaunch/resume).
type Brief struct {
	StoryPath        string
	ContextPath      string
	Note             string
	InjectCheckpoint bool
}

// Launch is the input an adapter needs to compose the full production argv (decision 0002 / ADR launch seam). Model,
// Effort, and Flags are resolved from policy by the caller (cmd/cox); the adapter owns their spelling, the executable
// name, trust/resource flags, and the prompt. Worktree is the worker's checkout, which a harness may need to compose a
// resource flag (e.g. codex's writable roots for a linked worktree). The backend receives the resulting []string as
// data and never imports this layer.
type Launch struct {
	Role     Role
	Worktree string
	Model    string
	Effort   string
	Flags    []string
	Arena    bool // an arena-role launch (read-only report writer); a sandboxed harness grants it no extra writable roots
	Brief    Brief
}

// WorkerPrompt renders the single prompt argument a dispatched worker harness receives, shared by every adapter so the
// wording never drifts between harnesses. The story file is the task (read in full); an inline Note is appended as a
// progress note (relaunch); InjectCheckpoint prepends a checkpoint-first instruction for a harness with no SessionStart
// hook to do it automatically. With no story path it falls back to the Note alone.
func WorkerPrompt(role Role, b Brief) string {
	if role != RoleWorker || b.StoryPath == "" {
		return b.Note
	}
	p := "Your task is the story file " + b.StoryPath + " - read it in full and follow its Working rules exactly."
	if b.InjectCheckpoint {
		p = "Read your checkpoint with `cox checkpoint inject` first, then continue from Next action. " + p
	}
	if b.Note != "" {
		p += " Progress note from your previous attempt: " + b.Note
	}
	return p
}

// Context is a telemetry reading. Known is false when the harness cannot report usage; callers must render that as
// "unknown", never as 0 tokens (F11).
type Context struct {
	Tokens int
	Turns  int
	Known  bool
}

// Harness is the surface the core needs from an agent runtime.
type Harness interface {
	// Card returns the capability contract; it never varies at runtime.
	Card() Capability
	// Package renders this harness's instructions (from AGENTS.md + skills) into dst for the given role.
	Package(role Role, dst string) error
	// LaunchArgs returns the full production argv to start this harness: executable name, model/provider syntax,
	// effort/thinking level, trust/resource flags, and the prompt. cmd/cox composes it and threads it to the backend
	// as data, so the backend never imports this layer (ADR 0002).
	LaunchArgs(l Launch) []string
	// Telemetry reports token/turn usage for a session (identified by its worktree path). It returns Known=false,
	// never 0, when the harness has no usable log (F11).
	Telemetry(session string) (Context, error)
}
