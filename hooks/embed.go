// Package hooks embeds hooks.json, the one source of the leader hook group shapes (events, matchers, timeouts, async).
// cox workspace init and cox workspace hooks read it to write <ws>/.claude/settings.json and <ws>/.codex/hooks.json
// with `cox hook <name>` commands, so the shapes cannot drift between the plugin manifest and what init writes.
package hooks

import _ "embed"

// JSON is the raw hooks.json bytes (the Claude Code plugin hook manifest).
//
//go:embed hooks.json
var JSON []byte
