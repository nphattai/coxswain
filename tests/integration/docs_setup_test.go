package integration

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The Set up pages must be executable, not aspirational: every `cox ...` command in a fenced block on Install, Create a
// workspace, and First epic has to be one the onboarding E2E actually runs (DESIGN §6 / AC 4). This test extracts the
// command key (up to the first two non-flag tokens after a cox invocation) from the pages and from tests/e2e/onboarding.sh
// and asserts the page set is a subset of the E2E set. Adding an untested command to a Set up page fails the build.

// The README is the user guide now (DESIGN item 1), so it joins the Set up pages: every `cox` command in its Quick
// Start fences must be exercised by the onboarding E2E, exactly as the three getting-started pages are.
var setupPages = []string{
	"../../README.md",
	"../../docs/getting-started/install.md",
	"../../docs/getting-started/workspace.md",
	"../../docs/getting-started/first-epic.md",
}

const e2eScript = "../../tests/e2e/onboarding.sh"

// TestSetupCommandsUseHomeNotTilde guards the zsh trap the leader hit in review: `--repo alias=~/path` is NOT expanded
// after `=` in zsh (macOS default), so the tilde is recorded literally as a backend repo name. Every fenced command on
// the Set up pages must use $HOME instead of ~ for a home path.
func TestSetupCommandsUseHomeNotTilde(t *testing.T) {
	for _, page := range setupPages {
		inFence := false
		for i, line := range strings.Split(readFile(t, page), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if inFence && strings.Contains(line, "~/") {
				t.Errorf("%s:%d fenced command uses ~ for a home path; use $HOME (zsh does not expand ~ after =): %q", page, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestSetupPageCommandsAreInE2E(t *testing.T) {
	// The E2E's coverage per subcommand: subcommand -> the union of flag NAMES it exercises across all invocations.
	e2e := map[string]map[string]bool{}
	for _, c := range coxCommands(readFile(t, e2eScript), false) {
		if e2e[c.sub] == nil {
			e2e[c.sub] = map[string]bool{}
		}
		for f := range c.flags {
			e2e[c.sub][f] = true
		}
	}
	if len(e2e) == 0 {
		t.Fatalf("no cox commands found in %s", e2eScript)
	}
	total := 0
	for _, page := range setupPages {
		for _, c := range coxCommands(readFile(t, page), true) { // fenced blocks only
			total++
			cov, ok := e2e[c.sub]
			if !ok {
				t.Errorf("%s runs `cox %s` but %s never runs that subcommand; extend the E2E", page, c.sub, e2eScript)
				continue
			}
			for f := range c.flags {
				if !cov[f] {
					t.Errorf("%s runs `cox %s %s` but the E2E does not exercise %s on `cox %s`; extend the E2E", page, c.sub, f, f, c.sub)
				}
			}
		}
	}
	if total == 0 {
		t.Fatalf("no cox commands extracted from the Set up pages; the extractor is broken")
	}
}

// coxCmd is one cox invocation reduced to its subcommand (up to the first two non-flag tokens) and the set of flag
// NAMES it carries (values are ignored, so `--repo api` and `--repo web` both count as the `--repo` flag).
type coxCmd struct {
	sub   string
	flags map[string]bool
}

// coxCommands returns every cox invocation in text. When fencedOnly is true (markdown pages) only ``` fenced blocks are
// scanned.
func coxCommands(text string, fencedOnly bool) []coxCmd {
	var cmds []coxCmd
	inFence := !fencedOnly
	for _, line := range strings.Split(text, "\n") {
		if fencedOnly && strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		if c, ok := parseCoxCommand(strings.Fields(line)); ok {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

func parseCoxCommand(fields []string) (coxCmd, bool) {
	for i, f := range fields {
		if !isCoxInvocation(f) {
			continue
		}
		c := coxCmd{flags: map[string]bool{}}
		var sub []string
		seenFlag := false
		for _, tok := range fields[i+1:] {
			if strings.HasPrefix(tok, "-") {
				c.flags[strings.SplitN(tok, "=", 2)[0]] = true // flag name, dropping any =value
				seenFlag = true
				continue
			}
			// Subcommand is the first (up to two) non-flag tokens BEFORE any flag; tokens after a flag are flag values
			// or positional args, never part of the subcommand name.
			if !seenFlag && len(sub) < 2 {
				sub = append(sub, tok)
			}
		}
		c.sub = strings.Join(sub, " ")
		return c, c.sub != ""
	}
	return coxCmd{}, false
}

func isCoxInvocation(tok string) bool {
	tok = strings.Trim(tok, `"'`)
	return tok == "cox" || tok == "$COX" || strings.HasSuffix(tok, "/cox")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestNavStartsWithSetUp checks the docs contract behind AC 1, 2, and 5. The generated site nav is gone (DESIGN item 2), so the
// README's Documentation index is the nav now: it must list the three Set up pages first, in order. The rest of the
// contract is unchanged - the pages exist, both workspace shapes each end in a dispatched story, the QUICKSTART pointer
// survives for old links, and the README affirms single-repo use.
func TestNavStartsWithSetUp(t *testing.T) {
	readme := readFile(t, "../../README.md")

	// DESIGN item 3: the README Documentation index lists the three Set up pages first, in order.
	sec := docSection(readme, "## Documentation")
	if sec == "" {
		t.Fatal("README has no '## Documentation' section")
	}
	links := docSectionLinks(sec)
	want := []string{"docs/getting-started/install.md", "docs/getting-started/workspace.md", "docs/getting-started/first-epic.md"}
	for i, w := range want {
		if i >= len(links) || links[i] != w {
			t.Fatalf("README Documentation index must list the Set up pages first, in order; want %v as the first links, got %v", want, links)
		}
		if _, err := os.Stat("../../" + w); err != nil {
			t.Errorf("Set up page missing on disk: %s (%v)", w, err)
		}
	}

	// AC 2: both shapes, each worked example ending in a dispatched story.
	ws := readFile(t, "../../docs/getting-started/workspace.md")
	for _, shape := range []string{"Shape A", "Shape B"} {
		if !strings.Contains(ws, shape) {
			t.Errorf("workspace.md has no %q worked example", shape)
		}
	}
	if n := strings.Count(ws, "cox story dispatch"); n < 2 {
		t.Errorf("workspace.md: want both shapes to end in `cox story dispatch` (>=2), found %d", n)
	}

	// AC 5: old QUICKSTART URL still resolves (pointer page) and links into Set up.
	qs := readFile(t, "../../docs/QUICKSTART.md")
	if !strings.Contains(qs, "getting-started/install.md") {
		t.Error("QUICKSTART.md pointer does not link to the Set up pages")
	}

	// AC 5: README affirms single-repo support and does not imply it is out of scope.
	lower := strings.ToLower(readme)
	if strings.Contains(lower, "designed for work that crosses repositories") {
		t.Error("README still implies single-repo use is out of scope (\"designed for work that crosses repositories\")")
	}
	if !strings.Contains(lower, "single repo") {
		t.Error("README does not affirm single-repo support")
	}
}

// TestInstallPluginNote guards DESIGN item 4 (backlog B-26): the install page must say the Claude plugin is optional and
// skills-only (workspace .claude/settings.json hooks are the one hook source), and that an older plugin that still ships
// hooks makes every leader hook fire twice.
func TestInstallPluginNote(t *testing.T) {
	install := readFile(t, "../../docs/getting-started/install.md")
	for _, want := range []string{"optional", ".claude/settings.json", "fires twice"} {
		if !strings.Contains(install, want) {
			t.Errorf("install.md §3 is missing the plugin-optional note fragment %q (B-26)", want)
		}
	}
}

// docSection returns the body of a top-level `## ` section, from its heading to the next top-level heading.
func docSection(md, heading string) string {
	i := strings.Index(md, heading)
	if i < 0 {
		return ""
	}
	rest := md[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

var docLinkRe = regexp.MustCompile(`\]\((docs/[^)#\s]+)`)

// docSectionLinks returns the ordered docs/* markdown link targets in a section.
func docSectionLinks(section string) []string {
	var out []string
	for _, m := range docLinkRe.FindAllStringSubmatch(section, -1) {
		out = append(out, m[1])
	}
	return out
}
