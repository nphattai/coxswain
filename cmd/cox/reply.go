package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/protocol/question"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdReply is the leader's reply to a worker's question. On the terminal plane (ADR 0012 C8) it is file-based:
// `cox reply <story> qNNN "<answer>" --epic <dir> [--again]` writes the answer file, appends an inbox record (kind=reply,
// budget-exempt) so the worker sees it on its next inbox read, and rings the worker terminal. On the orchestration plane
// it keeps the old mailbox path: `cox reply <msg-id> "<text>" --epic <dir>`.
func cmdReply(args []string) int {
	if resolveOrcaPlane(argEpic(args)) == workspace.PlaneTerminal {
		return replyFile(args)
	}
	return replyMailbox(args)
}

// replyFile handles the terminal-plane file-based reply.
func replyFile(args []string) int {
	story, qid, answer, rest := threePositionals(args)
	fs := flag.NewFlagSet("reply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	again := fs.Bool("again", false, "add another reply to an already-answered question")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" || qid == "" || answer == "" {
		return usageErr("cox reply <story> qNNN \"<answer>\" --epic <dir> [--again]")
	}
	// Echo the canonical id everywhere below: the watcher reads "answer to qNNN:" case-sensitively (replyAnswerRe), so a
	// raw `Q001` record was never marked consumed.
	qid, err := question.CanonID(qid)
	if err != nil {
		return fail("%v", err)
	}
	if _, err := question.Answer(*epicDir, story, qid, answer, *again); err != nil {
		return fail("%v", err)
	}
	// A durable inbox record so the worker sees the answer on its next inbox read even if it missed the ring. It carries
	// kind=reply, so the steer budget never counts it (a reply is an answer, not a fresh instruction).
	body := fmt.Sprintf("answer to %s: %s", qid, answer)
	if _, err := inbox.WriteReply(*epicDir, story, body); err != nil {
		return fail("record answer in inbox: %v", err)
	}
	// Best-effort ring so a worker not currently in `cox question wait` notices.
	if b, _ := newBackend(*epicDir); b != nil {
		if sess, err := loadSession(*epicDir, story); err == nil {
			_, _ = b.Send(sess, fmt.Sprintf("Answer to %s is ready: cox question wait %s --epic %s --story %s", qid, qid, *epicDir, story))
		}
	}
	fmt.Printf("answered %s/%s\n", story, qid)
	return 0
}

// replyMailbox handles the orchestration-plane reply over the backend mailbox (the pre-ADR-0012 path).
func replyMailbox(args []string) int {
	msgID, text, rest := twoPositionals(args)
	fs := flag.NewFlagSet("reply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || msgID == "" || text == "" {
		return usageErr("cox reply <msg-id> \"<text>\" --epic <dir>")
	}
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("reply needs a live backend: set ORCA_RUN_ID or %s/.cox/run", *epicDir)
	}
	if err := b.Mail().Reply(msgID, text); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("replied to %s\n", msgID)
	return 0
}

// argEpic peels the --epic value out of args ahead of full flag parsing, so cmdReply can decide the plane (and thus the
// positional shape) before it knows which parser to use. It returns "" when --epic is absent.
func argEpic(args []string) string {
	for i, a := range args {
		if a == "--epic" || a == "-epic" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
		if v, ok := cutFlag(a, "--epic="); ok {
			return v
		}
		if v, ok := cutFlag(a, "-epic="); ok {
			return v
		}
	}
	return ""
}

func cutFlag(arg, prefix string) (string, bool) {
	if len(arg) > len(prefix) && arg[:len(prefix)] == prefix {
		return arg[len(prefix):], true
	}
	return "", false
}
