// Package workspace loads the two enterprise registry files a leader resolves against: cox/workspace.json (projects,
// repos, services, hosts) and cox/policy.json (topology, waves, context thresholds, harness options, arena trigger,
// delivery style). The plan (5.3) and architecture.html show these as YAML; this package uses JSON instead, decided in
// M3: policy needs three levels of nesting (harness.leader.options), past the two-level minimal-YAML target the brief
// set, and encoding/json is stdlib and unambiguous where a hand-rolled YAML subset would be fragile. The brief
// authorizes the JSON choice explicitly. No third-party dependency is added.
package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ControlDir is the workspace-level cox directory that holds workspace.json, policy.json, and services/.
const ControlDir = "cox"

// aliasRe restricts a repo alias to a single path-safe component. An alias is joined into paths (filepath.Join(epicDir,
// alias) for the worktree symlink), so a value like "..", "a/b", or "" would escape the epic dir; only [A-Za-z0-9._-]
// (and not "." or "..") is allowed.
var aliasRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Repo is one repository the workspace tracks. Name is the backend-registered name (Orca) and Path is an absolute
// checkout path; a repo may carry either or both, and dispatch addresses it by whichever is set (an absolute path wins,
// for a repo the backend has not named - see the orca adapter's repoSelector). Production and Staging are branch names.
type Repo struct {
	Alias      string `json:"alias"`
	Name       string `json:"name,omitempty"`
	Path       string `json:"path,omitempty"`
	Production string `json:"production"`
	Staging    string `json:"staging,omitempty"`
}

// Ref returns the value dispatch uses to address the repo: the absolute path when set, else the name.
func (r Repo) Ref() string {
	if r.Path != "" {
		return r.Path
	}
	return r.Name
}

// Project is one product folder under the workspace root.
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Service names a project service adapter script (cox/services/<alias>.sh) with the four verbs preflight|start|health|stop.
type Service struct {
	Alias  string `json:"alias"`
	Script string `json:"script"`
}

// Host is a machine a worker can run on. Name "local" is the machine the leader runs on.
type Host struct {
	Name string `json:"name"`
	SSH  string `json:"ssh,omitempty"`
}

// Workspace is the parsed cox/workspace.json.
type Workspace struct {
	Projects []Project `json:"projects"`
	Repos    []Repo    `json:"repos"`
	Services []Service `json:"services"`
	Hosts    []Host    `json:"hosts"`
	// WorktreeBase is where a backend that owns its own git worktrees (herdr) creates them. Optional; Orca manages its
	// own worktree paths, so this is only read by the herdr adapter (ADR 0012).
	WorktreeBase string `json:"worktree_base,omitempty"`
}

// Load reads and parses <ws>/cox/workspace.json.
func Load(wsRoot string) (*Workspace, error) {
	return LoadFile(filepath.Join(wsRoot, ControlDir, "workspace.json"))
}

// LoadFile parses a workspace.json at an explicit path and validates it. A parse error is a real error; a missing file
// is reported as such rather than an empty workspace, so a caller never proceeds against a registry that was silently
// absent. Validation (unique alias, path-or-name present, absolute path, non-empty production) names the field and the
// file so a hand-edited registry fails with a fixable message.
func LoadFile(path string) (*Workspace, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workspace: %w", err)
	}
	var w Workspace
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &w, nil
}

// Validate reports the first structural problem in the repos[] registry, naming the field: an empty or duplicate alias,
// a repo with neither path nor name, a non-absolute path, or an empty production branch. It is pure (no filesystem I/O);
// the "path is a real git checkout" check is a `cox doctor` concern, not a load-time one, so a registry loads on a
// machine that has not cloned the repos yet.
func (w *Workspace) Validate() error {
	seen := map[string]bool{}
	for i, r := range w.Repos {
		where := fmt.Sprintf("repos[%d]", i)
		if r.Alias != "" {
			where = fmt.Sprintf("repo %q", r.Alias)
		}
		switch {
		case strings.TrimSpace(r.Alias) == "":
			return fmt.Errorf("%s: alias is required", where)
		case r.Alias == "." || r.Alias == ".." || !aliasRe.MatchString(r.Alias):
			return fmt.Errorf("%s: alias must be a single path-safe component ([A-Za-z0-9._-], not '.', '..', or containing a separator)", where)
		case seen[r.Alias]:
			return fmt.Errorf("%s: duplicate alias", where)
		case r.Path == "" && r.Name == "":
			return fmt.Errorf("%s: needs path or name", where)
		case r.Path != "" && !filepath.IsAbs(r.Path):
			return fmt.Errorf("%s: path %q must be absolute", where, r.Path)
		case strings.TrimSpace(r.Production) == "":
			return fmt.Errorf("%s: production branch is required", where)
		}
		seen[r.Alias] = true
	}
	return nil
}

// Repo returns the repo with the given alias, or false.
func (w *Workspace) Repo(alias string) (Repo, bool) {
	for _, r := range w.Repos {
		if r.Alias == alias {
			return r, true
		}
	}
	return Repo{}, false
}

// Service returns the service adapter for an alias, or false.
func (w *Workspace) Service(alias string) (Service, bool) {
	for _, s := range w.Services {
		if s.Alias == alias {
			return s, true
		}
	}
	return Service{}, false
}
