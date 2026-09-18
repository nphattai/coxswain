// Package cite verifies source citations against git. A citation is "<alias>/<path>:<line>[@<sha>]": the first path
// segment is a repo alias (an epic's alias symlink, e.g. <epic>/cox, points at that repo's worktree), the rest is the
// in-repo path, line is 1-based, and the optional sha pins the blob to a commit (HEAD when absent). Both arena's context
// pack (scout-check at HEAD) and arena check (per-claim evidence at a sha) resolve citations the same way, so the rule
// lives here once.
package cite

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// tokenRe matches a citation token: a slashed path, ":", a line number, and an optional "@<sha>". The path needs at
// least one slash so a bare "word:12" in prose is not mistaken for a citation; the leading segment is the repo alias.
var tokenRe = regexp.MustCompile(`([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_./-]+)+):(\d+)(?:@([0-9a-fA-F]{7,40}))?`)

// Citation is one parsed reference.
type Citation struct {
	Raw   string // the exact matched text
	Alias string // first path segment
	Path  string // in-repo path (after the alias)
	Line  int
	SHA   string // "" when the citation is unpinned (checked at HEAD)
}

// Parse pulls one citation from a token. ok is false when the token is not a citation shape.
func Parse(token string) (Citation, bool) {
	m := tokenRe.FindStringSubmatch(strings.TrimSpace(token))
	if m == nil {
		return Citation{}, false
	}
	return fromMatch(m), true
}

// Find returns every citation token in text whose leading segment is a known alias. A slashed "path:line" whose first
// segment is not an alias is prose (or a URL fragment), not a citation, and is skipped rather than flagged.
func Find(text string, aliases map[string]bool) []Citation {
	var out []Citation
	for _, m := range tokenRe.FindAllStringSubmatch(text, -1) {
		c := fromMatch(m)
		if aliases[c.Alias] {
			out = append(out, c)
		}
	}
	return out
}

func fromMatch(m []string) Citation {
	line, _ := strconv.Atoi(m[2])
	alias, path, _ := strings.Cut(m[1], "/")
	return Citation{Raw: m[0], Alias: alias, Path: path, Line: line, SHA: m[3]}
}

// RepoDir resolves the local git checkout for an alias. It reads the epic repos file first (the alias's ref as an
// absolute path, or a repo name resolved through cox/workspace.json); the <epic>/<alias> symlink is only a fallback for
// an epic bootstrapped with a symlink and no path. It errors when nothing resolves to a directory, which also validates
// the alias. arena check (per-claim evidence) and the context pack (scout-check) both call this, so alias resolution is
// never split-brained between the repos file and the symlink.
func RepoDir(epicDir, alias string) (string, error) {
	if dir := resolveRepoDir(epicDir, alias); dir != "" {
		return dir, nil
	}
	link := filepath.Join(epicDir, alias)
	if info, err := os.Stat(link); err == nil && info.IsDir() {
		return link, nil
	}
	return "", fmt.Errorf("unknown repo alias %q (no repos path, no workspace repo, no checkout at %s)", alias, link)
}

// Verify confirms a citation exists: its file is present at the pinned sha (or HEAD when unpinned) in the alias repo and
// has at least Line lines. The returned error names precisely what failed (unknown alias, missing file at sha, or a line
// past end of file) so a caller can list every broken citation.
func Verify(epicDir string, c Citation) error {
	dir, err := RepoDir(epicDir, c.Alias)
	if err != nil {
		return fmt.Errorf("%s: %w", c.Raw, err)
	}
	ref := c.SHA
	if ref == "" {
		ref = "HEAD"
	}
	out, err := exec.Command("git", "-C", dir, "show", ref+":"+c.Path).Output()
	if err != nil {
		return fmt.Errorf("%s: file %q not found at %s", c.Raw, c.Path, shortRef(ref))
	}
	if n := lineCount(out); c.Line < 1 || c.Line > n {
		return fmt.Errorf("%s: line %d out of range (%q has %d lines at %s)", c.Raw, c.Line, c.Path, n, shortRef(ref))
	}
	return nil
}

func shortRef(ref string) string {
	if len(ref) > 12 && ref != "HEAD" {
		return ref[:12]
	}
	return ref
}

// lineCount counts lines in content. A file ending without a trailing newline still counts its last line.
func lineCount(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := strings.Count(string(b), "\n")
	if !strings.HasSuffix(string(b), "\n") {
		n++
	}
	return n
}
