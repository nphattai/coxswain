// Package arena orchestrates one arena round: trigger from policy, build the blinded context pack, resolve each role's
// harness, render a story per role, and dispatch each on its own read-only worktree. It is the leader-side glue over the
// pack/roles/check/synth subpackages and depends only on the backend interface (decision 0002), so the integration test
// drives it with the fake backend. cmd/cox wires the concrete Orca backend and persists the returned sessions.
package arena

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/arena/cite"
	"github.com/nphattai/coxswain/internal/arena/pack"
	"github.com/nphattai/coxswain/internal/arena/roles"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
	"github.com/nphattai/coxswain/internal/worktree"
)

// Options are the inputs to Run. Leader is the leader's harness (adversary resolves to not-leader). Lite forces a single
// adversary; Reason is the captain's --reason (a full-arena trigger and a signal fed to the sensitive scan).
type Options struct {
	EpicDir string
	WsRoot  string
	Policy  *workspace.Policy
	Leader  string
	Model   string // optional model for every role story; "" lets the adapter default
	Lite    bool
	Reason  string
	Round   int
	Force   bool   // re-render a role story that exists for a different round, and re-run a role already done this round (A10)
	Role    string // when set, run only this role of the active set (a targeted re-run); empty runs all active roles
}

// RoleResult is the outcome of dispatching one role. On a worktree or spawn failure Err is set and a pending_external
// event was appended (the pack is already written, so nothing is lost); Session/Worktree are zero in that case. On
// success Session/Worktree are set and Err is nil; the caller persists the working event, session, and worktree as one
// compensating transaction (a spawned role is live and must not be recorded before it is monitored).
type RoleResult struct {
	Role     roles.Role
	Story    string // arena-<role>
	Harness  string
	Worktree backend.Worktree
	Session  backend.Session
	Attempt  int         // dispatch attempt to record (bumped when relaunching a role left pending_external)
	From     state.State // the state this dispatch transitions from (submitted for a fresh role, its current state on relaunch)
	Skipped  bool        // this role already finished the round (report present or completed); left alone unless --force
	Err      error
}

// HeadlessResult is the outcome of running one role headlessly (captain ruling 2026-09-16): the role ran as a subprocess
// in the leader checkout, its report was written to ReportPath, and the caller records a headless event. On a run or
// parse failure Err is set and ReportPath is empty.
type HeadlessResult struct {
	Role       roles.Role
	Harness    string
	ReportPath string
	Sha        string // the leader-checkout HEAD the role ran at (citations resolve against it)
	Attempt    int
	From       state.State
	Skipped    bool // a report for this round already exists (or the role completed it); left alone unless --force
	Err        error
}

// Result is what Run/RunHeadless report back for the caller to print and persist. Roles is set by the terminal path,
// Headless by the headless path.
type Result struct {
	Level    roles.Level
	Reasons  []string
	PackPath string
	Roles    []RoleResult
	Headless []HeadlessResult
}

// prepData is the shared arena preparation both the terminal dispatch and the headless run consume: the trigger outcome,
// the built pack, and the resolved active roles.
type prepData struct {
	slug     string
	alias    string // first repo alias (role worktree repo / leader checkout)
	repoName string // first repo ref (worktree branch base)
	packPath string
	active   []roles.Role
	resolved map[roles.Role]string
}

