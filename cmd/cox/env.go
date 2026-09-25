package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/service"
	"github.com/nphattai/coxswain/internal/env"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdEnv implements `cox env up|down|refresh|smoke|status|snapshot|release <story> --epic <dir>`. Story env allocation
// and ownership live in internal/env; service start/stop/health are delegated to the project's cox/services/<alias>.sh
// adapter (plan 5.3, F13). Exit codes: 0 ok, 1 a failure (or a smoke Fail), 3 a smoke Unknown with no Fail.
func cmdEnv(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox env up|down|refresh|smoke|status|snapshot|release <story>|reconcile --epic <dir>")
		return 2
	}
	verb := args[0]
	rest := args[1:]
	// release takes a story positional; the rest do not.
	story := ""
	if verb == "release" {
		story, rest = onePositional(rest)
	}
	fs := flag.NewFlagSet("env "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	svcAlias := fs.String("service", "", "service alias (default: epic.env BACKEND)")
	apply := fs.Bool("apply", false, "reconcile: write released state for freed allocations (default: dry-run)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox env " + verb + " --epic <dir>")
	}
	alloc := &env.Allocator{EpicDir: *epicDir, Ops: env.RealOps()}

	switch verb {
	case "release":
		if story == "" {
			return usageErr("cox env release <story> --epic <dir>")
		}
		if err := alloc.Release(story); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("released %s\n", story)
		return 0
	case "smoke":
		return envSmoke(*epicDir, *svcAlias, alloc)
	case "status":
		return envStatus(*epicDir, alloc)
	case "up":
		return envUp(*epicDir, *svcAlias, alloc)
	case "down":
		return envDown(alloc)
	case "snapshot":
		return envSnapshot(alloc)
	case "reconcile":
		return envReconcile(alloc, *apply)
	case "refresh":
		if code := envDown(alloc); code != 0 {
			return code
		}
		return envUp(*epicDir, *svcAlias, alloc)
	default:
		return usageErr("cox env up|down|refresh|smoke|status|snapshot|release|reconcile")
	}
}

// envReconcile probes each migrated allocation (from cox migrate) and reports it; with --apply it releases the ones
// whose port is free and whose env file is gone. A port still listening is kept and its pid printed.
func envReconcile(alloc *env.Allocator, apply bool) int {
	results, err := alloc.ReconcileMigrated(apply)
	if err != nil {
		return fail("%v", err)
	}
	if len(results) == 0 {
		fmt.Println("reconcile: no migrated allocations (.cox/env is empty)")
		return 0
	}
	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	fmt.Printf("reconcile (%s): %d migrated allocation(s)\n", mode, len(results))
	for _, r := range results {
		fmt.Printf("  %-28s port=%-6d state=%-18s %s\n", r.Story, r.Port, r.State, r.Note)
	}
	return 0
}

// resolveService finds the workspace and the service adapter for the epic. alias defaults to epic.env BACKEND.
func resolveService(epicDir, alias string, alloc *env.Allocator) (*service.Adapter, error) {
	wsRoot, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Load(wsRoot)
	if err != nil {
		return nil, err
	}
	if alias == "" {
		alias = readEpicEnvKey(epicDir, "BACKEND")
	}
	if alias == "" {
		return nil, fmt.Errorf("no service alias (set --service or epic.env BACKEND)")
	}
	svc, ok := ws.Service(alias)
	if !ok {
		return nil, fmt.Errorf("no service %q in %s/cox/workspace.json", alias, wsRoot)
	}
	script := filepath.Join(wsRoot, workspace.ControlDir, svc.Script)
	backendEnv, _, err := alloc.BackendEnv()
	if err != nil {
		return nil, err
	}
	return service.New(script, backendEnv), nil
}

func envSmoke(epicDir, alias string, alloc *env.Allocator) int {
	adapter, err := resolveService(epicDir, alias, alloc)
	if err != nil {
		return fail("%v", err)
	}
	h := adapter.Health()
	fmt.Printf("%-16s %s\n", "service", h)
	switch h {
	case service.Fail:
		return 1
	case service.Unknown:
		return 3
	default:
		return 0
	}
}

func envStatus(epicDir string, alloc *env.Allocator) int {
	fmt.Printf("epic %s\n", filepath.Base(epicDir))
	// resources.json summary via a fresh smoke-less read.
	_, port, err := alloc.BackendEnv()
	if err == nil {
		fmt.Printf("backend port %d\n", port)
	}
	return 0
}

func envUp(epicDir, alias string, alloc *env.Allocator) int {
	adapter, err := resolveService(epicDir, alias, alloc)
	if err != nil {
		return fail("%v", err)
	}
	_, port, err := alloc.BackendEnv()
	if err != nil {
		return fail("%v", err)
	}
	if s, held := env.PortHeldByMigrated(epicDir, port); held {
		return fail("port %d is still claimed by migrated story %s (state != released); run cox env reconcile --apply first", port, s)
	}
	if err := adapter.Preflight(); err != nil {
		return fail("preflight: %v", err)
	}
	if err := alloc.EnsurePortFree(port); err != nil {
		return fail("%v", err)
	}
	if err := adapter.Start(); err != nil {
		return fail("start: %v", err)
	}
	// The adapter records its pid where health/stop find it; ownership recording is best-effort here.
	fmt.Printf("backend up on :%d\n", port)
	return 0
}

func envDown(alloc *env.Allocator) int {
	stopped, err := alloc.StopBackend()
	if err != nil {
		return fail("%v", err)
	}
	if stopped {
		fmt.Println("backend stopped")
	} else {
		fmt.Println("no owned backend to stop")
	}
	return 0
}

func envSnapshot(alloc *env.Allocator) int {
	name, tpl, err := alloc.EpicDBName()
	if err != nil {
		return fail("%v", err)
	}
	if err := alloc.PromoteSnapshot(name, tpl); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("promoted snapshot %s from %s\n", tpl, name)
	return 0
}

// findWorkspaceRoot walks up from dir to the first ancestor holding cox/workspace.json.
func findWorkspaceRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, workspace.ControlDir, "workspace.json")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no cox/workspace.json above %s", dir)
		}
		abs = parent
	}
}

// readEpicEnvKey reads one KEY from <epic>/epic.env, or "".
func readEpicEnvKey(epicDir, key string) string {
	b, err := os.ReadFile(filepath.Join(epicDir, "epic.env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(strings.SplitN(v, "#", 2)[0])
		}
	}
	return ""
}

// epicFlag registers --epic on fs and returns its value made absolute at parse time (B-71a, B-50): a relative --epic
// (or COX_EPIC default) is resolved against the caller's cwd once, so everything downstream - the rendered brief a
// worker reads from its own worktree, the watcher argv doctor stats from its own cwd, the lock files - sees one
// absolute path. "" stays "" (the flag's "not given").
func epicFlag(fs *flag.FlagSet, def, usage string) *string {
	p := new(string)
	*p = absDir(def)
	fs.Var(absDirValue{p}, "epic", usage)
	return p
}

// absDirValue is the flag.Value behind epicFlag.
type absDirValue struct{ p *string }

func (v absDirValue) String() string {
	if v.p == nil {
		return ""
	}
	return *v.p
}

func (v absDirValue) Set(s string) error {
	*v.p = absDir(s)
	return nil
}

// absDir is filepath.Abs for a non-empty path; "" and an unresolvable path are returned as given.
func absDir(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
