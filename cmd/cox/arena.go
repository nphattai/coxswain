package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/arena"
	"github.com/nphattai/coxswain/internal/arena/check"
	"github.com/nphattai/coxswain/internal/arena/cite"
	arenaRoles "github.com/nphattai/coxswain/internal/arena/roles"
	"github.com/nphattai/coxswain/internal/arena/synth"
	"github.com/nphattai/coxswain/internal/arena/verify"
	"github.com/nphattai/coxswain/internal/artifact"
	"github.com/nphattai/coxswain/internal/epic"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// cmdArena implements `cox arena check|synth` (the leader's post-dispatch steps). `cox epic arena` (dispatch) lives on
// the epic command so it sits next to the epic lifecycle; check/synth are their own verb because they run over reports.
func cmdArena(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox arena check|collect|verify|synth|answer|close --epic <dir>")
		return 2
	}
	switch args[0] {
	case "check":
		return arenaCheck(args[1:])
	case "collect":
		return arenaCollect(args[1:])
	case "verify":
		return arenaVerify(args[1:])
	case "synth":
		return arenaSynth(args[1:])
	case "answer":
		return arenaAnswer(args[1:])
	case "close":
		return arenaCloseCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cox arena: unknown subcommand %q\n", args[0])
		return 2
	}
}

// arenaCloseCmd implements `cox arena close --round N --epic <dir>`: detach and WorktreeRemove every arena role worktree
// still tracked under .cox/wt/arena-* (keeping the branches, F01) and delete those records, so the next round is not
// blocked by an unreleased worktree on a stale branch. Run it once a round's reports are collected.
func arenaCloseCmd(args []string) int {
	fs := flag.NewFlagSet("arena close", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	round := fs.Int("round", 0, "round being closed (recorded in the summary)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || *round == 0 {
		return usageErr("cox arena close --round <n> --epic <dir>")
	}
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("arena close needs a live backend: set ORCA_RUN_ID or %s/.cox/run", *epicDir)
	}
	closed, err := closeArenaWorktrees(b, *epicDir)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("closed %d arena worktree(s) for round %d (branches kept)\n", closed, *round)
	return 0
}

// closeArenaWorktrees detaches and removes every arena role worktree tracked under .cox/wt/arena-* (branches kept) and
// deletes its record. It returns the count removed. A WorktreeRemove failure is reported and skipped (its record is
// kept so it can be retried), never fatal, so one stuck worktree does not strand the rest.
func closeArenaWorktrees(b backend.Backend, epicDir string) (int, error) {
	wtDir := filepath.Join(epicDir, controlDir, "wt")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read worktree records: %w", err)
	}
	closed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "arena-") {
			continue
		}
		rec := filepath.Join(wtDir, e.Name())
		r, err := state.ReadWorktreeRecord(epicDir, e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "cox: warning: %v (record kept)\n", err)
			continue
		}
		path := r.Path
		if path == "" {
			_ = os.Remove(rec)
			continue
		}
		if err := b.WorktreeRemove(backend.Worktree{Path: path}); err != nil {
			fmt.Fprintf(os.Stderr, "cox: warning: remove arena worktree %s (%s): %v\n", e.Name(), path, err)
			continue
		}
		if err := os.Remove(rec); err != nil && !os.IsNotExist(err) {
			return closed, fmt.Errorf("clear worktree record %s: %w", e.Name(), err)
		}
		closed++
	}
	return closed, nil
}

// openArenaWorktrees reports whether any arena role worktree is still tracked under .cox/wt/arena-*, so arena check can
// remind the leader to run `cox arena close` once a round's reports are in.
func openArenaWorktrees(epicDir string) bool {
	entries, err := os.ReadDir(filepath.Join(epicDir, controlDir, "wt"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "arena-") {
			return true
		}
	}
	return false
}