// Run triggers the arena, builds the pack, and dispatches the active roles. It returns a Result even when some roles fail
// to dispatch (each such role carries its Err); it returns a hard error only when the trigger, pack build, or role
// resolution fails - i.e. before any role could be dispatched. A None trigger returns a Result with no roles and no error.
func Run(b backend.Backend, o Options) (Result, error) {
	p, res, err := prepare(o)
	if err != nil || p == nil {
		return res, err
	}
	alias, repoName, slug := p.alias, p.repoName, p.slug
	packPath, resolved, active := p.packPath, p.resolved, p.active
	for _, role := range active {
		hname := resolved[role]
		rr := RoleResult{Role: role, Story: "arena-" + string(role), Harness: hname}
		// A role that already finished this round (its report is present, or its story completed the round) is skipped so a
		// full re-run fills only the missing roles instead of re-dispatching or failing a done one (A10). --force overrides.
		if !o.Force && roleDone(o.EpicDir, role, o.Round) {
			rr.Skipped = true
			res.Roles = append(res.Roles, rr)
			continue
		}
		// Decide the attempt and From-state up front from the role's current state, so a role left pending_external by a
		// failed spawn last time relaunches as a new attempt instead of appending a second submitted->working (A7).
		rr.Attempt, rr.From = dispatchTransition(foldStory(o.EpicDir, rr.Story), o.Round)
		storyPath, err := roles.Render(o.EpicDir, roles.StoryData{
			Role: role, Repo: alias, Harness: hname, Model: o.Model,
			Slug: slug, EpicDir: o.EpicDir, PackPath: packPath, Round: o.Round,
		}, o.Force)
		if err != nil {
			rr.Err = err
			res.Roles = append(res.Roles, rr)
			continue
		}
		// Per-round role branch (arena/<role>-r<N>): a round reuses neither the branch nor the worktree of a prior round,
		// so an unreleased round-1 worktree never blocks round 2 with a switch onto a stale branch (A9). Release each
		// round's worktrees with `cox arena close --round N` once its reports are collected.
		wt, err := worktree.Ensure(b, repoName, fmt.Sprintf("arena/%s-r%d", role, o.Round), "epic/"+slug)
		if err != nil {
			rr.Err = err
			pending(o.EpicDir, slug, rr.Story, rr.Attempt, rr.From, err)
			res.Roles = append(res.Roles, rr)
			continue
		}
		arenaModel, _ := o.Policy.WorkerModel(hname, o.Model)
		if err := registry.PrepareWorktree(hname, wt.Path); err != nil {
			rr.Err = err
			pending(o.EpicDir, slug, rr.Story, rr.Attempt, rr.From, err)
			res.Roles = append(res.Roles, rr)
			continue
		}
		// An arena role launches with the read-only arena flags (harness.launch.arena.<h>), never the worker's bypass
		// flags: in terminal mode the role runs plan/read-only so it can only write its report (ADR 0013). Argv is
		// adapter-owned and marked Arena so a sandboxed harness grants it no extra writable roots (ADR 0013).
		argv, err := registry.LaunchArgs(hname, harness.Launch{
			Role: harness.RoleWorker, Worktree: wt.Path, Model: arenaModel, Arena: true,
			Flags: o.Policy.ArenaLaunchFlags(hname), Brief: harness.Brief{StoryPath: storyPath},
		})
		if err != nil {
			rr.Err = err
			pending(o.EpicDir, slug, rr.Story, rr.Attempt, rr.From, err)
			res.Roles = append(res.Roles, rr)
			continue
		}
		sess, err := b.Spawn(wt, backend.HarnessSpec{Name: hname, Model: arenaModel, LaunchFlags: o.Policy.ArenaLaunchFlags(hname), Argv: argv}, backend.Brief{StoryPath: storyPath, Arena: true})
		if err != nil {
			rr.Err = err
			pending(o.EpicDir, slug, rr.Story, rr.Attempt, rr.From, err)
			res.Roles = append(res.Roles, rr)
			continue
		}
		rr.Worktree, rr.Session = wt, sess
		res.Roles = append(res.Roles, rr)
	}
	return res, nil
}

// prepare runs the shared arena setup: the round guard, the trigger, the pack build, and role resolution. It returns the
// prepData and a Result carrying Level/Reasons/PackPath. When the trigger is None it returns (nil, res, nil) so the
// caller reports "nothing to review" and stops. A hard error (round > 3, unreadable design, pack build) returns
// (nil, Result{}, err).
func prepare(o Options) (*prepData, Result, error) {
	if o.Round == 0 {
		o.Round = 1
	}
	if o.Round > 3 {
		return nil, Result{}, fmt.Errorf("max 3 rounds (ADR 0013)")
	}
	design, err := os.ReadFile(filepath.Join(o.EpicDir, "DESIGN.md"))
	if err != nil {
		return nil, Result{}, fmt.Errorf("read DESIGN.md: %w", err)
	}
	first, err := cite.First(o.EpicDir)
	if err != nil {
		return nil, Result{}, err
	}

	sig := roles.Signals{Design: string(design), Repos: repoCount(o.EpicDir), Reason: o.Reason}
	level, reasons := roles.Trigger(sig, o.Policy)
	if o.Lite && level == roles.Full {
		level, reasons = roles.Lite, append([]string{"captain forced --lite"}, reasons...)
	}
	res := Result{Level: level, Reasons: reasons}
	if level == roles.None {
		return nil, res, nil
	}

	packPath, err := pack.Build(o.EpicDir, o.WsRoot, o.Round)
	if err != nil {
		return nil, Result{}, err
	}
	res.PackPath = packPath

	resolved, err := roles.Resolve(o.Policy, o.Leader)
	if err != nil {
		return nil, Result{}, err
	}
	active := roles.Active(level, reasons)
	// --role runs a single role of the active set (a targeted re-run). An unknown or inactive role is refused up front,
	// naming the active roles, so a typo does not silently run nothing.
	if o.Role != "" {
		one := roles.Role(o.Role)
		if !containsRole(active, one) {
			return nil, Result{}, fmt.Errorf("role %q is not active for this arena (active: %s)", o.Role, joinRoles(active))
		}
		active = []roles.Role{one}
	}
	// Enforce the capability card for every role's harness before running any of them: an unadaptered harness (omp,
	// opencode) is refused up front with a clear message, and a reduced-mode harness prints its notice once.
	seen := map[string]bool{}
	for _, role := range active {
		hn := resolved[role]
		// Arena roles need no --allow-unsandboxed: an unsandboxed harness used in arena is authorized only by a standing
		// card ack (claude); a harness that lacks it (pi) is refused here, matching arena's explicit no-substitution rule.
		notices, _, err := registry.Notices(hn, harness.RoleWorker, false)
		if err != nil {
			return nil, Result{}, fmt.Errorf("arena role %s: %w", role, err)
		}
		if !seen[hn] {
			seen[hn] = true
			for _, n := range notices {
				fmt.Printf("%s: %s\n", hn, n)
			}
		}
	}
	return &prepData{
		slug: filepath.Base(o.EpicDir), alias: first.Alias, repoName: first.Ref,
		packPath: packPath, active: active, resolved: resolved,
	}, res, nil
}

