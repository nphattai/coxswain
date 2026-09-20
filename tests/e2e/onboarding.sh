#!/usr/bin/env bash
# Hermetic onboarding E2E (DESIGN §7): a person with cox, Orca and one harness goes from an empty folder plus an existing
# git checkout to a dispatched story, and `cox doctor` reports the setup truthfully; then, on a fresh clone that has the
# epic dir but no .cox, `cox epic attach` brings it back and doctor is clean again.
#
# It is hermetic so it runs in CI: HOME and the roots are temp dirs, and PATH holds ONLY cox plus a fake `orca` and a
# fake `claude` (the harness). The fake orca creates real git worktrees and returns benign JSON for terminals; the fake
# claude is a never-run stub (the worker is not exercised here - dispatch-live.sh covers a real worker). No network, no
# real Orca, no real harness.
set -uo pipefail

COX="${COX:-$PWD/dist/cox}"
[ -x "$COX" ] || { echo "build the candidate first: make build (looked for $COX)"; exit 1; }
COX="$(cd "$(dirname "$COX")" && pwd)/$(basename "$COX")"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/cox-onboarding.XXXXXX")"
TMP="$(cd "$TMP" && pwd -P)"   # canonicalise (TMPDIR may carry a trailing slash; doctor prints cleaned paths)
export HOME="$TMP/home"                       # temp HOME so trust writes and default roots are hermetic
export ORCA_WORKSPACES="$TMP/orca-empty"      # a default root that stays empty
export ORCA_RUN_ID="run_onboarding"           # a backend run id so cox never shells out to create one
export COX_SPAWN_CONFIRM="1s"                 # the spawn confirm window is best-effort; keep it short
mkdir -p "$HOME" "$ORCA_WORKSPACES"
WS="$TMP/ws"                                  # the workspace, deliberately NOT under $HOME/Work
REPO="$TMP/throwaway-repo"                    # one throwaway git checkout
WTBASE="$TMP/worktrees"
BIN="$TMP/bin"
mkdir -p "$WTBASE" "$BIN"

pass=0 fail=0
step() { printf '\n=== %s ===\n' "$1"; }
ok()   { echo "PASS: $1"; pass=$((pass+1)); }
no()   { echo "FAIL: $1"; fail=$((fail+1)); }

cleanup() {
  pkill -f "cox watch --epic $WS" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

# --- a throwaway git repo with a default branch main --------------------------------------------------------------
git init -q -b main "$REPO"
( cd "$REPO"
  git config user.email t@t; git config user.name t
  echo hi > README.md; git add -A; git commit -q -m init )

# --- fake orca: real git worktrees, benign JSON for run/terminal ---------------------------------------------------
cat > "$BIN/orca" <<'ORCA'
#!/usr/bin/env bash
# Minimal Orca stub for the onboarding E2E. It implements exactly the subcommands cox invokes in
# init -> epic new -> stories -> dispatch -> attach, backing worktree ops with real git.
sub="${1:-}"; verb="${2:-}"
arg() { # arg <flag> <args...> : echo the value following <flag>
  local want="$1"; shift
  local prev=""
  for a in "$@"; do [ "$prev" = "$want" ] && { echo "$a"; return; }; prev="$a"; done
}
# Every orca command returns the {"ok":true,"result":<payload>} envelope cox unwraps.
case "$sub $verb" in
  "status "*|"status") exit 0 ;;
  "orchestration run-create") echo '{"ok":true,"result":{"run":{"id":"run_onboarding"}}}' ;;
  "orchestration check"|"orchestration worker-list") echo '{"ok":true,"result":{"workers":[]}}' ;;
  "worktree create")
    repo="$(arg --repo "$@")"; repo="${repo#path:}"
    branch="$(arg --name "$@")"; base="$(arg --base-branch "$@")"
    path="$WTBASE_ENV/$(echo "$branch" | tr '/' '-')"
    if git -C "$repo" show-ref --verify --quiet "refs/heads/$branch"; then
      git -C "$repo" worktree add "$path" "$branch" >/dev/null 2>&1
    else
      git -C "$repo" worktree add -b "$branch" "$path" "$base" >/dev/null 2>&1
    fi
    printf '{"ok":true,"result":{"worktree":{"path":"%s","branch":"%s"}}}\n' "$path" "$branch" ;;
  "worktree ps") echo '{"ok":true,"result":{"worktrees":[]}}' ;;
  "terminal create") echo '{"ok":true,"result":{"terminal":{"handle":"term_fake"}}}' ;;
  *) echo '{"ok":true,"result":{}}' ;;
