package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/watch"
)

// cmdSteer implements `cox steer <story> "<text>" --epic <dir> [--fyi] [--override <why>]` and the retry-only
// `cox steer --ring <story> --epic <dir>`. Flags may appear in any position (before, between, or after the story and
// text); the two leading non-flag tokens are the story and text. After the durable record is written, it knocks on the
// worker's terminal now (doorbell via Backend.Send), the way v1 send.sh does, and reports whether it rang.
func cmdSteer(args []string) int {
	fs := flag.NewFlagSet("steer", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	fyi := fs.Bool("fyi", false, "send as fyi (does not count against the budget, never interrupts)")
	override := fs.String("override", "", "reason to override the steer budget")
	ring := fs.Bool("ring", false, "only re-ring the doorbell for unhandled inbox (no new record, no budget)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	story, text := "", ""
	if len(pos) > 0 {
		story = pos[0]
	}
	if len(pos) > 1 {
		text = pos[1]
	}
	if *ring {
		return steerRing(*epicDir, story)
	}
	if *epicDir == "" || story == "" || text == "" {
		fmt.Fprintln(os.Stderr, "usage: cox steer <story> \"<text>\" --epic <dir> [--fyi] [--override <why>]")
		return 2
	}
	urgency := inbox.Steer
	if *fyi {
		urgency = inbox.FYI
	}
	path, err := inbox.Write(*epicDir, story, text, urgency, *override)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(path)

	// Knock on the worker now so it reads the record at its next tool boundary, instead of waiting for the watcher's
	// re-ring. The doorbell only types into an empty composer, so it never clobbers a busy turn (F: fyi never interrupts).
	b, _ := newBackend(*epicDir)
	sess, serr := loadSession(*epicDir, story)
	status := steerKnock(b, sess, serr == nil, inbox.Doorbell(inbox.Dir(*epicDir, story)))
	fmt.Println("doorbell:", status)
	// A busy composer swallowed the knock; the durable record still stands. Tell the leader the watcher will re-ring on
	// its own, and how to force a retry now instead of waiting for the next grace window.
	if strings.HasPrefix(status, "skipped:") {
		fmt.Printf("watcher re-rings up to %d times; run cox steer --ring %s to retry now\n", watch.DefaultInboxRingMax, story)
	}
	return 0
}

// steerRing implements `cox steer --ring <story>`: it re-rings the worker doorbell for a story that already has
// unhandled inbox, writing no new record and spending no budget. With no unhandled inbox there is nothing to retry.
func steerRing(epicDir, story string) int {
	if epicDir == "" || story == "" {
		fmt.Fprintln(os.Stderr, "usage: cox steer --ring <story> --epic <dir>")
		return 2
	}
	recs, err := inbox.List(epicDir, story)
	if err != nil {
		return fail("%v", err)
	}
	if len(recs) == 0 {
		fmt.Printf("no unhandled inbox for %s; nothing to re-ring\n", story)
		return 0
	}
	b, _ := newBackend(epicDir)
	sess, serr := loadSession(epicDir, story)
	fmt.Println("doorbell:", steerKnock(b, sess, serr == nil, inbox.Doorbell(inbox.Dir(epicDir, story))))
	return 0
}

// steerKnock rings the worker's doorbell and returns a short status token: "rang" (delivered to an empty composer),
// "skipped:busy" (a live session whose composer was busy/pending), "no-session" (no backend or no saved session), or
// "error:<msg>" (the send itself failed). It never fails the steer: the record is the durable delivery.
func steerKnock(b backend.Backend, sess backend.Session, haveSess bool, door string) string {
	if b == nil || !haveSess {
		return "no-session"
	}
	rang, err := b.Send(sess, door)
	switch {
	case err != nil:
		return "error:" + err.Error()
	case rang:
		return "rang"
	default:
		return "skipped:busy"
	}
}

// parseInterleaved parses flags that may be interspersed with positionals: it repeatedly runs fs.Parse over the
// remaining args, peeling one leading positional each time stdlib flag stops at a non-flag token. The collected
// positionals are returned in order. A positional that begins with `-` is still treated as a flag by stdlib and errors,
// as before.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// twoPositionals peels up to two leading non-flag args (story, text) and returns the rest for flag parsing.
func twoPositionals(args []string) (a, b string, rest []string) {
	i := 0
	for i < len(args) && len(args[i]) > 0 && args[i][0] != '-' {
		switch i {
		case 0:
			a = args[i]
		case 1:
			b = args[i]
		default:
			return a, b, args[i:]
		}
		i++
	}
	return a, b, args[i:]
}

// threePositionals peels up to three leading non-flag args and returns the rest for flag parsing.
func threePositionals(args []string) (a, b, c string, rest []string) {
	i := 0
	for i < len(args) && len(args[i]) > 0 && args[i][0] != '-' {
		switch i {
		case 0:
			a = args[i]
		case 1:
			b = args[i]
		case 2:
			c = args[i]
		default:
			return a, b, c, args[i:]
		}
		i++
	}
	return a, b, c, args[i:]
}

// onePositional peels one leading non-flag arg and returns the rest.
func onePositional(args []string) (string, []string) {
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		return args[0], args[1:]
	}
	return "", args
}
