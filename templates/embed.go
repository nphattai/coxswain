// Package templates embeds the coxswain file templates (workspace/policy scaffolds, epic DESIGN.md, story.md, the
// service adapter example) so the cox binary carries them with no runtime file lookup. Callers read a template by its
// path within this directory via FS.
package templates

import "embed"

// FS holds the embedded templates. Paths are relative to the templates/ directory, e.g. "policy.json",
// "epic/DESIGN.md", "story.md", "services/example.sh".
//
//go:embed policy.json workspace.json story.md ship-pr-body.md scout-report.md epic/DESIGN.md services/example.sh arena/adversary.md arena/reviewer.md arena/domain.md arena/adversary-round2.md arena/reviewer-round2.md arena/domain-round2.md arena/synthesis.md
var FS embed.FS

// File returns the embedded template bytes for name, or an error if it is not embedded.
func File(name string) ([]byte, error) { return FS.ReadFile(name) }
