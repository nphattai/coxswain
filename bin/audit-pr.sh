#!/bin/bash
# Leader audit of a story PR before the captain sees it: shape, commits, where the diff lands, credential scan of the
# diff AND of the evidence branch text files, CI, and the frame list to LOOK at (Read each PNG, match its caption).
# Usage: bin/audit-pr.sh <project>/epics/<slug> <story-id> [pr-number]   (pr defaults to the open PR from story/<id>)
set -uo pipefail
. "$(dirname "$0")/lib.sh"; . "$(dirname "$0")/inbox-lib.sh"
epic_paths "${1:?usage: audit-pr.sh <epic> <story-id> [pr]}"; story="${2:?story-id}"
wt="$(story_worktree "$epic_dir" "$story")" || { echo "no worktree for $story" >&2; exit 1; }
cd "$wt" || exit 1
pr="${3:-$(gh pr list --head "story/$story" --state all --json number --jq '.[0].number' 2>/dev/null)}"
[ -n "$pr" ] || { echo "no PR from story/$story" >&2; exit 1; }
echo "== PR #$pr"; gh pr view "$pr" --json isDraft,state,baseRefName,additions,deletions,files,commits \
  --jq '"draft=\(.isDraft) state=\(.state) base=\(.baseRefName) commits=\(.commits|length) files=\(.files|length) +\(.additions) -\(.deletions)"'
echo "== commits"; gh pr view "$pr" --json commits --jq '.commits[] | "  \(.oid[0:8]) \(.messageHeadline)"'
echo "== diff by directory (top 12)"; gh pr diff "$pr" --name-only | sed 's|/[^/]*$||' | sort | uniq -c | sort -rn | head -12 | sed 's/^/  /'
# credential scan: VN mobile numbers, seed login ids, JWTs, passwords, OTP request ids. Test/fixture lines are shown too -
# the reviewer decides; the story rule has no exemption for synthetic data.
pat='0[35789][0-9]{8}|INSU[0-9]{8,}|eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}|password *[:=] *["\x27][^"\x27]{3,}|requestId *[:=] *["\x27][A-Za-z0-9]{8,}|x-pin-code[^\n]*[0-9]{4,6}'
echo "== credential scan on the diff (+ lines)"; gh pr diff "$pr" | grep -E '^\+' | grep -nE "$pat" | sed 's/^/  /' || echo "  clean"
ev="evidence/$story"
if git fetch -q origin "$ev" 2>/dev/null; then
  echo "== evidence branch $ev @ $(git rev-parse --short "origin/$ev")"
  echo "== credential scan on evidence text files"; git ls-tree -r --name-only "origin/$ev" | grep -vE '\.(png|jpg|jpeg|gif|pdf)$' \
    | while read -r f; do git show "origin/$ev:$f" | grep -nE "$pat" | sed "s|^|  $f:|"; done; echo "  (scan end)"
  echo "== frames to LOOK at (Read each, match its caption in README.md):"
  git ls-tree -r -l "origin/$ev" | awk '$5 ~ /\.png$/ {printf "  %s  %d KB\n", $5, $4/1024}'
  [ -d "$wt/evidence/$story/shots" ] && echo "  local copies: $wt/evidence/$story/shots/"
else echo "== no evidence branch $ev on origin"; fi
echo "== repo AI reviewer (rs-pr-reviewer) - 'Changes Requested' blocks ready"
gh pr view "$pr" --json comments -q '.comments[] | select(.author.login=="rs-pr-reviewer") | .body' 2>/dev/null | grep -oE '(❌ Changes Requested|⚠️ Review Required|✅ [A-Za-z ]+)' | tail -1 | sed 's/^/  verdict: /'
gh api "repos/{owner}/{repo}/pulls/$pr/comments" --jq '.[] | "  \(.path):\(.line // .original_line) \(.body | split("\n")[0] | .[0:110])"' 2>/dev/null
echo "== CI"; gh pr checks "$pr" 2>&1 | sed 's/^/  /' | head -8
