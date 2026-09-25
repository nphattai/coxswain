package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/boundexec"
	"github.com/nphattai/coxswain/internal/doctor"
	"github.com/nphattai/coxswain/internal/epic"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/watch"
	"github.com/nphattai/coxswain/internal/workspace"
)

// harnessCard is the doctor view of one harness capability card plus whether an adapter is implemented for it.
type harnessCard struct {
	Name       string   `json:"name"`
	Roles      []string `json:"roles"`
	Wake       string   `json:"wake"`
	Checkpoint string   `json:"checkpoint"`
	Doorbell   bool     `json:"doorbell"`
	Interrupt  bool     `json:"interrupt"`
	Telemetry  bool     `json:"telemetry"`
	Adapter    bool     `json:"adapter"`
}

// policyOption is one harness name declared in a policy's options, with whether an adapter exists for it.
type policyOption struct {
	Name    string `json:"name"`
	Adapter bool   `json:"adapter"`
}

// doctorOutput wraps the installation report with the harness card table (and, when --epic resolves a policy, the
// per-option adapter column), the recognised v2 workspaces, and the environment checks, so `cox doctor --json` carries
// the whole setup picture.
type doctorOutput struct {
	doctor.Report
	Harnesses     []harnessCard            `json:"harnesses"`
	PolicyOptions []policyOption           `json:"policy_options,omitempty"`
	Quota         quotaDoctor              `json:"quota"`
	Workspaces    []doctor.WorkspaceReport `json:"workspaces"`
	Checks        []doctor.Check           `json:"checks"`
}

// adapteredHarness reports whether a harness name has a registered adapter, injected into internal/doctor so that
// package stays free of the harness registry.
func adapteredHarness(name string) bool {
	_, ok := registry.Adapter(name)
	return ok
}

// environmentChecks builds the setup checks: exactly one cox on PATH, orca present and (when present) `orca status`
// reachable, the policy's harness binaries, and the optional review/quota binaries only when policy names them.
func environmentChecks(pol *workspace.Policy) []doctor.Check {
	var checks []doctor.Check
	checks = append(checks, doctor.SingleOnPATH("cox"))
	orca := doctor.Present("orca", "install Orca and put it on PATH")
	checks = append(checks, orca)
	if orca.Status == doctor.StatusPass {
		checks = append(checks, doctor.Reachable("orca", []string{"status"}, 10*time.Second))
	}
	checks = append(checks, doctor.HarnessBinaries(pol, adapteredHarness)...)
	if pol != nil {
		if c := doctor.OptionalBinary("review", pol.Review.Binary); c != nil {
			checks = append(checks, *c)
		}
		if c := doctor.OptionalBinary("quota", pol.Quota.Binary); c != nil {
			checks = append(checks, *c)
		}
	}
	return checks
}

// piLeaderExtensionCheck reports the workspace's Pi leader extension health when the policy lists pi as a leader option
// (DESIGN item 5). Missing, or a hash that differs from this cox binary's embedded copy (stale), is an ISSUE whose repair
// is `cox workspace init`; a verified install is a pass. It returns nil (no check) when pi is not a leader option or
// there is no primary workspace, so a claude/codex-only workspace gets no pi noise. It lives in the cmd layer because
// internal/doctor is deliberately free of the harness adapters.
func piLeaderExtensionCheck(pol *workspace.Policy, wsRoot string) *doctor.Check {
	if pol == nil || wsRoot == "" {
		return nil
	}
	isLeaderOption := false
	for _, h := range pol.Harness.Leader.Options {
		if h == "pi" {
			isLeaderOption = true
			break
		}
	}
	if !isLeaderOption {
		return nil
	}
	extDir := filepath.Join(wsRoot, pi.ExtensionRelDir)
	if _, ok := pi.VerifyExtension(wsRoot); ok {
		return &doctor.Check{Name: "pi leader extension", Status: doctor.StatusPass, Detail: extDir}
	}
	detail := "stale (hash differs from this cox binary)"
	if _, err := os.Stat(filepath.Join(extDir, pi.ExtensionEntry)); os.IsNotExist(err) {
		detail = "missing"
	}
	return &doctor.Check{
		Name:   "pi leader extension",
		Status: doctor.StatusFail,
		Detail: fmt.Sprintf("%s at %s", detail, extDir),
		Fix:    "cox workspace init",
	}
}