esac
exit 0
ORCA
sed -i.bak "s#\$WTBASE_ENV#$WTBASE#g" "$BIN/orca" && rm -f "$BIN/orca.bak"
chmod +x "$BIN/orca"

# --- fake claude: a never-run harness stub (present so doctor's default-harness check passes) ----------------------
printf '#!/usr/bin/env bash\nexit 0\n' > "$BIN/claude"
chmod +x "$BIN/claude"

# The candidate cox, the fake orca and the fake claude are the only cox/orca/harness tools on PATH; /usr/bin:/bin provide
# git and coreutils. No dev-machine dir (where a real claude/codex/orca/cox might live) is included.
ln -sf "$COX" "$BIN/cox"
export PATH="$BIN:/usr/bin:/bin"

# ------------------------------------------------------------------------------------------------------------------
step "1. cox workspace init (registry, hooks, skills, AGENTS.md come with it)"
if "$COX" workspace init --root "$WS" --repo "app=$REPO"; then ok "init"; else no "init"; echo abort; exit 1; fi
[ -f "$WS/cox/workspace.json" ] && ok "workspace.json written" || no "workspace.json missing"
[ -f "$WS/.claude/settings.json" ] && grep -q "cox hook prompt-drain" "$WS/.claude/settings.json" && ok "claude hooks written" || no "claude hooks missing"
[ -f "$WS/.codex/hooks.json" ] && ok "codex hooks written" || no "codex hooks missing"
[ -f "$WS/.agents/skills/cox-epic/SKILL.md" ] && ok "leader skills written" || no "skills missing"
[ -f "$WS/AGENTS.md" ] && ok "AGENTS.md written" || no "AGENTS.md missing"
grep -q "cox/workspace.json" "$WS/.gitignore" && ok ".gitignore written" || no ".gitignore missing"
# Idempotent re-run.
"$COX" workspace init --root "$WS" >/dev/null && ok "init idempotent" || no "init re-run failed"

step "2. cox epic new --no-push"
if "$COX" epic new proj hello --repo app --no-push --root "$WS"; then ok "epic new"; else no "epic new"; echo abort; exit 1; fi
EPIC="$WS/proj/epics/hello"
[ -f "$EPIC/DESIGN.md" ] && [ -L "$EPIC/app" ] && ok "epic dir + alias symlink" || no "epic layout wrong"

step "3. cox epic stories"
"$COX" epic stories --epic "$EPIC" >/dev/null && [ -f "$EPIC/stories/hello-app.md" ] && ok "story rendered" || no "stories failed"

step "4. cox story dispatch"
if "$COX" story dispatch hello-app --epic "$EPIC" >/dev/null 2>&1; then ok "dispatched"; else no "dispatch failed"; fi

step "5. cox doctor exits 0 and lists workspace, epic, watcher, hooks"
OUT="$("$COX" doctor --root "$WS" --epic "$EPIC" 2>&1)"; code=$?
echo "$OUT"
[ $code -eq 0 ] && ok "doctor exit 0" || no "doctor exit $code (want 0)"
echo "$OUT" | grep -q "workspace $WS" && ok "workspace listed" || no "workspace not listed"
echo "$OUT" | grep -q "epic hello" && ok "epic listed" || no "epic not listed"
echo "$OUT" | grep -q "watcher" && ok "watcher listed" || no "watcher not listed"
echo "$OUT" | grep -q "hooks:" && ok "hooks listed" || no "hooks not listed"

