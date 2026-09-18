#!/usr/bin/env bash
# Live E2E for arena v3 (ADR 0013), the tech lead's to run. It drives the headless-default arena end to end on a small
# seeded-flaw epic and prints PASS/FAIL per step:
#
#   epic arena --lite (headless)  ->  arena check  ->  arena verify  ->  arena synth
#   ->  design --sign FAILS (open captain_decision)  ->  fill it  ->  design --sign PASSES
#   ->  dirty the leader checkout, epic arena  ->  cox switches to terminal mode (notice printed)
#
# Headless runs each role as a read-only subprocess in the leader checkout and cox writes the report from the JSON, so
# NO Orca backend is needed for the headless steps (only the terminal-switch step touches the backend, and it is expected
# to stop at the switch notice when no backend is configured).
#
# Guarded: runs only when COX_E2E=1. It creates the epic and a seeded repo under $TMPDIR, never in this repo, and leaves
# the seeded repo's branch behind (rule: no tool deletes a branch). The lone adversary runs the NOT-leader harness, so
# LEADER=codex makes it claude (the default here); set LEADER=claude to exercise a codex adversary instead.
#
#   COX_E2E=1 tests/e2e/arena-live.sh
#
# The synthesis the leader adjudicates is written deterministically by this script (six sections, the seeded claim
# accepted+verified, one open captain_decision) so the sign-gate steps are reproducible; the arena/check/verify/synth
# steps above it exercise the real pipeline against whatever the live role actually wrote.
set -uo pipefail

[ "${COX_E2E:-}" = "1" ] || { echo "set COX_E2E=1 to run the arena live E2E"; exit 0; }

COX="${COX:-$HOME/go/bin/cox}"
LEADER="${LEADER:-codex}"   # the lone adversary is the NOT-leader harness; codex leader => claude adversary
TS="$(date +%s)"
SLUG="arena-e2e-$TS"
PROJECT="smoke"
WS="$(mktemp -d "${TMPDIR:-/tmp}/cox-arena-e2e.XXXXXX")"
EPIC="$WS/$PROJECT/epics/$SLUG"
REPO="$WS/billing-repo"

pass=0 fail=0
step() { printf '\n=== %s ===\n' "$1"; }
ok()   { echo "PASS: $1"; pass=$((pass+1)); }
no()   { echo "FAIL: $1"; fail=$((fail+1)); }

echo "cox    = $COX ($($COX version 2>/dev/null))"
echo "leader = $LEADER (adversary = the other harness)"
echo "ws     = $WS"
echo "epic   = $EPIC"

# ---------------------------------------------------------------------------
# Seeded-flaw repo: billing reads accounts.tier; a migration drops that column. The adversary should catch it.
mkdir -p "$REPO/app" "$REPO/db"
cat > "$REPO/app/billing.go" <<'GO'
package billing

// Price prices an invoice from the account tier, read from accounts.tier.
func Price(tier string) int { // reads accounts.tier
	return len(tier)
}
GO
cat > "$REPO/db/migrate.sql" <<'SQL'
CREATE TABLE tiers (id INT PRIMARY KEY, name TEXT);
ALTER TABLE accounts DROP COLUMN tier;
SQL
git -C "$REPO" init -q
git -C "$REPO" config user.email t@t; git -C "$REPO" config user.name t
git -C "$REPO" add -A; git -C "$REPO" commit -qm "seed billing"
SHA="$(git -C "$REPO" rev-parse HEAD)"

# Workspace + policy so cox epic arena resolves harness policy.
mkdir -p "$WS/cox" "$WS/$PROJECT"
cat > "$WS/cox/workspace.json" <<JSON
{
  "projects": [{ "name": "$PROJECT", "path": "$PROJECT" }],
  "repos": [{ "alias": "billing", "path": "billing-repo", "production": "main" }],
  "services": [],
  "hosts": [{ "name": "local" }]
}
JSON
"$COX" workspace init --root "$WS" >/dev/null 2>&1 || true

# Epic dir (built by hand; the headless arena needs no epic-new/backend).
mkdir -p "$EPIC/stories" "$EPIC/reports/arena"
ln -s "$REPO" "$EPIC/billing"
printf 'billing billing-repo\n' > "$EPIC/repos"
cat > "$EPIC/DESIGN.md" <<DESIGN
# $SLUG - drop the legacy accounts.tier column

We migrate tiers into their own table and DROP COLUMN accounts.tier. Billing must be updated to read tier_id before the
drop lands, or pricing breaks. This is a schema migration touching money.
DESIGN

# ---------------------------------------------------------------------------
step "1. cox epic arena --lite (headless)"
ALOG="$WS/arena.log"
"$COX" epic arena --epic "$EPIC" --lite --leader "$LEADER" 2>&1 | tee "$ALOG"
if grep -q "headless" "$ALOG" && [ -f "$EPIC/reports/arena/round-1-adversary.md" ]; then
  ok "headless adversary wrote round-1-adversary.md"
else
  no "no round-1-adversary.md from the headless run"; echo "aborting"; echo "RESULT: $pass passed, $((fail+1)) failed"; exit 1
fi

# ---------------------------------------------------------------------------
step "2. cox arena check --round 1"
if "$COX" arena check --epic "$EPIC" --round 1; then
  ok "check passed (v3 report: citations, tiers, confidence, checks)"
else
  no "arena check failed - inspect round-1-adversary.md (the live role may have written an invalid v3 report)"
fi