// checkLine renders one check row: name, status, detail, and the repair when there is one.
func checkLine(c doctor.Check) string {
	line := fmt.Sprintf("%-22s %s", c.Name, c.Status)
	if c.Detail != "" {
		line += "  " + c.Detail
	}
	if c.Fix != "" {
		line += "  (fix: " + c.Fix + ")"
	}
	return line
}

// quotaDoctor is the doctor view of the quota-axi adapter and any manual readings in effect: whether the binary is
// found, its version, whether the automatic source is Keychain-granted (derived from a live claude reading when an epic
// is given), and the active captain-declared readings.
type quotaDoctor struct {
	Found    bool     `json:"found"`
	Path     string   `json:"path,omitempty"`
	Version  string   `json:"version,omitempty"`
	Keychain string   `json:"keychain"` // ok | needs-grant | unknown
	Manual   []string `json:"manual_active,omitempty"`
}

// cmdDoctor implements `cox doctor [--epic <dir>] [--json]`. It prints every installation and each epic's control
// directory, the capability card of every implemented harness, and (with --epic) an adapter yes|no column for each
// harness declared in that epic's resolved policy options, then exits 1 if any installation issue was found (version
// divergence, or a live v1 .run beside a v2 .cox), 0 otherwise.
func cmdDoctor(args []string) int {
	var rootFlags repoList
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	epicDir := epicFlag(fs, "", "epic directory (optional; adds an adapter column for its policy harness options)")
	fs.Var(&rootFlags, "root", "extra root to scan for workspaces (repeatable; adds to $HOME/Work, $ORCA_WORKSPACES, $COX_ROOTS)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	now := time.Now().UTC()
	rep := doctor.Run(doctor.DefaultRoots())

	// Recognise every v2 workspace under the roots, always including the workspace that contains --epic or the cwd.
	roots := doctor.Roots(rootFlags)
	target := *epicDir
	if target == "" {
		target = "."
	}
	var explicit []string
	if wsRoot, err := findWorkspaceRoot(target); err == nil {
		explicit = []string{wsRoot}
	}
	wsDirs := doctor.FindWorkspaces(roots, explicit)
	wsReports := make([]doctor.WorkspaceReport, 0, len(wsDirs))
	for _, d := range wsDirs {
		r := doctor.InspectWorkspace(d)
		if r.Valid {
			// Per workspace, not once for the cwd/--epic workspace: `--root <ws>` from outside <ws> must check <ws>'s own
			// Pi leader extension (finding 3 / dogfood AC6).
			wpol, _ := workspace.LoadPolicy(d)
			r.PiLeader = piLeaderExtensionCheck(wpol, d)
			// A checkout Orca does not know fails every epic worktree for it late (B-34b): report it with the fix.
			if ws, err := workspace.Load(d); err == nil {
				r.RepoIssues = append(r.RepoIssues, unregisteredOrcaRepos(ws.Repos)...)
			}
		}
		wsReports = append(wsReports, r)
	}

	// The primary policy for the environment checks: the workspace of --epic/cwd, else the first workspace found.
	primaryWs := ""
	if len(explicit) > 0 {
		primaryWs = explicit[0]
	} else if len(wsDirs) > 0 {
		primaryWs = wsDirs[0]
	}
	var pol *workspace.Policy
	if primaryWs != "" {
		pol, _ = workspace.LoadPolicy(primaryWs)
	}
	checks := environmentChecks(pol)

	out := doctorOutput{Report: rep, Harnesses: harnessCards(codexCoxHooksInstalled(".")), PolicyOptions: policyOptions(*epicDir), Quota: quotaReport(*epicDir), Workspaces: wsReports, Checks: checks}
	var watcherIssues []string
	// A dead watcher with active stories in a workspace found only via --root or the epic path must also fail doctor, not
	// just those under the default installation scan (PR#3 review finding 5). Computed for both --json and human output.
	wsWatcherIssues := watcherIssuesForWorkspaces(wsReports)
	wsRepoIssues := workspaceRepoIssues(wsReports)
	wsSignedIssues := workspaceSignedIssues(wsReports)
	wsLeaderIssues := leaderHandleIssues(wsReports)
	wsDupLeaderIssues := duplicateLeaderIssues(wsReports)
	// Item 2: live cox-watch processes whose epic dir is gone or outside every known workspace root (B-37). Item 3: an
	// epic whose leader has been unreachable for DoorbellFailAlarm+ consecutive doorbell nudges.
	orphanIssues := doctor.OrphanWatchers(roots)
	doorbellIssues := doorbellFailIssues(wsReports)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, "cox doctor:", err)
			return 1
		}
	} else {
		// rep.Installations are only the legacy v1 kits that own epics (a bare kit or dev checkout is dropped, B-45), so
		// "none" is not news beside a v2 workspace: say so only when nothing at all was found.
		if len(rep.Installations) == 0 && len(wsReports) == 0 {
			fmt.Printf("no coxswain workspace found under %s (run cox doctor inside one, or pass --root)\n", strings.Join(roots, ", "))
		}
		for _, in := range rep.Installations {
			line := fmt.Sprintf("%s  [%s]  %s", in.Path, in.Type, in.Version)
			if in.KitPath != in.Path {
				line += "  (kit: " + in.KitPath + ")"
			}
			fmt.Println(line)
			for _, ep := range in.Epics {
				ctrl := ".run"
				if ep.HasCox {
					ctrl = ".cox/events.jsonl"
				}
				if ep.HasRun && ep.HasCox {
					ctrl = ".run + .cox/events.jsonl"
				}
				fmt.Printf("    epic %s  (%s)\n", ep.Path, ctrl)
				wi := watcherInfo(ep.Path)
				fmt.Printf("      %s\n", watcherLine(wi, now))
				if iss := watcherIssue(ep.Path); iss != "" {
					watcherIssues = append(watcherIssues, iss)
				}
				if drift := policyDriftFor(ep.Path); len(drift) > 0 {
					fmt.Printf("      policy drift vs template (missing keys): %s\n", strings.Join(drift, ", "))
				}
			}
		}
		if len(wsReports) == 0 {
			fmt.Println("no cox workspaces found (looked under", strings.Join(roots, ", ")+")")
		}
		for _, w := range wsReports {
			if !w.Valid {
				fmt.Printf("workspace %s  INVALID: %s\n", w.Root, w.Error)
				continue
			}
			hooks := make([]string, 0, len(w.Hooks))
			for _, h := range sortedKeys(w.Hooks) {
				hooks = append(hooks, fmt.Sprintf("%s=%s", h, yesNo(w.Hooks[h])))
			}
			fmt.Printf("workspace %s  (%d repo(s), hooks: %s)\n", w.Root, w.Repos, strings.Join(hooks, " "))
			if w.PolicyError != "" {
				fmt.Fprintf(os.Stderr, "ISSUE: workspace %s policy.json: %s\n", w.Root, w.PolicyError)
			}
			for _, ep := range w.Epics {
				if ep.Closed {
					fmt.Printf("    epic %s  closed\n", ep.Slug)
					continue
				}
				watch := "watcher dead"
				if ep.WatcherAlive {
					watch = "watcher alive"
				}
				status := ep.Status
				if status == "" {
					status = "no Status:"
				}
				signed := "unsigned"
				if ep.Signed {
					signed = "signed"
				}
				fmt.Printf("    epic %s  [%s]  %s  ledger=%s\n", ep.Slug, status, watch, signed)
				if iss := signedDivergence(ep); iss != "" {
					fmt.Fprintf(os.Stderr, "ISSUE: workspace %s epic %s %s\n", w.Root, ep.Slug, iss)
				}
			}
			if c := w.PiLeader; c != nil {
				fmt.Println("    " + checkLine(*c))
			}
			for _, alias := range w.PolicyInRepo {
				fmt.Fprintf(os.Stderr, "WARN: repo %q checkout carries cox/policy.json; nothing reads it and it drifts from the workspace policy - delete it\n", alias)
			}
			for _, iss := range w.RepoIssues {
				fmt.Fprintf(os.Stderr, "ISSUE: workspace %s %s\n", w.Root, iss)
			}
		}
		fmt.Println("checks:")
		for _, c := range checks {
			fmt.Println("  " + checkLine(c))
		}
		fmt.Println("harness cards:")
		for _, c := range out.Harnesses {
			fmt.Printf("  %-8s roles=%-13s wake=%-4s checkpoint=%-6s doorbell=%-5t interrupt=%-5t telemetry=%-5t adapter=%s\n",
				c.Name, strings.Join(c.Roles, ","), c.Wake, c.Checkpoint, c.Doorbell, c.Interrupt, c.Telemetry, yesNo(c.Adapter))
		}
		if len(out.PolicyOptions) > 0 {
			fmt.Printf("policy harness options (%s):\n", *epicDir)
			for _, o := range out.PolicyOptions {
				fmt.Printf("  %-10s adapter=%s\n", o.Name, yesNo(o.Adapter))
			}
		}
		q := out.Quota
		if q.Found {
			fmt.Printf("quota-axi: found=yes path=%s version=%s keychain=%s\n", q.Path, orNone(q.Version), q.Keychain)
		} else {
			fmt.Println("quota-axi: found=no (install it or set policy quota.binary; npx is opt-in via policy quota.npx)")
		}
		for _, m := range q.Manual {
			fmt.Printf("  manual reading: %s\n", m)
		}
		for _, iss := range rep.Issues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
		for _, iss := range watcherIssues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
		for _, iss := range wsWatcherIssues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
		for _, iss := range wsLeaderIssues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
		for _, iss := range wsDupLeaderIssues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
		for _, iss := range orphanIssues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
		for _, iss := range doorbellIssues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
	}

	// Exit code: any fail (an install issue, a dead watcher with open stories, an invalid workspace, or a failed check)
	// is 1; any unknown with no fail (e.g. orca present but `orca status` unreachable) is 3; otherwise 0.
	hasFail := len(rep.Issues) > 0 || len(watcherIssues) > 0 || len(wsWatcherIssues) > 0 || len(wsRepoIssues) > 0 || len(wsSignedIssues) > 0 || len(wsLeaderIssues) > 0 || len(wsDupLeaderIssues) > 0 || len(orphanIssues) > 0 || len(doorbellIssues) > 0
	hasUnknown := false
	for _, w := range wsReports {
		if !w.Valid || w.PolicyError != "" || (w.PiLeader != nil && w.PiLeader.Status == doctor.StatusFail) {
			hasFail = true
		}
	}
	for _, c := range checks {
		switch c.Status {
		case doctor.StatusFail:
			hasFail = true
		case doctor.StatusUnknown:
			hasUnknown = true
		}
	}
	return doctorExit(hasFail, hasUnknown)
}

