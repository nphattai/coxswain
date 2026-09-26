package doctor

// This file is the setup oracle: it recognises a v2 workspace by cox/workspace.json, validates it, lists its epics with
// their Status:/watcher/hooks, and checks the environment a leader needs (one cox on PATH, orca present and reachable,
// the policy's default harness binaries, and the optional quota/review binaries only when policy names them). It is
// registry-free: the caller passes in which harness names have an adapter, so this package never imports the harness
// adapters. Each check is pass/fail/unknown/info with a fix hint, and the caller maps any fail to exit 1 and any
// unknown to exit 3.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/boundexec"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// CheckStatus is a check outcome. Info never affects the exit code; Unknown maps to exit 3, Fail to exit 1.
type CheckStatus string

const (
	StatusPass    CheckStatus = "pass"
	StatusFail    CheckStatus = "fail"
	StatusUnknown CheckStatus = "unknown"
	StatusInfo    CheckStatus = "info"
)

// Check is one environment check with a human fix hint (empty for pass/info).
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
	Fix    string      `json:"fix,omitempty"`
}

// EpicReport is one epic under a workspace as the doctor sees it.
type EpicReport struct {
	Path         string `json:"path"`
	Slug         string `json:"slug"`
	Status       string `json:"status,omitempty"`
	WatcherAlive bool   `json:"watcher_alive"`
	WatcherPid   int    `json:"watcher_pid,omitempty"`
	// Closed is true for an archived epic: a .cox.closed exists and .cox does not. A closed epic has no live watcher and
	// no open stories, so doctor prints it as "closed" rather than "active ... watcher dead" (finding 12).
	Closed bool `json:"closed,omitempty"`
	// Signed is the epic's signed state read from the durable log (the committed ledger, merged with the runtime log by
	// state.Load), NOT from the free-text Status: line of DESIGN.md. doctor prints this and flags a disagreement with the
	// Status: text, so a re-attach that lost the signature can no longer hide behind DESIGN.md still saying signed (finding 2).
	Signed bool `json:"signed"`
}

// WorkspaceReport is one recognised v2 workspace: its validity, hook install state per leader harness, epics, and any
// repo checkout that still carries a cox/policy.json (which nothing reads and which drifts from the workspace copy).
type WorkspaceReport struct {
	Root         string          `json:"root"`
	Valid        bool            `json:"valid"`
	Error        string          `json:"error,omitempty"`
	PolicyError  string          `json:"policy_error,omitempty"` // cox/policy.json missing, malformed, or invalid
	Repos        int             `json:"repos"`
	Hooks        map[string]bool `json:"hooks"` // leader harness -> hooks installed
	Epics        []EpicReport    `json:"epics"`
	PolicyInRepo []string        `json:"policy_in_repo,omitempty"`
	// RepoIssues names each path-backed repo in workspace.json whose checkout is missing or is not a git checkout. The
	// workspace loads (structural JSON is valid), but a leader cannot cut a worktree from it, so doctor fails on these
	// (cox-onboarding finding 7 / codex PR#3 r3): existence and git-checkout are a doctor concern, not a load-time one.
	RepoIssues []string `json:"repo_issues,omitempty"`
	// PiLeader is this workspace's Pi leader extension check (nil when pi is not a leader option). It is filled per
	// workspace by the doctor command, so `cox doctor --root <ws>` checks <ws>'s own extension (finding 3).
	PiLeader *Check `json:"pi_leader,omitempty"`
}

