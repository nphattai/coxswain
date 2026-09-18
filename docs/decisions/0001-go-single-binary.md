# 0001 - Core language is Go, one static binary

- Status: Accepted
- Date: 2026-09-15

## Context

v1 is 27 loosely coupled bash scripts. The 14/9 review traced most of the 15 findings to shared root causes that shell
makes easy: swallowed exit codes (`|| true` on the data path), TSV parsing that collapses fields, path comparison by
prefix, and no typed state. There is also no single installed artifact whose version can be checked across the three
active clones (F09). A core rewrite needs a language with real error types, typed data, a test runner with fakeable
seams, and a single distributable artifact.

## Decision

Write the `cox` core as one Go binary. Use `errors`/typed returns for the exit-code and parse classes of bug, interfaces
for adapters (fakeable in `go test`), `encoding/json` for the protocol, cross-compilation for macOS and Linux, and
goreleaser for releases. Hooks are three-line shims that call `cox hook <name>`.

## Consequences

- One `cox version` replaces comparing 27 scripts; `cox doctor` can compare the binary hash across clones (fixes F09's
  root).
- Contributors edit Go and the compile step becomes a real gate, not an aspiration.
- A build toolchain is now required to develop the core; end users get a static binary with no runtime.
- Bash remains only for the frozen v1 (`bin/`) until it is deleted at M5.

## Review when

Reconsider if a hard dependency forces another runtime, or if the community backend work (tmux) shows Go raises the
contribution bar too high for the intended audience.
