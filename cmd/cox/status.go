package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/status"
)

// cmdStatus implements `cox status <phase> "<note>"`, run by a worker in its worktree. The epic and story come from
// flags or the COX_EPIC/COX_STORY env vars the brief sets; the attempt is read from the event log. The event is the
// durable record; the backend status mail is best-effort (skipped when no run/leader is configured).
func cmdStatus(args []string) int {
	phase, note, rest := twoPositionals(args)
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, os.Getenv("COX_EPIC"), "epic directory")
	story := fs.String("story", os.Getenv("COX_STORY"), "story id")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" || phase == "" {
		return usageErr("cox status <phase> \"<note>\" --epic <dir> --story <id>")
	}
	var mail backend.Mailbox
	b, _ := newBackend(*epicDir)
	if b != nil {
		mail = b.Mail()
	}
	if err := status.Report(*epicDir, *story, currentAttempt(*epicDir, *story), phase, note, mail, readLeader(*epicDir)); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("status logged: %s %s\n", *story, phase)
	return 0
}
