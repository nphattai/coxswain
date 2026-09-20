package integration

import (
	"os"
	"strings"
	"testing"
)

// The Set up pages must be executable, not aspirational: every `cox ...` command in a fenced block on Install, Create a
// workspace, and First epic has to be one the onboarding E2E actually runs (DESIGN §6 / AC 4). This test extracts the
// command key (up to the first two non-flag tokens after a cox invocation) from the pages and from tests/e2e/onboarding.sh
// and asserts the page set is a subset of the E2E set. Adding an untested command to a Set up page fails the build.

var setupPages = []string{
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
	e2e := coxCommandKeys(readFile(t, e2eScript), false)
	if len(e2e) == 0 {
		t.Fatalf("no cox commands found in %s", e2eScript)
	}
	total := 0
	for _, page := range setupPages {
		keys := coxCommandKeys(readFile(t, page), true) // fenced blocks only
		for key := range keys {
			total++
			if !e2e[key] {
				t.Errorf("%s runs `cox %s` but %s does not; extend the E2E instead of documenting an untested command", page, key, e2eScript)
			}
		}
	}
	if total == 0 {
		t.Fatalf("no cox commands extracted from the Set up pages; the extractor is broken")
	}
}

// coxCommandKeys returns the set of command keys in text. A key is up to the first two non-flag tokens after a cox
// invocation token (`cox`, `$COX`, `"$COX"`, or any token ending in /cox), e.g. "workspace init", "epic new", "doctor".
// When fencedOnly is true (markdown pages), only lines inside ``` fenced code blocks are scanned.
func coxCommandKeys(text string, fencedOnly bool) map[string]bool {
	keys := map[string]bool{}
	inFence := !fencedOnly
	for _, line := range strings.Split(text, "\n") {
		if fencedOnly && strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		if key := commandKey(strings.Fields(line)); key != "" {
			keys[key] = true
		}
	}
	return keys
}

func commandKey(fields []string) string {
	for i, f := range fields {
		if !isCoxInvocation(f) {
			continue
		}
		var parts []string
		for _, tok := range fields[i+1:] {
			if strings.HasPrefix(tok, "-") { // a flag ends the command name
				break
			}
			parts = append(parts, tok)
			if len(parts) == 2 {
				break
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
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

// TestNavStartsWithSetUp checks the docs contract behind AC 1, 2, and 5: the nav starts with Set up, the three pages
// exist, both workspace shapes each end in a dispatched story, the QUICKSTART pointer survives for old links, and the
// README no longer implies single-repo use is out of scope.
func TestNavStartsWithSetUp(t *testing.T) {
	nav := readFile(t, "../../mkdocs.yml")
	setUp := strings.Index(nav, "- Set up:")
	operate := strings.Index(nav, "- Operate:")
	reference := strings.Index(nav, "- Reference:")
	understand := strings.Index(nav, "- Understand:")
	if setUp < 0 {
		t.Fatal("mkdocs.yml nav has no top-level 'Set up' section")
	}
	if !(setUp < operate && operate < reference && reference < understand) {
		t.Errorf("nav order wrong: want Set up < Operate < Reference < Understand, got offsets %d, %d, %d, %d", setUp, operate, reference, understand)
	}
	for _, p := range []string{"getting-started/install.md", "getting-started/workspace.md", "getting-started/first-epic.md"} {
		if !strings.Contains(nav, p) {
			t.Errorf("nav does not list %s", p)
		}
		if _, err := os.Stat("../../docs/" + p); err != nil {
			t.Errorf("Set up page missing on disk: docs/%s (%v)", p, err)
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

	// AC 5: README no longer implies single-repo use is out of scope.
	readme := strings.ToLower(readFile(t, "../../README.md"))
	if strings.Contains(readme, "designed for work that crosses repositories") {
		t.Error("README still implies single-repo use is out of scope (\"designed for work that crosses repositories\")")
	}
	if !strings.Contains(readme, "single repo") {
		t.Error("README does not affirm single-repo support")
	}
}
