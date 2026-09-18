# 0002 - Backend: Orca default, herdr optional, one interface

- Status: Accepted
- Date: 2026-09-15

## Context

The kit runs on Orca today (worktree RPCs, orchestration mailbox, remote hosts). firstmate demonstrates a herdr backend
with native agent-state probes and submit confirmation. Locking the core to Orca would make the design untestable
without it and unusable for the open-source audience, who have neither Orca nor herdr.

## Decision

Define one Go `Backend` interface (WorktreeCreate/Remove, Spawn, Send, Interrupt, Stop, Probe, Mail). Ship an Orca
adapter as the default and a herdr adapter as an option behind the same interface. The core imports neither directly.
`WorktreeCreate` returns a verified path and branch and never falls back; `WorktreeRemove` never deletes a branch;
`Stop` returns a confirmation boolean.

## Consequences

- The core is testable with a fake backend; adapters are swappable by policy.
- The interface is small enough that the community can add a tmux backend later without touching the core.
- Orca keeps its extras (remote hosts, orchestration mailbox); herdr uses native agent state for Probe.
- QUICKSTART (M5) must state plainly that a backend (Orca or herdr) is required; there is no zero-backend mode yet.

## Review when

Reconsider the default if the community tmux backend matures, or if a required capability cannot be expressed by the
interface without leaking a backend concept into the core.