step "6. discard only .cox, cox epic attach reuses the surviving clean worktree"
pkill -f "cox watch --epic $WS" 2>/dev/null || true
REUSE_TGT="$(readlink "$EPIC/app")"
rm -rf "$EPIC/.cox"
"$COX" epic attach --epic "$EPIC" >/dev/null 2>&1 && ok "attach (reuse)" || no "attach reuse failed"
[ -f "$EPIC/.cox/epic.json" ] && ok ".cox recreated (reuse)" || no ".cox not recreated (reuse)"
[ "$(readlink "$EPIC/app")" = "$REUSE_TGT" ] && ok "reused the surviving worktree" || no "reuse changed the worktree symlink"

step "7. discard .cox and the worktree (fresh clone), cox epic attach, doctor clean again"
pkill -f "cox watch --epic $WS" 2>/dev/null || true
BEFORE="$(git -C "$REPO" branch --format='%(refname:short)' | sort | tr '\n' ',')"
# Simulate a fresh clone: remove the worktree(s) and .cox, keep the branches.
for l in "$EPIC"/app; do t="$(readlink "$l" 2>/dev/null)"; [ -n "$t" ] && git -C "$REPO" worktree remove --force "$t" 2>/dev/null; rm -f "$l"; done
rm -rf "$EPIC/.cox"
if "$COX" epic attach --epic "$EPIC" >/dev/null 2>&1; then ok "attach"; else no "attach failed"; fi
[ -f "$EPIC/.cox/epic.json" ] && ok ".cox recreated" || no ".cox not recreated"
AFTER="$(git -C "$REPO" branch --format='%(refname:short)' | sort | tr '\n' ',')"
[ "$BEFORE" = "$AFTER" ] && ok "branch set unchanged" || no "branch set changed ($BEFORE -> $AFTER)"
"$COX" doctor --root "$WS" --epic "$EPIC" >/dev/null 2>&1 && ok "doctor exit 0 after attach" || no "doctor non-zero after attach"

step "8. true fresh clone: epic branch only on origin, cox epic attach fetches and creates the local branch"
pkill -f "cox watch --epic $WS" 2>/dev/null || true
BARE="$TMP/origin.git"
git init -q --bare "$BARE"
git -C "$REPO" remote add origin "$BARE" 2>/dev/null || git -C "$REPO" remote set-url origin "$BARE"
git -C "$REPO" push -q origin epic/hello
# Remove the worktree + symlink first (so the branch can be deleted), then drop the LOCAL branch and .cox: now epic/hello
# exists only on origin, like a machine that has never checked it out.
t="$(readlink "$EPIC/app" 2>/dev/null)"; [ -n "$t" ] && git -C "$REPO" worktree remove --force "$t" 2>/dev/null
rm -f "$EPIC/app"; rm -rf "$EPIC/.cox"
git -C "$REPO" branch -D epic/hello >/dev/null 2>&1
if git -C "$REPO" show-ref --verify --quiet refs/heads/epic/hello; then no "precondition: local epic/hello should be gone"; else ok "local epic/hello removed (only on origin)"; fi
"$COX" epic attach --epic "$EPIC" >/dev/null 2>&1 && ok "attach (fresh clone)" || no "fresh-clone attach failed"
git -C "$REPO" show-ref --verify --quiet refs/heads/epic/hello && ok "local epic/hello recreated from origin" || no "local epic/hello not recreated from origin"
"$COX" doctor --root "$WS" --epic "$EPIC" >/dev/null 2>&1 && ok "doctor exit 0 after fresh-clone attach" || no "doctor non-zero after fresh-clone attach"

echo
echo "RESULT: $pass passed, $fail failed"
[ $fail -eq 0 ]
