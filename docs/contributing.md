# Contributing

Coxswain is a Go binary plus a harness plugin. The core is designed around small interfaces so new backends and harnesses
plug in without touching the engine.

## Build and test

```bash
make install          # build + install cox
go test -race ./...   # unit tests with fakes for every adapter
bats tests/hooks      # hook tests
claude plugin validate .
```

## Extension points

- **Backend adapter** (`internal/adapter/backend/`): drives terminals and worktrees. Implement the `Backend` interface;
  Orca is the reference.
- **Forge / review / quota adapters**: each is an interface with a fake in tests. Add a new one behind its interface.
- **Harness capability card** (`docs/adapters/`): declare a harness's roles, wake mode, checkpoint mode, and sandbox;
  `cox doctor` and `cox story dispatch` read it.

Read [Author an adapter](contributing/adapters.md) before changing an external boundary. It maps each interface to its
fake, tests, reduced-mode rules, and evidence requirements.

## Conventions

Conventional commits. No generated files edited by hand (`CHANGELOG.md`, anything marked auto-generated). Every non-trivial
change lands a runnable test. See the [ADRs](decisions/index.md) for the decisions behind the architecture.

Documentation points to executable owners instead of copying mutable command, schema, or configuration inventories.
Dated compatibility results belong under [Evidence](evidence/index.md).