// RunHeadless is the default arena path (captain ruling 2026-09-16): each active role runs as a headless subprocess in
// the leader checkout (the first repo, read-only via plan / read-only sandbox), cox parses the harness JSON, extracts the
// report from a fenced ```report block, and writes reports/arena/round-N-<role>.md. No worktree, no terminal, no watcher.
// The caller records a headless event per result. RunHeadless needs no backend.
func RunHeadless(o Options) (Result, error) {
	p, res, err := prepare(o)
	if err != nil || p == nil {
		return res, err
	}
	leaderCwd, err := cite.RepoDir(o.EpicDir, p.alias)
	if err != nil {
		return Result{}, fmt.Errorf("leader checkout for %q: %w", p.alias, err)
	}
	sha := gitHead(leaderCwd)
	packContent, err := os.ReadFile(p.packPath)
	if err != nil {
		return Result{}, err
	}
	for _, role := range p.active {
		hname := p.resolved[role]
		hr := HeadlessResult{Role: role, Harness: hname, Sha: sha}
		// Skip a role whose report for this round is already written (or that completed it), so a re-run fills only the
		// missing roles rather than overwriting a good report (A10). --force re-runs it.
		if !o.Force && roleDone(o.EpicDir, role, o.Round) {
			hr.Skipped = true
			res.Headless = append(res.Headless, hr)
			continue
		}
		hr.Attempt, hr.From = dispatchTransition(foldStory(o.EpicDir, "arena-"+string(role)), o.Round)
		roleTmpl, err := roles.Prompt(roles.StoryData{
			Role: role, Repo: p.alias, Harness: hname, Model: o.Model,
			Slug: p.slug, EpicDir: o.EpicDir, PackPath: p.packPath, Round: o.Round,
		})
		if err != nil {
			hr.Err = err
			res.Headless = append(res.Headless, hr)
			continue
		}
		prompt := string(packContent) + "\n\n---\n\n" + roleTmpl + "\n\n" + headlessDirective()
		model, _ := o.Policy.WorkerModel(hname, o.Model)
		out, runErr := runHeadless(hname, model, leaderCwd, prompt)
		reportText, exErr := extractReport(out)
		if exErr != nil {
			if runErr != nil {
				hr.Err = fmt.Errorf("%s headless run failed and produced no report block: %w", hname, runErr)
			} else {
				hr.Err = exErr
			}
			res.Headless = append(res.Headless, hr)
			continue
		}
		reportPath := filepath.Join(o.EpicDir, "reports", "arena", fmt.Sprintf("round-%d-%s.md", o.Round, role))
		if err := os.WriteFile(reportPath, []byte(strings.TrimSpace(reportText)+"\n"), 0o644); err != nil {
			hr.Err = err
			res.Headless = append(res.Headless, hr)
			continue
		}
		hr.ReportPath = reportPath
		res.Headless = append(res.Headless, hr)
	}
	return res, nil
}

