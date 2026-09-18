package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
)

// cmdCheckpoint implements `cox checkpoint facts | inject`.
func cmdCheckpoint(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox checkpoint facts|inject")
		return 2
	}
	switch args[0] {
	case "facts":
		return checkpointFacts(args[1:])
	case "inject":
		return checkpointInject(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cox checkpoint: unknown subcommand %q\n", args[0])
		return 2
	}
}

func checkpointFacts(args []string) int {
	fs := flag.NewFlagSet("checkpoint facts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	story := fs.String("story", "", "story id")
	worktree := fs.String("worktree", ".", "worker worktree path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" {
		return usageErr("cox checkpoint facts --epic <dir> --story <id> [--worktree <path>]")
	}
	f, err := checkpoint.Facts(*worktree, *epicDir, *story)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Print(f.Markdown())
	return 0
}

func checkpointInject(args []string) int {
	fs := flag.NewFlagSet("checkpoint inject", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	story := fs.String("story", "", "story id")
	attempt := fs.Int("attempt", 0, "current attempt (0 = read from event log)")
	head := fs.String("head", "", "current HEAD sha (for stale detection)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" {
		return usageErr("cox checkpoint inject --epic <dir> --story <id> [--attempt N] [--head <sha>]")
	}
	at := *attempt
	if at == 0 {
		at = currentAttempt(*epicDir, *story)
	}
	inj, err := checkpoint.Inject(*epicDir, *story, at, *head)
	if err != nil {
		var wa *checkpoint.ErrWrongAttempt
		if errors.As(err, &wa) {
			// F07: refuse to inject a wrong-attempt checkpoint; exit 1 with the reason.
			fmt.Fprintln(os.Stderr, "cox checkpoint inject:", err)
			return 1
		}
		return fail("%v", err)
	}
	fmt.Print(inj.Text)
	return 0
}
