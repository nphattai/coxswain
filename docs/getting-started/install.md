# Install

Coxswain has two pieces: the **`cox` binary** and the harness instructions or integration used by the leader and
workers. Claude Code installs those instructions as a plugin. Codex reads `AGENTS.md` and Markdown skills directly.

## The plugin (Claude Code)

```bash
claude plugin marketplace add nphattai/coxswain
claude plugin install coxswain@coxswain
```

The first command registers this repo's marketplace; the second installs the plugin.

## The instruction pack (Codex)

Codex reads the project-level `AGENTS.md` and the Markdown workflows under `skills/` directly. The release archive and
repository checkout both include them. For a project that does not already have agent instructions:

```bash
cp /path/to/coxswain/AGENTS.md /path/to/your/project/AGENTS.md
cp -R /path/to/coxswain/skills /path/to/your/project/skills
```

If the project already has an `AGENTS.md` or `skills/` directory, merge the Coxswain leader section and `cox-*`
skill directories instead of overwriting local guidance. [Codex adapter](../adapters/codex.md) explains the pull wake
fallback and optional project hooks.

## The binary

Download a goreleaser archive from [GitHub Releases](https://github.com/nphattai/coxswain/releases). Archives are named
`coxswain_<version>_<os>_<arch>.tar.gz` for Darwin and Linux on amd64 and arm64. Extract the archive and put `cox` on your
`PATH`. It also carries `AGENTS.md`, `templates/`, and `skills/` for a binary-only install. Install project hook
configuration with `cox workspace hooks` after an epic root is available. To build from source:

```bash
git clone https://github.com/nphattai/coxswain && cd coxswain
make install   # go install into $GOBIN
```

## Verify

```bash
cox doctor
```

It confirms the binary is on `PATH` and lists every coxswain installation, epic, and watcher.

Continue with [Run your first epic](../QUICKSTART.md). If doctor reports a harness or backend gap, use
[Adapters](../adapters/index.md) to find the owning contract.
