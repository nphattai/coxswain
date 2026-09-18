package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend/orca"
	"github.com/nphattai/coxswain/internal/env"
	"github.com/nphattai/coxswain/internal/epic"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdEpic implements `cox epic new|stories|close`.
func cmdEpic(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox epic new|stories|close|arena|design ...")
		return 2
	}
	switch args[0] {
	case "new":
		return epicNew(args[1:])
	case "stories":
		return epicStories(args[1:])
	case "close":
		return epicClose(args[1:])
	case "arena":
		return epicArena(args[1:])
	case "design":
		return epicDesign(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cox epic: unknown subcommand %q\n", args[0])
		return 2
	}
}

// repoList collects repeated --repo flags. Each value is an alias, or alias=ref to register an ad-hoc repo.
type repoList []string

func (r *repoList) String() string { return strings.Join(*r, ",") }
func (r *repoList) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func epicNew(args []string) int {
	project, slug, rest := twoPositionals(args)
	var repos repoList
	fs := flag.NewFlagSet("epic new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "workspace root")
	backendAlias := fs.String("backend", "", "alias whose stories run the epic backend")
	noPush := fs.Bool("no-push", false, "do not publish epic/<slug> to origin")
	fs.Var(&repos, "repo", "repo alias (repeatable); alias=ref to register an ad-hoc repo")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if project == "" || slug == "" || len(repos) == 0 {
		return usageErr("cox epic new <project> <slug> --repo <alias> ... [--no-push]")
	}
	wsRoot, err := filepath.Abs(*root)
	if err != nil {
		return fail("%v", err)
	}
	ws, err := workspace.Load(wsRoot)
	if err != nil {
		return fail("%v", err)
	}
	aliases, err := resolveRepoAliases(ws, repos)
	if err != nil {
		return fail("%v", err)
	}
	rt := orca.New(os.Getenv("ORCA_RUN_ID"))
	epicDir, err := epic.New(epic.NewOptions{
		Runtime: rt, Workspace: ws, WsRoot: wsRoot, Project: project, Slug: slug,
		Repos: aliases, BackendAlias: *backendAlias, NoPush: *noPush,
	})
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("created epic %s\n", epicDir)
	return 0
}

// resolveRepoAliases turns the --repo values into aliases, registering any alias=ref pair into the in-memory workspace.
func resolveRepoAliases(ws *workspace.Workspace, repos repoList) ([]string, error) {
	var aliases []string
	for _, r := range repos {
		alias, ref, hasRef := strings.Cut(r, "=")
		if hasRef {
			if _, ok := ws.Repo(alias); !ok {
				repo := workspace.Repo{Alias: alias, Production: "main"}
				if strings.HasPrefix(ref, "/") {
					repo.Path = ref
				} else {
					repo.Name = ref
				}
				ws.Repos = append(ws.Repos, repo)
			}
		} else if _, ok := ws.Repo(alias); !ok {
			return nil, fmt.Errorf("repo alias %q not in workspace.json (use alias=ref to register)", alias)
		}
		aliases = append(aliases, alias)
	}
	return aliases, nil
}

func epicStories(args []string) int {
	fs := flag.NewFlagSet("epic stories", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	var storyFlags repoList
	fs.Var(&storyFlags, "story", "story as id=repo (repeatable); default is one per repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox epic stories --epic <dir> [--story id=repo ...]")
	}
	wsRoot, err := findWorkspaceRoot(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	project := projectOf(*epicDir)
	specs, err := storySpecs(*epicDir, storyFlags)
	if err != nil {
		return fail("%v", err)
	}
	written, err := epic.Stories(*epicDir, wsRoot, project, specs)
	if err != nil {
		return fail("%v", err)
	}
	for _, p := range written {
		fmt.Println("rendered", p)
	}
	return 0
}

// storySpecs builds the story specs: the explicit --story id=repo pairs, or one skeleton per repo alias in the epic
// repos file (id = <slug>-<alias>).
func storySpecs(epicDir string, storyFlags repoList) ([]epic.StorySpec, error) {
	slug := filepath.Base(epicDir)
	if len(storyFlags) > 0 {
		var specs []epic.StorySpec
		for _, s := range storyFlags {
			id, repo, ok := strings.Cut(s, "=")
			if !ok {
				return nil, fmt.Errorf("--story must be id=repo, got %q", s)
			}
			specs = append(specs, epic.StorySpec{ID: id, Repo: repo, Title: id})
		}
		return specs, nil
	}
	b, err := os.ReadFile(filepath.Join(epicDir, "repos"))
	if err != nil {
		return nil, fmt.Errorf("read epic repos file: %w", err)
	}
	var specs []epic.StorySpec
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 1 || f[0] == "" {
			continue
		}
		alias := f[0]
		specs = append(specs, epic.StorySpec{ID: slug + "-" + alias, Repo: alias, Title: slug + " - " + alias})
	}
	return specs, nil
}

func epicClose(args []string) int {
	fs := flag.NewFlagSet("epic close", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	yes := fs.Bool("yes", false, "execute (default is a dry run)")
	force := fs.Bool("force", false, "remove dirty/unpushed worktrees")
	storiesOnly := fs.Bool("stories-only", false, "skip epic backend and epic worktrees")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox epic close --epic <dir> [--yes] [--force] [--stories-only]")
	}
	rt, _ := newBackend(*epicDir) // may be nil; dry run and no-session close tolerate it
	alloc := &env.Allocator{EpicDir: *epicDir, Ops: env.RealOps()}
	err := epic.Close(epic.CloseOptions{
		EpicDir: *epicDir, Runtime: rt, Alloc: alloc,
		Yes: *yes, Force: *force, StoriesOnly: *storiesOnly,
	})
	if err != nil {
		return fail("%v", err)
	}
	return 0
}

// projectOf returns the project folder name for an epic dir <ws>/<project>/epics/<slug>.
func projectOf(epicDir string) string {
	abs, err := filepath.Abs(epicDir)
	if err != nil {
		return ""
	}
	// <ws>/<project>/epics/<slug> -> project is three levels up's base... epics parent.
	return filepath.Base(filepath.Dir(filepath.Dir(abs)))
}
