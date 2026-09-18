# Install

Coxswain has two pieces: the **`cox` binary** (the engine) and the **coxswain plugin** (skills + hooks for Claude Code
and Codex). You usually want both.

## The plugin (Claude Code)

```bash
claude plugin marketplace add nphattai/coxswain
claude plugin install coxswain@coxswain
```

The first command registers this repo's marketplace; the second installs the plugin. For Codex, the harness reads
`AGENTS.md` and `skills/` directly, so no plugin install is needed.

## The binary

Published as goreleaser release archives (`cox_<version>_<os>_<arch>.tar.gz` for darwin and linux, amd64 and arm64).
Extract and put `cox` on your `PATH`. The archive also carries `AGENTS.md`, `hooks/`, `templates/`, and `skills/` for a
binary-only install. To build from source:

```bash
git clone https://github.com/nphattai/coxswain && cd coxswain
make install   # go install into $GOBIN
```

## Verify

```bash
cox doctor
```

It confirms the binary is on `PATH` and lists every coxswain installation, epic, and watcher.