// arenaCheck machine-verifies one role report (or every round-N report when --report is omitted). It exits 1 and lists
// every broken citation or bad severity; a clean report prints the claim count per severity and exits 0.
func arenaCheck(args []string) int {
	fs := flag.NewFlagSet("arena check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	report := fs.String("report", "", "one report file; default checks reports/arena/round-<round>-*.md")
	round := fs.Int("round", 1, "round to check when --report is omitted")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox arena check --epic <dir> [--report <file> | --round <n>]")
	}
	// Bring each role's report home from its worktree before checking, so a report written on a relative path in an
	// isolated worktree is not invisible to the leader's check.
	if copied, err := arena.Collect(*epicDir, *round); err != nil {
		return fail("collect reports: %v", err)
	} else {
		for _, c := range copied {
			fmt.Fprintln(os.Stderr, "collected", filepath.Base(c))
		}
	}
	reports := []string{}
	if *report != "" {
		reports = append(reports, *report)
	} else {
		matches, _ := filepath.Glob(filepath.Join(*epicDir, "reports", "arena", fmt.Sprintf("round-%d-*.md", *round)))
		reports = matches
	}
	if len(reports) == 0 {
		return fail("no arena reports to check")
	}
	bad := false
	for _, r := range reports {
		counts, errs, warnings, err := check.Report(*epicDir, r)
		if err != nil {
			return fail("%v", err)
		}
		name := filepath.Base(r)
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "%s: warning: %s\n", name, w)
		}
		if len(errs) > 0 {
			bad = true
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "%s: %s\n", name, e)
			}
			continue
		}
		fmt.Printf("%s: ok (%s)\n", name, check.FormatCounts(counts))
	}
	printRound2(*epicDir)
	// Once the round's reports are checked clean and its role worktrees are still open, remind the leader to release
	// them before the next round (A9), so a stale worktree does not block a later switch.
	if !bad && openArenaWorktrees(*epicDir) {
		fmt.Printf("all round-%d reports collected; run cox arena close --round %d to release the role worktrees (branches kept)\n", *round, *round)
	}
	if bad {
		return 1
	}
	return 0
}

// printRound2 computes and prints the round-2 signal from the current synthesis.md (when one exists), so the leader sees
// whether a second round is required without a stale field in the file. It never changes the check's exit code.
func printRound2(epicDir string) {
	b, err := os.ReadFile(filepath.Join(epicDir, "reports", "arena", "synthesis.md"))
	if err != nil {
		return
	}
	if need, reasons := synth.Round2(string(b)); need {
		fmt.Printf("round2: yes (%s)\n", strings.Join(reasons, "; "))
	} else {
		fmt.Println("round2: no")
	}
}

// arenaCollect copies each role's round-N report from its worktree into the epic reports dir when it is not already
// there. It is called automatically by arena check, and exposed on its own so the leader can pull reports without
// re-running the check.
func arenaCollect(args []string) int {
	fs := flag.NewFlagSet("arena collect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	round := fs.Int("round", 1, "round to collect")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox arena collect --epic <dir> [--round <n>]")
	}
	copied, err := arena.Collect(*epicDir, *round)
	if err != nil {
		return fail("%v", err)
	}
	if len(copied) == 0 {
		fmt.Println("no reports to collect")
		return 0
	}
	for _, c := range copied {
		fmt.Println("collected", c)
	}
	return 0
}

// arenaVerify runs the `check` on every checked claim in the round's reports and writes reports/arena/verify-round-N.json
// (ADR 0013). Each check runs in a temporary detached worktree at the cited sha, never in the leader checkout. It prints
// a per-claim status line and a summary; it exits 0 even when a check fails or is unknown (the leader reads the results
// and decides), and non-zero only when the reports cannot be read.
func arenaVerify(args []string) int {
	fs := flag.NewFlagSet("arena verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	round := fs.Int("round", 1, "round to verify")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox arena verify --epic <dir> [--round <n>]")
	}
	r, err := verify.Run(*epicDir, *round)
	if err != nil {
		return fail("%v", err)
	}
	pass, failed, unknown := 0, 0, 0
	for _, res := range r.Results {
		fmt.Printf("%-7s %s: %s (%s)\n", res.Status, res.Role, res.Check, res.Detail)
		switch res.Status {
		case verify.Pass:
			pass++
		case verify.Fail:
			failed++
		default:
			unknown++
		}
	}
	fmt.Printf("verify round %d: %d pass, %d fail, %d unknown -> reports/arena/verify-round-%d.json\n", *round, pass, failed, unknown, *round)
	return 0
}

