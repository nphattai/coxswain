package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/arena/cite"
	"github.com/nphattai/coxswain/internal/protocol/brief"
	"github.com/nphattai/coxswain/internal/protocol/control"
	"github.com/nphattai/coxswain/internal/routing"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
	"github.com/nphattai/coxswain/internal/worktree"
)

// cmdStory implements `cox story dispatch|park|resume`.
func cmdStory(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cox story dispatch|done|fail|cancel|park|resume <id> --epic <dir>")
		return 2
	}
	switch args[0] {
	case "dispatch":
		return storyDispatch(args[1:])
	case "report":
		return cmdStoryReport(args[1:])
	case "done":
		return storyDone(args[1:])
	case "fail":
		return storyTerminate(state.Failed, args[1:])
	case "cancel":
		return storyTerminate(state.Canceled, args[1:])
	case "park":
		return storyControl("park", args[1:])
	case "resume":
		return storyControl("resume", args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cox story: unknown subcommand %q\n", args[0])
		return 2
	}
}

func storyDispatch(args []string) int {
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("story dispatch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	harnessFlag := fs.String("harness", "", "harness (claude|codex); default from the story frontmatter")
	model := fs.String("model", "", "model id or alias (opus -> claude-opus-4-8)")
	forceModel := fs.Bool("force-model", false, "allow a model whose vendor does not match the harness")
	forceQuota := fs.Bool("force-quota", false, "dispatch even when the chosen harness reads exhausted_now")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" {
		return usageErr("cox story dispatch <id> --epic <dir> [--harness claude|codex --model <id>] [--force-quota]")
	}
	meta := readStoryMeta(*epicDir, story)
	// A story with `harness: auto` (and no --harness override) is routed: Decide picks the harness/model from policy,
	// capability cards, quota, and the baseline table, and the Choice is recorded as evidence.route on the working
	// event so the decision is auditable. An explicit --harness or a fixed frontmatter harness skips routing.
	var routeChoice *routing.Choice
	if *harnessFlag == "" && meta.Harness == "auto" {
		ch, err := routeStory(*epicDir, story)
		if err != nil {
			return fail("route %s: %v", story, err)
		}
		routeChoice = &ch
		fmt.Printf("routed %s -> harness=%s (%s)\n", story, ch.Harness, strings.Join(ch.Reasons, "; "))
	}
	harnessName := nonEmpty(*harnessFlag, routedHarness(routeChoice, nonEmpty(meta.Harness, "claude")))
	// Enforce the capability card before doing any work: a harness with no adapter (e.g. omp, opencode) is refused here,
	// and a pull-wake or manual-checkpoint harness prints a reduced-mode notice and still dispatches (phase-07 item 4).
	notices, err := registry.Notices(harnessName, harness.RoleWorker)
	if err != nil {
		return fail("%v", err)
	}
	for _, n := range notices {
		fmt.Println(n)
	}
	pol := loadPolicyQuiet(*epicDir)
	explicitModel := nonEmpty(*model, nonEmpty(routedModel(routeChoice), meta.Model))
	// Guard a cross-harness model before spawning: an explicit claude model at a codex worker (or a codex model at a
	// claude worker) is refused, since the harness will reject it at launch. --force-model overrides (M10c).
	if !*forceModel {
		if bad, msg := modelHarnessMismatch(harnessName, explicitModel); bad {
			return fail("%s; pass --force-model to override", msg)
		}
	}
	// The model resolves per harness: explicit/routed/frontmatter, else the policy default for this harness, else
	// claude-opus-4-8 for claude only. A harness with no default resolves to "" and LaunchLine omits --model.
	modelID := resolveWorkerModel(pol, harnessName, explicitModel)
	// Quota gate (observe-only, ADR 0011): it never changes the harness, it only refuses to send work into an
	// exhausted_now harness (overridable with --force-quota) and warns on a low-but-not-exhausted one. The reading is
	// printed so the decision is auditable.
	if code, blocked := quotaDispatchGate(*epicDir, harnessName, modelID, *forceQuota); blocked {
		return code
	}
	slug := filepath.Base(*epicDir)
	repo := repoName(*epicDir, meta.Repo)
	warnParallelSameRepo(*epicDir, story, meta.Repo)

	run, err := ensureRun(*epicDir, slug)
	if err != nil {
		return fail("%v", err)
	}
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("no backend after ensureRun (run=%q)", run)
	}

	wt, err := worktree.Ensure(b, repo, "story/"+story, "epic/"+slug)
	if err != nil {
		return fail("%v", err)
	}
	if _, err := brief.Build(*epicDir, story); err != nil {
		return fail("build brief: %v", err)
	}
	storyPath := filepath.Join(*epicDir, "stories", story+".md")
	sess, err := b.Spawn(wt, backend.HarnessSpec{Name: harnessName, Model: modelID, LaunchFlags: pol.LaunchFlags(harnessName)}, backend.Brief{StoryPath: storyPath})
	if err != nil {
		return fail("spawn: %v", err)
	}

	attempt := currentAttempt(*epicDir, story)
	if err := commitDispatch(b, *epicDir, slug, story, attempt, state.Leader, sess, wt.Path, routeEvidence(routeChoice), state.Submitted); err != nil {
		return fail("%v", err)
	}
	if h := os.Getenv("ORCA_TERMINAL_HANDLE"); h != "" {
		_ = writeCoxFile(*epicDir, "leader", h)
	}
	startWatcher(*epicDir, story)
	fmt.Printf("dispatched %s (attempt %d) as %s on %s -> %s\n", story, attempt, harnessName, wt.Path, sess.ID)
	return 0
}

