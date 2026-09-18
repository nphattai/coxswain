# quota-axi quota adapter

quota-axi is an optional source for the provider-neutral `coxswain.quota.v1` projection. It supplies evidence only. It
does not choose a harness, reroute a worker, or become a prerequisite for dispatch unless a fresh reading proves the
configured exhausted condition.

## Support contract

- The configured binary is preferred. Any npx fallback requires an exact, explicit policy opt-in.
- Provider output is projected in memory to Coxswain's quota type. Raw output and identity fields are not persisted.
- Binary absence, timeout, parse failure, schema drift, stale provider state, or authorization failure becomes an
  unknown reading with a reason.
- The cache contains only the projection and is bounded by file permissions and freshness rules.
- Routing may display quota evidence but does not silently turn it into a model or harness decision.

The executable owners are `internal/quota/quotaaxi.go`, `internal/quota/cache.go`, and their tests. Version and schema
observations are in [Optional adapter compatibility evidence](../evidence/compatibility/optional-adapters.md).

See [Quota](../quota.md) for the operating policy and [quota protocol](../protocol/quota.v1.md) for compatibility.
