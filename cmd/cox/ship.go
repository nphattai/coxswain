package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/github"
	"github.com/nphattai/coxswain/internal/verdict"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdShip implements `cox ship facts --epic <dir> [--json]`. It reports per-repo ahead/behind and conflicts of the epic
// branch against each repo's production branch, using three-state facts: a failed fetch or merge-tree is unknown, never
// a fabricated "conflicts: none" (F12). Exit code 3 if any repo is unknown, else 0.
func cmdShip(args []string) int {
	if len(args) == 0 || args[0] != "facts" {
		fmt.Fprintln(os.Stderr, "usage: cox ship facts --epic <dir> [--json]")
		return 2
	}
	fs := flag.NewFlagSet("ship facts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	asJSON := fs.Bool("json", false, "emit JSON")
	noForge := fs.Bool("no-forge", false, "skip the GitHub forge probe (PR/checks/merged stay unknown)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox ship facts --epic <dir> [--json] [--no-forge]")
	}
	targets, err := shipTargets(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	git := func(dir string, gitArgs ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, gitArgs...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	rep := verdict.Ship(targets, git)
	var forgeFacts []verdict.ShipForgeFacts
	if !*noForge {
		forgeFacts = verdict.ShipForge(targets, func(dir string) forge.Forge { return github.New(dir) })
	}

	anyUnknown := false
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			verdict.ShipReport
			Forge []verdict.ShipForgeFacts `json:"forge,omitempty"`
		}{rep, forgeFacts})
		for _, r := range rep.Repos {
			if r.Verdict == verdict.Unknown {
				anyUnknown = true
			}
		}
	} else {
		for _, r := range rep.Repos {
			fmt.Printf("%-12s verdict=%s ahead=%d behind=%d", r.Alias, r.Verdict, r.Ahead, r.Behind)
			switch {
			case r.Verdict == verdict.Unknown:
				anyUnknown = true
				fmt.Print(" conflicts=unknown")
			case len(r.Conflicts) == 0:
				fmt.Print(" conflicts=none")
			default:
				fmt.Printf(" conflicts=%d files", len(r.Conflicts))
			}
			fmt.Println()
			for _, reason := range r.Reasons {
				fmt.Printf("  - %s\n", reason)
			}
		}
		for _, ff := range forgeFacts {
			fmt.Printf("%-12s forge verdict=%s pr=%s checks=%s merged=%s head=%s\n",
				ff.Alias, ff.Verdict, prNum(ff.PR), ff.Checks, ff.Merged, shortSha(ff.Head))
			for _, reason := range ff.Reasons {
				fmt.Printf("  - %s\n", reason)
			}
		}
	}
	if anyUnknown {
		return 3
	}
	return 0
}

// prNum renders a PR number for the human table, or "none" when there is no PR (0).
func prNum(n int) string {
	if n == 0 {
		return "none"
	}
	return "#" + strconv.Itoa(n)
}

// shipTargets assembles one RepoTarget per alias in the epic's `repos` file, resolving the production branch from the
// workspace and the checkout dir from the epic's alias symlink.
func shipTargets(epicDir string) ([]verdict.RepoTarget, error) {
	slug := filepath.Base(epicDir)
	wsRoot, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Load(wsRoot)
	if err != nil {
		return nil, err
	}
	reposFile, err := os.ReadFile(filepath.Join(epicDir, "repos"))
	if err != nil {
		return nil, fmt.Errorf("read epic repos file: %w", err)
	}
	var targets []verdict.RepoTarget
	for _, line := range strings.Split(string(reposFile), "\n") {
		f := strings.Fields(line)
		if len(f) < 1 || f[0] == "" {
			continue
		}
		alias := f[0]
		repo, ok := ws.Repo(alias)
		if !ok {
			continue
		}
		dir := filepath.Join(epicDir, alias) // link.sh symlinks the alias to the epic worktree
		targets = append(targets, verdict.RepoTarget{
			Alias:      alias,
			Dir:        dir,
			EpicBranch: "epic/" + slug,
			Production: repo.Production,
			Staging:    repo.Staging,
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no ship targets: epic repos file empty or no matching workspace repos")
	}
	return targets, nil
}
