#!/bin/bash
# For a WORKER: how full is my own context? Run from the worktree (cwd) at every phase start.
# Budgets are ABSOLUTE tokens, not a share of the model window: the cost of a call is the context it carries, and
# example-app-mobile paid 82% of its tokens in cache reads at an average 340k per call (docs/history/scorecards/*-cost.md).
# Verdict: ok | plan-compact (>= CTX_PLAN, default 400k: finish the phase, write <epic>/handoffs/<story>.md, ask the
# leader to compact) | compact-now (>= CTX_NOW, default 500k: do that immediately).
# Usage: <ws>/bin/my-context.sh [worktree]
set -uo pipefail
. "$(dirname "$0")/inbox-lib.sh"
CTX_PLAN="${CTX_PLAN:-400000}"; CTX_NOW="${CTX_NOW:-500000}"   # captain 2026-09-08: compact above 500k, not 200k (compactions cost minutes)
wt="$(cd "${1:-.}" && pwd)"; read -r ctx turns _ < <(session_ctx "$wt") || { echo "no session log for $wt" >&2; exit 1; }
v=ok; [ "$ctx" -ge "$CTX_PLAN" ] && v=plan-compact; [ "$ctx" -ge "$CTX_NOW" ] && v=compact-now
echo "context=$((ctx / 1000))k turns=$turns -> $v (plan at $((CTX_PLAN / 1000))k, now at $((CTX_NOW / 1000))k)"
