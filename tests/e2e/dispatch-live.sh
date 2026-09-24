#!/usr/bin/env bash
# Live E2E for cox against the real Orca backend, on the coxswain repo itself (an Orca repo with no name, so the path
# selector is exercised). It runs the full dispatch lifecycle with a real claude worker and prints PASS/FAIL per step.
#
# Guarded: it only runs when COX_E2E=1 (the //go:build orca equivalent). It creates the epic in a temp dir under
# $TMPDIR, NOT in the repo. It leaves the temp branches story/e2e-hello-<ts> and epic/e2e-<ts> behind (rule: no tool
# deletes a branch); their names are printed at the end for the tech lead, who prunes them.
#
# NOTE: the story id is unique per run (e2e-hello-<ts>), so its branch story/e2e-hello-<ts> never already exists. This
# is deliberate since A11: WorktreeCreate now brings a reused existing branch to the requested base and refuses one that
# is ahead of base, so a fixed story id whose leftover branch had advanced would make WorktreeCreate refuse the dispatch.
# A fresh id per run sidesteps that; the reused-branch reset path is covered by the orca adapter unit tests instead.
#
#   COX_E2E=1 tests/e2e/dispatch-live.sh                    # orchestration plane (default)
#   COX_E2E=1 COX_PLANE=terminal tests/e2e/dispatch-live.sh # terminal plane (ADR 0012)
#
# TERMINAL PLANE (COX_PLANE=terminal): the whole run uses backend.orca.plane=terminal (COX_PLANE is exported, so every
# cox command and the spawned worker use it). The smoke story adds the M10 channels: the worker asks a question with
# `cox story report question`, this script replies with `cox reply <story> qNNN`, the worker unblocks via
# `cox question wait`, and the worker reports completion TWICE with `cox story report done` (no worker_done cap on this
# plane) - the run asserts two worker_done wakes and that the reply reached the worker. This script is the tech lead's
# to run: a worker cannot dispatch a sub-worker (Orca depth limit), so the dispatched worker of M10 does NOT run it; it
# only prepared and verified the script shape. Run it on claude AND repeat with a real codex story before the default
# flips to terminal.
set -uo pipefail

[ "${COX_E2E:-}" = "1" ] || { echo "set COX_E2E=1 to run the live E2E"; exit 0; }

PLANE="${COX_PLANE:-orchestration}"
[ "$PLANE" = "terminal" ] && export COX_PLANE=terminal
COX="${COX:-$HOME/go/bin/cox}"
REPO="${REPO:-$HOME/Work/repo/nphattai/crewkit}"
TS="$(date +%s)"
SLUG="e2e-$TS"
STORY_ID="e2e-hello-$TS"   # unique per run so its branch story/<id> is never reused (A11 would refuse an advanced one)
WS="$(mktemp -d "${TMPDIR:-/tmp}/cox-e2e.XXXXXX")"
PROJECT="smoke"
EPIC="$WS/$PROJECT/epics/$SLUG"
BASE="main"

pass=0 fail=0
step() { printf '\n=== %s ===\n' "$1"; }
ok()   { echo "PASS: $1"; pass=$((pass+1)); }
no()   { echo "FAIL: $1"; fail=$((fail+1)); }
# wd_gen: highest gen among worker_done wakes in wake.jsonl (0 if none). Used to tell attempt 2's worker_done from
# attempt 1's - a new one has a strictly larger gen.
wd_gen() {
  local g
  g="$(grep '"kind":"worker_done"' "$EPIC/.cox/wake.jsonl" 2>/dev/null \
    | grep -o '"gen": *[0-9]*' | grep -o '[0-9]*' | sort -n | tail -1)"
  echo "${g:-0}"
}

echo "cox   = $COX ($($COX version))"
echo "plane = $PLANE"
echo "repo  = $REPO (base $BASE)"
echo "ws    = $WS"
echo "epic  = $EPIC"

# Workspace registry: the coxswain repo by absolute path (Orca has no name for it), production = main.
mkdir -p "$WS/cox" "$WS/$PROJECT"
cat > "$WS/cox/workspace.json" <<JSON
{
  "projects": [{ "name": "$PROJECT", "path": "$PROJECT" }],
  "repos": [{ "alias": "app", "path": "$REPO", "production": "$BASE" }],
  "services": [],
  "hosts": [{ "name": "local" }]
}
JSON
"$COX" workspace init --root "$WS" >/dev/null 2>&1 || true  # writes policy.json if missing

