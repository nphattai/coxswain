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

The first command registers this repo's marketplace; the second installs the plugin (skills and agent
instructions; it ships no hooks).

> **The plugin is optional and skills-only.** `cox workspace init` writes the leader hooks (`.claude/settings.json`)
> and skills into your workspace (see [Create a workspace](workspace.md)), and that is the only hook source; the Quick
> Start installs no plugin. A plugin installed from an older release still ships the four leader hooks (`UserPromptSubmit`, `Stop`,
> `PreCompact`, `SessionStart`), so beside the workspace hooks every leader hook fires twice: update the plugin, or
> disable it.

### Codex (`AGENTS.md` and skills)

Codex reads a project-level `AGENTS.md` and the Markdown workflows under `skills/` directly; `cox workspace init` writes
both into your workspace (see [Create a workspace](workspace.md)). The [Codex adapter](../adapters/codex.md) explains the
pull-wake fallback and optional project hooks.

### Pi (project-local extension)

Pi's push/checkpoint extension is project-local and hash-verifiable, written under `.pi/extensions/`, and never touches
user-level Pi config. When `pi` is a leader option, `cox workspace init` installs it once per workspace for an **unbound
leader** that supervises every active epic (and adds `.pi/extensions/` to `.gitignore` - per-machine, never committed);
re-run just the extension with `cox workspace hooks --harness pi` (add `--epic <dir>` to bind one epic instead). A driver
upgrade needs `cox workspace init` again, and `cox doctor` flags a missing or stale extension with that repair. Worker
dispatch loads the same extension explicitly with `-e` and requires `--allow-unsandboxed` because Pi provides no
host-filesystem confinement. The [Pi adapter](../adapters/pi.md) explains the capability card, the leader hooks, and the
reduced-mode fallback.

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