// routedHarness returns the routed harness when a story was routed, else the fallback (fixed frontmatter or claude).
func routedHarness(ch *routing.Choice, fallback string) string {
	if ch != nil && ch.Harness != "" {
		return ch.Harness
	}
	return fallback
}

// routedModel returns the routed model, or "" when the story was not routed (the caller then uses the frontmatter model).
func routedModel(ch *routing.Choice) string {
	if ch != nil {
		return ch.Model
	}
	return ""
}

// routeEvidence records the routing Choice under evidence.route on the working event, or nil when the story was not
// routed (a fixed-harness dispatch carries no route evidence).
func routeEvidence(ch *routing.Choice) map[string]any {
	if ch == nil {
		return nil
	}
	return map[string]any{"route": ch}
}

// warnParallelSameRepo prints a warning when another story in the same repo alias is already working and the policy has
// not enabled parallel-same-repo. Decision 0003 permits two workers in one repo only when their files_owned are disjoint,
// but no scheduler enforces that yet; this warning is the interim guard. It never blocks a dispatch.
func warnParallelSameRepo(epicDir, story, repoAlias string) {
	if repoAlias == "" {
		return
	}
	pol := loadPolicyQuiet(epicDir)
	if pol == nil || pol.WorkersPerRepo.AllowParallelWhen.Enforced {
		return
	}
	if others := sameRepoWorking(epicDir, story, repoAlias); len(others) > 0 {
		fmt.Fprintf(os.Stderr, "cox: warning: %s shares repo %q with working story %s; parallel-same-repo is not enforced (files_owned overlap is unchecked, workers_per_repo=%d)\n",
			story, repoAlias, strings.Join(others, ", "), pol.WorkersPerRepo.Value)
	}
}

// sameRepoWorking returns the ids of stories other than `story` that are currently working and declare the same repo
// alias. It reads each candidate story's frontmatter repo, so an alias that maps to the same repo counts.
func sameRepoWorking(epicDir, story, repoAlias string) []string {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return nil
	}
	var others []string
	for _, s := range state.Fold(events).SortedStories() {
		if s.ID == story || s.State != state.Working {
			continue
		}
		if readStoryMeta(epicDir, s.ID).Repo == repoAlias {
			others = append(others, s.ID)
		}
	}
	return others
}

