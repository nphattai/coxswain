# lavish-axi review adapter

lavish-axi is an optional local review surface for `coxswain.artifact.v1` pages. Coxswain owns the artifact and durable
feedback record; lavish displays the page and returns reviewer feedback. Core review still works by file path and chat
when the adapter is absent.

## Support contract

- Open and bounded poll use a configured executable, never a shell command.
- The child environment is intentionally narrow.
- Every delivered feedback item becomes a durable inbox record and wake before further automation.
- A synthesis answer is applied only through the Coxswain command that verifies the artifact's synthesis identity.
- Public sharing is refused unless the captain explicitly opts in through policy or command authority.

Delivery currently acts as acknowledgement in the external tool. A process failure between delivery and Coxswain's
synchronous record write can therefore lose redelivery. Coxswain reports the raw payload on that failure rather than
hiding it.

The executable owner is `internal/adapter/review/lavish/`; review orchestration is in `cmd/cox/review.go`. Dated output
format and version observations are in [Optional adapter compatibility evidence](../evidence/compatibility/optional-adapters.md).

See [Visual review](../review.md) for the operator workflow.