// Roots merges the default roots ($HOME/Work and $ORCA_WORKSPACES), $COX_ROOTS (path-list separated), and any explicit
// --root values, de-duplicated in first-seen order.
func Roots(extra []string) []string {
	var all []string
	all = append(all, DefaultRoots()...)
	if env := os.Getenv("COX_ROOTS"); env != "" {
		all = append(all, filepath.SplitList(env)...)
	}
	all = append(all, extra...)
	seen := map[string]bool{}
	var out []string
	for _, r := range all {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// FindWorkspaces returns every directory holding cox/workspace.json, looking at each root itself and one level below it
// (a workspace usually sits at <root>/<ws>), plus any explicit dirs the caller adds (the workspace of --epic or the
// cwd). Results are unique and sorted.
func FindWorkspaces(roots, explicit []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir) // normalise so /a//b and /a/b dedup to one
		if seen[dir] {
			return
		}
		if exists(filepath.Join(dir, workspace.ControlDir, "workspace.json")) {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	for _, root := range roots {
		add(root)
		matches, _ := filepath.Glob(filepath.Join(root, "*"))
		for _, m := range matches {
			add(m)
		}
	}
	for _, e := range explicit {
		add(e)
	}
	sort.Strings(out)
	return out
}

// SingleOnPATH is the `which -a <bin>` check: exactly one executable named bin on PATH passes; none fails; more than one
// fails (an ambiguous driver, F09's classic hazard). Results are de-duplicated by RESOLVED path, so a PATH entry listed
// twice, or two entries that symlink to the same real binary, count as one install; only a genuine second binary (a
// distinct real file) fails (finding 15).
func SingleOnPATH(bin string) Check {
	var found []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			key := p
			if real, err := filepath.EvalSymlinks(p); err == nil {
				key = real // a symlink and its target, or the same dir listed twice, resolve to one real binary
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		return Check{Name: bin + " on PATH", Status: StatusPass, Detail: found[0]}
	case 0:
		return Check{Name: bin + " on PATH", Status: StatusFail, Detail: "not found", Fix: "install " + bin + " and ensure it is on PATH"}
	default:
		return Check{Name: bin + " on PATH", Status: StatusFail, Detail: strings.Join(found, ", "),
			Fix: "remove the extra " + bin + " so exactly one is on PATH"}
	}
}

// Present checks that a binary is on PATH (pass), else fails with the given fix hint.
func Present(bin, fix string) Check {
	if p, err := exec.LookPath(bin); err == nil {
		return Check{Name: bin + " present", Status: StatusPass, Detail: p}
	}
	return Check{Name: bin + " present", Status: StatusFail, Detail: "not on PATH", Fix: fix}
}

// Reachable runs a probe command with a timeout: exit 0 is pass, any non-zero or launch error is unknown (the tool is
// there but its service did not answer, which the caller maps to exit 3, not a hard fail).
func Reachable(name string, args []string, timeout time.Duration) Check {
	label := name + " " + strings.Join(args, " ")
	if _, err := exec.LookPath(name); err != nil {
		return Check{Name: label, Status: StatusUnknown, Detail: name + " not on PATH", Fix: "install " + name}
	}
	code, err := boundexec.Run(context.Background(), timeout, exec.Command(name, args...))
	detail := ""
	switch {
	case err != nil:
		detail = err.Error()
	case code == boundexec.ExitTimeout:
		detail = "timed out after " + timeout.String()
	case code != 0:
		detail = fmt.Sprintf("exit status %d", code)
	default:
		return Check{Name: label, Status: StatusPass}
	}
	return Check{Name: label, Status: StatusUnknown, Detail: detail, Fix: "check that " + name + " is configured and reachable"}
}

// HarnessBinaries checks the policy's harness options: the default leader and default worker binaries are required (a
// fail with a fix hint when missing); any other adaptered option is present->pass, missing->info (a second harness you
// may not have installed); an option with no adapter is info ("no adapter, cannot dispatch"). adaptered reports whether
// a harness name has an adapter, injected so this package stays registry-free.
func HarnessBinaries(pol *workspace.Policy, adaptered func(string) bool) []Check {
	if pol == nil {
		return nil
	}
	required := map[string]bool{
		strings.TrimSpace(pol.Harness.Leader.Default): true,
		strings.TrimSpace(pol.Harness.Worker.Default): true,
	}
	seen := map[string]bool{}
	var names []string
	for _, n := range append(append([]string{}, pol.Harness.Leader.Options...), pol.Harness.Worker.Options...) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var checks []Check
	for _, n := range names {
		if !adaptered(n) {
			// A default harness with no adapter cannot dispatch, so it is a hard fail; a non-default option with no adapter
			// stays info (a harness you listed but do not drive) - finding 8.
			if required[n] {
				checks = append(checks, Check{Name: "harness " + n, Status: StatusFail, Detail: "policy default harness has no adapter, cannot dispatch",
					Fix: "set harness." + roleOf(pol, n) + ".default to an adaptered harness (claude or codex)"})
				continue
			}
			checks = append(checks, Check{Name: "harness " + n, Status: StatusInfo, Detail: "no adapter, cannot dispatch"})
			continue
		}
		_, err := exec.LookPath(n)
		switch {
		case err == nil:
			checks = append(checks, Check{Name: "harness " + n, Status: StatusPass})
		case required[n]:
			checks = append(checks, Check{Name: "harness " + n, Status: StatusFail, Detail: "policy default harness not on PATH",
				Fix: "install " + n + " (it is a default in policy.json harness options)"})
		default:
			checks = append(checks, Check{Name: "harness " + n, Status: StatusInfo, Detail: "not installed; dispatch to " + n + " unavailable"})
		}
	}
	return checks
}

// roleOf names which default role(s) a harness fills in a policy, for the fix hint on a no-adapter default.
func roleOf(pol *workspace.Policy, name string) string {
	leader := strings.TrimSpace(pol.Harness.Leader.Default) == name
	worker := strings.TrimSpace(pol.Harness.Worker.Default) == name
	switch {
	case leader && worker:
		return "leader/worker"
	case worker:
		return "worker"
	default:
		return "leader"
	}
}

// OptionalBinary checks a named optional tool (quota-axi, lavish-axi) only when policy names it: present->pass,
// missing->info (never a fail, so a minimal install stays clean). named is the policy-configured binary name ("" skips
// the check entirely).
func OptionalBinary(role, named string) *Check {
	if strings.TrimSpace(named) == "" {
		return nil
	}
	c := Check{Name: role + " (" + named + ")"}
	if p, err := exec.LookPath(named); err == nil {
		c.Status, c.Detail = StatusPass, p
	} else {
		c.Status, c.Detail = StatusInfo, "not installed (optional; policy names "+named+")"
	}
	return &c
}

// InspectWorkspace validates a workspace and lists its epics with Status:/watcher/hooks and any repo checkout carrying a
// stray cox/policy.json. A validation error leaves Valid=false with the field-named message; the rest is still filled in
// where possible so the report is useful even for a partly-broken workspace.
func InspectWorkspace(wsRoot string) WorkspaceReport {
	rep := WorkspaceReport{Root: wsRoot, Hooks: map[string]bool{}}
	ws, err := workspace.LoadFile(filepath.Join(wsRoot, workspace.ControlDir, "workspace.json"))
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.Valid = true
	rep.Repos = len(ws.Repos)

	// The policy must load and validate: a missing, malformed, or invalid cox/policy.json is a failure (dispatch and
	// other policy-dependent commands would fail), not something to skip silently past a "valid" workspace.
	pol, perr := workspace.LoadPolicy(wsRoot)
	if perr != nil {
		rep.PolicyError = perr.Error()
	} else {
		// Leader hooks per harness the policy allows (claude -> .claude/settings.json, codex -> .codex/hooks.json).
		for _, h := range pol.Harness.Leader.Options {
			if target := hookTarget(wsRoot, h); target != "" {
				rep.Hooks[h] = hooksComplete(target)
			}
		}
	}

	// Epics at <ws>/<project>/epics/<slug> and the nested <ws>/apps/foo/epics/<slug> layout (matching cox epic new).
	seenEpic := map[string]bool{}
	var epicDirs []string
	for _, pat := range []string{
		filepath.Join(wsRoot, "*", "epics", "*"),
		filepath.Join(wsRoot, "*", "*", "epics", "*"),
	} {
		m, _ := filepath.Glob(pat)
		for _, ep := range m {
			if !seenEpic[ep] {
				seenEpic[ep] = true
				epicDirs = append(epicDirs, ep)
			}
		}
	}
	sort.Strings(epicDirs)
	for _, ep := range epicDirs {
		if fi, err := os.Stat(ep); err != nil || !fi.IsDir() {
			continue
		}
		// A closed epic is archived: .cox.closed exists and .cox does not. It has no live watcher and no open stories.
		closed := exists(filepath.Join(ep, ".cox.closed")) && !exists(filepath.Join(ep, ".cox"))
		pid := readPid(filepath.Join(ep, ".cox", "watch.pid"))
		rep.Epics = append(rep.Epics, EpicReport{
			Path:         ep,
			Slug:         filepath.Base(ep),
			Status:       epicStatus(filepath.Join(ep, "DESIGN.md")),
			WatcherPid:   pid,
			WatcherAlive: pid > 0 && pidAlive(pid),
			Closed:       closed,
			Signed:       epicSigned(ep),
		})
	}

	// A repo checkout that still carries cox/policy.json: nothing reads it and it drifts from the workspace copy. A repo
	// registered at the workspace root (in-repo workspace) carries the workspace's own policy, which is not a copy.
	for _, r := range ws.Repos {
		if r.Path != "" && !samePath(r.Path, wsRoot) && exists(filepath.Join(r.Path, "cox", "policy.json")) {
			rep.PolicyInRepo = append(rep.PolicyInRepo, r.Alias)
		}
	}
	// A path-backed repo whose checkout is missing or is not a git checkout: the workspace loaded, but no worktree can be
	// cut from it. A name-only repo has no local path to check.
	rep.RepoIssues = repoCheckoutIssues(ws)
	return rep
}

// samePath reports whether two paths name one directory, resolving symlinks when both resolve (macOS /var is
// /private/var, so a registered path and the discovered root can differ only by a link).
func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		if rb, err := filepath.EvalSymlinks(b); err == nil {
			return ra == rb
		}
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// repoCheckoutIssues returns a fix-hinted message for every path-backed repo whose checkout is missing or is not a git
// checkout (no .git). Repos are checked in registry order; a name-only repo is skipped (nothing to verify locally).
func repoCheckoutIssues(ws *workspace.Workspace) []string {
	var issues []string
	for _, r := range ws.Repos {
		if r.Path == "" {
			continue
		}
		switch {
		case !exists(r.Path):
			issues = append(issues, fmt.Sprintf("repo %q path %s does not exist (clone it, or fix its path in cox/workspace.json)", r.Alias, r.Path))
		case !exists(filepath.Join(r.Path, ".git")):
			issues = append(issues, fmt.Sprintf("repo %q path %s is not a git checkout (no .git; clone the repo there, or fix its path in cox/workspace.json)", r.Alias, r.Path))
		}
	}
	return issues
}

// hookTarget returns the workspace settings file a harness's leader hooks live in, or "" for a harness with no target.
func hookTarget(wsRoot, harness string) string {
	switch harness {
	case "claude":
		return filepath.Join(wsRoot, ".claude", "settings.json")
	case "codex":
		return filepath.Join(wsRoot, ".codex", "hooks.json")
	default:
		return ""
	}
}

// epicSigned reports whether the epic's durable log carries a design_signed event. state.Load merges the committed
// ledger with the runtime log, so a signature written to either is seen (finding 2). Any read error is treated as
// unsigned rather than crashing doctor.
func epicSigned(epicDir string) bool {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return false
	}
	for _, ev := range events {
		if ev.Type == state.DesignSigned {
			return true
		}
	}
	return false
}

// epicStatus reads the first `Status:` line from an epic's DESIGN.md, or "" when absent.
func epicStatus(designPath string) string {
	b, err := os.ReadFile(designPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "Status:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
	}
	return ""
}

// requiredHookNames are the four leader hook commands a complete install carries.
var requiredHookNames = []string{"prompt-drain", "stop-rewake", "precompact", "session-start"}

// hooksComplete parses a settings/hooks file and reports true only when every one of the four `cox hook <name>`
// commands is present. A single `cox hook` (e.g. only prompt-drain) is not enough - reporting hooks=yes while Stop is
// missing would hide broken rewaking or checkpointing (PR#3 review finding 6).
func hooksComplete(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return false
	}
	var cmds []string
	for _, groups := range doc.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				cmds = append(cmds, h.Command)
			}
		}
	}
	for _, name := range requiredHookNames {
		found := false
		for _, c := range cmds {
			if strings.Contains(c, "cox hook "+name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func readPid(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