// arenaSynth assembles the checked-clean round reports into reports/arena/synthesis.md for the leader to adjudicate.
func arenaSynth(args []string) int {
	fs := flag.NewFlagSet("arena synth", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	round := fs.Int("round", 1, "round to synthesize")
	force := fs.Bool("force", false, "overwrite a synthesis that already carries adjudication")
	htmlOut := fs.Bool("html", false, "render a review artifact under reports/visual/ instead of building the synthesis")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox arena synth --epic <dir> [--round <n>] [--force] [--html]")
	}
	if *htmlOut {
		out, err := artifact.GenerateArenaSynth(*epicDir, *round, synth.ContentSha)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Println("wrote", out)
		return 0
	}
	out, err := synth.Build(*epicDir, *round, *force)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println("wrote", out)
	return 0
}

// arenaAnswer implements `cox arena answer <claim-id> <yes|no|text> --by <name> --epic <dir>`: the single write path for
// the captain cells (M13). It fills the synthesis row's `captain agrees` cell by id, refusing when synthesis.md changed
// since synth (sha guard). Positionals may come before or after the flags, and a multi-word answer is joined.
func arenaAnswer(args []string) int {
	var epicDir, by string
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--epic" && i+1 < len(args):
			i++
			epicDir = args[i]
		case strings.HasPrefix(a, "--epic="):
			epicDir = strings.TrimPrefix(a, "--epic=")
		case a == "--by" && i+1 < len(args):
			i++
			by = args[i]
		case strings.HasPrefix(a, "--by="):
			by = strings.TrimPrefix(a, "--by=")
		default:
			pos = append(pos, a)
		}
	}
	if epicDir == "" || len(pos) < 2 {
		return usageErr("cox arena answer <claim-id> <yes|no|text> --by <name> --epic <dir>")
	}
	round, err := synth.Answer(epicDir, pos[0], strings.Join(pos[1:], " "), by)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("recorded answer for %s in synthesis round %d\n", pos[0], round)
	return 0
}

// epicArena implements `cox epic arena`: trigger, build the pack, resolve roles, and run each role. Headless is the
// default (captain ruling 2026-09-16): each role runs as a read-only subprocess in the leader checkout and cox writes its
// report from the JSON. --terminal keeps the worktree+terminal path; cox also switches to terminal automatically when the
// leader checkout is dirty or a role template declares needs_worktree. The terminal path must run from the leader (a
// worker cannot dispatch a sub-worker; that surfaces as a depth error reported as "run from the leader").
func epicArena(args []string) int {
	fs := flag.NewFlagSet("epic arena", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	leader := fs.String("leader", "", "leader harness (default from policy harness.leader.default)")
	model := fs.String("model", "", "model id or alias for every role story")
	reason := fs.String("reason", "", "captain reason (forces a full arena and feeds the sensitive scan)")
	lite := fs.Bool("lite", false, "force a single adversary even when the trigger is full")
	round := fs.Int("round", 1, "arena round")
	force := fs.Bool("force", false, "re-run a role even if it already finished this round (else it is skipped)")
	role := fs.String("role", "", "run only this role of the active set (adversary|reviewer|domain)")
	terminal := fs.Bool("terminal", false, "run each role in its own worktree and terminal instead of headless")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox epic arena --epic <dir> [--round <n>] [--role <name>] [--terminal] [--force] [--lite] [--reason <text>] [--leader claude|codex]")
	}
	wsRoot, err := findWorkspaceRoot(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	pol, err := workspace.Resolve(wsRoot, projectDirOf(*epicDir))
	if err != nil {
		return fail("%v", err)
	}
	leaderHarness := nonEmpty(*leader, pol.Harness.Leader.Default)
	if leaderHarness == "" {
		return fail("no leader harness (pass --leader or set policy harness.leader.default)")
	}
	opts := arena.Options{
		EpicDir: *epicDir, WsRoot: wsRoot, Policy: pol, Leader: leaderHarness,
		Model: modelAlias(*model), Lite: *lite, Reason: *reason, Round: *round, Force: *force, Role: *role,
	}

	if useTerminal := arenaTerminalMode(*epicDir, *round, *terminal); useTerminal {
		return arenaTerminalRun(opts, *terminal)
	}
	return arenaHeadlessRun(opts)
}

// arenaTerminalMode decides whether the arena runs in terminal mode: the --terminal flag, a dirty leader checkout (a
// headless role runs in that checkout, so uncommitted changes would leak into its read), or a role template that
// declares needs_worktree. When it auto-switches (not --terminal) it prints why.
func arenaTerminalMode(epicDir string, round int, forceTerminal bool) bool {
	if forceTerminal {
		return true
	}
	if dir, err := arenaLeaderCheckout(epicDir); err == nil && gitDirtyOutside(dir, epicDir) {
		fmt.Println("leader checkout is dirty; switching to terminal mode (headless runs in the leader checkout)")
		return true
	}
	for _, role := range []arenaRoles.Role{arenaRoles.Adversary, arenaRoles.Reviewer, arenaRoles.Domain} {
		if arenaRoles.TemplateNeedsWorktree(role, round) {
			fmt.Printf("role %s declares needs_worktree; switching to terminal mode\n", role)
			return true
		}
	}
	return false
}

// arenaHeadlessRun runs the arena headlessly and records a headless event per role. No backend, no watcher.
func arenaHeadlessRun(opts arena.Options) int {
	res, err := arena.RunHeadless(opts)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("arena %s (headless): %s\n", res.Level, strings.Join(res.Reasons, "; "))
	if res.Level == arenaNone {
		fmt.Println("nothing to review")
		return 0
	}
	fmt.Println("pack:", res.PackPath)
	slug := filepath.Base(opts.EpicDir)
	failed := false
	for _, r := range res.Headless {
		story := "arena-" + string(r.Role)
		if r.Skipped {
			fmt.Printf("skipped %s: already done for round %d (--force to re-run)\n", story, opts.Round)
			continue
		}
		if r.Err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "%s (%s): headless failed: %v\n", story, r.Harness, r.Err)
			continue
		}
		if err := commitHeadless(opts.EpicDir, slug, story, r.Attempt, r.From,
			map[string]any{"arena_role": string(r.Role), "round": opts.Round, "kind": "headless", "sha": r.Sha}); err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "%s (%s): %v\n", story, r.Harness, err)
			continue
		}
		fmt.Printf("ran %s as %s (headless) -> %s\n", story, r.Harness, r.ReportPath)
	}
	if failed {
		return 1
	}
	return 0
}

