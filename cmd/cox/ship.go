package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/github"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/verdict"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdShip implements `cox ship facts --epic <dir> [--json]`. It reports per-repo ahead/behind and conflicts of the epic
// branch against each repo's production branch, using three-state facts: a failed fetch or merge-tree is unknown, never
// a fabricated "conflicts: none" (F12). Exit code 3 if any repo is unknown, else 0.
func cmdShip(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox ship facts|merge --epic <dir> ...")
		return 2
	}
	switch args[0] {
	case "facts":
		return cmdShipFacts(args)
	case "merge":
		return cmdShipMerge(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cox ship: unknown subcommand %q (want facts|merge)\n", args[0])
		return 2
	}
}

// cmdShipFacts implements `cox ship facts` (see cmdShip). args[0] is "facts".
func cmdShipFacts(args []string) int {
	fs := flag.NewFlagSet("ship facts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	asJSON := fs.Bool("json", false, "emit JSON")
	noForge := fs.Bool("no-forge", false, "skip the GitHub forge probe (PR/checks/merged stay unknown)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox ship facts --epic <dir> [--json] [--no-forge]")
	}
	targets, err := shipTargets(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	git := func(dir string, gitArgs ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, gitArgs...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	rep := verdict.Ship(targets, git)
	var forgeFacts []verdict.ShipForgeFacts
	if !*noForge {
		forgeFacts = verdict.ShipForge(targets, func(dir string) forge.Forge { return github.New(dir) })
	}

	anyUnknown := false
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			verdict.ShipReport
			Forge []verdict.ShipForgeFacts `json:"forge,omitempty"`
		}{rep, forgeFacts})
		for _, r := range rep.Repos {
			if r.Verdict == verdict.Unknown {
				anyUnknown = true
			}
		}
	} else {
		for _, r := range rep.Repos {
			fmt.Printf("%-12s verdict=%s ahead=%d behind=%d", r.Alias, r.Verdict, r.Ahead, r.Behind)
			switch {
			case r.Verdict == verdict.Unknown:
				anyUnknown = true
				fmt.Print(" conflicts=unknown")
			case len(r.Conflicts) == 0:
				fmt.Print(" conflicts=none")
			default:
				fmt.Printf(" conflicts=%d files", len(r.Conflicts))
			}
			fmt.Println()
			for _, reason := range r.Reasons {
				fmt.Printf("  - %s\n", reason)
			}
		}
		for _, ff := range forgeFacts {
			fmt.Printf("%-12s forge verdict=%s pr=%s checks=%s merged=%s head=%s\n",
				ff.Alias, ff.Verdict, prNum(ff.PR), ff.Checks, ff.Merged, shortSha(ff.Head))
			for _, reason := range ff.Reasons {
				fmt.Printf("  - %s\n", reason)
			}
		}
	}
	if anyUnknown {
		return 3
	}
	return 0
}

