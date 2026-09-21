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

  // sessionStart injects the saved checkpoint on session start THROUGH the hook, which computes the current git HEAD from
  // the worktree so the CHECKPOINT STALE freshness check runs. `cox checkpoint inject` without --head has an empty HEAD
  // and silently suppresses that warning.
  sessionStart: (epic: string, story: string, worktree: string): string[] => [
    "hook",
    "session-start",
    "--epic",
    epic,
    "--story",
    story,
    "--worktree",
    worktree,
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
