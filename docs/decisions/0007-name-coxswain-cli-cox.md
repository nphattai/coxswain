# 0007 - Name the project coxswain, CLI cox

- Status: Accepted
- Date: 2026-09-15

## Context

firstmate establishes a nautical metaphor (captain, crew). The project needs a name that fits the leader role (takes the
captain's command, sets the pace for the crew), reads well as a short CLI, and does not collide with a well-known
GitHub project. A GitHub survey on 2026-09-15 ruled out bosun, helmsman, flotilla, quartermaster, convoy, armada,
skipper, and helm as heavily used.

## Decision

The project is `coxswain`; the CLI is `cox`. A coxswain steers and calls the stroke for the rowing crew under the
captain's orders, which matches the leader role. The survey found no agent/devtool project by that name (only a small
rowing app and a GHC plugin). The Go module is `github.com/nphattai/coxswain`; the GitHub repo is renamed
`nphattai/crewkit` -> `nphattai/coxswain` (captain owns the rename, an outward-facing action).

## Consequences

- User-facing docs, module path, binary, and skills use `coxswain`/`cox`.
- The GitHub rename breaks old remotes and the downstream submodule pointer; remotes and `.gitmodules` are updated the same
  day and GitHub redirects the old name for a while.
- Historical references to "crewkit" remain in `plans/` as the v1 name and are not rewritten.

## Review when

Effectively permanent. Revisit only if a naming collision or trademark issue surfaces.