// watcherIssuesForWorkspaces returns a watcherIssue for every discovered-workspace epic whose watcher is dead while it
// still has active stories - the case the older default-root scan would catch but the new --root/epic scan would miss.
func watcherIssuesForWorkspaces(reps []doctor.WorkspaceReport) []string {
	var issues []string
	for _, w := range reps {
		for _, ep := range w.Epics {
			if ep.WatcherAlive || ep.Closed {
				continue // a closed epic has no watcher to be dead and no open stories
			}
			if iss := watcherIssue(ep.Path); iss != "" {
				issues = append(issues, iss)
			}
		}
	}
	return issues
}

// signedWordRe matches the whole word "signed" (case-insensitive), so "unsigned" does not count as signed - a word
// boundary sits before "signed" in "(signed" or "re-signed" but not inside "unsigned".
var signedWordRe = regexp.MustCompile(`(?i)\bsigned\b`)

// signedDivergence reports a disagreement between an epic's DESIGN.md Status: text and its durable ledger state: the
// text claims signed while the ledger has no design_signed (a re-attach that lost the signature, finding 2), or the
// ledger is signed while the text does not say so. It returns "" when they agree. A closed epic is skipped (its Status:
// text is historical and the archive is the truth).
func signedDivergence(ep doctor.EpicReport) string {
	// Closed on any evidence git carries too (the ledger's epic_closed, a closed Status), not only the machine-local
	// .cox.closed: a second machine, or an epic signed before ledgers existed and closed by hand, is not "signature
	// lost" (B-46).
	if ep.Closed || (ep.Path != "" && epic.Closed(ep.Path)) {
		return ""
	}
	// Match the whole word "signed" so an honestly unsigned Status (e.g. "active (unsigned, arena pending)") is not read
	// as signed - a bare substring check treats "unsigned" as "signed" and false-fails.
	textSaysSigned := signedWordRe.MatchString(ep.Status)
	switch {
	case textSaysSigned && !ep.Signed:
		return "DESIGN.md Status says signed but the ledger has no design_signed (signature lost - re-sign with cox epic design --sign, or fix the Status line)"
	case !textSaysSigned && ep.Signed:
		return "the ledger is signed but DESIGN.md Status does not say so (update the Status line)"
	default:
		return ""
	}
}

