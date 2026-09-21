# Install

Coxswain has two pieces: the **`cox` binary** and the **harness integration** the leader and workers run under. You also
need **Orca**, the backend that owns worktrees and terminals. Install all three, then verify with `cox doctor`.

## 1. Orca (prerequisite)

Coxswain drives an Orca backend to create worktrees and launch agent terminals. `cox epic new` and `cox story dispatch`
fail without it, so install Orca first and make sure it is on your `PATH`:

```bash
orca status
```

If `orca` is not found, install it and re-open your shell so `PATH` picks it up. `cox doctor` (step 4) reports Orca as a
named check, so you do not have to remember this later.

## 2. The `cox` binary

Download a goreleaser archive from [GitHub Releases](https://github.com/nphattai/coxswain/releases). Archives are named
`coxswain_<version>_<os>_<arch>.tar.gz` for Darwin and Linux on amd64 and arm64. Extract it and put `cox` on your
`PATH`. The archive also carries `AGENTS.md`, `templates/`, and `skills/` for a binary-only install.

To build from source instead:

```bash
git clone https://github.com/nphattai/coxswain && cd coxswain
make install   # go install into $GOBIN
```

Confirm exactly one `cox` is on `PATH`:

```bash
which -a cox
```

## 3. Harness integration

The leader and workers run under a harness. Set up the one(s) you use.

### Claude Code (plugin)

```bash
claude plugin marketplace add nphattai/coxswain
claude plugin install coxswain@coxswain
```

The first command registers this repo's marketplace; the second installs the plugin (leader hooks, skills, and agent
instructions).

### Codex (`AGENTS.md` and skills)

Codex reads a project-level `AGENTS.md` and the Markdown workflows under `skills/` directly; `cox workspace init` writes
both into your workspace (see [Create a workspace](workspace.md)). The [Codex adapter](../adapters/codex.md) explains the
pull-wake fallback and optional project hooks.

### Pi (project-local extension)

Install Pi's project-local push/checkpoint extension into the workspace with
`cox workspace hooks --harness pi --root <clone> --epic <dir>`; it is written under `.pi/extensions/`, is
hash-verifiable, and does not touch user-level Pi config. Worker dispatch loads it explicitly with `-e` and requires
`--allow-unsandboxed` because Pi provides no host-filesystem confinement. The [Pi adapter](../adapters/pi.md) explains
the capability card and its reduced-mode fallback.

## 4. Verify

```bash
cox doctor
```

`cox doctor` is the setup oracle. Before any workspace exists it confirms the binary is on `PATH` and checks Orca. The
harness-binary checks begin once a workspace is initialized, because doctor reads which harnesses to check from the
workspace's `policy.json`; from then on it also lists every workspace, epic, watcher, and whether leader hooks are
installed. Each check is pass, fail, or `unknown` with a fix hint. Its exit code tells you the worst state found:

| Exit | Meaning |
|---|---|
| `0` | Everything checked passed. |
| `1` | At least one check failed (a fixable problem, e.g. Orca missing, two `cox` on `PATH`). |
| `3` | At least one check is `unknown` (a fact could not be observed; it is never reported as a pass). |

`cox doctor --json` carries the same structure for scripts.

Next: [Create a workspace](workspace.md).
