package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/protocol/inbox"
)

// cmdInbox implements `cox inbox ack <rec>`: a thin wrapper over the mv-into-handled ack.
func cmdInbox(args []string) int {
	if len(args) == 0 || args[0] != "ack" {
		fmt.Fprintln(os.Stderr, "usage: cox inbox ack <record-path>")
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
