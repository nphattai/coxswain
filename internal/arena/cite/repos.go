package cite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/workspace"
)

// Repo is one epic repo membership: its alias and the ref the repos file records (an absolute checkout path or a
// backend-registered repo name). It is the one shape every alias consumer reads, so citation checking, pack membership,
// and arena repo selection never disagree on what an epic's repos are.
type Repo struct {
	Alias string
	Ref   string
}

// Repos reads the epic repos file ("<alias> <ref>" per line; a bare "<alias>" maps to itself) into ordered entries.
func Repos(epicDir string) ([]Repo, error) {
	b, err := os.ReadFile(filepath.Join(epicDir, "repos"))
	if err != nil {
		return nil, fmt.Errorf("read epic repos: %w", err)
	}
	var out []Repo
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "" {
			continue
		}
		ref := f[0]
		if len(f) >= 2 {
			ref = f[1]
		}
		out = append(out, Repo{Alias: f[0], Ref: ref})
	}
	return out, nil
}

// Aliases returns the epic's alias set from the repos file.
func Aliases(epicDir string) (map[string]bool, error) {
	repos, err := Repos(epicDir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range repos {
		out[r.Alias] = true
	}
	return out, nil
}

// First returns the first repo (alias + ref) in the epic repos file. Arena roles run in the epic's first repo.
func First(epicDir string) (Repo, error) {
	repos, err := Repos(epicDir)
	if err != nil {
		return Repo{}, err
	}
	if len(repos) == 0 {
		return Repo{}, fmt.Errorf("epic %s has no repos", filepath.Base(epicDir))
	}
	return repos[0], nil
}

// resolveRepoDir turns an alias into a local git checkout path using the repos file first: the ref is used directly when
// it is an existing directory (an absolute path), else resolved to a path through cox/workspace.json (by alias, then by
// name). It returns "" when the repos file yields nothing usable, so the caller falls back to the epic alias symlink.
func resolveRepoDir(epicDir, alias string) string {
	repos, err := Repos(epicDir)
	if err != nil {
		return ""
	}
	for _, r := range repos {
		if r.Alias != alias {
			continue
		}
		if filepath.IsAbs(r.Ref) && isDir(r.Ref) {
			return r.Ref
		}
		return workspacePath(epicDir, alias, r.Ref)
	}
	return ""
}

// workspacePath resolves a repo name to a local checkout path via cox/workspace.json, matching by alias first then by
// name. It returns "" when there is no workspace file, no match, or the matched repo has no existing path.
func workspacePath(epicDir, alias, name string) string {
	wsRoot := findWorkspaceRoot(epicDir)
	if wsRoot == "" {
		return ""
	}
	ws, err := workspace.Load(wsRoot)
	if err != nil {
		return ""
	}
	if r, ok := ws.Repo(alias); ok && isDir(r.Path) {
		return r.Path
	}
	for _, r := range ws.Repos {
		if r.Name == name && isDir(r.Path) {
			return r.Path
		}
	}
	return ""
}

// findWorkspaceRoot walks up from the epic dir for a cox/workspace.json, returning "" when none is found.
func findWorkspaceRoot(epicDir string) string {
	dir, err := filepath.Abs(epicDir)
	if err != nil {
		return ""
	}
	for {
		info, err := os.Stat(filepath.Join(dir, workspace.ControlDir, "workspace.json"))
		if err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
