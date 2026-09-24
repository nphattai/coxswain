# `busy.v1`

The busy record is the harness-owned idle/busy fact for one story incarnation (ADR 0016), ported from firstmate's
semantic busy-state contract (`bin/fm-busy-lib.sh`, `bin/fm-busy-event.sh` @1e0e773).

## Files

Under `<epic>/.cox/sessions/`: `<story>.busy.json` (one JSON line: schema, state, gen, seq, ts, source, event, harness,
sources), `<story>.busy-gen` (the armed gen, written by Arm), `<story>.busy-progress` (native progress, mtime only).

## Invariants

- Arm mints the gen, writes the sidecar, seeds `busy dispatch` at seq 1 and clears progress. Apply and Progress must
  present the sidecar's gen; a stale gen is rejected and writes nothing.
- A verdict is `busy|idle <source>` or `unknown <reason>`; unknown is never idle. Reasons: `missing`, `malformed`
  (unparseable, a field outside the schema, a second line, or no sidecar), `gen-mismatch`, `source-mismatch`,
  `codex-unverified`, `launch-prompt`; an applied unknown keeps its source (`unknown recovery`).
- A gone endpoint is `dead endpoint-gone` (`busy.ClassifyLive`), the only process-level override; no target is
  `unknown no-target`.
- The launch-prompt backstop reads only a caller-supplied terminal tail, only for a record still at `busy dispatch`,
  and only turns busy into unknown; cox never classifies busy from rendered text.
- Retire needs the armed gen; an absent sidecar is already retired.

Behavior is owned by `internal/protocol/busy/` and its port suite (`port_test.go`).
