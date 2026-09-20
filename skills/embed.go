// Package skills embeds the pinned Coxswain leader skills so `cox workspace init` can drop them under
// <ws>/.agents/skills/, giving both a Claude and a Codex leader the same skill set with no separate download. Only the
// leader set (cox-arena, cox-dispatch, cox-epic, cox-ship) is embedded; worker skills come from each source worktree.
package skills

import "embed"

// FS holds the embedded leader skill directories, each a "<name>/SKILL.md" tree. Iterate Names() to copy them.
//
//go:embed cox-arena cox-dispatch cox-epic cox-ship
var FS embed.FS

// Names is the embedded leader skill directory names, in a stable order.
func Names() []string { return []string{"cox-arena", "cox-dispatch", "cox-epic", "cox-ship"} }
