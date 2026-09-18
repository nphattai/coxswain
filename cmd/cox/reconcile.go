package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/reconcile"
)

// cmdReconcile implements `cox reconcile --epic <dir> [--apply] [--json]`. For every story left in pending_external it
// reads evidence.intended_to, probes the saved session, and decides purely from the probe whether the intended
// transition can be confirmed. Dry-run (default) prints the table and writes nothing; --apply writes only the confirmed
// transitions and leaves the unknown ones in pending_external with a reason. It never re-issues a side effect and never
// removes a branch. Exit 0 always (an observation tool): a probe failure is unknown, not an error.
func cmdReconcile(args []string) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	apply := fs.Bool("apply", false, "write the confirmed transitions (default: dry-run, print only)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox reconcile --epic <dir> [--apply] [--json]")
	}
	b, _ := newBackend(*epicDir) // may be nil: probes then resolve to unknown, never gone (F08)
	decisions, err := reconcile.Run(*epicDir, b, loadAllSessions(*epicDir), *apply)
	if err != nil {
		return fail("%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(decisions); err != nil {
			return fail("%v", err)
		}
		return 0
	}
	mode := "dry-run"
	if *apply {
		mode = "apply"
	}
	if len(decisions) == 0 {
		fmt.Printf("reconcile (%s): no pending_external stories\n", mode)
		return 0
	}
	fmt.Printf("reconcile (%s): %d pending_external story(ies)\n", mode, len(decisions))
	for _, d := range decisions {
		action := "keep"
		if d.Confirmable() {
			action = "-> " + string(d.To)
			if d.Applied {
				action += " (applied)"
			} else if !*apply {
				action += " (dry-run; use --apply)"
			}
		}
		fmt.Printf("  %-24s intended=%-10s liveness=%-8s %s\n    %s\n",
			d.Story, d.IntendedTo, d.Liveness, action, d.Reason)
	}
	return 0
}
