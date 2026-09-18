# `coxswain.fleet.v1`

The fleet document is a resolved view, not a source of truth. It combines folded story state with sourced observations
such as backend liveness, semantic activity, git, composer, and forge facts.

## Invariants

- Every observation identifies its source and observation time.
- A retrieval failure or unavailable fact is represented as unknown with its reason.
- Event-log state has a distinct authority from live observations.
- Consumers may add views, but they do not write story state through the fleet projection.

The exact document contract is [`schema/fleet.v1.json`](schema/fleet.v1.json). Resolution is owned by
`internal/state/resolve.go`; command rendering is owned by `cmd/cox/state.go`. Forge evidence comes through
`internal/adapter/forge/` and keeps its own three-state semantics.

## Example

```json
{
  "schema": "coxswain.fleet.v1",
  "generated_at": "2026-09-15T02:40:00Z",
  "epic": "pipo-admin",
  "stories": [
    {
      "id": "pipo-admin-cicd",
      "state": "working",
      "attempt": 2,
      "observations": {
        "liveness": { "value": "alive", "source": "backend", "observed_at": "2026-09-15T02:39:58Z" },
        "git": { "value": { "head": "a1b2c3d", "dirty": false, "ahead": 3 }, "source": "git", "observed_at": "2026-09-15T02:39:59Z" },
        "forge": { "value": { "pr": 42, "checks": "pass", "checks_count": 5, "merged": "false" }, "source": "forge", "observed_at": "2026-09-15T02:39:40Z" }
      }
    }
  ]
}
```

## Compatibility

New optional observation keys or sources may remain in `v1` when older readers can ignore them. Removing or renaming a
field, or making an optional field required, needs a new major schema id.

See [Board](../board.md) for a read-only captain view over this projection.
