package workspace

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/skills"
	"github.com/nphattai/coxswain/templates"
)

// Init writes cox/workspace.json and cox/policy.json under wsRoot from the defaults, creating cox/ and cox/services/.
// A file that already exists is left untouched (reported in `created`), so init is safe to re-run. When ws is non-nil
// its repos/projects seed workspace.json instead of the empty default (used by --from-repos-md).
func Init(wsRoot string, ws *Workspace) (created []string, err error) {
	// Validate a seed BEFORE writing anything: an invalid seed (e.g. a --from-repos-md row with an empty production)
	// must never land on disk, or the validating Load rejects it on every later command and the workspace is wedged.
	if ws != nil {
		if err := ws.Validate(); err != nil {
			return nil, err
		}
	}
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

// ScaffoldReport lists what a Scaffold run wrote (Created) and what it found already in place (Present), so the CLI can
// tell a first-time user everything it made and tell a re-run that nothing was missing.
type ScaffoldReport struct {
	Created []string
	Present []string
}

func (r *ScaffoldReport) created(p string) { r.Created = append(r.Created, p) }
func (r *ScaffoldReport) present(p string) { r.Present = append(r.Present, p) }

// Scaffold writes everything a leader needs under wsRoot, creating only what is missing and never rewriting a file that
// exists (so a re-run is idempotent and a user-edited workspace.json/policy.json/AGENTS.md is never clobbered): the two
// registry files and cox/services/ (via Init), a .gitignore covering the machine-bound paths, an AGENTS.md skeleton,
// and the pinned leader skills under .agents/skills/. Hooks are written by the caller (they depend on the policy's
// leader harness options). When workspace.json is absent, repos seeds it, so the caller must pass at least one repo in
// that case; on a re-run repos may be empty and the existing registry is kept.
func Scaffold(wsRoot string, repos []Repo) (ScaffoldReport, error) {
	var rep ScaffoldReport
	wsPath := filepath.Join(wsRoot, ControlDir, "workspace.json")
	wsExisted := fileExists(wsPath)
	var seed *Workspace
	if !wsExisted {
		if len(repos) == 0 {
			return rep, fmt.Errorf("no repos to write into %s (pass --repo alias=path)", wsPath)
		}
		seed = &Workspace{Repos: repos, Hosts: []Host{{Name: "local"}}}
	}
	created, err := Init(wsRoot, seed)
	if err != nil {
		return rep, err
	}
	for _, c := range created {
		rep.created(c)
	}
	// Everything Init did not (re)create is already present.
	for _, p := range []string{wsPath, filepath.Join(wsRoot, ControlDir, "policy.json")} {
		if !contains(created, p) {
			rep.present(p)
		}
	}

	ws, err := Load(wsRoot)
	if err != nil {
		return rep, err
	}
	if err := ensureGitignore(wsRoot, ws, &rep); err != nil {
		return rep, err
	}
	if err := ensureFile(filepath.Join(wsRoot, "AGENTS.md"), "agents.md", &rep); err != nil {
		return rep, err
	}
	if err := ensureSkills(wsRoot, &rep); err != nil {
		return rep, err
	}
	return rep, nil
}

// DetectProduction returns a git checkout's default branch: the short name of origin/HEAD (e.g. "main" from
// refs/remotes/origin/HEAD -> origin/main), falling back to "main" when there is no origin/HEAD (a fresh checkout with
// no remote, or a bare path). It never fails; a non-git path also yields "main".
func DetectProduction(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		if b := strings.TrimSpace(string(out)); b != "" {
			return strings.TrimPrefix(b, "origin/")
		}
	}
	return "main"
}

// AddRepo appends a repo to <ws>/cox/workspace.json, refusing a duplicate alias, and re-validates the result. It reads
// and rewrites the file (rather than the in-memory workspace) so `cox workspace add-repo` and `cox epic new --repo
// alias=ref` persist through the same path.
func AddRepo(wsRoot string, r Repo) error {
	path := filepath.Join(wsRoot, ControlDir, "workspace.json")
	ws, err := LoadFile(path)
	if err != nil {
		return err
	}
	if _, ok := ws.Repo(r.Alias); ok {
		return fmt.Errorf("repo alias %q already in %s", r.Alias, path)
	}
	ws.Repos = append(ws.Repos, r)
	if err := ws.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return err
	}
	// Keep .gitignore's per-alias symlink rule in step with the registry, so a repo added after init still has its
	// */epics/*/<alias> worktree symlinks ignored (DESIGN §2).
	var rep ScaffoldReport
	return ensureGitignore(wsRoot, ws, &rep)
}

// gitignoreRules returns the machine-bound paths a workspace .gitignore must cover (DESIGN §2): the local registry, the
// cache, every live and closed epic control tree, and one alias-symlink line per repo.
func gitignoreRules(ws *Workspace) []string {
	rules := []string{"cox/workspace.json", "cox/.cache/", "**/.cox/", "**/.cox.closed/"}
	for _, r := range ws.Repos {
		rules = append(rules, "*/epics/*/"+r.Alias)
	}
	return rules
}

// ensureGitignore adds any missing rule to <ws>/.gitignore, creating the file when absent and appending only the lines
// it lacks so a user's own entries survive.
func ensureGitignore(wsRoot string, ws *Workspace, rep *ScaffoldReport) error {
	path := filepath.Join(wsRoot, ".gitignore")
	existing := map[string]bool{}
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			existing[strings.TrimSpace(l)] = true
		}
		lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}
	var added []string
	for _, rule := range gitignoreRules(ws) {
		if !existing[rule] {
			lines = append(lines, rule)
			existing[rule] = true
			added = append(added, rule)
		}
	}
	if len(added) == 0 {
		rep.present(path)
		return nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	rep.created(fmt.Sprintf("%s (+%d rule(s))", path, len(added)))
	return nil
}

// ensureFile writes a template file to dest only when dest is absent, so a user-edited copy is never overwritten.
func ensureFile(dest, tmpl string, rep *ScaffoldReport) error {
	if fileExists(dest) {
		rep.present(dest)
		return nil
	}
	content, err := templates.File(tmpl)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return err
	}
	rep.created(dest)
	return nil
}

// ensureSkills copies the embedded leader skills under <ws>/.agents/skills/, writing only files that are missing.
func ensureSkills(wsRoot string, rep *ScaffoldReport) error {
	base := filepath.Join(wsRoot, ".agents", "skills")
	missing := 0
	present := 0
	err := fs.WalkDir(skills.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dest := filepath.Join(base, p)
		if fileExists(dest) {
			present++
			return nil
		}
		b, rErr := skills.FS.ReadFile(p)
		if rErr != nil {
			return rErr
		}
		if mErr := os.MkdirAll(filepath.Dir(dest), 0o755); mErr != nil {
			return mErr
		}
		if wErr := os.WriteFile(dest, b, 0o644); wErr != nil {
			return wErr
		}
		missing++
		return nil
	})
	if err != nil {
		return err
	}
	if missing > 0 {
		rep.created(fmt.Sprintf("%s (%d skill file(s))", base, missing))
	} else {
		rep.present(base)
	}
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
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