# ---------------------------------------------------------------------------
step "3. cox arena verify --round 1"
if "$COX" arena verify --epic "$EPIC" --round 1 && [ -f "$EPIC/reports/arena/verify-round-1.json" ]; then
  ok "verify wrote verify-round-1.json"
  echo "verify results:"; cat "$EPIC/reports/arena/verify-round-1.json"
else
  no "verify did not write verify-round-1.json"
fi

# ---------------------------------------------------------------------------
step "4. cox arena synth --round 1"
if "$COX" arena synth --epic "$EPIC" --round 1 && [ -f "$EPIC/reports/arena/synthesis.md" ]; then
  ok "synth wrote synthesis.md"
else
  no "synth did not write synthesis.md"
fi

# ---------------------------------------------------------------------------
# The leader adjudicates: write a deterministic v3 synthesis - six sections filled, the seeded claim accepted and
# verified pass, and one OPEN captain_decision - so the sign-gate steps below are reproducible regardless of the exact
# words the live role chose.
step "5. leader writes the adjudicated synthesis (with an open captain_decision)"
write_synth() {  # $1 = captain-agrees cell for the captain_decision row ("" = open)
  cat > "$EPIC/reports/arena/synthesis-round-1.md" <<SYN
---
epic: $SLUG
schema: coxswain.arena.v3
---
# Arena synthesis - $SLUG

## Adopted decision
Update billing to read tier_id, then drop accounts.tier.

## Decisive evidence
Tier-3 live code at billing/app/billing.go:4@$SHA reads accounts.tier.

## Rejected alternatives
Dropping the column first and backfilling later loses in-flight pricing.

## Preserved locked decisions
None affected.

## Remaining uncertainty
Whether any out-of-repo caller reads the column.

## Verification gates
grep finds no reader of accounts.tier after the billing change.

## Claims

| role | claim | evidence | tier | severity | verified | verdict | question | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|---|---|---|
| adversary | Dropping accounts.tier breaks billing which still reads it | billing/app/billing.go:4@$SHA | 3 | epic-blocking | pass | accepted | | real failure path | read tier_id first | yes |
| adversary | The migration order needs a captain call | billing/db/migrate.sql:2@$SHA | 3 | significant | pass | captain_decision | Ship the billing change one release before the drop? | ordering is a release-policy call | none | $1 |
SYN
  ln -sf "synthesis-round-1.md" "$EPIC/reports/arena/synthesis.md"
}
write_synth ""
ok "synthesis written with one open captain_decision"

# ---------------------------------------------------------------------------
step "6. cox epic design --sign FAILS while the captain_decision is open"
if "$COX" epic design --sign --epic "$EPIC" 2>"$WS/sign1.err"; then
  no "sign should have refused an open captain_decision"
else
  if grep -q "captain_decision" "$WS/sign1.err"; then
    ok "sign refused: $(cat "$WS/sign1.err")"
  else
    no "sign refused but not for the captain_decision: $(cat "$WS/sign1.err")"
  fi
fi

# ---------------------------------------------------------------------------
step "7. captain answers, then design --sign PASSES"
write_synth "agree, ship billing first"
if "$COX" epic design --sign --epic "$EPIC"; then
  ok "sign recorded design_signed after the captain answered"
else
  no "sign should pass once every gate is met: $("$COX" epic design --sign --epic "$EPIC" 2>&1)"
fi

# ---------------------------------------------------------------------------
# An untracked file UNDER the epic dir must NOT flip cox to terminal mode: the arena regenerates the pack, role stories,
# and reports under the epic dir on every run, so the dirty check (-uall, epic-dir excluded) ignores them. Only a change
# to the leader checkout OUTSIDE the epic dir should switch (next step).
step "8. untracked file under the epic dir stays headless"
echo "scratch" > "$EPIC/reports/arena/scratch-untracked.md"
"$COX" epic arena --epic "$EPIC" --lite --leader "$LEADER" 2>&1 | tee "$WS/arena-epicdirty.log" || true
if grep -q "switching to terminal mode" "$WS/arena-epicdirty.log"; then
  no "an untracked file under the epic dir must not switch to terminal mode"
else
  ok "untracked epic-dir file left the arena headless"
fi
rm -f "$EPIC/reports/arena/scratch-untracked.md"

# ---------------------------------------------------------------------------
# Terminal-mode auto-switch: a dirty leader checkout must flip cox to terminal mode with a notice. Terminal mode needs a
# backend, so the command is expected to stop AT the switch (or right after) when none is configured; we assert the
# notice, not a full terminal dispatch.
step "9. dirty leader checkout -> cox switches to terminal mode"
echo "// dirty" >> "$REPO/app/billing.go"
"$COX" epic arena --epic "$EPIC" --lite --leader "$LEADER" 2>&1 | tee "$WS/arena2.log" || true
if grep -q "switching to terminal mode" "$WS/arena2.log"; then
  ok "cox switched to terminal mode on the dirty checkout"
else
  no "no terminal-mode switch notice on a dirty leader checkout"
fi
git -C "$REPO" checkout -- app/billing.go 2>/dev/null || true

# ---------------------------------------------------------------------------
echo
echo "leftover temp branch (rule: tool never deletes a branch; tech lead removes if desired):"
echo "  (seeded repo $REPO on its default branch)"
echo "workspace temp dir (safe to delete): $WS"
echo
echo "RESULT: $pass passed, $fail failed"
[ $fail -eq 0 ]
