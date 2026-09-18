#!/usr/bin/env bats
# Bats tests for the four Claude Code hook shims (hooks/*.sh), which are three-line execs into `cox hook <name>`.
# The suite builds cox into a temp dir on PATH so the bare `cox` in each shim resolves, then drives each hook against
# a fixture epic. Run: bats tests/hooks/hooks.bats

setup() {
  REPO="$(cd "$BATS_TEST_DIRNAME/../.." && pwd)"
  BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$BIN"
  # Build cox once per test dir (cached by go); put it on PATH for the shims.
  ( cd "$REPO" && go build -o "$BIN/cox" ./cmd/cox ) || { echo "go build failed"; return 1; }
  export PATH="$BIN:$PATH"
  export CLAUDE_PROJECT_DIR="$REPO"
  # Hermetic: never inherit the developer's real terminal handle, or stop-rewake keys its single-waiter lock on it in the
  # shared $TMPDIR and a live lock from the dev session makes the rewake tests see "another waiter" and skip.
  unset ORCA_TERMINAL_HANDLE

  EPIC="$BATS_TEST_TMPDIR/epic"
  mkdir -p "$EPIC/stories" "$EPIC/handoffs"
  export COX_EPIC="$EPIC"
  export COX_STORY="s"
  cat > "$EPIC/stories/s.md" <<'STORY'
---
id: s
---
## Read first
- Contract: DESIGN.md

## Goal
Build it.
STORY
}

# --- prompt-drain ---

@test "prompt-drain prints queued wakes" {
  # queue a wake via the CLI (append through the fake is internal; use a real drain path by writing wake.jsonl)
  mkdir -p "$EPIC/.cox"
  printf '%s\n' '{"schema":"coxswain.wake.v1","gen":1,"ts":"2026-09-15T00:00:00Z","epic":"epic","story":"s","kind":"pr_ready","note":"PR up"}' > "$EPIC/.cox/wake.jsonl"
  run "$REPO/hooks/prompt-drain.sh" < /dev/null
  [ "$status" -eq 0 ]
  [[ "$output" == *"PR up"* ]]
}

