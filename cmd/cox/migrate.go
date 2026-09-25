package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/orca"
	"github.com/nphattai/coxswain/internal/env"
	"github.com/nphattai/coxswain/internal/migrate"
)

// cmdMigrate implements `cox migrate --epic <dir> [--root <clone>] [--apply] [--force]`. Without --apply it prints the
// plan and a go/no-go checklist and writes nothing; with --apply it writes the v2 control tree (events, sessions,
// migrated allocations, wrapped handoffs, run) and renames .run to .run.migrated. It queries Orca worker-list so a dead
// dispatch does not get a live session (a working story with a settled dispatch lands in pending_external for cox
// reconcile). --apply refuses while a live worker's composer is busy or pending, unless --force.
func cmdMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory (must hold a v1 .run)")
	root := fs.String("root", "", "workspace clone root to check for v1 hooks (<root>/.claude/settings.json)")
	apply := fs.Bool("apply", false, "write the v2 control tree and rename .run -> .run.migrated (default is a dry run)")
	force := fs.Bool("force", false, "apply even when a live worker's composer is busy or pending")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox migrate --epic <dir> [--root <clone>] [--apply] [--force]")
	}
	plan, err := migrate.Read(*epicDir)
	if err != nil {
		return fail("%v", err)
	}

	// Ask Orca which dispatches are still live. With no run (or an unreachable backend) keep Read's behavior and warn.
	var workers []backend.Worker
	haveBackend := false
	if plan.Run != "" {
		c := orca.New(plan.Run)
		if h := os.Getenv("ORCA_TERMINAL_HANDLE"); h != "" {
			c.From = h
		}
		if ws, werr := c.WorkerList(); werr == nil {
			workers, haveBackend = ws, true
			plan.ApplyLiveness(workers)
		} else {
			fmt.Fprintf(os.Stderr, "cox migrate: worker-list unavailable (%v); keeping v1 session behavior\n", werr)
		}
	} else {
		fmt.Fprintln(os.Stderr, "cox migrate: no run id in .run; keeping v1 session behavior")
	}

	fmt.Print(plan.Table())
	busyOrPending := printChecklist(plan, workers, haveBackend, *root)

	if !*apply {
		fmt.Println("dry run: pass --apply to write")
		return 0
	}
	if busyOrPending && !*force {
		return fail("refusing to apply: a live worker's composer is busy or pending; wait for it to idle or pass --force")
	}
	if err := migrate.Apply(plan); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("applied: wrote .cox tree (events, sessions, env, handoffs); .run -> .run.migrated (%s)\n", *epicDir)
	return 0
}

// printChecklist prints the go/no-go checklist and returns whether any live worker's composer is busy or pending (the
// condition that blocks --apply without --force).
func printChecklist(plan migrate.Plan, workers []backend.Worker, haveBackend bool, root string) bool {
	fmt.Println("\n-- migrate checklist --")
	fmt.Printf("run: %s\n", nonEmpty(plan.Run, "(none)"))

	// Live workers: dispatch, terminal, composer (Backend.Composer). Only ready|running dispatches are live.
	byDispatch := map[string]backend.Worker{}
	for _, w := range workers {
		byDispatch[w.Dispatch] = w
	}
	var c *orca.Client
	if plan.Run != "" {
		c = orca.New(plan.Run)
	}
	busyOrPending := false
	live := 0
	for _, sp := range plan.Stories {
		if sp.Dispatch == "" {
			continue
		}
		w, ok := byDispatch[sp.Dispatch]
		if !ok || !w.Alive() {
			continue
		}
		live++
		handle := w.Handle
		if handle == "" {
			handle = sp.Term
		}
		composer := "unknown"
		if c != nil {
			if cs, err := c.Composer(backend.Session{Kind: "orca", ID: sp.Dispatch, Handle: handle}); err == nil {
				composer = cs
			}
		}
		if composer == backend.ComposerBusy || composer == backend.ComposerPending {
			busyOrPending = true
		}
		fmt.Printf("worker %-24s dispatch=%s terminal=%s composer=%s\n", sp.ID, sp.Dispatch, nonEmpty(handle, "-"), composer)
	}
	if haveBackend && live == 0 {
		fmt.Println("workers: none live")
	}
	if !haveBackend {
		fmt.Println("workers: unknown (no Orca; v1 session behavior kept)")
	}

	// Allocations to write, with a live-port probe.
	for _, a := range plan.Allocs {
		portNote := ""
		if a.Port > 0 {
			if pid, _, occupied, perr := env.RealOps().PortOwner(a.Port); perr != nil {
				portNote = " port=unknown"
			} else if occupied {
				portNote = fmt.Sprintf(" port LISTENING pid=%d", pid)
			} else {
				portNote = " port=free"
			}
		}
		fmt.Printf("alloc  %-24s port=%d%s\n", a.Story, a.Port, portNote)
	}

	// Handoffs to wrap.
	for _, h := range plan.Handoffs {
		if h.Wrapped {
			fmt.Printf("handoff %-23s will wrap -> checkpoint.v1\n", h.Story)
		}
	}

	// Clone hooks still on v1.
	if root != "" {
		if cloneHasV1Hooks(root) {
			fmt.Printf("hooks: %s/.claude/settings.json still references bin/hook- (run cox workspace hooks)\n", root)
		} else {
			fmt.Printf("hooks: %s/.claude/settings.json has no v1 bin/hook- reference\n", root)
		}
	}

	// Conclusion.
	if busyOrPending {
		fmt.Println("=> NO-GO: a live worker is mid-turn (composer busy/pending); apply needs --force")
	} else {
		fmt.Println("=> GO")
	}
	return busyOrPending
}

// cloneHasV1Hooks reports whether <root>/.claude/settings.json still references a v1 bin/hook- shim.
func cloneHasV1Hooks(root string) bool {
	b, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "bin/hook-")
}