// workspaceSignedIssues collects every epic whose DESIGN.md text and ledger disagree on the signature, so doctor fails
// on the inconsistency rather than trusting the free-text Status line (finding 2).
func workspaceSignedIssues(reps []doctor.WorkspaceReport) []string {
	var issues []string
	for _, w := range reps {
		for _, ep := range w.Epics {
			if iss := signedDivergence(ep); iss != "" {
				issues = append(issues, w.Root+" epic "+ep.Slug+": "+iss)
			}
		}
	}
	return issues
}

// leaderHandleLive probes whether an epic's recorded leader terminal handle is still live. checked is false when there is
// no .cox/leader or no backend to probe (the check is skipped, not a failure). It is a package var so a test can inject a
// fake prober without a real Orca.
var leaderHandleLive = func(epicDir string) (live bool, checked bool) {
	handle := readLeader(epicDir)
	if handle == "" {
		return false, false
	}
	b, _ := newBackend(epicDir)
	if b == nil {
		return false, false
	}
	l, err := b.Probe(backend.Session{Kind: "orca", Handle: handle})
	if err != nil {
		return false, false // probe could not determine liveness; never infer a dead leader from doubt (F08)
	}
	switch l {
	case backend.Alive:
		return true, true
	case backend.Settled:
		return false, true // the handle is definitively disconnected
	default:
		return false, false // Unknown: not determined, do not fail
	}
}

