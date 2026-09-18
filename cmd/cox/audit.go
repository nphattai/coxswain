package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/forge/github"
	"github.com/nphattai/coxswain/internal/verdict"
)

// cmdAudit implements `cox audit pr <story> --epic <dir> [--pr <n>] [--json]`. It runs the story PR through the github
// forge and prints a three-state audit bundle bound to the head sha. If the head differs from the last audit's head,
// the output is marked stale. Exit codes: 0 pass, 1 fail, 3 unknown.
func cmdAudit(args []string) int {
	if len(args) == 0 || args[0] != "pr" {
		fmt.Fprintln(os.Stderr, "usage: cox audit pr <story> --epic <dir> [--pr <n>] [--json]")
		return 2
	}
	story, rest := onePositional(args[1:])
	fs := flag.NewFlagSet("audit pr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	pr := fs.String("pr", "", "PR number or URL (default: the story branch)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" {
		return usageErr("cox audit pr <story> --epic <dir> [--pr <n>] [--json]")
	}
	dir := readWorktree(*epicDir, story)
	if dir == "" {
		dir = *epicDir // fall back to the epic dir if the worktree is not recorded
	}
	selector := "story/" + story
	if *pr != "" {
		selector = *pr
	}
	rep := verdict.Audit(github.New(dir), story, selector)

	stale := recordAndCheckStale(*epicDir, story, rep.Head)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			verdict.AuditReport
			Stale bool `json:"stale"`
		}{rep, stale})
	} else {
		fmt.Printf("audit %s  PR #%d  head %s  verdict=%s\n", rep.Story, rep.PR, shortSha(rep.Head), rep.Verdict)
		if stale {
			fmt.Println("  STALE: head changed since the last audit")
		}
		fmt.Printf("  ci=%s reviewer=%s files=%d +%d -%d\n", rep.CI, orNone(rep.Reviewer), rep.Shape.Files, rep.Shape.Additions, rep.Shape.Deletions)
		if len(rep.CredMatches) > 0 {
			fmt.Printf("  credentials: %s\n", strings.Join(rep.CredMatches, ", "))
		}
		for _, r := range rep.Reasons {
			fmt.Printf("  - %s\n", r)
		}
	}

	switch rep.Verdict {
	case verdict.Fail:
		return 1
	case verdict.Unknown:
		return 3
	default:
		return 0
	}
}

// recordAndCheckStale stores the audited head under .cox/audit/<story>.head and reports whether it differs from the
// previously recorded head (the verdict is stale relative to the last audit).
func recordAndCheckStale(epicDir, story, head string) bool {
	if head == "" {
		return false
	}
	dir := filepath.Join(epicDir, ".cox", "audit")
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, story+".head")
	prev := readTrimmed(path)
	_ = os.WriteFile(path, []byte(head), 0o644)
	return prev != "" && prev != head
}

func shortSha(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	if s == "" {
		return "unknown"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
