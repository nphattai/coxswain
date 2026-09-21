package integration

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestDocsLinks replaces the former strict docs-site build gate (DESIGN item 3 / AC 4). It walks README.md and
// docs/**/*.md and fails on a relative link to a missing file, or a `#anchor` that no heading in the target file
// produces under GitHub's slug rules. External URLs (http, https, mailto, tel) are not fetched.
//
// This test FAILS on main @ 4dbd96f and passes on this PR: the former docs site rendered every page at a directory URL, so a
// page-relative link like docs/getting-started/concepts.md's `../../assets/diagrams/story-lifecycle.svg` resolved to the
// site root, and docs/index.md linked pretty URLs such as `getting-started/install/`. GitHub renders those relative to
// the file, where they point above docs/ (missing) or at a non-existent directory. Item 2 fixed the diagram paths and
// deleted the site hero; this test guards that they stay resolvable on GitHub.
func TestDocsLinks(t *testing.T) {
	files := docMarkdownFiles(t)
	anchors := make(map[string]map[string]bool, len(files))
	for _, f := range files {
		anchors[f] = headingAnchors(readFile(t, f))
	}

	for _, f := range files {
		dir := filepath.Dir(f)
		for _, ln := range docLinks(readFile(t, f)) {
			if isExternalLink(ln.target) {
				continue
			}
			path, anchor := splitAnchor(ln.target)
			if path == "" { // pure "#anchor" -> the same file
				if anchor != "" && !anchors[f][anchor] {
					t.Errorf("%s:%d links to #%s but no heading in the same file produces that anchor", f, ln.line, anchor)
				}
				continue
			}
			target := filepath.Clean(filepath.Join(dir, path))
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s:%d links to %q but %s does not exist", f, ln.line, ln.target, target)
				continue
			}
			if anchor != "" && strings.HasSuffix(target, ".md") {
				if !anchors[target][anchor] {
					t.Errorf("%s:%d links to %q but no heading in %s produces #%s", f, ln.line, ln.target, target, anchor)
				}
			}
		}
	}
}

// docMarkdownFiles is README.md plus every docs/**/*.md, each path prefixed with the repo root relative to this package
// so os.Stat and the link resolution below hit the real files.
func docMarkdownFiles(t *testing.T) []string {
	t.Helper()
	files := []string{"../../README.md"}
	err := filepath.Walk("../../docs", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".md") {
			files = append(files, filepath.Clean(p))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	return files
}

type docLink struct {
	line   int
	target string
}

var (
	mdLinkRe    = regexp.MustCompile(`\]\(([^)]+)\)`)
	htmlAttrRe  = regexp.MustCompile(`(?:href|src)="([^"]+)"`)
	srcsetRe    = regexp.MustCompile(`srcset="([^"]+)"`)
	headingLine = regexp.MustCompile(`^#{1,6}\s+(.*)`)
	headingLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// docLinks returns every link target in the markdown, from inline `](...)` links and from HTML href/src/srcset
// attributes, skipping fenced code blocks. A trailing markdown title or srcset descriptor is dropped.
func docLinks(content string) []docLink {
	var out []docLink
	inFence := false
	for i, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		add := func(raw string) {
			t := strings.TrimSpace(raw)
			if j := strings.IndexAny(t, " \t"); j >= 0 { // drop `](url "title")` and srcset "url 640w"
				t = t[:j]
			}
			if t != "" {
				out = append(out, docLink{line: i + 1, target: t})
			}
		}
		for _, m := range mdLinkRe.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		for _, m := range htmlAttrRe.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		for _, m := range srcsetRe.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
	}
	return out
}

// headingAnchors is the set of GitHub anchors a file's Markdown headings produce (github-slugger rules, with the
// -1/-2 suffix for duplicates). Explicit HTML id= anchors are intentionally not counted: DESIGN item 3 checks a link
// against the anchors a heading produces.
func headingAnchors(content string) map[string]bool {
	anchors := map[string]bool{}
	counts := map[string]int{}
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		st := strings.TrimSpace(line)
		if strings.HasPrefix(st, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := headingLine.FindStringSubmatch(st)
		if m == nil {
			continue
		}
		h := headingLink.ReplaceAllString(m[1], "$1") // keep link text, drop the URL
		h = strings.NewReplacer("`", "", "*", "").Replace(h)
		base := slugify(h)
		if n, ok := counts[base]; ok {
			counts[base] = n + 1
			anchors[base+"-"+strconv.Itoa(n+1)] = true
		} else {
			counts[base] = 0
			anchors[base] = true
		}
	}
	return anchors
}

// slugify is the github-slugger heading algorithm: lowercase, keep letters/marks/numbers/underscore/space/hyphen, drop
// everything else, then spaces become hyphens.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsMark(r), unicode.IsNumber(r), r == '_', r == '-', r == ' ':
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), " ", "-")
}

func isExternalLink(target string) bool {
	for _, p := range []string{"http://", "https://", "mailto:", "tel:", "//", "data:"} {
		if strings.HasPrefix(target, p) {
			return true
		}
	}
	return false
}

// splitAnchor splits `path#anchor` and strips any `?query` from the path.
func splitAnchor(target string) (path, anchor string) {
	path = target
	if i := strings.Index(path, "#"); i >= 0 {
		anchor = path[i+1:]
		path = path[:i]
	}
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	return path, anchor
}
