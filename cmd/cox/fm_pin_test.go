package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fmPin is the firstmate commit ADR 0021 Decision 2 pins the supervision corpus at.
const fmPin = "a8572f6"

// fmCitationRe is one `// fm: <path>:<line>` citation (ADR 0021 Decision 2), with its optional @<sha>.
var fmCitationRe = regexp.MustCompile(`fm: ?[A-Za-z0-9_./-]+\.(?:sh|md|js|ts|json):\d[\d,-]*(@[0-9a-f]+)?`)

// Every firstmate citation in the Go tree names the pin it was read at, and that pin is ADR 0021's, so a pin move
// cannot leave a citation silently pointing at the old corpus (epic cox-refresh, story cox-refresh-routing-pin).
func TestFMCitationsCarryTheADR0021Pin(t *testing.T) {
	root := filepath.Join("..", "..")
	adr, err := os.ReadFile(filepath.Join(root, "docs", "decisions", "0021-supervision-translated-from-firstmate.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adr), "at `"+fmPin+"`") {
		t.Fatalf("ADR 0021 Decision 2 does not pin firstmate at %s", fmPin)
	}
	var bad []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range fmCitationRe.FindAllStringSubmatch(line, -1) {
				if m[1] != "@"+fmPin {
					bad = append(bad, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(p), i+1, m[0]))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("%d firstmate citation(s) not pinned @%s:\n%s", len(bad), fmPin, strings.Join(bad[:min(len(bad), 20)], "\n"))
	}
}
