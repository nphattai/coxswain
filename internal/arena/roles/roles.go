// Package roles decides an arena's shape from policy: the trigger level (full/lite/none), which roles run, the harness
// each role uses (never hard-coded - the adversary is "not-leader", the reviewer is "same-as-leader"), and the story
// file each role is dispatched as. The harness rule and defaults come from policy.Harness.Arena so adding a harness is
// a policy edit, not a code change (decision 0004, 0008).
package roles

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/nphattai/coxswain/internal/workspace"
	"github.com/nphattai/coxswain/templates"
)

// Role is an arena reviewer role.
type Role string

const (
	Adversary Role = "adversary" // "where does this design fail?" - not-leader harness, sees only the context pack
	Reviewer  Role = "reviewer"  // precedent + journey - leader harness, fresh session, may grep the workspace
	Domain    Role = "domain"    // STRIDE / invariants / capacity - not-leader harness, on for sensitive triggers
)

// Level is the arena size the trigger selects.
type Level string

const (
	Full Level = "full" // adversary + reviewer (+ domain for sensitive triggers)
	Lite Level = "lite" // one adversary
	None Level = "none" // nothing to review
)

// Signals are the trigger inputs: the design text (scanned for sensitive keywords), the repo count, and the captain's
// --reason (a non-empty reason is a captain request). This is the "design + policy" the phase brief names, made explicit
// so repo count and captain intent are not guessed from prose.
type Signals struct {
	Design string
	Repos  int
	Reason string
}

// sensitive are the design-keyword triggers (matched as whole words, case-insensitive).
var sensitive = []string{"migration", "money", "auth", "identity", "pii"}

// Trigger returns the arena level and the reasons that fired. Any full trigger (3+ repos, a sensitive keyword in the
// design, or a captain request) means Full; otherwise Lite runs a single adversary. None is returned only when there is
// genuinely nothing to review (no design, no repos, no reason).
func Trigger(s Signals, pol *workspace.Policy) (Level, []string) {
	var reasons []string
	if s.Repos >= 3 {
		reasons = append(reasons, fmt.Sprintf("%d repos (>= 3)", s.Repos))
	}
	for _, kw := range sensitive {
		if wordRe(kw).MatchString(s.Design) {
			reasons = append(reasons, "design mentions "+kw)
		}
	}
	if strings.TrimSpace(s.Reason) != "" {
		reasons = append(reasons, "captain request: "+strings.TrimSpace(s.Reason))
	}
	if len(reasons) > 0 {
		return Full, reasons
	}
	if strings.TrimSpace(s.Design) == "" && s.Repos == 0 {
		return None, []string{"no design content"}
	}
	return Lite, []string{"below the full-arena bar; one adversary"}
}

// Active returns the roles that run at a level. Domain is added only when a sensitive trigger (migration/money/auth/
// identity/pii) is among the reasons, since that is what a domain role reviews.
func Active(level Level, reasons []string) []Role {
	switch level {
	case Lite:
		return []Role{Adversary}
	case Full:
		out := []Role{Adversary, Reviewer}
		if anySensitive(reasons) {
			out = append(out, Domain)
		}
		return out
	default:
		return nil
	}
}

func anySensitive(reasons []string) bool {
	for _, r := range reasons {
		for _, kw := range sensitive {
			if strings.Contains(strings.ToLower(r), kw) {
				return true
			}
		}
	}
	return false
}

// Resolve maps each role to the harness it runs under, from policy. The adversary rule (not-leader) picks the arena
// option that is not the leader's harness, preferring the policy default when it qualifies; the reviewer rule
// (same-as-leader-new-session) runs the leader's harness. Domain follows the adversary rule (not-leader). An error is
// returned when the not-leader rule cannot be satisfied (no other harness in the options).
func Resolve(pol *workspace.Policy, leader string) (map[Role]string, error) {
	notLeader, err := otherHarness(pol, leader)
	if err != nil {
		return nil, err
	}
	return map[Role]string{
		Adversary: notLeader,
		Reviewer:  leader,
		Domain:    notLeader,
	}, nil
}

