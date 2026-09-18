#!/usr/bin/env bash
# Live E2E for visual review (M13), the tech lead's to run. It drives one real review loop against the epic v2 design:
#
#   cox epic design --html  ->  cox review open (browser)  ->  cox review poll --max 3m
#   ->  tech lead annotates ONE element and sends a decision (answer <id> ...) from the browser
#   ->  assert a new _leader inbox record and a new review wake landed
#
# It needs lavish-axi installed and a browser (it opens a window). It is INTERACTIVE: the poll blocks until the tech
# lead sends feedback from the Lavish window, or 3 minutes pass. It never runs `lavish-axi share`.
#
# Guarded: runs only when COX_E2E=1. It targets the real epic v2 by default (override with EPIC=...); the design
# artifact and its sidecar are written under <epic>/reports/visual (untracked, safe to delete after).
#
#   COX_E2E=1 tests/e2e/review-live.sh
#
set -uo pipefail

[ "${COX_E2E:-}" = "1" ] || { echo "set COX_E2E=1 to run the review live E2E"; exit 0; }

COX="${COX:-$HOME/go/bin/cox}"
EPIC="${EPIC:-$HOME/Work/repo/nphattai/crewkit/epics/v2}"
MAX="${MAX:-3m}"

pass=0 fail=0
step() { printf '\n=== %s ===\n' "$1"; }
ok()   { echo "PASS: $1"; pass=$((pass+1)); }
no()   { echo "FAIL: $1"; fail=$((fail+1)); }

echo "cox  = $COX ($($COX version 2>/dev/null))"
echo "epic = $EPIC"
command -v lavish-axi >/dev/null 2>&1 && echo "lavish-axi = $(lavish-axi --version 2>/dev/null)" || echo "lavish-axi = NOT INSTALLED (open will print the path; the poll cannot collect feedback)"

[ -f "$EPIC/DESIGN.md" ] || { echo "no DESIGN.md at $EPIC; set EPIC=<epic dir>"; exit 1; }

# ---------------------------------------------------------------------------
step "1. cox epic design --html"
ART="$EPIC/reports/visual/design.html"
if "$COX" epic design --html --epic "$EPIC" && [ -f "$ART" ] && [ -f "${ART%.html}.artifact.json" ]; then
  ok "wrote design.html and its sidecar"
  echo "sidecar:"; cat "${ART%.html}.artifact.json"
else
  no "design --html did not write the artifact and sidecar"; echo "RESULT: $pass passed, $((fail+1)) failed"; exit 1
fi

# Baseline: count _leader inbox records before the review.
before_recs="$(find "$EPIC/inbox/_leader" -maxdepth 1 -name '*.msg' 2>/dev/null | wc -l | tr -d ' ')"
echo "before: $before_recs _leader records"

# ---------------------------------------------------------------------------
step "2. cox review open (opens a browser window)"
"$COX" review open "$ART" --epic "$EPIC" || true
ok "open returned (a browser window should be up; if lavish is absent, the path was printed instead)"

# ---------------------------------------------------------------------------
step "3. cox review poll --max $MAX  (INTERACTIVE)"
echo ">>> In the Lavish window: annotate ONE element and Send a comment, then annotate the design and send a decision"
echo ">>> shaped 'answer <claim-id> <yes|no|text>' (or a plain comment). The poll returns as soon as you Send."
"$COX" review poll "$ART" --epic "$EPIC" --max "$MAX"
code=$?
echo "poll exit code: $code  (0 feedback, 3 timeout, 4 ended, 5 disconnected)"

# ---------------------------------------------------------------------------
step "4. assert a record and a wake landed"
after_recs="$(find "$EPIC/inbox/_leader" -maxdepth 1 -name '*.msg' 2>/dev/null | wc -l | tr -d ' ')"
echo "after: $after_recs _leader records"
if [ "$after_recs" -gt "$before_recs" ]; then
  ok "a new _leader inbox record was written"
  echo "newest record:"; find "$EPIC/inbox/_leader" -maxdepth 1 -name '*.msg' 2>/dev/null | sort | tail -1 | xargs cat
else
  no "no new _leader inbox record (did you Send feedback before the timeout?)"
fi
if "$COX" wake drain --peek --epic "$EPIC" 2>/dev/null | grep -qE "review_feedback|review_decision"; then
  ok "a review wake is queued for the leader"
else
  no "no review_feedback/review_decision wake queued"
fi

# ---------------------------------------------------------------------------
echo
echo "artifact + sidecar left under $EPIC/reports/visual (untracked, safe to delete)"
echo "RESULT: $pass passed, $fail failed"
[ $fail -eq 0 ]
