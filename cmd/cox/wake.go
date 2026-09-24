package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/nphattai/coxswain/internal/wake"
)

// cmdWake implements `cox wake drain [--peek] | ack-through <gen> | wait --max <dur>`, all with --epic <dir>.
func cmdWake(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox wake drain|ack-through|wait --epic <dir>")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "drain":
		return wakeDrain(rest)
	case "ack-through":
		return wakeAckThrough(rest)
	case "wait":
		return wakeWait(rest)
	default:
		fmt.Fprintf(os.Stderr, "cox wake: unknown subcommand %q\n", sub)
		return 2
	}
}

func wakeDrain(args []string) int {
	fs := flag.NewFlagSet("wake drain", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	peek := fs.Bool("peek", false, "print without any side effect (identical output; ack stays explicit)")
	full := fs.Bool("full", false, "print each wake's full body instead of the 200-char note")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox wake drain --epic <dir> [--peek] [--full]")
	}
	alive := func() bool { return watcherHealthy(*epicDir, time.Now()) }
	if err := wake.Present(*epicDir, os.Stdout, os.Stderr, wake.PresentOptions{Peek: *peek, Full: *full, WatcherAlive: alive}); err != nil {
		return fail("%v", err)
	}
	return 0
}

func wakeAckThrough(args []string) int {
	gen, rest := onePositional(args)
	fs := flag.NewFlagSet("wake ack-through", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	g, err := strconv.Atoi(gen)
	if *epicDir == "" || err != nil {
		return usageErr("cox wake ack-through <gen> --epic <dir>")
	}
	res, err := wake.Ack(*epicDir, g)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprint(os.Stderr, res.Notice(*epicDir))
	return 0
}

func wakeWait(args []string) int {
	fs := flag.NewFlagSet("wake wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	max := fs.Duration("max", 25*time.Minute, "maximum time to block")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox wake wait --epic <dir> [--max <dur>]")
	}
	wakes, timedOut, err := wake.Wait(*epicDir, *max, 5*time.Second)
	if err != nil {
		return fail("%v", err)
	}
	if timedOut {
		return 3 // exit 3 on timeout (brief F)
	}
	printWakes(wakes, false)
	return 0
}

// printWakes renders each wake. With full=true it prints the untruncated body (Wake.Full) when the record kept one,
// otherwise the 200-char note; without it, always the note. An empty note falls back to the kind label.
func printWakes(wakes []wake.Wake, full bool) {
	for _, w := range wakes {
		note := w.Note
		if full && w.Full != "" {
			note = w.Full
		}
		if note == "" {
			note = string(w.Kind)
		}
		fmt.Printf("[gen %d] %s %s: %s\n", w.Gen, w.Kind, w.Story, note)
	}
}

func usageErr(msg string) int {
	fmt.Fprintln(os.Stderr, "usage:", msg)
	return 2
}
