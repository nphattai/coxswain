package workspace

import (
	"bytes"
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

// TemplatePolicy parses the embedded default policy template (templates/policy.json). It is the reference set of harness
// options `cox workspace init` compares an on-disk policy against for the stale-options notice (DESIGN item 6). It does
// not validate: it is read only for its harness option lists.
func TemplatePolicy() (*Policy, error) {
	b, err := templates.File("policy.json")
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse template policy: %w", err)
	}
	return &p, nil
}

// StaleOptionNotices returns one notice per harness the template lists in harness.leader.options or
// harness.worker.options that the on-disk policy's matching list lacks (DESIGN item 6). It reports MISSING options only
// (template ⟹ on-disk), never on-disk extras, so an old workspace policy that predates a newly-added harness (e.g. pi)
// gets a heads-up naming it. It is pure and never writes: `cox workspace init` prints the notices and leaves
// cox/policy.json byte-identical, so the captain adds the option by hand.
func StaleOptionNotices(onDisk, tmpl *Policy) []string {
	var notices []string
	missing := func(role string, have, want []string) {
		set := make(map[string]bool, len(have))
		for _, h := range have {
			set[h] = true
		}
		for _, h := range want {
			if !set[h] {
				notices = append(notices, fmt.Sprintf(
					"cox/policy.json harness.%s.options is missing %q (the template lists it); add it by hand to enable %s", role, h, h))
			}
		}
	}
	missing("leader", onDisk.Harness.Leader.Options, tmpl.Harness.Leader.Options)
	missing("worker", onDisk.Harness.Worker.Options, tmpl.Harness.Worker.Options)
	return notices
}

// HealPolicy writes into the policy file at path every REQUIRED section (the ones Validate checks) whose top-level key is
// absent, using the embedded template's section verbatim, and returns the keys it added in template order (captain
// ruling 2026-09-25, B-43: a workspace policy that predates a required section, such as merge, otherwise fails every
// command). It inserts text before the closing brace rather than re-marshalling, so every existing byte, key order and
// value stays as written; a present section is never touched, even when invalid (Validate keeps naming it). A file that
// is not a JSON object is an error naming the path.
func HealPolicy(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var disk map[string]json.RawMessage
	if err := json.Unmarshal(b, &disk); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if disk == nil {
		return nil, fmt.Errorf("parse %s: not a JSON object", path)
	}
	tb, err := templates.File("policy.json")
	if err != nil {
		return nil, err
	}
	var tmpl map[string]json.RawMessage
	if err := json.Unmarshal(tb, &tmpl); err != nil {
		return nil, fmt.Errorf("parse template policy: %w", err)
	}
	var added []string
	var ins strings.Builder
	for _, s := range (&Policy{}).sections() {
		if _, ok := disk[s.name]; ok {
			continue
		}
		if len(disk) > 0 || len(added) > 0 {
			ins.WriteString(",")
		}
		fmt.Fprintf(&ins, "\n  %q: %s", s.name, tmpl[s.name])
		added = append(added, s.name)
	}
	if len(added) == 0 {
		return nil, nil
	}
	body := bytes.TrimRight(b, " \t\r\n")
	body = bytes.TrimRight(body[:len(body)-1], " \t\r\n") // drop the closing brace of the object
	out := append(append([]byte{}, body...), ins.String()+"\n}\n"...)
	// Replace atomically: a crash or a full disk mid-write must never leave the captain's policy truncated.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy.json.*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	return added, nil
}

// ScaffoldReport lists what a Scaffold run wrote (Created), rewrote in place (Updated) and found already current
// (Present), so the CLI can name every file a run changed.
type ScaffoldReport struct {
	Created []string
	Updated []string
	Present []string
}

func (r *ScaffoldReport) created(p string) { r.Created = append(r.Created, p) }
func (r *ScaffoldReport) updated(p string) { r.Updated = append(r.Updated, p) }
func (r *ScaffoldReport) present(p string) { r.Present = append(r.Present, p) }

// Scaffold writes everything a leader needs under wsRoot, creating only what is missing and never rewriting a user-owned
// file (so a re-run is idempotent and a user-edited workspace.json/policy.json/AGENTS.md is never clobbered; the pinned
// leader skills are the exception, refreshed from the embed by ensureSkills): the two
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
		// Empty (not nil) projects/services so the JSON emits [] rather than null (a null trips strict readers and reads
		// as a broken registry).
		seed = &Workspace{Projects: []Project{}, Repos: repos, Services: []Service{}, Hosts: []Host{{Name: "local"}}}
	}
	created, err := Init(wsRoot, seed)
	if err != nil {
		return rep, err
	}
	for _, c := range created {
		rep.created(c)
	}
	// Everything Init did not (re)create is already present. An existing policy first gains any required section it
	// predates (B-43), each named on its own Updated line. A policy that cannot be healed (unparseable) does not stop the
	// rest of the scaffold: its error is returned once everything else is in place.
	var healErr error
	if !contains(created, wsPath) {
		rep.present(wsPath)
	}
	polPath := filepath.Join(wsRoot, ControlDir, "policy.json")
	if !contains(created, polPath) {
		added, err := HealPolicy(polPath)
		healErr = err
		for _, k := range added {
			rep.updated(fmt.Sprintf("%s (+%s from template)", polPath, k))
		}
		if len(added) == 0 && err == nil {
			rep.present(polPath)
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
	return rep, healErr
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
// cache, every live and closed epic control tree, the per-machine Pi leader extension (installed by `cox workspace init`
// / `cox workspace hooks --harness pi`, never committed - captain ruling 2026-09-23), and one alias-symlink line per repo.
func gitignoreRules(ws *Workspace) []string {
	rules := []string{"cox/workspace.json", "cox/.cache/", "**/.cox/", "**/.cox.closed/", ".pi/extensions/"}
	for _, r := range ws.Repos {
		// **/ (not */) so the rule also matches a nested project layout, e.g. apps/foo/epics/demo/<alias>.
		rules = append(rules, "**/epics/*/"+r.Alias)
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

// ensureSkills copies the embedded leader skills under <ws>/.agents/skills/. They are pinned copies of the driver's
// skills, not user-owned (B-72): a missing file is written, a file whose content differs from the embed is rewritten and
// reported in Updated, so a driver upgrade followed by init leaves the workspace current. Files the embed does not carry
// are never touched.
func ensureSkills(wsRoot string, rep *ScaffoldReport) error {
	base := filepath.Join(wsRoot, ".agents", "skills")
	missing := 0
	err := fs.WalkDir(skills.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rErr := skills.FS.ReadFile(p)
		if rErr != nil {
			return rErr
		}
		dest := filepath.Join(base, p)
		existed := fileExists(dest)
		if existed {
			if cur, cErr := os.ReadFile(dest); cErr == nil && bytes.Equal(cur, b) {
				return nil
			}
		}
		if mErr := os.MkdirAll(filepath.Dir(dest), 0o755); mErr != nil {
			return mErr
		}
		if wErr := os.WriteFile(dest, b, 0o644); wErr != nil {
			return wErr
		}
		if existed {
			rep.updated(dest)
		} else {
			missing++
		}
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
