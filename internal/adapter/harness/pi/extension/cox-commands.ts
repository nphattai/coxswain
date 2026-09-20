// Pure builders for the cox argv the extension runs, and the epic-binding resolver. Factored out of the Pi API wiring
// (cox-pi.ts) so the command choices and the epic fallback are deterministically testable.

export const coxArgs = {
  // wakeWait blocks until a durable wake (exit 0), a timeout (exit 3), or an unexpected close.
  wakeWait: (epic: string, max: string): string[] => ["wake", "wait", "--max", max, "--epic", epic],

  // precompact PERSISTS the checkpoint before Pi summarizes context: `cox hook precompact` writes
  // <epic>/handoffs/<story>.md, whereas `cox checkpoint facts` only prints the facts to stdout (which the extension
  // would discard, so the automatic checkpoint would never land). Uses the story worktree for the facts.
  precompact: (epic: string, story: string, worktree: string): string[] => [
    "hook",
    "precompact",
    "--epic",
    epic,
    "--story",
    story,
    "--worktree",
    worktree,
  ],

  // checkpointInject prints the saved checkpoint (recovery context) for injection on session start.
  checkpointInject: (epic: string, story: string): string[] => [
    "checkpoint",
    "inject",
    "--epic",
    epic,
    "--story",
    story,
  ],
};

// resolveEpic returns the epic binding for the extension. COX_EPIC in the environment wins (the launch seam sets it for
// a dispatched worker); otherwise it falls back to the epic marker installed next to the extension by
// `cox workspace hooks --harness pi --epic <dir>` (a leader whose environment does not carry COX_EPIC). Empty when
// neither is available, so the caller can keep wake supervision idle rather than run against the wrong epic.
export function resolveEpic(envEpic: string | undefined, markerContent: string | undefined): string {
  const e = (envEpic ?? "").trim();
  if (e) return e;
  return (markerContent ?? "").trim();
}
