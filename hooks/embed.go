// Package hooks embeds leader.json, the one source of the leader hook group shapes (events, matchers, timeouts, async).
// cox workspace init and cox workspace hooks read it to write <ws>/.claude/settings.json and <ws>/.codex/hooks.json
// with `cox hook <name>` commands. It is deliberately not hooks/hooks.json: that is Claude Code's default plugin hook
// path, and a plugin that ships the same four groups as the workspace fires every leader hook twice (B-26). The plugin
// is skills-only.
package hooks

import _ "embed"

// JSON is the raw leader.json bytes.
//
//go:embed leader.json
var JSON []byte
