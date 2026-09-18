package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/nphattai/coxswain/internal/protocol/report"
)

// kvFlag collects repeatable --evidence k=v pairs into a map.
type kvFlag map[string]string

func (f kvFlag) String() string { return "" }
func (f kvFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("evidence must be k=v, got %q", v)
	}
	f[k] = val
	return nil
}

// cmdStoryReport implements `cox story report status|done|stuck|question`, the backend-agnostic worker report channel
// (ADR 0012). It binds the story id from COX_STORY set at spawn and refuses a report for another story, so a confused
// worker cannot write for a story it was not dispatched to.
func cmdStoryReport(args []string) int {
	kind, rest := onePositional(args)
	if kind == "" {
		return usageErr("cox story report status|done|stuck|question --epic <dir> --story <id> [--note <s> | --body <q>] [--evidence k=v ...]")
	}
	fs := flag.NewFlagSet("story report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", os.Getenv("COX_EPIC"), "epic directory")
	story := fs.String("story", os.Getenv("COX_STORY"), "story id")
	note := fs.String("note", "", "status/done/stuck summary (3 sentences)")
	body := fs.String("body", "", "question body")
	evidence := kvFlag{}
	fs.Var(evidence, "evidence", "evidence k=v (repeatable)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" {
		return usageErr("cox story report " + kind + " --epic <dir> --story <id> ...")
	}
	// Bind to COX_STORY: refuse a report whose story differs from the one this worker was dispatched as.
	if env := strings.TrimSpace(os.Getenv("COX_STORY")); env != "" && env != *story {
		return fail("story mismatch: --story %s but COX_STORY=%s (a report may only be for the dispatched story)", *story, env)
	}
	attempt := currentAttempt(*epicDir, *story)

	if kind == "question" {
		if strings.TrimSpace(*body) == "" {
			return usageErr("cox story report question --body \"<question>\" --epic <dir> --story <id>")
		}
		id, err := report.Question(*epicDir, *story, attempt, *body)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Println(id)
		return 0
	}

	if strings.TrimSpace(*note) == "" {
		return usageErr("cox story report " + kind + " --note \"<summary>\" --epic <dir> --story <id>")
	}
	if _, err := report.Report(*epicDir, *story, attempt, kind, *note, toAnyMap(evidence)); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("reported %s: %s\n", kind, *story)
	return 0
}

// toAnyMap widens a kvFlag to map[string]any for the wake evidence, or nil when empty.
func toAnyMap(kv kvFlag) map[string]any {
	if len(kv) == 0 {
		return nil
	}
	out := make(map[string]any, len(kv))
	for k, v := range kv {
		out[k] = v
	}
	return out
}
