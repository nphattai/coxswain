package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/protocol/busy"
)

// cmdBusy implements `cox busy arm|apply|read <story> --epic <dir>`, the harness-neutral entry point to the busy-state
// record (DESIGN wave-3 item 1). Any harness hook - the Pi extension, or a future Claude/Codex hook - reports idle/busy
// through this command instead of a backend guessing from a UI. The gen minted by `arm` is threaded to the harness via
// the launch env (COX_BUSY_GEN); `apply` presents it and a stale gen is rejected.
func cmdBusy(args []string) int {
	verb, rest := onePositional(args)
	story, rest := onePositional(rest)
	switch verb {
	case "arm":
		return busyArm(story, rest)
	case "apply":
		return busyApply(story, rest)
	case "read":
		return busyRead(story, rest)
	default:
		return usageErr("cox busy arm|apply|read <story> --epic <dir>")
	}
}

func busyArm(story string, args []string) int {
	fs := flag.NewFlagSet("busy arm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" {
		return usageErr("cox busy arm <story> --epic <dir>")
	}
	gen, err := busy.Arm(*epicDir, story)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(gen) // stdout is the minted gen so the caller can embed it into the launch env
	return 0
}

func busyApply(story string, args []string) int {
	state, args := onePositional(args)
	fs := flag.NewFlagSet("busy apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	gen := fs.String("gen", os.Getenv("COX_BUSY_GEN"), "the incarnation gen minted at arm (defaults to $COX_BUSY_GEN)")
	source := fs.String("source", "", "who is reporting the state (e.g. pi-ext)")
	event := fs.String("event", "", "the lifecycle event (e.g. agent_start)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" || state == "" || *source == "" || *event == "" {
		return usageErr("cox busy apply <story> <busy|idle|unknown> --gen <g> --source <s> --event <e> --epic <dir>")
	}
	if err := busy.Apply(*epicDir, story, state, *gen, *source, *event); err != nil {
		return fail("%v", err)
	}
	return 0
}

func busyRead(story string, args []string) int {
	fs := flag.NewFlagSet("busy read", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	asJSON := fs.Bool("json", false, "print the full record as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" {
		return usageErr("cox busy read <story> [--json] --epic <dir>")
	}
	if *asJSON {
		if rec, ok := busy.ReadRecord(*epicDir, story); ok {
			b, _ := json.Marshal(rec)
			fmt.Println(string(b))
			return 0
		}
		fmt.Printf("{%q:%q}\n", "state", busy.Unknown)
		return 0
	}
	fmt.Println(busy.Read(*epicDir, story))
	return 0
}