// leaderHandleIssues flags every active (non-closed) epic whose recorded .cox/leader handle is not live: a leader
// restart re-bound to a new Orca handle, or died, so wakes ring a dead terminal and every leader hook bails out
// (finding 4). doctor fails on it with the hint to open a leader terminal in the workspace.
func leaderHandleIssues(reps []doctor.WorkspaceReport) []string {
	var issues []string
	for _, w := range reps {
		for _, ep := range w.Epics {
			if ep.Closed {
				continue
			}
			if live, checked := leaderHandleLive(ep.Path); checked && !live {
				issues = append(issues, fmt.Sprintf("%s epic %s: recorded .cox/leader handle is not live; open a leader terminal in the workspace so a hook re-binds it (cox hook prompt-drain)", w.Root, ep.Slug))
			}
		}
	}
	return issues
}

// doorbellFailIssues flags every active workspace epic whose leader terminal has been unreachable for DoorbellFailAlarm
// or more consecutive doorbell nudges (item 3): the watcher has raised a _leader stuck wake, and doctor mirrors it so a
// leader that is not reading its wakes still sees the unreachability from a health check.
func doorbellFailIssues(reps []doctor.WorkspaceReport) []string {
	var issues []string
	for _, w := range reps {
		for _, ep := range w.Epics {
			if ep.Closed {
				continue
			}
			if n := watch.DoorbellFailMax(ep.Path); n >= watch.DoorbellFailAlarm {
				issues = append(issues, fmt.Sprintf("%s epic %s: leader doorbell failed %d consecutive times; open a leader terminal in the workspace or run cox hook prompt-drain", w.Root, ep.Slug, n))
			}
		}
	}
	return issues
}

// epicTerminals lists the backend's terminals for an epic (for the duplicate-leader check). Package var so a test
// injects a fake listing without a real Orca. ok=false when there is no backend or the listing is unreadable: the
// check is skipped, never failed on doubt (F08).
var epicTerminals = func(epicDir string) (terms []backend.Terminal, ok bool) {
	b, _ := newBackend(epicDir)
	if b == nil {
		return nil, false
	}
	ts, err := b.Terminals()
	if err != nil {
		return nil, false
	}
	return ts, true
}

// leaderTerminalsIn returns the handles of connected terminals that run the leader harness and sit in the workspace
// root. The leader runs the leader harness in the workspace root, which is the terminal's worktree or a directory inside
// it (a cox workspace can live under a repo checkout); a worker's worktree is a sibling story dir, never an ancestor of
// the root. When harness is "" (policy unknown) any connected terminal that runs some agent in the root counts.
func leaderTerminalsIn(terms []backend.Terminal, wsRoot, harness string) []string {
	var handles []string
	for _, t := range terms {
		if !t.Connected || !terminalInWorkspaceRoot(t.WorktreePath, wsRoot) {
			continue
		}
		if harness != "" {
			if !strings.EqualFold(t.Harness, harness) {
				continue
			}
		} else if t.Harness == "" {
			continue // no agent runs in this terminal
		}
		handles = append(handles, t.Handle)
	}
	return handles
}