// cmdShipMerge implements `cox ship merge --pr <n> --epic <dir> [--method squash|merge|rebase] [--allow-red <name>]
// [--captain] [--check]`. It is the single merge command (item 8): through the forge it reads the PR live, refuses a PR
// that is not an open, non-draft, mergeable one on the epic/production branch whose every check is green at the live head,
// merges with the head pinned, reads the result back, and appends a `merged` event to the epic ledger. It is captain-run:
// refused from a worker terminal (COX_STORY) and, while merge.yolo is false, refused unless --captain. --check does
// everything but the merge and the ledger write. Exit codes: 0 merged, 1 refused (every failing reason listed), 3 unknown.
func cmdShipMerge(args []string) int {
	fs := flag.NewFlagSet("ship merge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	prNumber := fs.Int("pr", 0, "PR number to merge")
	method := fs.String("method", "squash", "merge method: squash|merge|rebase")
	check := fs.Bool("check", false, "read and print the verdict, but never merge or write the ledger")
	captain := fs.Bool("captain", false, "the captain runs the merge (required unless merge.yolo=true)")
	var allowRed repoList
	fs.Var(&allowRed, "allow-red", "waive a named check (repeatable); attended use only")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || *prNumber == 0 {
		return usageErr("cox ship merge --pr <n> --epic <dir> [--method squash|merge|rebase] [--allow-red <name>] [--captain] [--check]")
	}
	switch *method {
	case "squash", "merge", "rebase":
	default:
		return fail("--method must be squash|merge|rebase, got %q", *method)
	}
	targets, err := shipTargets(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	t := targets[0] // single-repo epic: the one alias checkout gh resolves the PR in
	in := verdict.MergeInput{
		Selector:   strconv.Itoa(*prNumber),
		EpicBranch: t.EpicBranch,
		Production: t.Production,
		Method:     *method,
		Yolo:       loadPolicyQuiet(*epicDir).MergeYolo(),
		Captain:    *captain,
		Worker:     os.Getenv("COX_STORY") != "",
		AllowRed:   allowRed,
		Check:      *check,
	}
	return runShipMerge(*epicDir, in, github.New(t.Dir))
}

// runShipMerge runs the merge decision through the forge, prints the verdict, and - only on a real (non-check) merge -
// appends the `merged` event to the epic ledger. It is the testable core of cox ship merge: a test injects a fake forge.
// Exit codes: 0 merged (or --check would merge), 1 refused, 3 unknown.
func runShipMerge(epicDir string, in verdict.MergeInput, f forge.Forge) int {
	rep := verdict.Merge(f, in)
	fmt.Printf("ship merge PR #%d  head %s  method %s  verdict=%s\n", rep.PR, shortSha(rep.Head), rep.Method, rep.State)
	for _, r := range rep.Reasons {
		fmt.Printf("  - %s\n", r)
	}
	switch rep.State {
	case verdict.MergeDone:
		if in.Check {
			fmt.Println("  --check: would merge; nothing merged")
			return 0
		}
		if err := state.AppendLedger(epicDir, state.Event{
			Type: state.Merged, Epic: filepath.Base(epicDir), Story: state.EpicStory, Actor: state.Captain,
			Evidence:          map[string]any{"pr": rep.PR, "head": rep.Head, "method": rep.Method, "by": mergeBy(in.Captain)},
			ExternalConfirmed: true,
		}); err != nil {
			return fail("record merged event: %v", err)
		}
		fmt.Printf("  merged; recorded in %s\n", state.LedgerPath(epicDir))
		return 0
	case verdict.MergeUndetermined:
		return 3
	default:
		if recordLandedMerge(epicDir, in, rep, f) {
			return 0
		}
		return 1
	}
}

// recordLandedMerge covers the re-run after an unknown read-back (#45 follow-up): the earlier `cox ship merge` merged the
// PR but could not confirm it, so it wrote no ledger row, and this run refuses the now-merged PR on the state gate. When
// that is the only refusal (mergeability of a merged PR is moot), the PR reads merged live, and the ledger has no
// `merged` row for it yet, the row is written now and the merge counts as done. It cannot tell a merge an earlier cox
// run made from one done by hand, so the row says only what it saw ("already merged when this run read it"). Never
// under --check.
func recordLandedMerge(epicDir string, in verdict.MergeInput, rep verdict.MergeReport, f forge.Forge) bool {
	if in.Check {
		return false
	}
	for _, r := range rep.Reasons {
		if !strings.HasPrefix(r, "state: ") && !strings.HasPrefix(r, "mergeable: ") {
			return false // authority, base, draft or a failed check still refuses
		}
	}
	pr, err := f.PR(in.Selector)
	if err != nil || pr.State != "merged" {
		return false
	}
	events, _, err := state.Load(epicDir)
	if err != nil {
		return false
	}
	for _, ev := range events {
		if n, ok := ev.Evidence["pr"].(float64); ok && ev.Type == state.Merged && int(n) == pr.Number {
			return false // already recorded: a plain refusal
		}
	}
	if err := state.AppendLedger(epicDir, state.Event{
		Type: state.Merged, Epic: filepath.Base(epicDir), Story: state.EpicStory, Actor: state.Captain,
		Evidence: map[string]any{"pr": pr.Number, "head": pr.Head, "method": in.Method, "by": mergeBy(in.Captain),
			"recorded": "already merged when this run read it"},
		ExternalConfirmed: true,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "cox: record merged event: %v\n", err)
		return false
	}
	fmt.Printf("  PR #%d was already merged and the ledger had no row for it; recorded in %s\n", pr.Number, state.LedgerPath(epicDir))
	return true
}

// mergeBy names who ran the merge for the ledger `by` field: "captain" when --captain, else the terminal handle, else
// "leader" (a non-captain merge is only reached when merge.yolo is true).
func mergeBy(captain bool) string {
	if captain {
		return "captain"
	}
	if h := os.Getenv("ORCA_TERMINAL_HANDLE"); h != "" {
		return h
	}
	return "leader"
}

// prNum renders a PR number for the human table, or "none" when there is no PR (0).
func prNum(n int) string {
	if n == 0 {
		return "none"
	}
	return "#" + strconv.Itoa(n)
}

// shipTargets assembles one RepoTarget per alias in the epic's `repos` file, resolving the production branch from the
// workspace and the checkout dir from the epic's alias symlink.
func shipTargets(epicDir string) ([]verdict.RepoTarget, error) {
	slug := filepath.Base(epicDir)
	wsRoot, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Load(wsRoot)
	if err != nil {
		return nil, err
	}
	reposFile, err := os.ReadFile(filepath.Join(epicDir, "repos"))
	if err != nil {
		return nil, fmt.Errorf("read epic repos file: %w", err)
	}
	var targets []verdict.RepoTarget
	for _, line := range strings.Split(string(reposFile), "\n") {
		f := strings.Fields(line)
		if len(f) < 1 || f[0] == "" {
			continue
		}
		alias := f[0]
		repo, ok := ws.Repo(alias)
		if !ok {
			continue
		}
		dir := filepath.Join(epicDir, alias) // link.sh symlinks the alias to the epic worktree
		targets = append(targets, verdict.RepoTarget{
			Alias:      alias,
			Dir:        dir,
			EpicBranch: "epic/" + slug,
			Production: repo.Production,
			Staging:    repo.Staging,
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no ship targets: epic repos file empty or no matching workspace repos")
	}
	return targets, nil
}
