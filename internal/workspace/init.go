package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/templates"
)

// Init writes cox/workspace.json and cox/policy.json under wsRoot from the defaults, creating cox/ and cox/services/.
// A file that already exists is left untouched (reported in `created`), so init is safe to re-run. When ws is non-nil
// its repos/projects seed workspace.json instead of the empty default (used by --from-repos-md).
func Init(wsRoot string, ws *Workspace) (created []string, err error) {
	dir := filepath.Join(wsRoot, ControlDir)
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		return nil, err
	}
	wsPath := filepath.Join(dir, "workspace.json")
	if _, statErr := os.Stat(wsPath); os.IsNotExist(statErr) {
		content, tErr := templates.File("workspace.json")
		if tErr != nil {
			return created, tErr
		}
		if ws != nil {
			b, mErr := json.MarshalIndent(ws, "", "  ")
			if mErr != nil {
				return created, mErr
			}
			content = append(b, '\n')
		}
		if err := os.WriteFile(wsPath, content, 0o644); err != nil {
			return created, err
		}
		created = append(created, wsPath)
	}
	polPath := filepath.Join(dir, "policy.json")
	if _, statErr := os.Stat(polPath); os.IsNotExist(statErr) {
		content, tErr := templates.File("policy.json")
		if tErr != nil {
			return created, tErr
		}
		if err := os.WriteFile(polPath, content, 0o644); err != nil {
			return created, err
		}
		created = append(created, polPath)
	}
	return created, nil
}

// FromReposMD parses a v1 <project>/docs/repos.md into a Workspace with one Repo per table row. It reads every GitHub
// pipe table in the file, maps columns by their header names (Alias, Repo, Production, Staging), and skips rows whose
// alias or repo is empty. A "none"/"-" staging cell becomes an empty Staging. Repo names are used as the Orca `name`;
// an absolute-path cell (leading /) is stored as Path instead. Header matching is case-insensitive.
func FromReposMD(path string) (*Workspace, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read repos.md: %w", err)
	}
	var (
		ws      Workspace
		header  []string
		inTable bool
		seen    = map[string]bool{}
	)
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") {
			inTable = false
			header = nil
			continue
		}
		cells := splitRow(t)
		if isSeparatorRow(cells) {
			inTable = true // the row before was the header
			continue
		}
		if !inTable {
			header = lower(cells)
			continue
		}
		r := rowToRepo(header, cells)
		if r.Alias == "" || (r.Name == "" && r.Path == "") || seen[r.Alias] {
			continue
		}
		seen[r.Alias] = true
		ws.Repos = append(ws.Repos, r)
	}
	if len(ws.Repos) == 0 {
		return nil, fmt.Errorf("no repo rows found in %s (expected a table with Alias/Repo/Production columns)", path)
	}
	return &ws, nil
}

// rowToRepo maps a table row to a Repo using the header names.
func rowToRepo(header, cells []string) Repo {
	col := func(names ...string) string {
		for i, h := range header {
			for _, n := range names {
				if h == n && i < len(cells) {
					return strings.TrimSpace(cells[i])
				}
			}
		}
		return ""
	}
	r := Repo{
		Alias:      col("alias"),
		Production: col("production", "production branch"),
		Staging:    normalizeBranch(col("staging", "staging branch")),
	}
	repo := col("repo", "repository")
	// A repos.md "Repo" cell may read "acme-apps (ExampleOrg)" or "ExampleOrg/acme-partner-admin"; keep the org/name
	// token, dropping a trailing "(owner)" note.
	repo = strings.TrimSpace(strings.SplitN(repo, "(", 2)[0])
	if strings.HasPrefix(repo, "/") {
		r.Path = repo
	} else {
		r.Name = repo
	}
	r.Production = normalizeBranch(r.Production)
	return r
}

// normalizeBranch treats "none", "-", and "" (and @-annotated cells like "dev (@dev)") as an empty/base value, keeping
// only the first branch token before any comma or parenthetical note (a cell may list several staging branches).
func normalizeBranch(v string) string {
	v = strings.TrimSpace(strings.SplitN(v, "(", 2)[0])
	v = strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
	switch strings.ToLower(v) {
	case "", "none", "-", "todo", "n/a":
		return ""
	}
	return v
}

func splitRow(line string) []string {
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

// isSeparatorRow reports whether every cell is a markdown table separator (---, :--:, etc).
func isSeparatorRow(cells []string) bool {
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" || strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return len(cells) > 0
}

func lower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}
