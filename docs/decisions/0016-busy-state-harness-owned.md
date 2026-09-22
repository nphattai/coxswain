# 0016 - Busy state is harness-owned, with a source trust table and versioned records

- Status: Accepted (captain, 2026-09-21)
- Date: 2026-09-21

## Context

The firstmate deep-dive (`research/reports/coxswain-vs-firstmate.md` §1 point 3, §2.1 rows "Busy record", "Who writes
busy", "Busy vs pane heuristics", §2.4 "Unguarded records") names three disciplines coxswain lacked around the
idle/busy fact a leader reads before it rings a worker:

- **One writer, a generation, and per-harness trusted sources.** Firstmate's `bin/fm-busy-lib.sh:1-60` makes idle/busy a
  record the harness owns: one writer, a `gen` minted per incarnation and a monotonic `seq`, and (`:140-215`,
  `fm_busy_sources_for_harness`, the Codex gate) a per-harness list of trusted sources - a source the harness does not
  list can never write the record, and an unknown state is never treated as idle. Coxswain had the record, the gen, and
  the seq (busy.v1, wave-1 dogfood), but no trust table: any `--source` could Apply, and a Claude worker never wrote a
  record at all (only the Pi extension did), so on Orca a Claude worker's idle/busy came from a TUI classifier the
  backend often could not read (BACKLOG B-36, B-02; `epics/pi-harness/reports/leader-findings.md`).
- **Records versioned by attempt.** `sessions/<story>.json`, `wt/<story>` and `.cox/leader` were plain writes with no
  incarnation marker, so a straggler write from a prior attempt (after a relaunch bumped the story to a new attempt)
  could clobber the newer record - the latent session-clobber the report flags under "Unguarded records".
- **The steer budget spanned attempts.** The 5-steer budget (`internal/protocol/inbox`) counted every budget-bearing
  record ever written, so a re-dispatched worker inherited the previous attempt's spent budget and a fresh steer was
  refused (BACKLOG B-23).

## Decision

Idle/busy is a fact the harness reports, for every harness, on every backend; the runtime records around it are
versioned by attempt; and the steer budget counts only the current attempt.

1. **Every harness reports its own busy state.** The Claude capability card gains `BusyRecord: true`, and dispatch,
   resume, and relaunch write worker hooks into the worktree's `.claude/settings.local.json` (the per-checkout,
   not-committed settings slot, excluded via the worktree's `info/exclude` when it is not already gitignored, so the
   runtime hooks never dirty the story worktree or touch a repo's tracked `.claude/settings.json`): `UserPromptSubmit`
   Applies
   `busy`, `Stop` Applies `idle`, `SessionEnd` retires the record. Each command runs `${COX_BIN:-cox} busy apply|retire`
   with the incarnation gen from `$COX_BUSY_GEN` and ends with `|| true`, so a refused Apply never breaks the harness
   turn and a worker runs the same cox that launched it. Codex is armed only behind policy `harness.busy_verified`
   (default false), which vouches that a `codex-hook` writer is wired; until then codex is not armed and a backend falls
   back to its own signal. No consumer branches on a harness name: the card's `BusyRecord` and the one policy flag
   decide (decision 0008).
2. **A per-harness source trust table.** Each card carries `BusySources` - the harness's own hook source plus the
   leader-side `dispatch`, `interrupt`, and `recovery` writers. `busy.Arm` stamps the harness and its trusted sources on
   the record; `busy.Apply` rejects a source the record does not list (`source %q not trusted for harness %q`), and
   `busy.Read` classifies a record whose current source is untrusted as `unknown`. A record a harness did not write
   never classifies its story - fail closed, both on write and on read.
3. **Retire is exact-gen.** `busy.Retire(epic, story, gen)` removes the record under the writer lock only when the
   presented gen matches the armed incarnation, so a `SessionEnd` hook or a leader release (`releaseStory` on
   done/fail/cancel, `control park`) retires its own incarnation and never clobbers a newer one a re-arm minted in
   between.
4. **A busy record that stays busy too long nudges the leader.** The watcher's `BusyTurnMax` pass (default 60 min,
   policy override `watch.busy_turn_max_min`) raises one routine `status` wake per window for a working story whose busy
   record has said busy with no fresh busy event and no fresh checkpoint. It is a nudge, never an interrupt: a long but
   legitimate turn is not a runaway.
5. **Runtime records are versioned by attempt.** `sessions/<story>.json` and `wt/<story>` carry the attempt that wrote
   them and are written by temp + rename; a write whose attempt is lower than the attempt already on disk is dropped
   with a stderr note. `.cox/leader` becomes a JSON record `{handle, pid, ts}` written by temp + rename; a single reader
   (`state.LeaderHandle`) accepts both the JSON form and the legacy plain-handle text, and every reader (hooks, watcher,
   epic close, doctor) routes through it while every writer emits JSON.
6. **The steer budget counts only the current attempt.** `inbox.Write` counts budget-bearing steer records whose
   timestamp is at or after the current attempt's dispatch (the latest working-by-leader event); older records are
   listed by `inbox.History` and reported by `cox steer` as history from a previous attempt, rather than refusing a
   fresh steer over records the current worker never saw.

## Consequences

- A Claude worker dispatched on Orca is seen idle/busy through the harness-owned record even when the backend's own TUI
  classifier or Orca's agent-status hooks cannot read it, so a steer reaches it on the harness-owned idle (DESIGN AC 2).
- The trust table is defence in depth over the gen: the gen already rejects a hook that outlived its incarnation, and
  the source trust additionally rejects a record a different harness wrote, so a cross-harness reroute cannot leave a
  stale record classifying the new incarnation.
- Codex busy state ships off. A captain flips `harness.busy_verified` only after a `codex-hook` writer is verified; the
  card lists `codex-hook` in its trust table but it is inert until arming is enabled, so no half-wired codex worker is
  ever stranded "busy" in a record nothing clears.
- The busy record's read-side trust check is self-contained (the record carries its own trust table), so every consult
  site - the Orca and herdr `BusyComposer`, the watcher's `composerState` and `busyTurnMaxPass` - enforces it without
  threading the harness into the backend layer, keeping the backend harness-agnostic (decision 0002).
- The `watch.busy_turn_max_min` and `harness.busy_verified` policy keys are additive: an epic policy without them keeps
  the code defaults (60 min, codex off), so no existing policy is invalidated.

## References

- `research/reports/coxswain-vs-firstmate.md` §1 point 3, §2.1 (rows "Busy record", "Who writes busy", "Busy vs pane
  heuristics"), §2.4 ("Unguarded records").
- firstmate `bin/fm-busy-lib.sh:1-60` (one writer, gen + seq, unknown never idle), `:140-215`
  (`fm_busy_sources_for_harness`, the Codex gate); `bin/fm-busy-event.sh:1-40` (arm/apply/progress/retire).
- BACKLOG rows B-36, B-02, B-23; `epics/pi-harness/reports/leader-findings.md`, `reports/pi-dogfood-summary.md`.
