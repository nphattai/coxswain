Path-scoped rules for Claude Code. Each file has frontmatter `paths: ["glob", ...]` and loads only when a
matching file is touched. Keep each under 40 lines; put anything universal in AGENTS.md instead.
Example: `worker.md` with `paths: ["apps/insurtech-worker/**"]`.