// terminalInWorkspaceRoot reports whether a terminal whose git worktree is worktreePath sits in the epic's workspace
// root (the worktree itself, or the root is a directory inside it).
func terminalInWorkspaceRoot(worktreePath, wsRoot string) bool {
	if worktreePath == "" || wsRoot == "" {
		return false
	}
	wp := filepath.Clean(worktreePath)
	root := filepath.Clean(wsRoot)
	return root == wp || strings.HasPrefix(root, wp+string(filepath.Separator))
}

// duplicateLeaderHandles returns the handles of the connected leader-harness terminals in an epic's workspace root when
// there is more than one - a duplicate leader, so a wake may reach the wrong terminal - else nil. Shared by doctor and
// the prompt-drain hook.
func duplicateLeaderHandles(epicDir, wsRoot string) []string {
	terms, ok := epicTerminals(epicDir)
	if !ok {
		return nil
	}
	harness := ""
	if pol, err := workspace.LoadPolicy(wsRoot); err == nil {
		harness = pol.Harness.Leader.Default
	}
	handles := leaderTerminalsIn(terms, wsRoot, harness)
	if len(handles) <= 1 {
		return nil
	}
	return handles
}

// firstActiveEpicPath returns the path of the first non-closed epic in a workspace report, or "" when none is active.
func firstActiveEpicPath(w doctor.WorkspaceReport) string {
	for _, ep := range w.Epics {
		if !ep.Closed {
			return ep.Path
		}
	}
	return ""
}

// duplicateLeaderIssues flags every workspace with an active epic where more than one connected Orca terminal in the
// workspace root runs the leader harness: two leaders drive one epic and a wake may reach the wrong one. The recorded
// .cox/leader handle is named as the one to keep. doctor fails on it (item 4).
func duplicateLeaderIssues(reps []doctor.WorkspaceReport) []string {
	var issues []string
	for _, w := range reps {
		active := firstActiveEpicPath(w)
		if active == "" {
			continue
		}
		if handles := duplicateLeaderHandles(active, w.Root); len(handles) > 1 {
			issues = append(issues, fmt.Sprintf("%s: %d connected leader terminals in the workspace root (%s); keep the recorded .cox/leader (%s) and close the rest",
				w.Root, len(handles), strings.Join(handles, ", "), orNone(readLeader(active))))
		}
	}
	return issues
}

// workspaceRepoIssues gathers every discovered workspace's path-backed repo checkout problems (missing path or not a git
// checkout), so doctor fails when a registered repo cannot back a worktree (finding 7).
func workspaceRepoIssues(reps []doctor.WorkspaceReport) []string {
	var issues []string
	for _, w := range reps {
		for _, iss := range w.RepoIssues {
			issues = append(issues, w.Root+": "+iss)
		}
	}
	return issues
}

// doctorExit maps the aggregate check outcome to an exit code: any fail is 1, any unknown with no fail is 3, else 0.
func doctorExit(hasFail, hasUnknown bool) int {
	switch {
	case hasFail:
		return 1
	case hasUnknown:
		return 3
	default:
		return 0
	}
}

// sortedKeys returns a map's keys sorted, for stable doctor output.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// policyDriftFor lists the template policy keys the epic's workspace policy is missing (drift), or nil when the
// workspace root or its policy.json cannot be resolved. It compares <wsRoot>/cox/policy.json against the embedded
// template so a policy that predates newer keys (e.g. harness.worker.models) is surfaced (M14).
func policyDriftFor(epicDir string) []string {
	wsRoot, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return nil
	}
	drift, err := workspace.PolicyDrift(filepath.Join(wsRoot, workspace.ControlDir, "policy.json"))
	if err != nil {
		return nil
	}
	return drift
}

// harnessCards renders the capability card of every implemented harness, tagged adapter=true (they are, by definition
// of the registry). The rows are the source doctor prints and the shape --json emits.
func harnessCards(codexHooksPresent bool) []harnessCard {
	var out []harnessCard
	for _, name := range registry.Names() {
		h, _ := registry.Adapter(name)
		c := h.Card()
		roles := make([]string, 0, len(c.Roles))
		for _, r := range c.Roles {
			roles = append(roles, string(r))
		}
		wake := string(c.Wake)
		// Codex's card default is pull (no hooks), but once the cox codex leader hooks are installed the Stop hook
		// delivers wakes on the push path, so reflect the effective mode.
		if c.Name == "codex" && codexHooksPresent {
			wake = "push"
		}
		out = append(out, harnessCard{
			Name: c.Name, Roles: roles, Wake: wake, Checkpoint: string(c.Checkpoint),
			Doorbell: c.Doorbell, Interrupt: c.Interrupt, Telemetry: c.Telemetry, Adapter: true,
		})
	}
	return out
}