// loadPolicyQuiet resolves the workspace+project policy for an epic dir, or nil when it cannot be loaded (a dispatch is
// never blocked by a policy read).
func loadPolicyQuiet(epicDir string) *workspace.Policy {
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

// storyDone records a story's completion so the leader never hand-edits events.jsonl. It appends a
// working|input_required -> completed event (with --merge <sha> as evidence when given), releases the backend session
// (best-effort Stop plus removing the session record so the watcher stops monitoring it), and, with --close-worktree,
// detaches and removes the story worktree (WorktreeRemove keeps the branch, F01).
func storyDone(args []string) int {
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("story done", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	merge := fs.String("merge", "", "merge commit sha to record as evidence")
	closeWt := fs.Bool("close-worktree", false, "detach and remove the story worktree (branch kept)")
	force := fs.Bool("force", false, "complete even when the worker composer is busy (worker still running)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" {
		return usageErr("cox story done <id> --epic <dir> [--merge <sha>] [--close-worktree] [--force]")
	}
	events, _, err := state.Load(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	snap := state.Fold(events).Stories[story]
	if snap == nil {
		return fail("no state for story %s (nothing to complete)", story)
	}
	if snap.State != state.Working && snap.State != state.InputRequired {
		return fail("cannot complete %s from %q (only working or input_required)", story, snap.State)
	}

	b, _ := newBackend(*epicDir)
	// Refuse to complete a worker that is mid-turn: a busy composer means it is still running, and marking it done would
	// stop it under itself. Only an observed "busy" blocks (F08: empty/pending/unknown never do); --force overrides.
	if composerBlocksDone(probeComposer(b, *epicDir, snap), *force) {
		return fail("%s worker composer is busy (still running); pass --force to complete anyway", story)
	}

	evidence := map[string]any{}
	if *merge != "" {
		evidence["merge"] = *merge
	}
	if err := releaseStory(*epicDir, story, snap, state.Completed, evidence, b, *closeWt); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("done: %s completed (attempt %d)\n", story, snap.Attempt)
	return 0
}

// storyTerminate records a terminal, non-success transition (failed or canceled) for a story and releases it exactly
// like `story done`. It accepts working|input_required|parked, requires --reason (recorded as evidence.reason), refuses
// a busy worker unless --force, and with --close-worktree detaches and removes the worktree (WorktreeRemove keeps the
// branch, F01). It never deletes a git branch.
func storyTerminate(to state.State, args []string) int {
	verb := "fail"
	if to == state.Canceled {
		verb = "cancel"
	}
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("story "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	reason := fs.String("reason", "", "why (recorded as evidence.reason; required)")
	closeWt := fs.Bool("close-worktree", false, "detach and remove the story worktree (branch kept)")
	force := fs.Bool("force", false, "proceed even when the worker composer is busy (worker still running)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" {
		return usageErr("cox story " + verb + " <id> --reason \"<why>\" --epic <dir> [--close-worktree] [--force]")
	}
	if strings.TrimSpace(*reason) == "" {
		return fail("cox story %s needs --reason \"<why>\" (evidence.reason is required)", verb)
	}
	events, _, err := state.Load(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	snap := state.Fold(events).Stories[story]
	if snap == nil {
		return fail("no state for story %s (nothing to %s)", story, verb)
	}
	if snap.State != state.Working && snap.State != state.InputRequired && snap.State != state.Parked {
		return fail("cannot %s %s from %q (only working, input_required, or parked)", verb, story, snap.State)
	}
	b, _ := newBackend(*epicDir)
	// A parked story has no live turn, so probeComposer returns unknown and never blocks; only an observed busy
	// working/input_required worker blocks, and --force overrides it (same guard as story done).
	if composerBlocksDone(probeComposer(b, *epicDir, snap), *force) {
		return fail("%s worker composer is busy (still running); pass --force to %s anyway", story, verb)
	}
	if err := releaseStory(*epicDir, story, snap, to, map[string]any{"reason": *reason}, b, *closeWt); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%s: %s %s (attempt %d)\n", verb, story, to, snap.Attempt)
	return 0
}

// releaseStory is the shared tail of story done|fail|cancel: it appends the terminal transition (external_confirmed),
// then releases the backend session (best-effort Stop plus removing the session record so the watcher stops monitoring
// it) and, with closeWt, detaches and removes the worktree - WorktreeRemove keeps the git branch (F01), so a completed,
// failed, or canceled story never loses its branch. The caller has already validated the from-state and the
// busy-composer guard.
func releaseStory(epicDir, story string, snap *state.StorySnap, to state.State, evidence map[string]any, b backend.Backend, closeWt bool) error {
	if err := state.Append(epicDir, state.Event{
		Epic: filepath.Base(epicDir), Story: story, Attempt: snap.Attempt, Actor: state.Leader,
		From: snap.State, To: to, Evidence: evidence, ExternalConfirmed: true,
	}); err != nil {
		return fmt.Errorf("append %s event: %w", to, err)
	}
	if sess, err := loadSession(epicDir, story); err == nil {
		if b != nil {
			if confirmed, err := b.Stop(sess); !confirmed {
				fmt.Fprintf(os.Stderr, "cox: warning: stop for %s not confirmed (%v); session record removed anyway (story is %s)\n", story, err, to)
			}
		}
		if err := os.Remove(sessionPath(epicDir, story)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("release session: %w", err)
		}
	}
	if closeWt {
		if wtPath := readWorktree(epicDir, story); wtPath != "" {
			if b == nil {
				return fmt.Errorf("--close-worktree needs a live backend: set ORCA_RUN_ID or %s/.cox/run", epicDir)
			}
			if err := b.WorktreeRemove(backend.Worktree{Path: wtPath}); err != nil {
				return fmt.Errorf("close worktree: %w", err)
			}
			if err := os.Remove(wtFilePath(epicDir, story)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("clear worktree record: %w", err)
			}
		}
	}
	return nil
}

// composerBlocksDone reports whether `cox story done` must refuse: a worker whose composer is observed busy is still
// mid-turn, so completing it would stop it under itself. Only "busy" blocks - empty, pending, and unknown never do
// (F08: an unreadable composer is never treated as running) - and --force overrides even a busy worker.
func composerBlocksDone(composer string, force bool) bool {
	return !force && composer == backend.ComposerBusy
}

// storyControl routes `cox story park|resume` through the control channel (park -> Park, resume -> Relaunch).
func storyControl(verb string, args []string) int {
	story, rest := onePositional(args)
	fs := flag.NewFlagSet("story "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	note := fs.String("note", "resume", "progress note (resume)")
	// resume only: reroute the next attempt onto a different harness (M11 leader reroute). The checkpoint is
	// harness-neutral, so nothing else changes; the model must fit the new harness (M10c).
	harnessFlag := fs.String("harness", "", "resume on a different harness (reroute); default keeps the current harness")
	modelFlag := fs.String("model", "", "model for the resumed harness (with --harness)")
	forceModel := fs.Bool("force-model", false, "allow a model whose vendor does not match the harness")
	// park only: how long to wait for the worker's checkpoint before refusing to park blind. Defaults from COX_PARK_WAIT
	// (0 => the controller's own default), so an E2E can shorten it and surface a checkpoint mismatch fast.
	parkWait := fs.Duration("park-wait", envDuration("COX_PARK_WAIT", 0), "max wait for a matching checkpoint (park)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" {
		return usageErr("cox story " + verb + " <id> --epic <dir>")
	}
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("story %s needs a live backend: set ORCA_RUN_ID or %s/.cox/run", verb, *epicDir)
	}
	meta := readStoryMeta(*epicDir, story)
	ctl := &control.Controller{EpicDir: *epicDir, Backend: b, Harness: harnessFor(meta.Harness), ParkWait: *parkWait}
	switch verb {
	case "park":
		sess, err := loadSession(*epicDir, story)
		if err != nil {
			return fail("no session for %s: %v", story, err)
		}
		if err := ctl.Park(story, readWorktree(*epicDir, story), sess); err != nil {
			return fail("%v", err)
		}
	case "resume":
		curHarness := currentHarness(*epicDir, story)
		rerouting := *harnessFlag != "" && *harnessFlag != curHarness
		targetHarness := nonEmpty(*harnessFlag, curHarness)
		// Resolve the model: a reroute takes the new harness's policy default (the old model belongs to the old harness,
		// M10c); a same-harness resume keeps the frontmatter/explicit model as before.
		var targetModel string
		if rerouting {
			targetModel = resolveWorkerModel(loadPolicyQuiet(*epicDir), targetHarness, *modelFlag)
		} else {
			targetModel = modelAlias(nonEmpty(*modelFlag, meta.Model))
		}
		if !*forceModel {
			if bad, msg := modelHarnessMismatch(targetHarness, targetModel); bad {
				return fail("%s; pass --force-model to override", msg)
			}
		}
		var extra map[string]any
		if rerouting {
			extra = map[string]any{"reroute": map[string]any{"from": curHarness, "to": targetHarness, "reason": *note}}
		}
		spec := backend.HarnessSpec{Name: targetHarness, Model: targetModel}
		prior, _ := loadSession(*epicDir, story) // previous attempt's terminal, closed before the new spawn (zero value when none)
		sess, err := ctl.Relaunch(story, readWorktree(*epicDir, story), *note, prior, spec, extra)
		if err != nil {
			return fail("%v", err)
		}
		if err := saveSession(*epicDir, story, sess); err != nil {
			return fail("save session: %v", err)
		}
		if rerouting {
			fmt.Printf("rerouted %s: %s -> %s (model=%s)\n", story, curHarness, targetHarness, orNone(targetModel))
		}
	}
	fmt.Printf("%s: %s ok\n", verb, story)
	return 0
}

// ensureRun reads .cox/run or creates a run via the orca CLI and records it. Returns the run id.
func ensureRun(epicDir, slug string) (string, error) {
	if r := resolveRun(epicDir); r != "" {
		return r, nil
	}
	out, err := exec.Command("orca", "orchestration", "run-create", "--objective", "epic "+slug, "--json").Output()
	if err != nil {
		return "", fmt.Errorf("orca run-create: %w", err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Run struct {
				ID string `json:"id"`
			} `json:"run"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil || env.Result.Run.ID == "" {
		return "", fmt.Errorf("orca run-create: could not read run id from %s", strings.TrimSpace(string(out)))
	}
	if err := writeCoxFile(epicDir, "run", env.Result.Run.ID); err != nil {
		return "", err
	}
	return env.Result.Run.ID, nil
}

// startWatcher launches `cox watch --epic <dir>` in the background once. It skips the launch when the watcher's own
// pidfile (owned by `cox watch`, not written here) names a live process, so a second dispatch does not start a
// duplicate. The spawned `cox watch` claims and later removes the pidfile itself. When a live watcher is reused it
// prints one line naming the story, so a dispatch into a running epic is not silent about who will pick the story up:
// the watcher now reloads its session set every tick (item 1), so the reused watcher covers this new story.
func startWatcher(epicDir, story string) {
	if pid := readPid(watchPidPath(epicDir)); pid > 0 && processAlive(pid) {
		fmt.Println(watcherReuseLine(pid, story))
		return
	}
	self, err := os.Executable()
	if err != nil {
		self = "cox"
	}
	cmd := exec.Command(self, "watch", "--epic", epicDir)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "cox: could not start watcher:", err)
		return
	}
	// Do not Wait: the watcher outlives this command.
}

// watcherReuseLine is the one line startWatcher prints when it reuses a live watcher instead of spawning one: the
// watcher's pid and the story it will cover on its next reload (item 1). Pure, so a test asserts the text.
func watcherReuseLine(pid int, story string) string {
	return fmt.Sprintf("watcher pid %d alive; it picks up %s on its next tick", pid, story)
}

// repoName maps a story's repo alias to its concrete ref (path or backend name) via the epic's `repos` file, through the
// one shared reader. An unmapped alias is used as-is.
func repoName(epicDir, alias string) string {
	if alias == "" {
		return ""
	}
	repos, err := cite.Repos(epicDir)
	if err != nil {
		return alias
	}
	for _, r := range repos {
		if r.Alias == alias {
			return r.Ref
		}
	}
	return alias
}
