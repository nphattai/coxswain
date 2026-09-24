// Pure builders for the cox argv the extension runs, and the epic-binding resolver. Factored out of the Pi API wiring
// (cox-pi.ts) so the command choices and the epic fallback are deterministically testable.
//
// Leader parity (DESIGN item 4): the leader role drives the SAME Go-owned hooks the Claude/Codex leader hooks run -
// `cox hook prompt-drain | stop-rewake | precompact | session-start`. Omitting `--epic` lets the Go side resolve every
// active epic of the workspace (leaderEpics/activeEpics) and stay the single owner of what a wake means; passing
// `--epic <dir>` narrows to one epic (a bound leader, or a worker whose COX_EPIC is set).

// withEpic appends `--epic <dir>` only when a bound epic is given; an empty epic means the workspace (unbound leader).
function withEpic(base: string[], epic: string): string[] {
  return epic ? [...base, "--epic", epic] : base;
}

export const coxArgs = {
  // promptDrain attaches the workspace's (or one bound epic's) unread watcher wakes as turn context. Unbound (no
  // --epic) drains every active epic with a per-epic header; the extension injects the stdout into the turn. It also
  // resets the turn-boundary block budget keyed on ORCA_TERMINAL_HANDLE (threaded via the child env).
  // reopen marks a turn a stop-rewake reopen opened (not a user prompt): Pi runs prompt-drain on every turn, and the
  // Go block budget must bound a reopen episode, so such a turn keeps it (dogfood finding 8).
  promptDrain: (epic: string, reopen = false): string[] =>
    withEpic(reopen ? ["hook", "prompt-drain", "--reopen"] : ["hook", "prompt-drain"], epic),

  // stopRewake blocks while the leader is idle, then reopens the turn: `--harness pi` reopens via exit 2 with the
  // reopen/repair text on stderr (the extension reads the exit code itself) and, at MAX_WAIT, exits 0 instead of a tick
  // turn (the extension re-arms its own waiter). No --max: the Go side owns batch timing. Unbound (no --epic) waits on
  // every active epic and runs the turn-boundary watcher guard (item 1).
  // guard=false is the waiter armed by the settle of the guard's own follow-up (firstmate's once-per-logical-run latch):
  // it waits for wakes without running the turn-end guard again.
  stopRewake: (epic: string, guard = true): string[] =>
    withEpic(["hook", "stop-rewake", "--harness", "pi", ...(guard ? [] : ["--guard=false"])], epic),

  // precompact PERSISTS the checkpoint before Pi summarizes context: `cox hook precompact` writes
  // <epic>/handoffs/<story>.md, whereas `cox checkpoint facts` only prints the facts to stdout (which the extension
  // would discard, so the automatic checkpoint would never land). Uses the story worktree for the facts. Unbound
  // (empty epic/story) is the workspace path: the leader checkpoint per active epic.
  precompact: (epic: string, story: string, worktree: string): string[] => checkpointArgs("precompact", epic, story, worktree),

  // sessionStart injects the saved checkpoint on session start THROUGH the hook, which computes the current git HEAD from
  // the worktree so the CHECKPOINT STALE freshness check runs. `cox checkpoint inject` without --head has an empty HEAD
  // and silently suppresses that warning. Unbound (empty epic/story) is the workspace path.
  sessionStart: (epic: string, story: string, worktree: string): string[] => [
    ...checkpointArgs("session-start", epic, story, worktree),
    "--harness",
    "pi",
  ],

  // busyApply reports the harness-owned busy state (DESIGN wave-3 item 2): `cox busy apply <story> <state> --gen G
  // --source pi-ext --event E --epic <dir>`. The gen is the one armed at dispatch (COX_BUSY_GEN); a stale gen is
  // rejected by cox, so a hook that outlived its incarnation fails closed. Best-effort: a refusal never breaks Pi's
  // lifecycle.
  busyApply: (epic: string, story: string, state: "busy" | "idle", gen: string, event: string): string[] => [
    "busy",
    "apply",
    story,
    state,
    "--gen",
    gen,
    "--source",
    "pi-ext",
    "--event",
    event,
    "--epic",
    epic,
  ],

  // interruptWait blocks until a durable interrupt record appears for the story (DESIGN wave-3 item 4): exit 0 = an
  // interrupt arrived (the record is marked handled by cox), exit 3 = timeout. The extension spawns it per turn and
  // aborts the running turn on exit 0. It is the worker-side counterpart to the leader's `cox hook stop-rewake` child.
  interruptWait: (epic: string, story: string, max: string): string[] => [
    "inbox",
    "interrupt-wait",
    "--epic",
    epic,
    "--story",
    story,
    "--max",
    max,
  ],
};

// checkpointArgs builds a precompact/session-start hook argv: --epic and --story are included only when bound (a worker
// or a --epic-bound leader); an unbound leader omits both so the Go workspace path resolves every active epic and uses
// the per-epic leader checkpoint. --worktree is always passed for the HEAD-freshness check.
function checkpointArgs(name: "precompact" | "session-start", epic: string, story: string, worktree: string): string[] {
  const a = ["hook", name];
  if (epic) a.push("--epic", epic);
  if (story) a.push("--story", story);
  a.push("--worktree", worktree);
  return a;
}

// resolveEpic returns the epic binding for the extension. COX_EPIC in the environment wins (the launch seam sets it for
// a dispatched worker); otherwise it falls back to the epic marker installed next to the extension by
// `cox workspace hooks --harness pi --epic <dir>` (a BOUND leader). Empty when neither is available: an UNBOUND leader,
// whose hooks run in workspace mode (no --epic) so the Go side supervises every active epic.
export function resolveEpic(envEpic: string | undefined, markerContent: string | undefined): string {
  const e = (envEpic ?? "").trim();
  if (e) return e;
  return (markerContent ?? "").trim();
}
