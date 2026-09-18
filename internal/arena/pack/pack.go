// Package pack builds the shared, verified, blinded context pack an arena hands every role. Build gathers the epic's
// DESIGN.md, its scout reports, the project architecture docs, and the decisions still in effect; scout-checks every
// "<alias>/<path>:<line>" citation against the repo it names (a broken citation is a hard error, no pack); blinds the
// text (authorship, model/harness names, discussion history, commit-message links) so review is on the design, not on
// who wrote it; and writes reports/arena/context-pack.md with a header naming each source and its content sha.
package pack

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/arena/cite"
)

// source is one gathered document.
type source struct {
	Label   string // human label for the pack header (e.g. "DESIGN.md", "scout/cox")
	Path    string // absolute path read from
	Content string // raw content (pre-blind)
}

// Build assembles the context pack for a round and returns its path (reports/arena/context-pack.md). wsRoot is the
// workspace root (unused for gathering today; the project dir is derived from the epic dir, but wsRoot is kept so a
// future workspace architecture layout does not change the signature). A citation that does not resolve aborts the build
// with every broken citation listed. For round > 1 the pack gains the prior round's verified claim table, the leader's
// provisional verdicts, and an "Answer these" section, so a round-2 role opposes the round-1 claims (ADR 0013); the
// added block is blinded too.
func Build(epicDir, wsRoot string, round int) (string, error) {
	if round < 1 {
		round = 1
	}
	sources, err := gather(epicDir)
	if err != nil {
		return "", err
	}
	aliases, err := cite.Aliases(epicDir)
	if err != nil {
		return "", err
	}
	if errs := scoutCheck(epicDir, aliases, sources); len(errs) > 0 {
		return "", fmt.Errorf("context pack aborted: %d broken citation(s):\n  %s", len(errs), strings.Join(errs, "\n  "))
	}

	repos, err := cite.Repos(epicDir)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# Arena context pack (blinded)\n\n")
	b.WriteString("Review the design on its merits. Authorship, model, and discussion history are removed on purpose.\n\n")
	b.WriteString("## Sources\n")
	b.WriteString("`content:<sha256>` is a hash of the text below, NOT a git commit. Do not cite it.\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "- %s content:%s\n", s.Label, contentSha(s.Content))
	}
	b.WriteString("\n## Repos\n")
	b.WriteString("Cite lines as `<alias>/<path>:<line>@<sha>` using each repo's git HEAD below (that sha resolves against `git show`).\n")
	for _, r := range repos {
		fmt.Fprintf(&b, "- %s git HEAD %s\n", r.Alias, headSha(epicDir, r.Alias))
	}
	b.WriteString("\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", s.Label, strings.TrimRight(blind(s.Content), "\n"))
	}

	if round > 1 {
		opp, err := opposition(epicDir, round)
		if err != nil {
			return "", err
		}
		b.WriteString(blind(opp))
	}

	outDir := filepath.Join(epicDir, "reports", "arena")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, "context-pack.md")
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// gather collects DESIGN.md, reports/scout/*.md, <project>/docs/architecture/*.md, and the in-effect decisions.
func gather(epicDir string) ([]source, error) {
	var sources []source
	design := filepath.Join(epicDir, "DESIGN.md")
	c, err := os.ReadFile(design)
	if err != nil {
		return nil, fmt.Errorf("read DESIGN.md: %w", err)
	}
	sources = append(sources, source{Label: "DESIGN.md", Path: design, Content: string(c)})

	sources = append(sources, glob(filepath.Join(epicDir, "reports", "scout"), "scout/")...)

	projectDir := filepath.Dir(filepath.Dir(epicDir)) // <ws>/<project>/epics/<slug> -> <ws>/<project>
	sources = append(sources, glob(filepath.Join(projectDir, "docs", "architecture"), "architecture/")...)
	sources = append(sources, decisions(filepath.Join(projectDir, "docs", "decisions"))...)
	return sources, nil
}

// glob reads every *.md in dir (sorted) as a source, labelled prefix+basename. A missing dir yields nothing.
func glob(dir, prefix string) []source {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []source
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		c, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, source{Label: prefix + strings.TrimSuffix(e.Name(), ".md"), Path: p, Content: string(c)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// supersededRe detects a decision that has been superseded (a non-empty superseded_by field), so it is dropped from the
// pack. It matches the frontmatter/field either as "superseded_by: 0009" or "- Superseded by: 0009".
var supersededRe = regexp.MustCompile(`(?im)^[-\s]*superseded[_ ]by:\s*\S`)

// decisions reads docs/decisions/*.md, skipping the 0000-template and any decision with a non-empty superseded_by.
func decisions(dir string) []source {
	var out []source
	for _, s := range glob(dir, "decision/") {
		if strings.Contains(s.Label, "0000-template") {
			continue
		}
		if supersededRe.MatchString(s.Content) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// scoutCheck verifies every alias-prefixed citation in every source. It returns one message per broken citation,
// prefixed with the source label so the operator knows where to fix it.
func scoutCheck(epicDir string, aliases map[string]bool, sources []source) []string {
	var errs []string
	for _, s := range sources {
		for _, cit := range cite.Find(s.Content, aliases) {
			if err := cite.Verify(epicDir, cit); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", s.Label, err))
			}
		}
	}
	return errs
}

func contentSha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum)[:12]
}

// headSha returns the git HEAD sha of the repo an alias resolves to, so a role cites against a real commit. It returns
// "unknown" when the alias does not resolve or git fails (the pack still builds; the role then reads the sha itself).
func headSha(epicDir, alias string) string {
	dir, err := cite.RepoDir(epicDir, alias)
	if err != nil {
		return "unknown"
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
