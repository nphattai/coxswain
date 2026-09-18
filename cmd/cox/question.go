package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nphattai/coxswain/internal/protocol/question"
)

// cmdQuestion implements `cox question wait qNNN --epic <dir> --story <id> --max 25m`: the worker blocks on the answer
// to a question it asked (ADR 0012 C9). It polls the answer file, prints it, and moves the question and answer into
// handled/. On timeout it exits 3 so the worker checkpoints and parks instead of hanging - it never blocks inside a
// backend call. An unknown id or a read error exits 1.
func cmdQuestion(args []string) int {
	if len(args) == 0 || args[0] != "wait" {
		return usageErr("cox question wait qNNN --epic <dir> --story <id> [--max 25m]")
	}
	qid, rest := onePositional(args[1:])
	fs := flag.NewFlagSet("question wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	story := fs.String("story", os.Getenv("COX_STORY"), "story id")
	max := fs.Duration("max", 25*time.Minute, "max wait before timing out (exit 3)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" || qid == "" {
		return usageErr("cox question wait qNNN --epic <dir> --story <id> [--max 25m]")
	}
	answer, timedOut, err := question.Wait(*epicDir, *story, qid, *max)
	if err != nil {
		return fail("%v", err)
	}
	if timedOut {
		fmt.Fprintf(os.Stderr, "cox: %s not answered within %s; checkpoint and park, then resume to wait again\n", qid, *max)
		return 3
	}
	fmt.Println(answer)
	return 0
}
