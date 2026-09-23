package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nphattai/coxswain/hooks"
	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdWorkspace implements `cox workspace init|hooks|add-repo ...`.
func cmdWorkspace(args []string) int {
	if len(args) == 0 {
		return usageErr("cox workspace init|hooks|add-repo ...")
	}
	switch args[0] {
	case "init":
		return cmdWorkspaceInit(args[1:])
	case "hooks":
		return cmdWorkspaceHooks(args[1:])
	case "add-repo":
		return cmdWorkspaceAddRepo(args[1:])
	default:
		return usageErr("cox workspace init|hooks|add-repo ...")
	}
}

// cmdWorkspaceInit sets up the whole workspace in one command: the registry files, cox/services/, .gitignore, an
// AGENTS.md skeleton, the pinned leader skills under .agents/skills/, and the leader hooks for every harness in the
// policy's leader options. --repo alias=path[:production] is repeatable (production defaults to the checkout's
// origin/HEAD, else main); --from-repos-md seeds repos[] from a v1 docs/repos.md. With neither, and no workspace.json
// yet, it refuses with the usage line and writes nothing. A re-run is idempotent: it reports what exists and creates
// only what is missing, never rewriting workspace.json, policy.json or a user-edited AGENTS.md.
func cmdWorkspaceInit(args []string) int {
	var repoFlags repoList
	fs := flag.NewFlagSet("workspace init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "workspace root")
	fromReposMD := fs.String("from-repos-md", "", "seed repos[] from a v1 docs/repos.md")
	fs.Var(&repoFlags, "repo", "repo as alias=path[:production] (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	wsRoot, err := filepath.Abs(*root)
	if err != nil {
		return fail("%v", err)
	}

	var repos []workspace.Repo
	switch {
	case *fromReposMD != "":
		ws, err := workspace.FromReposMD(*fromReposMD)
		if err != nil {
			return fail("%v", err)
		}
		repos = ws.Repos
	default:
		for _, rf := range repoFlags {
			r, err := parseRepoFlag(rf)
			if err != nil {
				return fail("%v", err)
			}
			repos = append(repos, r)
		}
	}

	// Refuse to write a placeholder: a brand-new workspace needs at least one repo.
	wsPath := filepath.Join(wsRoot, workspace.ControlDir, "workspace.json")
	if _, statErr := os.Stat(wsPath); os.IsNotExist(statErr) && len(repos) == 0 {
		return usageErr("cox workspace init --root <dir> --repo alias=path[:production] ... [--from-repos-md <path>]")
	}

	rep, err := workspace.Scaffold(wsRoot, repos)
	if err != nil {
		return fail("%v", err)
	}
	for _, c := range rep.Created {
		fmt.Println("created", c)
	}
	for _, p := range rep.Present {
		fmt.Println("present", p)
	}

	// Leader hooks for every harness the policy allows as a leader (claude -> .claude/settings.json,
	// codex -> .codex/hooks.json). A leader option with no hook target is skipped with a note.
	pol, err := workspace.LoadPolicy(wsRoot)
	if err != nil {
		return fail("%v", err)
	}
	// Stale-policy notice (DESIGN item 6): an existing cox/policy.json whose harness options lag the template (e.g. a
	// pre-pi workspace) gets one notice per missing harness. init never rewrites the file, so the captain enables it by
	// hand. On a fresh init the policy was just scaffolded from the template, so there is nothing to report.
	if tmpl, terr := workspace.TemplatePolicy(); terr == nil {
		for _, n := range workspace.StaleOptionNotices(pol, tmpl) {
			fmt.Println("notice:", n)
		}
	}
	for _, h := range pol.Harness.Leader.Options {
		// Pi has no claude/codex-style hook file: it installs the project-local, hash-verifiable extension into
		// <ws>/.pi/extensions/ WITHOUT an epic marker, so the unbound leader supervises every active epic of the
		// workspace (DESIGN item 3). The .pi/extensions/ gitignore line is added by Scaffold's gitignoreRules.
		if h == "pi" {
			entry, err := pi.InstallExtension(wsRoot, "")
			if err != nil {
				return fail("%v", err)
			}
			fmt.Printf("hooks: installed cox pi extension at %s (hash %s, unbound leader; load with -e)\n", entry, pi.ExtensionHash()[:12])
			continue
		}
		path, changed, err := writeLeaderHooks(wsRoot, h)
		if err != nil {
			return fail("%v", err)
		}
		switch {
		case path == "":
			fmt.Printf("hooks: %s has no hook target, skipped\n", h)
		case changed:
			fmt.Printf("hooks: wrote %s (%s leader)\n", path, h)
		default:
			fmt.Printf("hooks: %s already current (%s leader)\n", path, h)
		}
	}
	return 0
}

// cmdWorkspaceHooks (re)writes the leader hooks for one harness into the workspace, creating the settings file when
// absent and merging the four hook groups (from hooks/hooks.json, the single shape source) as `cox hook <name>`
// commands, keeping any existing user entries. Idempotent: a second run changes nothing.
func cmdWorkspaceHooks(args []string) int {
	fs := flag.NewFlagSet("workspace hooks", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "workspace root")
	harnessName := fs.String("harness", "claude", "harness whose hooks to install: "+harnessOptions())
	epicDir := fs.String("epic", "", "epic dir the pi extension binds (pi only)")
	dryRun := fs.Bool("dry-run", false, "pi only: print the extension install plan and write nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	wsRoot, err := filepath.Abs(*root)
	if err != nil {
		return fail("%v", err)
	}
	// Pi installs a project-local extension (no claude/codex hook file), so it routes to its own installer.
	if *harnessName == "pi" {
		return installPiHooks(wsRoot, *epicDir, *dryRun)
	}
	if *harnessName != "claude" && *harnessName != "codex" {
		return usageErr("cox workspace hooks [--root <dir>] --harness " + harnessOptions())
	}
	path, changed, err := writeLeaderHooks(wsRoot, *harnessName)
	if err != nil {
		return fail("%v", err)
	}
	if changed {
		fmt.Printf("hooks: wrote %s (%s leader: prompt-drain, stop-rewake, precompact, session-start)\n", path, *harnessName)
	} else {
		fmt.Printf("hooks: %s already current (%s leader)\n", path, *harnessName)
	}
	return 0
}

// installPiHooks installs the project-local, hash-verifiable Coxswain Pi extension into <root>/.pi/extensions/, without
// touching user-level Pi config (DESIGN section 4). It never installs claude/codex hooks. Worker launch loads the same
// packaged extension explicitly with -e, so worker correctness does not depend on project trust or ambient discovery.
func installPiHooks(root, epicDir string, dryRun bool) int {
	if dryRun {
		fmt.Printf("hooks (dry-run) would install the cox pi extension into %s (hash %s, epic %s)\n",
			filepath.Join(root, pi.ExtensionRelDir), pi.ExtensionHash()[:12], epicDir)
		return 0
	}
	entry, err := pi.InstallExtension(root, epicDir)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("hooks: installed cox pi extension at %s (hash %s, epic %s; load with -e)\n", entry, pi.ExtensionHash()[:12], epicDir)
	return 0
}

// cmdWorkspaceAddRepo appends a repo to cox/workspace.json (production defaults to the checkout's origin/HEAD, else
// main), persisting through the same path `cox epic new --repo alias=ref` uses.
func cmdWorkspaceAddRepo(args []string) int {
	spec, rest := onePositional(args)
	fs := flag.NewFlagSet("workspace add-repo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", ".", "workspace root")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if spec == "" {
		return usageErr("cox workspace add-repo <alias>=<path>[:<production>] [--root <dir>]")
	}
	wsRoot, err := filepath.Abs(*root)
	if err != nil {
		return fail("%v", err)
	}
	r, err := parseRepoFlag(spec)
	if err != nil {
		return fail("%v", err)
	}
	if err := workspace.AddRepo(wsRoot, r); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("added repo %s (%s, production %s)\n", r.Alias, r.Ref(), r.Production)
	return 0
}

// parseRepoFlag parses alias=ref[:production]. ref is an absolute checkout path (leading /) or a backend repo name; when
// production is omitted it is detected from a path's origin/HEAD, else defaults to main.
func parseRepoFlag(v string) (workspace.Repo, error) {
	alias, ref, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(alias) == "" || strings.TrimSpace(ref) == "" {
		return workspace.Repo{}, fmt.Errorf("--repo must be alias=path[:production], got %q", v)
	}
	// A path or name never contains ':', so the last ':' (after the '=') separates an optional production branch, which
	// itself may contain slashes (e.g. release/2026).
	prod := ""
	if i := strings.LastIndex(ref, ":"); i > 0 {
		prod, ref = ref[i+1:], ref[:i]
	}
	// Expand a leading ~ so a zsh user's `--repo alias=~/path` becomes an absolute checkout path rather than being
	// mistaken for a backend repo name (finding 11). Docs keep $HOME, which the shell already expands.
	ref = expandTilde(ref)
	r := workspace.Repo{Alias: alias}
	if strings.HasPrefix(ref, "/") {
		r.Path = ref
	} else {
		r.Name = ref
	}
	if prod == "" {
		if r.Path != "" {
			prod = workspace.DetectProduction(r.Path)
		} else {
			prod = "main"
		}
	}
	r.Production = prod
	return r, nil
}

// expandTilde replaces a leading ~ (bare, or ~/…) with the user's home directory, so a repo path typed with ~ resolves
// to an absolute checkout path. A ~user form or a ~ that is not a path prefix (e.g. embedded) is left untouched, and an
// unresolvable home leaves the value as-is.
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// hookScriptRe extracts the hook name from a hooks/hooks.json command (".../hooks/<name>.sh").
var hookScriptRe = regexp.MustCompile(`hooks/([a-z0-9-]+)\.sh`)

// writeLeaderHooks writes the leader hook groups for one harness into its workspace settings file, creating it when
// absent and merging with any non-cox entries. It returns the file path (empty when the harness has no hook target),
// whether it changed the file, and any error. The group shapes come from hooks/hooks.json; only the command is
// harness-specific (`cox hook <name>` for claude, plus `--harness codex` for codex, whose async key is `async` not
// `asyncRewake`). Idempotent: sorted-key JSON makes a re-run byte-identical.
func writeLeaderHooks(root, harnessName string) (string, bool, error) {
	var path string
	switch harnessName {
	case "claude":
		path = filepath.Join(root, ".claude", "settings.json")
	case "codex":
		path = filepath.Join(root, ".codex", "hooks.json")
	default:
		return "", false, nil // no hook target for this harness
	}

	doc := map[string]any{}
	orig, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(orig, &doc); err != nil {
			return path, false, fmt.Errorf("parse %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return path, false, fmt.Errorf("read %s: %w", path, err)
	}

	groups, err := coxHookGroups(harnessName)
	if err != nil {
		return path, false, err
	}
	hooksMap, _ := doc["hooks"].(map[string]any)
	if hooksMap == nil {
		hooksMap = map[string]any{}
	}
	for event, gs := range groups {
		hooksMap[event] = append(withoutCoxGroups(hooksMap[event]), gs...)
	}
	doc["hooks"] = hooksMap

	out, err := marshalSettings(doc)
	if err != nil {
		return path, false, err
	}
	if bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(orig)) {
		return path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// coxHookGroups derives the per-event cox hook groups for a harness from hooks/hooks.json: each group's shape (matcher,
// timeout, async) is kept, only the command is rewritten to `cox hook <name>` (plus `--harness codex` for codex, whose
// async key is `async`, not claude's `asyncRewake`). Returns event -> []group.
func coxHookGroups(harnessName string) (map[string][]any, error) {
	var manifest struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(hooks.JSON, &manifest); err != nil {
		return nil, fmt.Errorf("parse embedded hooks.json: %w", err)
	}
	out := map[string][]any{}
	for event, rawGroups := range manifest.Hooks {
		for _, rg := range rawGroups {
			var group map[string]any
			if err := json.Unmarshal(rg, &group); err != nil {
				return nil, fmt.Errorf("parse hooks.json %s group: %w", event, err)
			}
			hookList, _ := group["hooks"].([]any)
			for _, h := range hookList {
				hm, ok := h.(map[string]any)
				if !ok {
					continue
				}
				cmd, _ := hm["command"].(string)
				m := hookScriptRe.FindStringSubmatch(cmd)
				if m == nil {
					continue
				}
				newCmd := "cox hook " + m[1]
				if harnessName == "codex" {
					newCmd += " --harness codex"
					if v, ok := hm["asyncRewake"]; ok {
						delete(hm, "asyncRewake")
						hm["async"] = v
					}
				}
				hm["command"] = newCmd
			}
			out[event] = append(out[event], group)
		}
	}
	return out, nil
}

// legacyHookShimRe matches ONLY the known v1 cox hook shim commands (bin/hook-<name>.sh, with or without a
// $CLAUDE_PROJECT_DIR prefix). An upgraded workspace whose settings still carry these must have them removed before the
// new `cox hook` groups are appended, or both fire (double drain, two stop waiters with different lock names). It is
// pinned to the four cox names (plus the historical session-compact) so a user script like ./scripts/hook-format.sh is
// never mistaken for a cox shim and dropped.
var legacyHookShimRe = regexp.MustCompile(`hook-(prompt-drain|stop-rewake|precompact|session-start|session-compact)\.sh`)

// withoutCoxGroups strips cox's own hook entries - both the current `cox hook ` commands and the legacy v1 `bin/hook-*.sh`
// shims - from the event's matcher-groups, so a re-run (or an upgrade from v1) replaces them without disturbing anyone
// else's. It filters at the hook-entry level, not the group level: a group that mixes a cox command with a user command
// keeps the user command (only the cox entry is removed); a group left with no hooks is dropped. A non-map item or a
// group with no hooks array is kept untouched.
func withoutCoxGroups(v any) []any {
	arr, _ := v.([]any)
	kept := make([]any, 0, len(arr))
	for _, item := range arr {
		g, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		hooks, hadHooks := g["hooks"].([]any)
		if !hadHooks {
			kept = append(kept, item)
			continue
		}
		keptHooks := make([]any, 0, len(hooks))
		for _, h := range hooks {
			if b, err := json.Marshal(h); err == nil && (bytes.Contains(b, []byte("cox hook ")) || legacyHookShimRe.Match(b)) {
				continue // a cox/legacy hook entry
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) == 0 {
			continue // the group held only cox/legacy hooks
		}
		g["hooks"] = keptHooks
		kept = append(kept, g)
	}
	return kept
}

// marshalSettings pretty-prints the settings doc with two-space indent and a trailing newline, matching Claude Code's
// on-disk shape. Map keys are emitted sorted, so a re-run of an already-current file produces byte-identical output.
func marshalSettings(doc map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	return append(b, '\n'), nil
}
