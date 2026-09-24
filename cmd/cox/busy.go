package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/busy"
)

// cmdBusy implements `cox busy arm|apply|progress|read|retire <story> --epic <dir>`, the harness-neutral entry point to the
// busy-state record (DESIGN wave-2 item 6). Any harness hook - a Claude UserPromptSubmit/Stop/SessionEnd hook, the Pi
// extension, or a future Codex hook - reports idle/busy through this command instead of a backend guessing from a UI.
// The gen minted by `arm` is threaded to the harness via the launch env (COX_BUSY_GEN); `apply` and `retire` present it
// and a stale gen is rejected. The story is a leading positional (the Pi extension form) or the --story flag / $COX_STORY
// (the Claude worker hook form, which carries only the state as a positional).
func cmdBusy(args []string) int {
	verb, rest := onePositional(args)
	switch verb {
	case "arm":
		return busyArm(rest)
	case "apply":
		return busyApply(rest)
	case "progress":
		return busyProgress(rest)
	case "read":
		return busyRead(rest)
	case "retire":
		return busyRetire(rest)
	default:
		return usageErr("cox busy arm|apply|progress|read|retire <story> --epic <dir>")
	}
}

// busySources returns the story's harness and the sources that harness's capability card trusts, read from the story
// frontmatter (default claude). An unknown harness yields no adapter, so its trust table is empty and every source is
// rejected - fail closed.
func busySources(epicDir, story string) (string, []string) {
	h := nonEmpty(readStoryMeta(epicDir, story).Harness, "claude")
	return h, registry.Card(h).BusySources
}

func busyArm(args []string) int {
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("busy arm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	storyFlag := fs.String("story", os.Getenv("COX_STORY"), "story id (defaults to $COX_STORY; or pass it as the positional)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	story = nonEmpty(story, *storyFlag)
	if *epicDir == "" || story == "" {
		return usageErr("cox busy arm <story> --epic <dir>")
	}
	h, sources := busySources(*epicDir, story)
	gen, err := busy.Arm(*epicDir, story, h, sources)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(gen) // stdout is the minted gen so the caller can embed it into the launch env
	return 0
}

func busyApply(args []string) int {
	// Peel up to two leading positionals so the flags that follow them still parse. Two positionals is the Pi extension
	// form `busy apply <story> <state>`; one positional is the Claude worker hook form `busy apply <state> --story <id>`.
	a, bPos, rest := twoPositionals(args)
	fs := flag.NewFlagSet("busy apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	storyFlag := fs.String("story", os.Getenv("COX_STORY"), "story id (defaults to $COX_STORY)")
	gen := fs.String("gen", os.Getenv("COX_BUSY_GEN"), "the incarnation gen minted at arm (defaults to $COX_BUSY_GEN)")
	source := fs.String("source", "", "who is reporting the state (e.g. claude-hook, pi-ext)")
	event := fs.String("event", "", "the lifecycle event (e.g. prompt, stop, agent_start)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	story, state := *storyFlag, a
	if bPos != "" {
		story, state = a, bPos
	}
	if *epicDir == "" || story == "" || state == "" || *source == "" || *event == "" {
		return usageErr("cox busy apply <busy|idle|unknown> --story <id> --gen <g> --source <s> --event <e> --epic <dir>")
	}
	if err := busy.Apply(*epicDir, story, state, *gen, *source, *event); err != nil {
		return fail("%v", err)
	}
	return 0
}

// busyProgress is fm-busy-event.sh progress: record observed native-harness activity for the armed incarnation
// (the progress marker) without changing the semantic busy state; a stale gen is refused and writes nothing.
func busyProgress(args []string) int {
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("busy progress", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	storyFlag := fs.String("story", os.Getenv("COX_STORY"), "story id (defaults to $COX_STORY)")
	gen := fs.String("gen", os.Getenv("COX_BUSY_GEN"), "the incarnation gen minted at arm (defaults to $COX_BUSY_GEN)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	story = nonEmpty(story, *storyFlag)
	if *epicDir == "" || story == "" || *gen == "" {
		return usageErr("cox busy progress <story> --gen <g> --epic <dir>")
	}
	if err := busy.Progress(*epicDir, story, *gen); err != nil {
		return fail("%v", err)
	}
	return 0
}

func busyRetire(args []string) int {
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("busy retire", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	storyFlag := fs.String("story", os.Getenv("COX_STORY"), "story id (defaults to $COX_STORY)")
	gen := fs.String("gen", os.Getenv("COX_BUSY_GEN"), "the incarnation gen minted at arm (defaults to $COX_BUSY_GEN)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	story = nonEmpty(story, *storyFlag)
	if *epicDir == "" || story == "" || *gen == "" {
		return usageErr("cox busy retire --story <id> --gen <g> --epic <dir>")
	}
	if err := busy.Retire(*epicDir, story, *gen); err != nil {
		return fail("%v", err)
	}
	return 0
}

func busyRead(args []string) int {
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("busy read", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	storyFlag := fs.String("story", os.Getenv("COX_STORY"), "story id (defaults to $COX_STORY)")
	asJSON := fs.Bool("json", false, "print the full record as JSON")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	story = nonEmpty(story, *storyFlag)
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