// headlessArgv builds the harness command for a headless role (verified against the live CLIs 2026-09-16): claude runs
// `-p --model <m> --permission-mode plan --output-format json --no-session-persistence` (prompt on stdin); codex runs
// `exec --json -s read-only [-m <model>] -` (prompt on stdin). ok is false for an unadaptered harness.
func headlessArgv(hname, model string) (argv []string, ok bool) {
	switch hname {
	case "claude":
		argv = []string{"claude", "-p", "--permission-mode", "plan", "--output-format", "json", "--no-session-persistence"}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		return argv, true
	case "codex":
		argv = []string{"codex", "exec", "--json", "-s", "read-only"}
		if model != "" {
			argv = append(argv, "-m", model)
		}
		return append(argv, "-"), true
	default:
		return nil, false
	}
}

// runHeadless runs the harness argv in the leader checkout with the prompt on stdin and returns its stdout. A non-zero
// exit is returned as err (the caller still tries to extract a report from stdout, since a harness may exit non-zero yet
// have emitted the block).
func runHeadless(hname, model, cwd, prompt string) ([]byte, error) {
	argv, ok := headlessArgv(hname, model)
	if !ok {
		return nil, fmt.Errorf("headless mode not supported for harness %q", hname)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(prompt)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return stdout.Bytes(), err
}

// headlessDirective tells a headless role to emit its report inside a fenced ```report block instead of writing a file
// (cox writes the file from the block). Appended to the prompt after the role template.
func headlessDirective() string {
	return "## Headless output\n\n" +
		"You are running headless: you cannot write files. Output your entire coxswain.arena.v3 report - the " +
		"frontmatter block and the claim table exactly as specified above - inside a single fenced block that opens " +
		"with ```report and closes with ```. The report block is the only output that is read; write nothing to disk."
}

// gitHead returns the HEAD sha of a checkout, or "" when git fails (the pack still stamps its own; a blank here only
// weakens the recorded event evidence).
func gitHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dispatchTransition decides the attempt number and From-state for (re)dispatching a role from its current folded
// snapshot. A role with no prior events is a fresh dispatch at the round number, from submitted. A role that already has
// events - typically pending_external left by a failed spawn, or a completed prior round - is redispatched as a new
// attempt (prev.Attempt+1) from its current state, so cox epic arena relaunches a stuck role rather than appending a
// second submitted->working at the same attempt (A7).
func dispatchTransition(prev *state.StorySnap, round int) (attempt int, from state.State) {
	if prev == nil {
		return round, state.Submitted
	}
	return prev.Attempt + 1, prev.State
}

// roleDone reports whether a role has already finished the given round: its report file is present, or its story folded
// to completed at that round's attempt (a headless role never reaches completed, so the report file is the signal that
// covers it; a terminal role that finished shows completed). A skip check, not a gate: --force ignores it.
func roleDone(epicDir string, role roles.Role, round int) bool {
	reportPath := filepath.Join(epicDir, "reports", "arena", fmt.Sprintf("round-%d-%s.md", round, role))
	if _, err := os.Stat(reportPath); err == nil {
		return true
	}
	if snap := foldStory(epicDir, "arena-"+string(role)); snap != nil && snap.State == state.Completed && snap.Attempt == round {
		return true
	}
	return false
}

// containsRole reports whether role is in the list.
func containsRole(list []roles.Role, role roles.Role) bool {
	for _, r := range list {
		if r == role {
			return true
		}
	}
	return false
}

// joinRoles renders a role list for an error message.
func joinRoles(list []roles.Role) string {
	parts := make([]string, len(list))
	for i, r := range list {
		parts[i] = string(r)
	}
	return strings.Join(parts, ", ")
}

// foldStory returns the folded snapshot of one story, or nil when the epic has no event for it yet.
func foldStory(epicDir, story string) *state.StorySnap {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return nil
	}
	return state.Fold(events).Stories[story]
}

// pending records a pending_external event for a role that could not be dispatched, so the leader sees the gap without
// losing the pack. The dispatch failed on our side (worktree/spawn), so it is not externally confirmed. attempt and from
// come from dispatchTransition, so a repeated failure bumps the attempt instead of overwriting the last one.
func pending(epicDir, slug, story string, attempt int, from state.State, cause error) {
	_ = state.Append(epicDir, state.Event{
		Epic: slug, Story: story, Attempt: attempt, Actor: state.Leader,
		From: from, To: state.PendingExternal,
		Evidence: map[string]any{"intended_to": string(state.Working), "error": cause.Error()}, ExternalConfirmed: false,
	})
}

// repoCount is the number of repo lines in the epic repos file (a trigger signal).
func repoCount(epicDir string) int {
	b, err := os.ReadFile(filepath.Join(epicDir, "repos"))
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 1 && f[0] != "" {
			n++
		}
	}
	return n
}
