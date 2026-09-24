#!/usr/bin/env bash
# Live E2E: a real steer round trip against a CODEX worker on the terminal plane (ADR 0012, M14 item 2). It proves the
# doorbell rings a codex composer - the exact gap the 2026-09-16 dogfood hit, where cox typed the ring only on an "empty"
# text composer and never recognized the codex footer, so a steer to an idle codex worker was skipped forever.
#
# Flow: dispatch a codex worker that writes a file, reports a status, then ENDS its turn (idles at the codex composer).
# The watcher's inbox ladder rings the steer doorbell into that idle composer; the doorbell must land (agents[] idle/done
# or the codex footer, not the unrecognized-composer skip), reopening the worker's turn so it acts on the steer and
# reports done. The script asserts the steer record moved into handled/ and a worker_done wake arrived.
#
# Guarded: runs only when COX_E2E=1. Terminal plane only (COX_PLANE is forced). Creates the epic in a temp dir under
# $TMPDIR, never in the repo. It leaves the temp branches story/<id> and epic/<slug> behind (rule: no tool deletes a
# branch); their names are printed for the tech lead, who prunes them. This is the tech lead's to run (a dispatched
# worker cannot spawn a sub-worker), after the codex leader hooks are installed and trusted.
#
#   COX_E2E=1 tests/e2e/codex-terminal-steer.sh
set -uo pipefail

[ "${COX_E2E:-}" = "1" ] || { echo "set COX_E2E=1 to run the live E2E"; exit 0; }

export COX_PLANE=terminal
COX="${COX:-$HOME/go/bin/cox}"
REPO="${REPO:-$HOME/Work/repo/nphattai/crewkit}"
CODEX_MODEL="${COX_CODEX_MODEL:-gpt-5.6-sol}"
TS="$(date +%s)"
SLUG="e2e-codex-$TS"
STORY_ID="e2e-codex-steer-$TS"   # unique per run so its branch story/<id> is never reused (A11)
WS="$(mktemp -d "${TMPDIR:-/tmp}/cox-e2e-codex.XXXXXX")"
PROJECT="smoke"
EPIC="$WS/$PROJECT/epics/$SLUG"
BASE="main"
WATCH_PID=""

pass=0 fail=0
step() { printf '\n=== %s ===\n' "$1"; }
ok()   { echo "PASS: $1"; pass=$((pass+1)); }
no()   { echo "FAIL: $1"; fail=$((fail+1)); }
cleanup() { [ -n "$WATCH_PID" ] && kill "$WATCH_PID" 2>/dev/null; }
trap cleanup EXIT

echo "cox    = $COX ($($COX version))"
echo "plane  = terminal"
echo "harness= codex ($CODEX_MODEL)"
echo "repo   = $REPO (base $BASE)"
echo "epic   = $EPIC"

# Workspace registry: the coxswain repo by absolute path (Orca has no name for it).
mkdir -p "$WS/cox" "$WS/$PROJECT"
cat > "$WS/cox/workspace.json" <<JSON
{
  "projects": [{ "name": "$PROJECT", "path": "$PROJECT" }],
  "repos": [{ "alias": "app", "path": "$REPO", "production": "$BASE" }],
  "services": [],
  "hosts": [{ "name": "local" }]
}
JSON
"$COX" workspace init --root "$WS" >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
step "1. cox epic new (--no-push)"
if "$COX" epic new "$PROJECT" "$SLUG" --repo app --no-push --root "$WS" && [ -d "$EPIC" ]; then
  ok "epic dir created"
else
  no "epic new"; echo "aborting"; exit 1
fi

mkdir -p "$EPIC/stories"
cat > "$EPIC/stories/$STORY_ID.md" <<STORY
---
id: $STORY_ID
repo: app
agent: codex
model: $CODEX_MODEL
title: e2e codex steer round trip (terminal plane) - idle, then act on a steer
---

# e2e codex steer (terminal plane)

Smoke story. Do NOT plan, do NOT commit, do NOT push. Work in your worktree. COX_PLANE=terminal, so report THROUGH COX
(never orca orchestration) and send NO heartbeat. Run exactly:

