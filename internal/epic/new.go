// Package epic creates, renders stories for, and closes an epic. New builds the epic directory, one worktree per repo
// on branch epic/<slug> (through the verified worktree module, never a fallback), and the alias symlinks; it never
// deletes a branch (F01). Stories renders each story from the one template with the project policy resolved once and
// its provenance stamped (F14). Close tears down in a fixed, confirmed order - workers, services, resources,
// worktrees, then archive - and refuses to archive if any step fails (F03).
package epic

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/workspace"
	"github.com/nphattai/coxswain/internal/worktree"
	"github.com/nphattai/coxswain/templates"
)

// NewOptions configures New.
type NewOptions struct {
	Runtime      backend.Backend      // creates worktrees
	Workspace    *workspace.Workspace // repo registry (production branch per alias)
	WsRoot       string               // workspace root (for port scan and trust)
	Project      string               // project folder name
	Slug         string               // epic slug
	Repos        []string             // repo aliases to include
	BackendAlias string               // alias whose stories run the epic backend ("" = none)
	NoPush       bool                 // skip publishing epic/<slug> to origin (E2E)
	Warn         io.Writer            // best-effort warnings; nil => os.Stderr
}

func (o *NewOptions) warn() io.Writer {
	if o.Warn != nil {
		return o.Warn
	}
	return os.Stderr
}

// EpicDir returns <WsRoot>/<Project>/epics/<Slug>.
func (o *NewOptions) EpicDir() string {
	return filepath.Join(o.WsRoot, o.Project, "epics", o.Slug)
}

// EpicMeta is .cox/epic.json: an epic has no state machine, so this is a plain record, not an event.
type EpicMeta struct {
	Slug      string   `json:"slug"`
	Project   string   `json:"project"`
	Repos     []string `json:"repos"`
	CreatedAt string   `json:"created_at"`
}

