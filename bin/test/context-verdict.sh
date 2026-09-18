#!/bin/bash
# Self-check for bin/my-context.sh and bin/context.sh budgets on fake session logs (absolute token budgets).
# Run: /bin/bash bin/test/context-verdict.sh
set -u
here="$(cd "$(dirname "$0")" && pwd)"
t="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/ctx-test.XXXXXX")" && pwd)"; trap 'rm -rf "$t"' EXIT
mk() { # <name> <ctx tokens> -> fake worktree + session log under a fake HOME
  local wt="$t/ws/$1"; mkdir -p "$wt" "$t/home/.claude/projects/$(echo "$wt" | tr '/' '-')"
  printf '{"type":"assistant","message":{"usage":{"input_tokens":%s,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}}}\n' "$2" \
    > "$t/home/.claude/projects/$(echo "$wt" | tr '/' '-')/s.jsonl"
  echo "$wt"
}
fail=0
chk() { grep -q -- "$2" <<<"$1" || { echo "FAIL: want '$2' in: $1"; fail=1; }; }
a="$(mk low 120000)"; b="$(mk plan 420000)"; c="$(mk now 520000)"
chk "$(HOME=$t/home /bin/bash "$here/../my-context.sh" "$a")" '-> ok'
chk "$(HOME=$t/home /bin/bash "$here/../my-context.sh" "$b")" '-> plan-compact'
chk "$(HOME=$t/home /bin/bash "$here/../my-context.sh" "$c")" '-> compact-now'
chk "$(HOME=$t/home CTX_NOW=600000 /bin/bash "$here/../my-context.sh" "$c")" '-> plan-compact'
[ $fail = 0 ] && echo "ok: context budgets (400k plan, 500k now, CTX_NOW override)"
exit $fail