// arenaTerminalRun runs the arena on read-only worktrees with terminals (the pre-headless path) and records a dispatch
// per role.
func arenaTerminalRun(opts arena.Options, explicit bool) int {
	b, _ := newBackend(opts.EpicDir)
	if b == nil {
		return fail("terminal-mode arena needs a live backend: set ORCA_RUN_ID or %s/.cox/run", opts.EpicDir)
	}
	res, err := arena.Run(b, opts)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("arena %s (terminal): %s\n", res.Level, strings.Join(res.Reasons, "; "))
	if res.Level == arenaNone {
		fmt.Println("nothing to review")
		return 0
	}
	fmt.Println("pack:", res.PackPath)
	failed := false
	for _, r := range res.Roles {
		if r.Skipped {
			fmt.Printf("skipped %s: already done for round %d (--force to re-run)\n", r.Story, opts.Round)
			continue
		}
		if r.Err != nil {
			failed = true
			if isDepthError(r.Err) {
				return fail("cannot dispatch %s: run from the leader (a worker cannot dispatch a sub-worker)", r.Story)
			}
			fmt.Fprintf(os.Stderr, "%s (%s): pending_external: %v\n", r.Story, r.Harness, r.Err)
			continue
		}
		slug := filepath.Base(opts.EpicDir)
		if err := commitDispatch(b, opts.EpicDir, slug, r.Story, r.Attempt, state.Leader, r.Session, r.Worktree.Path,
			map[string]any{"arena_role": string(r.Role), "round": opts.Round}, r.From); err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "%s (%s): %v\n", r.Story, r.Harness, err)
			continue
		}
		fmt.Printf("dispatched %s as %s on %s -> %s\n", r.Story, r.Harness, r.Worktree.Path, r.Session.ID)
	}
	startWatcher(opts.EpicDir, "the arena roles")
	if failed {
		return 1
	}
	return 0
}