// New creates the epic directory, worktrees, symlinks, epic.env, DESIGN.md, and .cox/epic.json. It is not idempotent
// over worktrees (Orca owns those), but it refuses to clobber an existing epic dir. It returns the epic dir on success.
func New(o NewOptions) (string, error) {
	epicDir := o.EpicDir()
	if _, err := os.Stat(epicDir); err == nil {
		return "", fmt.Errorf("epic dir already exists: %s (use 'cox epic attach --epic %s' to re-attach it on a fresh clone)", epicDir, epicDir)
	}
	for _, sub := range []string{"stories", "plans", "reports", "handoffs", "briefs", "inbox", ".cox"} {
		if err := os.MkdirAll(filepath.Join(epicDir, sub), 0o755); err != nil {
			return "", err
		}
	}

	// repos file: "<alias> <ref>" per line.
	var reposLines, repoRows []string
	for _, alias := range o.Repos {
		repo, ok := o.Workspace.Repo(alias)
		if !ok {
			return "", fmt.Errorf("repo alias %q not in workspace.json", alias)
		}
		reposLines = append(reposLines, alias+" "+repo.Ref())
		repoRows = append(repoRows, fmt.Sprintf("| %s | %s | <does> |", alias, repo.Ref()))
	}
	if err := os.WriteFile(filepath.Join(epicDir, "repos"), []byte(strings.Join(reposLines, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}

	// DESIGN.md from the template.
	design, err := templates.File("epic/DESIGN.md")
	if err != nil {
		return "", err
	}
	rendered := strings.NewReplacer(
		"{{slug}}", o.Slug,
		"{{project}}", o.Project,
		"{{repo_rows}}", strings.Join(repoRows, "\n"),
	).Replace(string(design))
	if err := os.WriteFile(filepath.Join(epicDir, "DESIGN.md"), []byte(rendered), 0o644); err != nil {
		return "", err
	}

	// epic.env: a port block only when the epic declares a backend; otherwise EPIC and PROJECT only, so a docs-only or
	// CLI-only epic allocates no ports (§4).
	if err := os.WriteFile(filepath.Join(epicDir, "epic.env"), []byte(renderEpicEnv(&o)), 0o644); err != nil {
		return "", err
	}

	// One worktree per repo on epic/<slug>, cut from the repo's production branch; symlink alias -> worktree.
	for _, alias := range o.Repos {
		repo, _ := o.Workspace.Repo(alias)
		base := repo.Production
		if base == "" {
			return "", fmt.Errorf("repo %q has no production branch in workspace.json (needed as the epic base)", alias)
		}
		wt, err := worktree.Ensure(o.Runtime, repo.Ref(), "epic/"+o.Slug, base)
		if err != nil {
			return "", fmt.Errorf("epic worktree for %s: %w", alias, err)
		}
		link := filepath.Join(epicDir, alias)
		_ = os.Remove(link)
		if err := os.Symlink(wt.Path, link); err != nil {
			return "", fmt.Errorf("symlink %s -> %s: %w", alias, wt.Path, err)
		}
		fmt.Fprintf(o.warn(), "epic new: marking %s trusted in ~/.claude.json\n", wt.Path)
		trustWorktree(o.warn(), wt.Path)
		if !o.NoPush {
			fmt.Fprintf(o.warn(), "epic new: publishing epic/%s to origin for %s (use --no-push to skip)\n", o.Slug, alias)
			if err := pushEpicBranch(wt.Path, o.Slug); err != nil {
				fmt.Fprintf(o.warn(), "warn: could not publish epic/%s for %s: %v\n", o.Slug, alias, err)
			}
		}
	}

	meta := EpicMeta{Slug: o.Slug, Project: o.Project, Repos: o.Repos, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(epicDir, ".cox", "epic.json"), append(mb, '\n'), 0o644); err != nil {
		return "", err
	}
	return epicDir, nil
}

// renderEpicEnv builds epic.env. A docs-only or CLI-only epic (no --backend) carries EPIC and PROJECT only and allocates
// no ports; an epic that declares a backend gets the full block with a freshly allocated port range and a DB name.
func renderEpicEnv(o *NewOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "EPIC=%s\n", o.Slug)
	fmt.Fprintf(&b, "PROJECT=%s\n", o.Project)
	if o.BackendAlias == "" {
		return b.String()
	}
	api, base := allocatePorts(o.WsRoot)
	dbName := strings.ReplaceAll(o.Slug, "-", "_")
	fmt.Fprintf(&b, "BACKEND=%s\n", o.BackendAlias)
	b.WriteString("BACKEND_APP=\n")
	fmt.Fprintf(&b, "API_PORT=%d\n", api)
	fmt.Fprintf(&b, "STORY_PORT_BASE=%d\n", base)
	fmt.Fprintf(&b, "DB_NAME=%s\n", dbName)
	b.WriteString("DB_SCHEMA=\n")
	b.WriteString("SEED=\n")
	b.WriteString("SIM_BASE=\n")
	b.WriteString("SECRETS=\n")
	b.WriteString("STORY_ENV_EXTRA=\n")
	return b.String()
}

// allocatePorts scans existing epic.env files under the workspace for the highest API_PORT and STORY_PORT_BASE, then
// returns the next block (API_PORT+1 / STORY_PORT_BASE+100), defaulting to v1's 3333 / 3400.
func allocatePorts(wsRoot string) (api, base int) {
	api, base = 3333, 3400
	maxAPI, maxBase := 0, 0
	for _, pat := range []string{
		filepath.Join(wsRoot, "*", "epics", "*", "epic.env"),
		filepath.Join(wsRoot, "*", "*", "epics", "*", "epic.env"),
	} {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(b), "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
					n, _ := strconv.Atoi(strings.TrimSpace(v))
					switch k {
					case "API_PORT":
						if n > maxAPI {
							maxAPI = n
						}
					case "STORY_PORT_BASE":
						if n > maxBase {
							maxBase = n
						}
					}
				}
			}
		}
	}
	if maxAPI > 0 {
		api = maxAPI + 1
	}
	if maxBase > 0 {
		base = maxBase + 100
	}
	return api, base
}

// pushEpicBranch publishes epic/<slug> to origin when the remote does not already have it. It only ever pushes a new
// epic branch (never a default branch), which is allowed (F01 forbids deletion, not this publish).
func pushEpicBranch(worktreeDir, slug string) error {
	branch := "epic/" + slug
	out, err := exec.Command("git", "-C", worktreeDir, "ls-remote", "--heads", "origin", branch).Output()
	if err != nil {
		return fmt.Errorf("ls-remote: %w", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return nil // already published
	}
	if out, err := exec.Command("git", "-C", worktreeDir, "push", "-u", "origin", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("push %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// trustWorktree marks a worktree path trusted for Claude Code by adding hasTrustDialogAccepted to ~/.claude.json (v1
// trust_worktrees). It is additive, idempotent, and best-effort: a failure only warns, since a worker can still be
// trusted interactively.
func trustWorktree(warn io.Writer, path string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	cfgPath := filepath.Join(home, ".claude.json")
	cfg := map[string]any{}
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[path].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[path] = entry
	cfg["projects"] = projects
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(warn, "warn: trust %s: %v\n", path, err)
		return
	}
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		fmt.Fprintf(warn, "warn: trust %s: %v\n", path, err)
	}
}
