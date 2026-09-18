// Package doctor inventories every coxswain installation on this machine and the control directory of every epic
// under each, so a fix can be shown to reach the intended clone (F09). It reports v1 installs (a bin/lib.sh checkout)
// and v2 installs (a .cox control tree or the cox binary), each with its version, and flags two hazards: installations
// whose versions disagree, and an epic still carrying a live v1 .run next to a v2 .cox. It only reads; it never
// migrates or upgrades anything (F09 recommendation).
package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Epic is one epic's control directory under an installation.
type Epic struct {
	Path   string `json:"path"`
	HasRun bool   `json:"has_run"` // v1 last-value-wins .run present
	HasCox bool   `json:"has_cox"` // v2 .cox/events.jsonl present
}

// Installation is one coxswain checkout or control tree.
type Installation struct {
	Path    string `json:"path"`     // where it was found (the mount point; may symlink bin/ into the kit)
	KitPath string `json:"kit_path"` // the real coxswain checkout backing it; Version is taken from here
	Type    string `json:"type"`     // "v1" | "v2"
	Version string `json:"version"`  // v1: git short HEAD of the kit; v2: cox version; "unknown" if unreadable
	Epics   []Epic `json:"epics"`
}

// Report is the full doctor result.
type Report struct {
	Installations []Installation `json:"installations"`
	Issues        []string       `json:"issues"`
}

// DefaultRoots is where installations live: ~/Work and $ORCA_WORKSPACES (default ~/orca/workspaces).
func DefaultRoots() []string {
	home, _ := os.UserHomeDir()
	ws := os.Getenv("ORCA_WORKSPACES")
	if ws == "" {
		ws = filepath.Join(home, "orca", "workspaces")
	}
	return []string{filepath.Join(home, "Work"), ws}
}

// Run scans the roots and returns the report with issues computed.
func Run(roots []string) Report {
	insts := Scan(roots)
	return Report{Installations: insts, Issues: Issues(insts)}
}

// Scan finds every installation under the roots (candidate dirs one, two, and three levels deep, since a workspace
// clone such as ~/orca/workspaces/<ws>/<mount>/crewkit sits three deep) and gathers each one's epics. A v1 mount often
// symlinks bin/ into a shared crewkit checkout, so several mount points share one kit; results are de-duplicated by the
// kit's real path (the shallower mount, seen first, wins) and sorted by mount path. The version is read from the kit,
// not the mount, so a mount never reports its own unrelated git HEAD.
func Scan(roots []string) []Installation {
	seenKit := map[string]bool{}
	var insts []Installation
	for _, root := range roots {
		for _, dir := range candidateDirs(root) {
			typ := installType(dir)
			if typ == "" {
				continue
			}
			kit := kitDir(dir, typ)
			if seenKit[kit] {
				continue
			}
			seenKit[kit] = true
			insts = append(insts, Installation{
				Path:    dir,
				KitPath: kit,
				Type:    typ,
				Version: version(kit, typ),
				Epics:   epicsUnder(dir),
			})
		}
	}
	sort.Slice(insts, func(i, j int) bool { return insts[i].Path < insts[j].Path })
	return insts
}

// kitDir resolves the real coxswain checkout backing an installation. A v1 mount commonly symlinks its bin/ into a
// shared checkout (e.g. ~/Work/example/bin -> crewkit/bin), so the mount's own git HEAD is not the kit's version. The
// kit is the parent of the real directory that holds bin/lib.sh (EvalSymlinks resolves the symlink, then strip
// /bin/lib.sh). A v2 install has no such symlink, so its kit is itself; a v1 mount whose symlink cannot be resolved
// falls back to itself.
func kitDir(dir, typ string) string {
	if typ == "v1" {
		if real, err := filepath.EvalSymlinks(filepath.Join(dir, "bin", "lib.sh")); err == nil {
			return filepath.Dir(filepath.Dir(real))
		}
	}
	return dir
}

// candidateDirs returns dirs one, two, and three levels below root.
func candidateDirs(root string) []string {
	var out []string
	for _, pat := range []string{"*", "*/*", "*/*/*"} {
		matches, _ := filepath.Glob(filepath.Join(root, pat))
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.IsDir() {
				out = append(out, m)
			}
		}
	}
	return out
}

// installType classifies a dir: v1 has bin/lib.sh, v2 has a .cox control tree. v1 takes precedence when both exist
// (a v1 checkout mid-migration is still driven by its scripts).
func installType(dir string) string {
	if exists(filepath.Join(dir, "bin", "lib.sh")) {
		return "v1"
	}
	if exists(filepath.Join(dir, ".cox")) {
		return "v2"
	}
	return ""
}

// epicsUnder finds epic control dirs at <install>/<project>/epics/<slug> and <install>/epics/<slug>, each flagged for
// a v1 .run and a v2 .cox/events.jsonl.
func epicsUnder(dir string) []Epic {
	seen := map[string]bool{}
	var epics []Epic
	for _, pat := range []string{
		filepath.Join(dir, "*", "epics", "*"),
		filepath.Join(dir, "epics", "*"),
	} {
		matches, _ := filepath.Glob(pat)
		for _, ep := range matches {
			info, err := os.Stat(ep)
			if err != nil || !info.IsDir() || seen[ep] {
				continue
			}
			hasRun := exists(filepath.Join(ep, ".run"))
			hasCox := exists(filepath.Join(ep, ".cox", "events.jsonl"))
			if !hasRun && !hasCox {
				continue
			}
			seen[ep] = true
			epics = append(epics, Epic{Path: ep, HasRun: hasRun, HasCox: hasCox})
		}
	}
	sort.Slice(epics, func(i, j int) bool { return epics[i].Path < epics[j].Path })
	return epics
}

// Issues flags the two F09 hazards: operational installations whose versions disagree, and an epic carrying a live v1
// .run next to a v2 .cox. Only installations that own at least one epic are operational; an install with no epics is a
// bare dev checkout whose version is its author's business and is excluded from the divergence check.
func Issues(insts []Installation) []string {
	var issues []string

	versions := map[string]bool{}
	for _, in := range insts {
		if len(in.Epics) == 0 {
			continue // dev checkout, not operational
		}
		if in.Version != "" && in.Version != "unknown" {
			versions[in.Version] = true
		}
	}
	if len(versions) > 1 {
		list := make([]string, 0, len(versions))
		for v := range versions {
			list = append(list, v)
		}
		sort.Strings(list)
		issues = append(issues, "installations disagree on version: "+strings.Join(list, ", "))
	}

	for _, in := range insts {
		for _, ep := range in.Epics {
			if ep.HasRun && ep.HasCox {
				issues = append(issues, "epic has a live v1 .run alongside a v2 .cox: "+ep.Path)
			}
		}
	}
	return issues
}

func version(kit, typ string) string {
	if typ == "v2" {
		if out, err := exec.Command("cox", "version").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
		return "unknown"
	}
	out, err := exec.Command("git", "-C", kit, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
