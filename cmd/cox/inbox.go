package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nphattai/coxswain/internal/protocol/inbox"
)

// cmdInbox implements `cox inbox ack <rec>` and `cox inbox interrupt-wait`.
func cmdInbox(args []string) int {
	if len(args) > 0 && args[0] == "interrupt-wait" {
		return inboxInterruptWait(args[1:])
	}
	if len(args) == 0 || args[0] != "ack" {
		fmt.Fprintln(os.Stderr, "usage: cox inbox ack <record-path> | cox inbox interrupt-wait --epic <dir> --story <id>")
		return 2
	}
	rec, rest := onePositional(args[1:])
	fs := flag.NewFlagSet("inbox ack", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if rec == "" {
		return usageErr("cox inbox ack <record-path>")
	}
	if err := inbox.Ack(rec); err != nil {
		return fail("%v", err)
	}
	return 0
}

// inboxInterruptWait blocks until an unhandled interrupt record (kind=interrupt) appears for the story, marks it
// handled, prints its path, and exits 0; it exits 3 on timeout (so a caller keeps waiting) and 1 on a bad invocation.
// The harness extension spawns this per turn and calls the harness abort API on exit 0 (DESIGN wave-3 item 4): it is the
// worker-side counterpart to the leader's `cox wake wait` child, keeping inbox parsing in one place (Go).
func inboxInterruptWait(args []string) int {
	fs := flag.NewFlagSet("inbox interrupt-wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, os.Getenv("COX_EPIC"), "epic directory")
	story := fs.String("story", os.Getenv("COX_STORY"), "story id")
	max := fs.Duration("max", time.Hour, "max wait before timing out (exit 3)")
	poll := fs.Duration("poll", 500*time.Millisecond, "poll interval")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" {
		return usageErr("cox inbox interrupt-wait --epic <dir> --story <id>")
	}
	deadline := time.Now().Add(*max)
	for {
		recs, err := inbox.List(*epicDir, *story)
		if err == nil {
			for _, r := range recs {
				if r.Kind == inbox.KindInterrupt {
					if err := inbox.Handled(r); err != nil {
						return fail("mark interrupt handled: %v", err)
					}
					fmt.Println(r.Path)
					return 0
				}
			}
		}
		if time.Now().After(deadline) {
			return 3
		}
		time.Sleep(*poll)
	}
}
