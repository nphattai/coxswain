package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/review/lavish"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdReview implements `cox review open|poll|reply` (M13): the leader opens a generated artifact in the lavish review surface
// and polls once (bounded) for the reviewer's feedback, which lands as inbox records for _leader plus wakes. The poll is
// a separate bounded subprocess, never inside the watcher (arena round-1 reviewer claim).
func cmdReview(args []string) int {
	if len(args) == 0 {
		return usageErr("cox review open|poll|reply|share <artifact> --epic <dir> [--max <dur>]")
	}
	switch args[0] {
	case "open":
		return reviewOpen(args[1:])
	case "poll":
		return reviewPoll(args[1:])
	case "reply":
		return reviewReply(args[1:])
	case "share":
		return reviewShare(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cox review: unknown subcommand %q\n", args[0])
		return 2
	}
}

// resolveReviewPolicy loads the merged policy for the epic, best-effort (a nil policy means the PATH default and share
// off, so a review still degrades gracefully when policy cannot be read).
func resolveReviewPolicy(epicDir string) *workspace.Policy {
	wsRoot, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return nil
	}
	pol, err := workspace.Resolve(wsRoot, projectDirOf(epicDir))
	if err != nil {
		return nil
	}
	return pol
}

// reviewConfig resolves the lavish adapter config from policy review.binary/npx (PATH default when policy is absent).
func reviewConfig(epicDir string) lavish.Config {
	pol := resolveReviewPolicy(epicDir)
	if pol == nil {
		return lavish.Config{}
	}
	cfg := lavish.Config{Binary: pol.Review.Binary}
	if n := pol.Review.NPX; n != nil && n.Version != "" {
		cfg.NPX = &lavish.NPXOptIn{Version: n.Version, Integrity: n.Integrity}
	}
	return cfg
}

// parseInterspersed parses flags that may appear before or after positionals (the stdlib flag package stops at the
// first positional), returning the positionals in order. It returns false on a flag error.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, bool) {
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, false
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return pos, true
		}
		pos = append(pos, rest[0])
		rest = rest[1:]
	}
}

func reviewOpen(args []string) int {
	fs := flag.NewFlagSet("review open", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	pos, ok := parseInterspersed(fs, args)
	if !ok {
		return 2
	}
	artifact := ""
	if len(pos) > 0 {
		artifact = pos[0]
	}
	if artifact == "" {
		return usageErr("cox review open <artifact> [--epic <dir>]")
	}
	if err := lavish.Open(reviewConfig(*epicDir), artifact, os.Stdout, os.Stderr); err != nil {
		return fail("%v", err)
	}
	return 0
}

func reviewPoll(args []string) int {
	fs := flag.NewFlagSet("review poll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	max := fs.Duration("max", 25*time.Minute, "how long to wait for feedback before timing out")
	pos, ok := parseInterspersed(fs, args)
	if !ok {
		return 2
	}
	artifact := ""
	if len(pos) > 0 {
		artifact = pos[0]
	}
	if *epicDir == "" || artifact == "" {
		return usageErr("cox review poll <artifact> --epic <dir> [--max <dur>]")
	}
	res, err := lavish.Poll(reviewConfig(*epicDir), *epicDir, artifact, *max, nil, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cox review poll:", err)
		return 1
	}
	for _, r := range res.Records {
		fmt.Println("recorded", r)
	}
	fmt.Printf("review poll: %s (exit %d)\n", res.Status, res.ExitCode)
	return res.ExitCode
}

func reviewReply(args []string) int {
	fs := flag.NewFlagSet("review reply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	max := fs.Duration("max", 25*time.Minute, "how long to wait for feedback after replying")
	pos, ok := parseInterspersed(fs, args)
	if !ok {
		return 2
	}
	if *epicDir == "" || len(pos) != 2 || pos[0] == "" || pos[1] == "" {
		return usageErr("cox review reply <message> <artifact> --epic <dir> [--max <dur>]")
	}
	res, err := lavish.Reply(reviewConfig(*epicDir), *epicDir, pos[1], pos[0], *max, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cox review reply:", err)
		return 1
	}
	for _, r := range res.Records {
		fmt.Println("recorded", r)
	}
	fmt.Printf("review reply: %s (exit %d)\n", res.Status, res.ExitCode)
	return res.ExitCode
}

// reviewShare implements `cox review share <artifact> [--share] --epic <dir>`: it refuses to publish to ht-ml.app while
// policy review.share is false unless --share is passed, printing the outward-facing warning either way. cox never
// shares on its own.
func reviewShare(args []string) int {
	fs := flag.NewFlagSet("review share", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	share := fs.Bool("share", false, "publish to the third-party ht-ml.app host (outward-facing; required when policy review.share is false)")
	pos, ok := parseInterspersed(fs, args)
	if !ok {
		return 2
	}
	artifact := ""
	if len(pos) > 0 {
		artifact = pos[0]
	}
	if artifact == "" {
		return usageErr("cox review share <artifact> [--share] --epic <dir>")
	}
	policyAllows := false
	if pol := resolveReviewPolicy(*epicDir); pol != nil {
		policyAllows = pol.Review.Share
	}
	if !policyAllows && !*share {
		fmt.Fprintln(os.Stderr, "cox review share: refused. Publishing to ht-ml.app is outward-facing (a third-party host; the link is public by default) and policy review.share is false. Pass --share to publish anyway.")
		return 1
	}
	fmt.Fprintln(os.Stderr, "WARNING: outward-facing. cox review share publishes this artifact to ht-ml.app, a third-party host; the link is public unless you pass lavish --private, and it may be cached or indexed even after removal.")
	if err := lavish.Share(reviewConfig(*epicDir), artifact, os.Stdout, os.Stderr); err != nil {
		return fail("%v", err)
	}
	return 0
}