# ---------------------------------------------------------------------------
step "1. cox epic new (--no-push)"
if "$COX" epic new "$PROJECT" "$SLUG" --repo app --no-push --root "$WS" && [ -d "$EPIC" ]; then
  ok "epic dir created with worktrees + symlinks"
else
  no "epic new"; echo "aborting"; exit 1
fi

# The smoke story: write one line, log status, write a matching checkpoint, then report completion. No plan, no commit.
# The completion step differs by plane: orchestration uses the Orca worker_done from the task preamble; terminal uses
# cox story report (asking a question first, then reporting done twice - no worker_done cap on this plane).
mkdir -p "$EPIC/stories"
CHECKPOINT_STEP="3. Write a checkpoint so the leader can park you. Run:
   \`\`\`
   H=\$(git rev-parse HEAD)
   cat > $EPIC/handoffs/$STORY_ID.md <<CP
   ---
   schema: coxswain.checkpoint.v1
   story: $STORY_ID
   attempt: 1
   head: \$H
   base: origin/epic/$SLUG@\$H
   written_at: \$(date -u +%Y-%m-%dT%H:%M:%SZ)
   reason: park
   ---
   ## Intent
   Write E2E.md and report (smoke).
   ## Next action
   Nothing; smoke complete.
   CP
   \`\`\`"

if [ "$PLANE" = "terminal" ]; then
cat > "$EPIC/stories/$STORY_ID.md" <<STORY
---
id: $STORY_ID
repo: app
agent: claude
model: claude-opus-4-8
title: e2e smoke (terminal plane) - ask, then write one file and report
---

# e2e smoke (terminal plane)

This is a smoke story. Do NOT plan, do NOT commit, do NOT push. Work in your worktree. COX_PLANE=terminal, so report
THROUGH COX (never orca orchestration) and send NO heartbeat. Run exactly:

1. Ask one question and wait for the answer:
   \`\`\`
   QID=\$(cox story report question --body "e2e: which single line should E2E.md contain?" --epic $EPIC --story $STORY_ID)
   cox question wait \$QID --max 15m --epic $EPIC --story $STORY_ID
   \`\`\`
   Write the answer text into E2E.md in your worktree root.
2. Log a status: \`cox story report status --note "E2E.md written" --epic $EPIC --story $STORY_ID\`.
$CHECKPOINT_STEP
4. Report completion TWICE (this plane has no worker_done cap):
   \`\`\`
   cox story report done --note "e2e smoke complete (first)" --epic $EPIC --story $STORY_ID
   cox story report done --note "e2e smoke complete (second)" --epic $EPIC --story $STORY_ID
   \`\`\`

Then end your turn.
STORY
else
cat > "$EPIC/stories/$STORY_ID.md" <<STORY
---
id: $STORY_ID
repo: app
agent: claude
model: claude-opus-4-8
title: e2e smoke - write one file and report
---

# e2e smoke

This is a smoke story. Do NOT plan, do NOT ask, do NOT commit, do NOT push. Work in your worktree. Run exactly:

1. Write a file E2E.md in your worktree root with a single line: \`cox e2e ok $SLUG\`.
2. Log a status: \`cox status done "E2E.md written" --epic $EPIC --story $STORY_ID\`.
$CHECKPOINT_STEP
4. Send worker_done using the orchestration send command from your task preamble (type worker_done, a one-line body).

Then end your turn.
STORY
fi

# ---------------------------------------------------------------------------
step "2. cox story dispatch"
DLOG="$WS/dispatch.log"
"$COX" story dispatch $STORY_ID --epic "$EPIC" 2>&1 | tee "$DLOG"
if [ "${PIPESTATUS[0]}" -eq 0 ]; then
  ok "dispatched"
else
  no "story dispatch"; echo "aborting"; exit 1
fi

# ---------------------------------------------------------------------------
# Terminal plane only (M10b): the launch command typed into the worker terminal must carry --model and the policy
# approval flag, and the spawn must NOT print the old "not confirmed busy within 20s" warning (the 60s window now
# confirms on the agents[] entry). Read the worker terminal's screen for the launch line, and grep the dispatch log.
if [ "$PLANE" = "terminal" ]; then
  step "2a. launch command has --model + approval flag; no 20s confirm warning"
  # Old 20s composer-only warning must be gone. The 60s window may still warn on a genuinely slow start, but never at 20s.
  if grep -q "not confirmed busy within 20s" "$DLOG" || grep -q "not confirmed within 20s" "$DLOG"; then
    no "spawn still warns at the 20s window (should be the 60s agents[] window)"
  else
    ok "no 20s confirm warning in the dispatch log"
  fi
  # Resolve the worker terminal handle from the session file and read its screen.
  HANDLE="$(grep -o '"Handle": *"[^"]*"' "$EPIC/.cox/sessions/$STORY_ID.json" 2>/dev/null | head -1 | grep -o 'term_[A-Za-z0-9_-]*')"
  if [ -n "$HANDLE" ]; then
    SCREEN="$(orca terminal read --terminal "$HANDLE" --screen --json 2>/dev/null)"
    if grep -q -- "--model" <<<"$SCREEN"; then
      ok "launch command carries --model"
    else
      no "launch command missing --model (screen may have scrolled; check the terminal)"
    fi
    # claude uses --permission-mode; codex uses -a never -s workspace-write. Accept either shape.
    if grep -qE -- "--permission-mode|-a +never| -s +workspace-write" <<<"$SCREEN"; then
      ok "launch command carries the approval flag"
    else
      no "launch command missing the approval flag (screen may have scrolled; check the terminal)"
    fi
  else
    no "no worker terminal handle in the session file to read the launch line"
  fi
fi

# ---------------------------------------------------------------------------
# Terminal plane only: the worker asks a question first (report question -> input_required wake with evidence.question).
# Answer it with the file-based reply so the worker's `cox question wait` unblocks.
if [ "$PLANE" = "terminal" ]; then
  step "2b. answer the worker's question (report question -> cox reply -> question wait)"
  qdeadline=$((SECONDS + 600)); QID=""
  while [ $SECONDS -lt $qdeadline ]; do
    remain=$((qdeadline - SECONDS)); [ $remain -lt 20 ] && remain=20
    "$COX" wake wait --max "${remain}s" --epic "$EPIC" >/dev/null 2>&1 || true
    QID="$(grep '"kind":"input_required"' "$EPIC/.cox/wake.jsonl" 2>/dev/null \
      | grep -o '"question": *"[^"]*"' | grep -o 'q[0-9]\+' | tail -1)"
    [ -n "$QID" ] && break
  done
  if [ -n "$QID" ]; then
    if "$COX" reply $STORY_ID "$QID" "cox e2e ok $SLUG" --epic "$EPIC"; then
      ok "replied to $QID (answer file + inbox record + ring)"
    else
      no "cox reply $QID failed"
    fi
  else
    no "no input_required question wake within 10m"
  fi
fi

# ---------------------------------------------------------------------------
step "3. wait for worker_done (cox wake wait --max 15m)"
deadline=$((SECONDS + 900))
found=0
while [ $SECONDS -lt $deadline ]; do
  remain=$((deadline - SECONDS)); [ $remain -lt 30 ] && remain=30
  "$COX" wake wait --max "${remain}s" --epic "$EPIC" >/dev/null 2>&1 || true
  if grep -q '"kind":"worker_done"' "$EPIC/.cox/wake.jsonl" 2>/dev/null; then found=1; break; fi
done
if [ $found -eq 1 ]; then ok "worker_done wake observed"; else no "no worker_done within 15m"; fi

# Terminal plane: the story reports done twice, so there must be two worker_done wakes (no completion cap on this plane).
if [ "$PLANE" = "terminal" ]; then
  step "3b. two worker_done wakes (no cap on the terminal plane)"
  # Give the second report a moment to land after the first was observed. `wake wait` returns as soon as ANY unacked
  # wake exists, so it cannot be used as the delay here; poll the durable queue until two worker_done rows are present.
  n_done=0
  for _ in $(seq 1 60); do
    n_done="$(grep -c '"kind":"worker_done"' "$EPIC/.cox/wake.jsonl" 2>/dev/null || echo 0)"
    [ "$n_done" -ge 2 ] && break
    sleep 1
  done
  [ "$n_done" -ge 2 ] && ok "two worker_done wakes ($n_done)" || no "expected >=2 worker_done wakes, got $n_done"
fi

# ---------------------------------------------------------------------------
step "4. cox state --json"
STATE_JSON="$("$COX" state $STORY_ID --epic "$EPIC" --json 2>/dev/null)"
echo "$STATE_JSON" | head -c 400; echo
if grep -q '"state": *"working"' <<<"$STATE_JSON"; then
  ok "state is working (dispatched, not yet merged)"
else
  no "unexpected state"
fi

# ---------------------------------------------------------------------------
step "5. cox steer --fyi and inbox file"
"$COX" steer $STORY_ID "FYI e2e steer $TS" --epic "$EPIC" --fyi >/dev/null 2>&1
if ls "$EPIC"/inbox/$STORY_ID/*.msg >/dev/null 2>&1; then
  ok "steer wrote an inbox record"
else
  no "no inbox record after steer"
fi

# ---------------------------------------------------------------------------
step "6. cox story park (checkpoint present)"
# Short park-wait so a checkpoint mismatch (e.g. sha short/long drift) fails in ~2m instead of the 20m default.
PW="${COX_PARK_WAIT:-120s}"
if [ -f "$EPIC/handoffs/$STORY_ID.md" ]; then
  if "$COX" story park $STORY_ID --epic "$EPIC" --park-wait "$PW"; then
    ok "parked with a matching checkpoint"
  else
    no "park failed (waited $PW for a matching checkpoint)"
  fi
else
  no "worker did not write a checkpoint; skipping park"
fi

# ---------------------------------------------------------------------------
step "7. cox story resume -> attempt 2"
PREV_WD_GEN="$(wd_gen)"  # attempt 1's worker_done gen; attempt 2 must land a strictly larger one
if "$COX" story resume $STORY_ID --epic "$EPIC"; then
  A="$("$COX" state $STORY_ID --epic "$EPIC" --json 2>/dev/null | grep -o '"attempt": *[0-9]*' | head -1 | grep -o '[0-9]*')"
  echo "attempt = $A"
  [ "$A" = "2" ] && ok "resume bumped attempt to 2" || no "attempt is $A, want 2"
else
  no "resume failed"
fi

# ---------------------------------------------------------------------------
# Orca needs a few seconds to spin the resumed worker up. Closing while it is still starting makes Runtime.Stop return
# an error (the terminal is mid-launch), which is exactly the flake that broke close in run 2. So wait for attempt 2 to
# actually finish - a NEW worker_done wake whose gen exceeds attempt 1's - before closing, up to 10 minutes.
step "7b. wait for attempt 2 worker_done (gen > $PREV_WD_GEN)"
deadline=$((SECONDS + 600))
found2=0
while [ $SECONDS -lt $deadline ]; do
  remain=$((deadline - SECONDS)); [ $remain -lt 30 ] && remain=30
  "$COX" wake wait --max "${remain}s" --epic "$EPIC" >/dev/null 2>&1 || true
  [ "$(wd_gen)" -gt "$PREV_WD_GEN" ] && { found2=1; break; }
done
[ $found2 -eq 1 ] && ok "attempt 2 worker_done observed" || no "attempt 2 worker_done observed (waited 10m)"

# ---------------------------------------------------------------------------
step "8. cox epic close --yes --force"
# --force removes the (dirty/unpushed) worktrees; the branches are kept (detach, never delete).
if "$COX" epic close --epic "$EPIC" --yes --force; then
  ok "close completed and archived .cox"
else
  no "close failed"
fi
[ -d "$EPIC/.cox.closed" ] && ok "archived .cox -> .cox.closed" || no ".cox not archived"

# Branches must survive.
for br in "story/$STORY_ID" "epic/$SLUG"; do
  if [ -n "$(git -C "$REPO" branch --list "$br")" ]; then ok "branch kept: $br"; else no "branch missing: $br"; fi
done

# ---------------------------------------------------------------------------
echo
echo "leftover temp branches (rule: tool never deletes a branch; tech lead removes):"
echo "  story/$STORY_ID"
echo "  epic/$SLUG"
echo "workspace temp dir (safe to delete): $WS"
echo
echo "RESULT: $pass passed, $fail failed"
[ $fail -eq 0 ]