// otherHarness returns the arena harness that is not the leader, honoring the adversary rule and default from policy.
func otherHarness(pol *workspace.Policy, leader string) (string, error) {
	if pol.Harness.Arena.Adversary.Rule != "not-leader" {
		return "", fmt.Errorf("arena adversary rule %q is not supported (want not-leader)", pol.Harness.Arena.Adversary.Rule)
	}
	def := pol.Harness.Arena.Adversary.Default
	if def != "" && def != leader {
		return def, nil
	}
	for _, opt := range pol.Harness.Leader.Options {
		if opt != leader {
			return opt, nil
		}
	}
	return "", fmt.Errorf("no not-leader harness for leader %q in options %v", leader, pol.Harness.Leader.Options)
}

// StoryData is what an arena role template renders against.
type StoryData struct {
	Role     Role
	ID       string // arena-<role>
	Repo     string // alias the role runs its worktree in
	Harness  string
	Model    string
	Slug     string
	EpicDir  string
	PackPath string // reports/arena/context-pack.md
	Round    int
}

// Render writes stories/arena-<role>.md from templates/arena/<role>.md for round d.Round and returns its path. An
// existing story file for the same round is reused (an idempotent resume). One rendered for a different round is
// re-rendered only with force; without force Render refuses, so a new round never silently reuses a prior round's story
// (which reviewed the old report to the old output path, the round-2 bug). round is read from the file's frontmatter.
func Render(epicDir string, d StoryData, force bool) (string, error) {
	d.ID = "arena-" + string(d.Role)
	if d.Round == 0 {
		d.Round = 1
	}
	rendered, err := renderTemplate(d)
	if err != nil {
		return "", err
	}
	path := filepath.Join(epicDir, "stories", d.ID+".md")
	if existing, err := os.ReadFile(path); err == nil {
		switch er := frontmatterRound(existing); {
		case er == d.Round:
			return path, nil // same round: idempotent resume, keep the file
		case !force:
			return "", fmt.Errorf("stories/%s.md exists for round %d; pass --force to re-render it for round %d", d.ID, er, d.Round)
		}
		// force + different round: fall through and overwrite with the round-N story.
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Prompt renders a role's template to a string for the given round without writing a story file. Headless mode embeds
// this in the prompt fed to the harness on stdin (no worktree, no story file). The round-2 template is selected the same
// way as Render.
func Prompt(d StoryData) (string, error) {
	d.ID = "arena-" + string(d.Role)
	if d.Round == 0 {
		d.Round = 1
	}
	return renderTemplate(d)
}

// renderTemplate executes the round-appropriate role template against d and returns the text.
func renderTemplate(d StoryData) (string, error) {
	tmplBytes, err := templates.File("arena/" + templateName(d.Role, d.Round) + ".md")
	if err != nil {
		return "", fmt.Errorf("arena role template %s: %w", d.Role, err)
	}
	tmpl, err := template.New(string(d.Role)).Parse(string(tmplBytes))
	if err != nil {
		return "", fmt.Errorf("parse arena template %s: %w", d.Role, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("render arena story %s: %w", d.Role, err)
	}
	return buf.String(), nil
}

// TemplateNeedsWorktree reports whether a role's round template declares `needs_worktree: true` in its frontmatter. Such
// a role cannot run headless (it must read the repo through a worktree), so cox switches the run to terminal mode for it
// (captain ruling 2026-09-16).
func TemplateNeedsWorktree(role Role, round int) bool {
	b, err := templates.File("arena/" + templateName(role, round) + ".md")
	if err != nil {
		return false
	}
	return needsWorktreeRe.Match(b)
}

// needsWorktreeRe matches a `needs_worktree: true` frontmatter line.
var needsWorktreeRe = regexp.MustCompile(`(?m)^needs_worktree:\s*true\s*$`)

// templateName is the arena template a role renders from: the base <role> for round 1, and <role>-round2 for any later
// round (the round-2 template tells the role to oppose the prior round's claims carried in the pack, ADR 0013).
func templateName(role Role, round int) string {
	if round > 1 {
		return string(role) + "-round2"
	}
	return string(role)
}

func wordRe(word string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
}

// frontmatterRoundRe captures the round from a story frontmatter `round: <n>` line.
var frontmatterRoundRe = regexp.MustCompile(`(?m)^round:\s*(\d+)\s*$`)

// frontmatterRound reads the round from a rendered story's frontmatter, or 0 when there is no round line (a story
// rendered before the round field was added, treated as "a different round" so it is re-rendered under --force).
func frontmatterRound(content []byte) int {
	m := frontmatterRoundRe.FindSubmatch(content)
	if m == nil {
		return 0
	}
	n := 0
	for _, c := range m[1] {
		n = n*10 + int(c-'0')
	}
	return n
}
