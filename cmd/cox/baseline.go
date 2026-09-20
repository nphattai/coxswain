package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	harnesspkg "github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/baseline"
	"github.com/nphattai/coxswain/internal/worktree"
)

// cmdBaseline implements `cox baseline run --story <id> --epic <dir> --harness claude|codex --condition bare|v2
// --before <sha> [--dry-run] [--repo <path>]`. It replays a story from the repo state before its solution so a
// harness's unassisted performance can be measured. It never fetches PR refs and refuses when the before sha already
// contains the story's solution branch (LeakCheck). --dry-run validates and records the plan without a worktree or a
// worker (the safe smoke path; a real spawn must run from the leader, since a worker cannot dispatch a sub-worker).
func cmdBaseline(args []string) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: cox baseline run --story <id> --epic <dir> --harness claude|codex --condition bare|v2 --before <sha> [--dry-run]")
		return 2
	}
	fs := flag.NewFlagSet("baseline run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	story := fs.String("story", "", "story id to replay")
	harness := fs.String("harness", "", "harness (claude|codex)")
	condition := fs.String("condition", "", "bare|v2")
	before := fs.String("before", "", "sha to check out before the solution")
	repoFlag := fs.String("repo", "", "repo path to replay in (default: the story's repo alias)")
	dryRun := fs.Bool("dry-run", false, "validate and record the plan without a worktree or a worker")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *epicDir == "" || *story == "" || *harness == "" || *condition == "" || *before == "" {
		return usageErr("cox baseline run --story <id> --epic <dir> --harness claude|codex --condition bare|v2 --before <sha> [--dry-run]")
	}
	if *condition != baseline.ConditionBare && *condition != baseline.ConditionV2 {
		return fail("condition must be %q or %q, got %q", baseline.ConditionBare, baseline.ConditionV2, *condition)
	}

	repo := *repoFlag
	if repo == "" {
		repo = repoName(*epicDir, readStoryMeta(*epicDir, *story).Repo)
	}
	if repo == "" {
		return fail("no repo for story %s: pass --repo <path> or set the story frontmatter repo", *story)
	}

	// Leak guard: never replay from a state that already contains the solution (before rev is resolved here too).
	leak, detail, err := baseline.LeakCheck(repo, *story, *before)
	if err != nil {
		return fail("%v", err)
	}
	if leak {
		return fail("refusing to replay %s: %s; pick an earlier --before sha", *story, detail)
	}
	fmt.Println("leak check:", detail)

	baselinesDir := filepath.Join(*epicDir, "..", "..", "docs", "baselines")
	if bd := repoBaselinesDir(repo); bd != "" {
		baselinesDir = bd
	}
	date := time.Now().UTC().Format("2006-01-02")

	if *dryRun {
		fmt.Printf("dry-run: would create a worktree of %s at %s on branch baseline/%s-%s, condition %s, harness %s\n",
			repo, *before, *story, *condition, *condition, *harness)
		path, err := baseline.Record(baselinesDir, date, baseline.Row{
			Story: *story, SHA: *before, Harness: *harness, Condition: *condition, Result: "dry-run", LeaderFix: "-",
		})
		if err != nil {
			return fail("record: %v", err)
		}
		fmt.Println("recorded", path)
		return 0
	}

	// Real replay: worktree at the before sha (base = sha, no PR fetch), then spawn per condition. This must run from the
	// leader; a worker cannot dispatch a sub-worker (Orca depth max 1).
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("baseline run needs a live backend: set ORCA_RUN_ID or %s/.cox/run (or use --dry-run)", *epicDir)
	}
	branch := "baseline/" + *story + "-" + *condition
	wt, err := worktree.Ensure(b, repo, branch, *before)
	if err != nil {
		return fail("create replay worktree at %s: %v", *before, err)
	}
	// Re-verify in the worktree that the solution is not reachable, the way the brief asks (branch --contains in-tree).
	if leak, detail, err := baseline.LeakCheck(wt.Path, *story, "HEAD"); err != nil {
		return fail("in-worktree leak check: %v", err)
	} else if leak {
		return fail("replay worktree exposes the solution: %s", detail)
	}

	pol := loadPolicyQuiet(*epicDir)
	model := resolveWorkerModel(pol, *harness, readStoryMeta(*epicDir, *story).Model)
	brief := backend.Brief{}
	var hb harnesspkg.Brief
	if *condition == baseline.ConditionBare {
		// bare: the story text only, no cox AGENTS.md / hooks injected via the story path.
		brief.Text = readStoryText(*epicDir, *story)
		hb = harnesspkg.Brief{Note: brief.Text}
	} else {
		brief.StoryPath = filepath.Join(*epicDir, "stories", *story+".md")
		hb = harnesspkg.Brief{StoryPath: brief.StoryPath}
	}
	argv, err := registry.LaunchArgs(*harness, harnesspkg.Launch{
		Role: harnesspkg.RoleWorker, Worktree: wt.Path, Model: model, Flags: pol.LaunchFlags(*harness), Brief: hb,
	})
	if err != nil {
		return fail("compose launch argv: %v", err)
	}
	spec := backend.HarnessSpec{Name: *harness, Model: model, LaunchFlags: pol.LaunchFlags(*harness), Argv: argv}
	sess, err := b.Spawn(wt, spec, brief)
	if err != nil {
		return fail("spawn baseline worker: %v", err)
	}
	fmt.Printf("baseline %s/%s spawned on %s -> %s\n", *story, *condition, wt.Path, sess.ID)
	path, err := baseline.Record(baselinesDir, date, baseline.Row{
		Story: *story, SHA: *before, Harness: *harness, Condition: *condition, Result: "unknown", LeaderFix: "-",
	})
	if err != nil {
		return fail("record: %v", err)
	}
	fmt.Println("recorded", path, "(update the test result and leader-fix count after the run)")
	return 0
}

// repoBaselinesDir returns <repo>/docs/baselines when repo is an absolute path with a docs dir, else "".
func repoBaselinesDir(repo string) string {
	if !filepath.IsAbs(repo) {
		return ""
	}
	dir := filepath.Join(repo, "docs", "baselines")
	if _, err := os.Stat(filepath.Join(repo, "docs")); err == nil {
		return dir
	}
	return ""
}

// readStoryText returns the body of stories/<id>.md with its YAML frontmatter stripped, for the bare-condition prompt.
func readStoryText(epicDir, story string) string {
	b, err := os.ReadFile(filepath.Join(epicDir, "stories", story+".md"))
	if err != nil {
		return ""
	}
	return stripFrontmatter(string(b))
}

// stripFrontmatter drops a leading `---`-delimited YAML block, returning the remaining body.
func stripFrontmatter(s string) string {
	if len(s) < 3 || s[:3] != "---" {
		return s
	}
	rest := s[3:]
	if i := indexOfDelim(rest); i >= 0 {
		return trimLeadingNewlines(rest[i+3:])
	}
	return s
}

func indexOfDelim(s string) int {
	for i := 0; i+3 <= len(s); i++ {
		if s[i:i+3] == "---" && (i == 0 || s[i-1] == '\n') {
			return i
		}
	}
	return -1
}

func trimLeadingNewlines(s string) string {
	for len(s) > 0 && (s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	return s
}
