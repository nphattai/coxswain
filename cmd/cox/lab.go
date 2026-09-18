package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/lab"
	"github.com/nphattai/coxswain/internal/scorecard"
	"github.com/nphattai/coxswain/internal/state"
)

// cmdLab implements `cox lab new|assign|report|retire`. The lab defines an experiment that turns a policy rule on/off
// and compares a scorecard metric across the two variants. It only computes and proposes (ADR 0011): it never edits
// policy.json, and retire writes a draft the captain reads.
func cmdLab(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox lab new|assign|report|retire <name> --epic <dir>")
		return 2
	}
	switch args[0] {
	case "new":
		return labNew(args[1:])
	case "assign":
		return labAssign(args[1:])
	case "report":
		return labReport(args[1:])
	case "retire":
		return labRetire(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cox lab: unknown subcommand %q\n", args[0])
		return 2
	}
}

func labNew(args []string) int {
	name, rest := onePositional(args)
	fs := flag.NewFlagSet("lab new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	rule := fs.String("rule", "", "policy key the experiment toggles (e.g. arena.trigger)")
	metric := fs.String("metric", "", "scorecard metric to compare (e.g. cost_usd, ci_wall_incl_queue_s)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || name == "" || *rule == "" || *metric == "" {
		return usageErr("cox lab new <name> --rule <policy.key> --metric <scorecard.metric> --epic <dir>")
	}
	if !lab.KnownMetric(*metric) {
		return fail("unknown metric %q (want one of wall_working_s, wall_parked_s, steers, questions, resumes, tokens_in, tokens_out, cost_usd, ci_wall_incl_queue_s)", *metric)
	}
	dir, err := labDir(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	exp, err := lab.New(dir, name, *rule, *metric)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("created experiment %s: rule=%s metric=%s variants=%v -> %s\n", exp.Name, exp.Rule, exp.Metric, exp.Variants, filepath.Join(dir, name+".json"))
	return 0
}

func labAssign(args []string) int {
	name, rest := onePositional(args)
	fs := flag.NewFlagSet("lab assign", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	story := fs.String("story", "", "story id to assign")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || name == "" || *story == "" {
		return usageErr("cox lab assign <name> --story <id> --epic <dir>")
	}
	dir, err := labDir(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	exp, err := lab.Load(dir, name)
	if err != nil {
		return fail("%v", err)
	}
	exp, variant := lab.Assign(exp, *story)
	if err := lab.Save(dir, exp); err != nil {
		return fail("save experiment: %v", err)
	}
	// Record the assignment as evidence.lab on a self-loop event so the run carries which variant it used. The worker or
	// leader must apply the rule value by hand; the lab never edits policy.json.
	if err := appendLabEvidence(*epicDir, *story, exp, variant); err != nil {
		fmt.Fprintf(os.Stderr, "cox lab: warning: could not record evidence.lab: %v\n", err)
	}
	fmt.Printf("lab %s: %s -> variant %s\n", exp.Name, *story, variant)
	fmt.Printf("apply by hand (policy.json is NOT changed): rule %s = %s\n", exp.Rule, variant)
	return 0
}

// appendLabEvidence records evidence.lab on a self-loop event for the story (from/to its current state), so the
// experiment assignment is a durable fact on the epic log. It is best-effort: a story with no state yet is skipped.
func appendLabEvidence(epicDir, story string, exp lab.Experiment, variant string) error {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return err
	}
	snap := state.Fold(events).Stories[story]
	if snap == nil {
		return fmt.Errorf("story %s has no state yet (assignment recorded in the lab file only)", story)
	}
	return state.Append(epicDir, state.Event{
		Epic: filepath.Base(epicDir), Story: story, Attempt: snap.Attempt, Actor: state.Leader,
		From: snap.State, To: snap.State, ExternalConfirmed: true,
		Evidence: map[string]any{"lab": map[string]any{"name": exp.Name, "variant": variant, "rule": exp.Rule, "metric": exp.Metric}},
	})
}

func labReport(args []string) int {
	name, rest := onePositional(args)
	fs := flag.NewFlagSet("lab report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	noForge := fs.Bool("no-forge", false, "skip the GitHub forge probe (ci metric stays unknown)")
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || name == "" {
		return usageErr("cox lab report <name> --epic <dir> [--json] [--no-forge]")
	}
	rep, err := buildLabReport(*epicDir, name, *noForge)
	if err != nil {
		return fail("%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fail("%v", err)
		}
		return 0
	}
	fmt.Printf("lab %s: rule=%s metric=%s\n", rep.Name, rep.Rule, rep.Metric)
	for _, s := range rep.Stats {
		fmt.Printf("  variant %-4s n=%d mean=%.3f variance=%.3f\n", s.Variant, s.N, s.Mean, s.Variance)
	}
	fmt.Printf("epics on v2: %d (bar for retirement: %d)\n", rep.EpicCount, rep.Bar)
	if rep.EpicCount < rep.Bar {
		fmt.Println("below the bar: read every number next to n and the epic count, never as a rule (ADR 0011)")
	}
	return 0
}

func labRetire(args []string) int {
	name, rest := onePositional(args)
	fs := flag.NewFlagSet("lab retire", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	noForge := fs.Bool("no-forge", false, "skip the GitHub forge probe")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || name == "" {
		return usageErr("cox lab retire <name> --epic <dir> [--no-forge]")
	}
	rep, err := buildLabReport(*epicDir, name, *noForge)
	if err != nil {
		return fail("%v", err)
	}
	ws, err := findWorkspaceRoot(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	decisions := filepath.Join(ws, "docs", "decisions")
	if err := os.MkdirAll(decisions, 0o755); err != nil {
		return fail("create decisions dir: %v", err)
	}
	draft := lab.DraftPath(decisions, name)
	if err := os.WriteFile(draft, []byte(lab.DraftRetirement(rep)), 0o644); err != nil {
		return fail("write draft: %v", err)
	}
	fmt.Printf("wrote retirement proposal to %s (policy.json is NOT changed; the captain decides)\n", draft)
	return 0
}

// buildLabReport loads an experiment, builds the epic scorecard, counts v2 epics, and groups the metric by variant.
func buildLabReport(epicDir, name string, noForge bool) (lab.Report, error) {
	dir, err := labDir(epicDir)
	if err != nil {
		return lab.Report{}, err
	}
	exp, err := lab.Load(dir, name)
	if err != nil {
		return lab.Report{}, err
	}
	card, err := scorecard.Build(epicDir, "", claudeUsageReader(epicDir), forgeChecksReader(epicDir, noForge))
	if err != nil {
		return lab.Report{}, err
	}
	ws, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return lab.Report{}, err
	}
	return lab.BuildReport(exp, card, countV2Epics(ws)), nil
}

// labDir returns <workspace>/cox/lab, the shared home for experiment files.
func labDir(epicDir string) (string, error) {
	ws, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(ws, "cox", "lab"), nil
}

// countV2Epics counts the epics under <ws>/epics that have an event log (a scorecard source), the denominator behind the
// lab retirement bar (ADR 0011).
func countV2Epics(wsRoot string) int {
	entries, err := os.ReadDir(filepath.Join(wsRoot, "epics"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(wsRoot, "epics", e.Name(), state.ControlDir, state.EventsFile)); err == nil {
			n++
		}
	}
	return n
}