// epicDesign implements `cox epic design --sign|--amend`. Signing records design_signed with the DESIGN.md and synthesis
// shas (the synthesis must have every verdict and every captain-agrees cell filled, and no round-2 trigger); --by records
// who signed. An already-signed design is never re-signed; amending records design_amended and requires --reason.
func epicDesign(args []string) int {
	fs := flag.NewFlagSet("epic design", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	sign := fs.Bool("sign", false, "record design_signed")
	amend := fs.Bool("amend", false, "record design_amended (requires --reason)")
	reason := fs.String("reason", "", "why (required for --amend)")
	by := fs.String("by", "", "who signed (recorded in the design_signed evidence)")
	htmlOut := fs.Bool("html", false, "render a review artifact under reports/visual/design.html (no sign)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || (!*sign && !*amend && !*htmlOut) {
		return usageErr("cox epic design --html | --sign [--by <name>] | --amend --reason <why> --epic <dir>")
	}
	if *htmlOut {
		return designHTML(*epicDir)
	}
	if *sign && *amend {
		return fail("pass one of --sign or --amend, not both")
	}
	if *amend {
		if err := epic.Amend(*epicDir, *reason); err != nil {
			return fail("%v", err)
		}
		fmt.Println("design_amended recorded")
		return 0
	}
	if err := epic.Sign(*epicDir, *by); err != nil {
		return fail("%v", err)
	}
	fmt.Println("design_signed recorded")
	return 0
}

// designHTML renders the epic's DESIGN.md, its decisions in force, and the latest arena synthesis into a review
// artifact under reports/visual/design.html plus a sidecar. Decisions come from the first repo's docs/decisions
// (best-effort; skipped when the repo cannot be resolved).
func designHTML(epicDir string) int {
	designMD := filepath.Join(epicDir, "DESIGN.md")
	synthesisMD := filepath.Join(epicDir, "reports", "arena", "synthesis.md")
	if !fileExists(synthesisMD) {
		synthesisMD = ""
	}
	decisionsDir := ""
	if repo, err := arenaLeaderCheckout(epicDir); err == nil {
		if d := filepath.Join(repo, "docs", "decisions"); fileExists(d) {
			decisionsDir = d
		}
	}
	out, err := artifact.GenerateDesign(epicDir, designMD, synthesisMD, decisionsDir)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println("wrote", out)
	return 0
}

// fileExists reports whether p exists.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// arenaNone mirrors roles.None without importing roles in the CLI just for the constant comparison.
const arenaNone = "none"

// arenaLeaderCheckout resolves the leader checkout an arena runs headless in: the epic's first repo. It is where a
// headless role reads code (read-only) and the sha its citations resolve against.
func arenaLeaderCheckout(epicDir string) (string, error) {
	first, err := cite.First(epicDir)
	if err != nil {
		return "", err
	}
	return cite.RepoDir(epicDir, first.Alias)
}

// gitDirty reports whether a checkout has uncommitted changes (git status --porcelain is non-empty). A git failure is
// treated as clean so a non-repo path does not force terminal mode.
func gitDirty(dir string) bool { return gitDirtyOutside(dir, "") }

// gitDirtyOutside is gitDirty that ignores changes under ignoreDir (absolute). The arena regenerates the epic's own
// files (context pack, role stories, reports) in the same command that decides the mode, so when the epic dir lives
// inside the reviewed repo those files must not count as leader-checkout changes (first live v3 run, 2026-09-16).
func gitDirtyOutside(dir, ignoreDir string) bool {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "-z", "-uall").Output()
	if err != nil {
		return false
	}
	absDir, _ := filepath.Abs(dir)
	rel := ""
	if ignoreDir != "" {
		if r, err := filepath.Rel(absDir, ignoreDir); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r) + "/"
		}
	}
	for _, ent := range strings.Split(string(out), "\x00") {
		if len(ent) < 4 {
			continue
		}
		path := ent[3:]
		if rel != "" && strings.HasPrefix(path, rel) {
			continue
		}
		return true
	}
	return false
}

// commitHeadless records a headless arena role event: submitted/prev -> working, with the arena role, round, kind, and
// the leader-checkout sha as evidence. The role ran locally and produced its report, so the event is externally
// confirmed (there is no separate session to reconcile).
func commitHeadless(epicDir, slug, story string, attempt int, from state.State, evidence map[string]any) error {
	if from == "" {
		from = state.Submitted
	}
	return state.Append(epicDir, state.Event{
		Epic: slug, Story: story, Attempt: attempt, Actor: state.Leader,
		From: from, To: state.Working, Evidence: evidence, ExternalConfirmed: true,
	})
}

// isDepthError reports whether a dispatch failure is the Orca "sub-worker dispatch is not permitted" depth ceiling.
func isDepthError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "depth") && strings.Contains(msg, "sub-worker")
}

// projectDirOf returns <ws>/<project> for an epic dir <ws>/<project>/epics/<slug>, for the policy overlay.
func projectDirOf(epicDir string) string {
	abs, err := filepath.Abs(epicDir)
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(abs))
}