1. Write a file E2E.md in your worktree root with a single line: \`cox e2e ready $SLUG\`, then prove the M14 sandbox
   launch flags work from inside the codex sandbox:
   - \`git commit --allow-empty -m "e2e: sandbox can commit in a linked worktree"\` MUST succeed (git common dir writable).
   - \`go test ./cmd/cox -run TestBoardHandler\` MUST run (its httptest loopback bind proves network_access=true).
   If either fails, report it with \`cox story report status\` and stop.
2. Log a status: \`cox story report status --note "E2E.md ready, commit+httptest ok, idling for a steer" --epic $EPIC --story $STORY_ID\`.
3. Write a checkpoint so the leader can park you:
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
   ## Next action
   Idle; act on the steer when the doorbell arrives.
   CP
   \`\`\`
4. END YOUR TURN. Do NOT report done yet. When a leader instruction (doorbell) arrives, list \`$EPIC/inbox/$STORY_ID/*.msg\`
   in numeric order, read and act on each, then \`mv\` each handled file into \`$EPIC/inbox/$STORY_ID/handled/\`, and follow
   the instruction (it will tell you to append a line and report done).
STORY

# ---------------------------------------------------------------------------
step "2. cox story dispatch (codex worker)"
DLOG="$WS/dispatch.log"
"$COX" story dispatch $STORY_ID --epic "$EPIC" 2>&1 | tee "$DLOG"
[ "${PIPESTATUS[0]}" -eq 0 ] && ok "dispatched" || { no "dispatch"; echo "aborting"; exit 1; }

# The launch line must carry --model (codex worker default) and the codex approval flags.
HANDLE="$(grep -o '"Handle": *"[^"]*"' "$EPIC/.cox/sessions/$STORY_ID.json" 2>/dev/null | head -1 | grep -o 'term_[A-Za-z0-9_-]*')"
if [ -n "$HANDLE" ]; then
  SCREEN="$(orca terminal read --terminal "$HANDLE" --screen --json 2>/dev/null)"
  grep -q -- "--model" <<<"$SCREEN" && ok "launch carries --model" || no "launch missing --model (screen may have scrolled)"
  grep -qE -- "-a +never| -s +workspace-write" <<<"$SCREEN" && ok "launch carries codex approval flags" || no "launch missing codex approval flags"
else
  no "no worker terminal handle in the session file"
fi

# ---------------------------------------------------------------------------
# Start the watcher so its inbox ladder rings the steer doorbell into the (idle) codex composer.
step "3. start the watcher"
"$COX" watch --epic "$EPIC" --replace >"$WS/watch.log" 2>&1 &
WATCH_PID=$!
sleep 2
kill -0 "$WATCH_PID" 2>/dev/null && ok "watcher running (pid $WATCH_PID)" || no "watcher did not start"

# ---------------------------------------------------------------------------
# Wait for the worker to reach step 2 (a status wake), i.e. it has written E2E.md and is idling at the codex composer.
step "4. wait for the worker to idle (status wake)"
deadline=$((SECONDS + 600)); idled=0
while [ $SECONDS -lt $deadline ]; do
  remain=$((deadline - SECONDS)); [ $remain -lt 20 ] && remain=20
  "$COX" wake wait --max "${remain}s" --epic "$EPIC" >/dev/null 2>&1 || true
  if grep -q '"kind":"status"' "$EPIC/.cox/wake.jsonl" 2>/dev/null; then idled=1; break; fi
done
[ $idled -eq 1 ] && ok "worker reported status and idled" || no "no status wake within 10m"

# ---------------------------------------------------------------------------
step "5. steer the idle codex worker (the doorbell must ring the codex composer)"
STEER="append the line 'cox e2e steered $SLUG' to E2E.md in your worktree, then run: cox story report done --note 'steered done' --epic $EPIC --story $STORY_ID"
if "$COX" steer $STORY_ID "$STEER" --epic "$EPIC"; then
  ok "steer wrote an inbox record"
else
  no "cox steer failed"
fi

# ---------------------------------------------------------------------------
# The round trip: the watcher rings the codex composer, the worker reopens, acts, and reports done. If the doorbell were
# skipped (the pre-M14 bug), no worker_done would ever arrive and the steer record would sit unhandled.
step "6. wait for worker_done (the steer round trip completed)"
deadline=$((SECONDS + 900)); done_ok=0
while [ $SECONDS -lt $deadline ]; do
  remain=$((deadline - SECONDS)); [ $remain -lt 30 ] && remain=30
  "$COX" wake wait --max "${remain}s" --epic "$EPIC" >/dev/null 2>&1 || true
  if grep -q '"kind":"worker_done"' "$EPIC/.cox/wake.jsonl" 2>/dev/null; then done_ok=1; break; fi
done
[ $done_ok -eq 1 ] && ok "worker_done after steer: the doorbell reached the codex worker" || no "no worker_done within 15m (doorbell likely did not ring the codex composer)"

# ---------------------------------------------------------------------------
step "7. steer record moved into handled/ (worker acked)"
if ls "$EPIC"/inbox/$STORY_ID/handled/*.msg >/dev/null 2>&1; then
  ok "steer record in handled/"
else
  no "steer record not handled (worker never acted on the steer)"
fi

# Best-effort: the steered line landed in E2E.md in the worker's worktree.
step "8. steered line in E2E.md (best-effort)"
WT="$(git -C "$REPO" worktree list --porcelain 2>/dev/null | awk -v b="refs/heads/story/$STORY_ID" '/^worktree /{p=$2} $0=="branch "b{print p}')"
if [ -n "$WT" ] && grep -q "cox e2e steered $SLUG" "$WT/E2E.md" 2>/dev/null; then
  ok "E2E.md carries the steered line"
else
  no "steered line not found in E2E.md (worktree $WT; screen may lag - inspect manually)"
fi

# ---------------------------------------------------------------------------
step "9. cox epic close --yes --force"
cleanup; WATCH_PID=""
"$COX" epic close --epic "$EPIC" --yes --force && ok "epic closed" || no "close failed"
for br in "story/$STORY_ID" "epic/$SLUG"; do
  [ -n "$(git -C "$REPO" branch --list "$br")" ] && ok "branch kept: $br" || no "branch missing: $br"
done

echo
echo "leftover temp branches (tech lead prunes; a tool never deletes a branch):"
echo "  story/$STORY_ID"
echo "  epic/$SLUG"
echo "workspace temp dir (safe to delete): $WS"
echo
echo "RESULT: $pass passed, $fail failed"
[ $fail -eq 0 ]