@test "prompt-drain is silent with no wakes" {
  run "$REPO/hooks/prompt-drain.sh" < /dev/null
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "prompt-drain suppresses the Orca doorbell with no wake (exit 2)" {
  # Single quotes keep the backticks literal; the JSON goes on stdin as the UserPromptSubmit hook would deliver it.
  echo '{"prompt":"You have 3 orchestration messages. Run `orca orchestration check --run r` to read them."}' > "$BATS_TEST_TMPDIR/p.json"
  run "$REPO/hooks/prompt-drain.sh" < "$BATS_TEST_TMPDIR/p.json"
  [ "$status" -eq 2 ]
  [[ "$output" == *"no wake pending - suppressed"* ]]
}

@test "prompt-drain passes the doorbell through when a wake is queued (exit 0)" {
  mkdir -p "$EPIC/.cox"
  printf '%s\n' '{"schema":"coxswain.wake.v1","gen":1,"ts":"2026-09-15T00:00:00Z","epic":"epic","story":"s","kind":"worker_done","note":"done"}' > "$EPIC/.cox/wake.jsonl"
  echo '{"prompt":"You have 1 orchestration message. Run `orca orchestration check` to read it."}' > "$BATS_TEST_TMPDIR/p.json"
  run "$REPO/hooks/prompt-drain.sh" < "$BATS_TEST_TMPDIR/p.json"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Watcher wakes"* ]]
}

# --- stop-rewake ---

@test "stop-rewake exits 2 on an urgent wake" {
  # One poll window (REWAKE_MAX_WAIT=15): an urgent wake rewakes on the first peek, before any sleep.
  mkdir -p "$EPIC/.cox"
  printf '%s\n' '{"schema":"coxswain.wake.v1","gen":1,"ts":"2026-09-15T00:00:00Z","epic":"epic","story":"s","kind":"worker_done","note":"done"}' > "$EPIC/.cox/wake.jsonl"
  REWAKE_MAX_WAIT=15 run "$REPO/hooks/stop-rewake.sh"
  [ "$status" -eq 2 ]
}

@test "stop-rewake exits 0 with no wake and no open story" {
  # REWAKE_MAX_WAIT=0: no wait; with no dispatched story open, stay idle.
  mkdir -p "$EPIC/.cox"
  printf '%s\n' '{"schema":"coxswain.wake.v1","gen":1,"ts":"2026-09-15T00:00:00Z","epic":"epic","story":"s","kind":"status","note":"phase 1"}' > "$EPIC/.cox/wake.jsonl"
  REWAKE_MAX_WAIT=0 run "$REPO/hooks/stop-rewake.sh"
  [ "$status" -eq 0 ]
}

@test "stop-rewake tick: MAX_WAIT with a working story exits 2" {
  mkdir -p "$EPIC/.cox"
  printf '%s\n' '{"schema":"coxswain.event.v1","ts":"2026-09-15T00:00:00Z","epic":"epic","story":"s","attempt":1,"actor":"leader","from":"submitted","to":"working","external_confirmed":true}' > "$EPIC/.cox/events.jsonl"
  REWAKE_MAX_WAIT=0 run "$REPO/hooks/stop-rewake.sh"
  [ "$status" -eq 2 ]
  [[ "$output" == *"Rewake tick"* ]]
}

@test "stop-rewake tick: MAX_WAIT with only a parked story exits 0" {
  mkdir -p "$EPIC/.cox"
  printf '%s\n' '{"schema":"coxswain.event.v1","ts":"2026-09-15T00:00:00Z","epic":"epic","story":"s","attempt":1,"actor":"leader","from":"working","to":"parked","external_confirmed":true}' > "$EPIC/.cox/events.jsonl"
  REWAKE_MAX_WAIT=0 run "$REPO/hooks/stop-rewake.sh"
  [ "$status" -eq 0 ]
}

# --- precompact ---

@test "precompact replaces its facts block instead of appending, and refreshes written_at" {
  cat > "$EPIC/handoffs/s.md" <<'HANDOFF'
---
schema: coxswain.checkpoint.v1
story: s
attempt: 1
head: abc123
base: origin/epic/epic@000
written_at: 2026-09-15T00:00:00Z
reason: phase-end
---
## Next action
go
HANDOFF
  run "$REPO/hooks/precompact.sh"
  [ "$status" -eq 0 ]
  grep -q "## Facts (máy tính)" "$EPIC/handoffs/s.md"
  # The checkpoint body is kept.
  grep -q "## Next action" "$EPIC/handoffs/s.md"
  # Running it again must not stack a second facts block.
  run "$REPO/hooks/precompact.sh"
  [ "$status" -eq 0 ]
  [ "$(grep -c '## Facts (máy tính)' "$EPIC/handoffs/s.md")" -eq 1 ]
  # written_at is refreshed off the seed value.
  ! grep -q 'written_at: 2026-09-15T00:00:00Z' "$EPIC/handoffs/s.md"
}

# --- session-start ---

@test "session-start injects the checkpoint" {
  cat > "$EPIC/handoffs/s.md" <<'HANDOFF'
---
schema: coxswain.checkpoint.v1
story: s
attempt: 1
head: abc123
base: origin/epic/epic@000
written_at: 2026-09-15T00:00:00Z
reason: park
---
## Next action
rerun the test
HANDOFF
  run "$REPO/hooks/session-start.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"rerun the test"* ]]
}

@test "session-start refuses a wrong-attempt checkpoint (exit 1)" {
  # The event log puts the story at attempt 2; the checkpoint is attempt 1.
  mkdir -p "$EPIC/.cox"
  printf '%s\n' '{"schema":"coxswain.event.v1","ts":"2026-09-15T00:00:00Z","epic":"epic","story":"s","attempt":2,"actor":"leader","from":"parked","to":"working","external_confirmed":true}' > "$EPIC/.cox/events.jsonl"
  cat > "$EPIC/handoffs/s.md" <<'HANDOFF'
---
schema: coxswain.checkpoint.v1
story: s
attempt: 1
head: abc123
base: origin/epic/epic@000
written_at: 2026-09-15T00:00:00Z
reason: park
---
## Next action
stale
HANDOFF
  run "$REPO/hooks/session-start.sh"
  [ "$status" -eq 1 ]
}