// codexCoxHooksInstalled reports whether root/.codex/hooks.json carries the cox codex leader hooks (detected by the
// stop-rewake command). The workspace-level file is where `cox workspace hooks --harness codex` writes them, and the
// leader runs `cox doctor` from that root, so doctor checks the current directory.
func codexCoxHooksInstalled(root string) bool {
	b, err := os.ReadFile(filepath.Join(root, ".codex", "hooks.json"))
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte("cox hook stop-rewake"))
}

// policyOptions resolves the epic's policy (when epicDir is given and a policy loads) and returns each distinct harness
// name declared in its leader/worker options, tagged with whether an adapter is implemented. It returns nil when no
// epic is given or the policy cannot be resolved, so doctor still prints the card table without an epic.
func policyOptions(epicDir string) []policyOption {
	if epicDir == "" {
		return nil
	}
	pol := loadPolicyQuiet(epicDir)
	if pol == nil {
		return nil
	}
	return policyOptionsFrom(pol)
}

// policyOptionsFrom is the pure core of policyOptions: the distinct harness names across the policy's leader and worker
// options, sorted, each tagged with whether an adapter is implemented.
func policyOptionsFrom(pol *workspace.Policy) []policyOption {
	seen := map[string]bool{}
	var names []string
	for _, n := range append(append([]string{}, pol.Harness.Leader.Options...), pol.Harness.Worker.Options...) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]policyOption, 0, len(names))
	for _, n := range names {
		_, ok := registry.Adapter(n)
		out = append(out, policyOption{Name: n, Adapter: ok})
	}
	return out
}

// quotaReport builds the quota doctor view: it locates the quota-axi binary (policy override then PATH), reads its
// version, and - when an epic is given - derives the Keychain status from a live claude reading and lists any active
// manual readings. It never fails: a missing binary is found=false, and a slow read leaves keychain unknown.
func quotaReport(epicDir string) quotaDoctor {
	var q quotaDoctor
	q.Keychain = "unknown"
	bin := ""
	if epicDir != "" {
		if b := quotaAxiConfig(epicDir).Binary; b != "" {
			bin = b
		}
	}
	if bin == "" {
		if p, err := exec.LookPath("quota-axi"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		return q
	}
	q.Found = true
	q.Path = bin
	q.Version = quotaAxiVersion(bin)
	if epicDir == "" {
		return q
	}
	readings := mergedQuotaReadings(epicDir)
	q.Keychain = keychainStatus(readings)
	q.Manual = manualReadingLines(epicDir)
	return q
}

// quotaAxiVersionBound bounds `quota-axi --version` (doctor's other probes use 10s too).
var quotaAxiVersionBound = 10 * time.Second

// quotaAxiVersion runs `<bin> --version` through the one bounded exec (internal/boundexec: its own process group, TERM
// then KILL at the bound, a descendant holding stdout cannot park the read) and returns the trimmed first line, or "" on
// any error or timeout.
func quotaAxiVersion(bin string) string {
	var buf bytes.Buffer
	cmd := exec.Command(bin, "--version")
	cmd.Stdout = &buf
	if code, err := boundexec.Run(context.Background(), quotaAxiVersionBound, cmd); err != nil || code != 0 {
		return ""
	}
	out := buf.Bytes()
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line
}

// keychainStatus derives whether the automatic source is granted from the claude reading: known -> ok, an unknown whose
// reason mentions keychain/auth -> needs-grant, else unknown.
func keychainStatus(readings []quota.Reading) string {
	c := quota.Pick(readings, "claude", "")
	if c.Known {
		return "ok"
	}
	low := strings.ToLower(c.Reason)
	if strings.Contains(low, "keychain") || strings.Contains(low, "auth") {
		return "needs-grant"
	}
	return "unknown"
}

// manualReadingLines lists the active captain-declared readings for the doctor (source manual only).
func manualReadingLines(epicDir string) []string {
	man, _ := (&quota.Manual{EpicDir: epicDir}).Read(context.Background())
	var out []string
	for _, r := range man {
		out = append(out, quotaReadingLine(r))
	}
	return out
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
