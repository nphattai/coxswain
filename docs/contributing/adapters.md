# Author an adapter

Add an adapter only at an existing external boundary. If the proposed integration changes story state or handoff
semantics, it belongs in the core first, not behind a provider-specific implementation.

## Choose the seam

- Backend: `internal/adapter/backend/backend.go`
- Harness: `internal/adapter/harness/harness.go`
- Forge: `internal/adapter/forge/forge.go`
- Service: `internal/adapter/service/service.go`
- Review: `internal/adapter/review/`
- Quota: `internal/quota/`

Read the interface, a production implementation, its fake, and its tests before editing. [Adapter model](../adapters/index.md)
explains the stable boundaries.

## Required properties

1. Preserve three-state observations. Retrieval failure is `unknown`, not a guessed result.
2. Return real errors across the adapter boundary. Do not swallow a failed external command.
3. Keep identity explicit. Do not infer a story, worktree, or session from a display label when a canonical handle
   exists.
4. Confirm destructive effects before clearing ownership.
5. Declare reduced capabilities honestly and make callers refuse or surface the reduced mode.
6. Keep optional integrations optional. Core state and recovery must work without them.

Backends must preserve worktree isolation and the no-branch-deletion invariant. Harnesses must declare their capability
card through `Card()` and update the matching contract test. Review, quota, and forge adapters must keep outward-facing
or sensitive behavior behind explicit authority.

## Prove the boundary

- Unit-test parsing, command shape, errors, and unknown mappings through an injected runner or fake.
- Add integration coverage for invariants that cross packages.
- Put real-tool checks behind an explicit build tag or E2E script when they require a local installation or account.
- Record a dated real-tool result under [Evidence](../evidence/index.md), including version, date, method, result, and
  executable evidence path.

Do not copy the implementation into the adapter page. The page should state the support contract, ceilings, and where
to verify them.
