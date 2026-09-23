package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/doctor"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// piLeaderExtensionCheck (DESIGN item 5): when the policy lists pi as a leader option, a missing or stale
// <ws>/.pi/extensions/ is an ISSUE with the repair `cox workspace init`; a current install is clean; and when pi is not
// a leader option (or there is no workspace) the check is skipped entirely.
func TestPiLeaderExtensionCheck(t *testing.T) {
	leaderPol := &workspace.Policy{}
	leaderPol.Harness.Leader.Options = []string{"claude", "codex", "pi"}

	// Missing: pi is a leader option but nothing is installed -> ISSUE + repair.
	ws := t.TempDir()
	c := piLeaderExtensionCheck(leaderPol, ws)
	if c == nil || c.Status != doctor.StatusFail {
		t.Fatalf("missing extension must be a fail issue, got %+v", c)
	}
	if c.Fix != "cox workspace init" {
		t.Errorf("repair must be `cox workspace init`, got %q", c.Fix)
	}
	if !strings.Contains(c.Detail, "missing") {
		t.Errorf("detail should say missing, got %q", c.Detail)
	}

	// Current: install it -> clean pass.
	if _, err := pi.InstallExtension(ws, ""); err != nil {
		t.Fatal(err)
	}
	if c := piLeaderExtensionCheck(leaderPol, ws); c == nil || c.Status != doctor.StatusPass {
		t.Fatalf("a verified extension must pass, got %+v", c)
	}

	// Stale: corrupt an installed source so the hash diverges from this binary -> ISSUE + repair.
	entry := filepath.Join(ws, pi.ExtensionRelDir, pi.ExtensionEntry)
	if err := os.WriteFile(entry, []byte("// tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c = piLeaderExtensionCheck(leaderPol, ws)
	if c == nil || c.Status != doctor.StatusFail || c.Fix != "cox workspace init" {
		t.Fatalf("a stale extension must be a fail issue with the repair, got %+v", c)
	}
	if !strings.Contains(c.Detail, "stale") {
		t.Errorf("detail should say stale, got %q", c.Detail)
	}

	// Not a leader option -> no check (no pi noise for a claude/codex-only workspace).
	nonPi := &workspace.Policy{}
	nonPi.Harness.Leader.Options = []string{"claude", "codex"}
	if c := piLeaderExtensionCheck(nonPi, ws); c != nil {
		t.Errorf("pi not a leader option must skip the check, got %+v", c)
	}
	// Nil policy / empty workspace -> no check.
	if c := piLeaderExtensionCheck(nil, ws); c != nil {
		t.Errorf("nil policy must skip the check, got %+v", c)
	}
	if c := piLeaderExtensionCheck(leaderPol, ""); c != nil {
		t.Errorf("empty workspace must skip the check, got %+v", c)
	}
}

// A workspace discovered via --root/epic whose epic has a dead watcher and an active story yields a watcher issue, so
// doctor's exit reflects it (PR#3 review finding 5). An alive watcher, or no open story, yields none.
func TestWatcherIssuesForWorkspaces(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, controlDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(watchPidPath(epic), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if watcherInfo(epic).Alive {
		t.Skip("pid 999999 happens to be alive on this host")
	}
	seedEvent(t, epic, state.Submitted, state.Working) // an open story

	dead := []doctor.WorkspaceReport{{Epics: []doctor.EpicReport{{Path: epic, WatcherAlive: false}}}}
	if len(watcherIssuesForWorkspaces(dead)) == 0 {
		t.Error("a dead watcher with an active story in a discovered workspace must raise an issue")
	}
	// An alive watcher raises nothing.
	alive := []doctor.WorkspaceReport{{Epics: []doctor.EpicReport{{Path: epic, WatcherAlive: true}}}}
	if len(watcherIssuesForWorkspaces(alive)) != 0 {
		t.Error("an alive watcher must raise no issue")
	}
}

// doctor flags a disagreement between an epic's DESIGN.md Status text and its ledger signed-state, and stays quiet when
// they agree or the epic is closed (finding 2).
func TestSignedDivergence(t *testing.T) {
	// Status says signed, ledger unsigned: the dangerous case (a lost signature) is flagged.
	if signedDivergence(doctor.EpicReport{Status: "active (signed 2026-09-21)", Signed: false}) == "" {
		t.Error("status-signed + ledger-unsigned must be flagged")
	}
	// Ledger signed, status silent: also a mismatch.
	if signedDivergence(doctor.EpicReport{Status: "active", Signed: true}) == "" {
		t.Error("ledger-signed + status-silent must be flagged")
	}
	// Agreement raises nothing.
	if signedDivergence(doctor.EpicReport{Status: "active (signed)", Signed: true}) != "" {
		t.Error("agreement must raise nothing")
	}
	if signedDivergence(doctor.EpicReport{Status: "draft", Signed: false}) != "" {
		t.Error("both unsigned must raise nothing")
	}
	// "unsigned" must not read as "signed": an honestly-unsigned epic with no ledger signature agrees, no flag.
	if signedDivergence(doctor.EpicReport{Status: "active (unsigned, arena pending)", Signed: false}) != "" {
		t.Error(`Status "unsigned" must not be treated as signed`)
	}
	// A hyphenated "re-signed" still counts as signed.
	if signedDivergence(doctor.EpicReport{Status: "active (re-signed 2026-09-21)", Signed: false}) == "" {
		t.Error(`"re-signed" must count as signed`)
	}
	// A closed epic's historical Status text is never flagged.
	if signedDivergence(doctor.EpicReport{Status: "active (signed)", Signed: false, Closed: true}) != "" {
		t.Error("a closed epic must not be flagged")
	}
	// The exit aggregator prefixes with the workspace root and epic slug.
	reps := []doctor.WorkspaceReport{{Root: "/ws", Epics: []doctor.EpicReport{{Slug: "e1", Status: "signed", Signed: false}}}}
	if got := workspaceSignedIssues(reps); len(got) != 1 || !strings.Contains(got[0], "e1") {
		t.Fatalf("workspaceSignedIssues = %v, want one issue naming e1", got)
	}
}

// doctor fails an active epic whose recorded .cox/leader handle is not live (a leader restart left a dead handle), skips
// an epic with a live handle or no probe, and never flags a closed epic (finding 4). The prober is injected - no Orca.
func TestLeaderHandleIssues(t *testing.T) {
	restore := leaderHandleLive
	t.Cleanup(func() { leaderHandleLive = restore })

	reps := []doctor.WorkspaceReport{{Root: "/ws", Epics: []doctor.EpicReport{
		{Slug: "dead", Path: "/ws/proj/epics/dead"},
		{Slug: "live", Path: "/ws/proj/epics/live"},
		{Slug: "norun", Path: "/ws/proj/epics/norun"},
		{Slug: "gone", Path: "/ws/proj/epics/gone", Closed: true},
	}}}
	leaderHandleLive = func(epicDir string) (bool, bool) {
		switch {
		case strings.HasSuffix(epicDir, "/dead"):
			return false, true // recorded handle not live
		case strings.HasSuffix(epicDir, "/live"):
			return true, true
		default:
			return false, false // no leader file / no backend to probe
		}
	}
	got := leaderHandleIssues(reps)
	if len(got) != 1 || !strings.Contains(got[0], "dead") || !strings.Contains(got[0], "not live") {
		t.Fatalf("leaderHandleIssues = %v, want one issue naming the dead-handle epic", got)
	}
}

// Item 4: leaderTerminalsIn counts only connected leader-harness terminals sitting in the workspace root; a
// disconnected terminal, a worker in a story worktree, and a non-leader harness are all excluded.
func TestLeaderTerminalsIn(t *testing.T) {
	terms := []backend.Terminal{
		{Handle: "lead1", WorktreePath: "/ws", Harness: "claude", Connected: true},
		{Handle: "lead2", WorktreePath: "/ws", Harness: "claude", Connected: true}, // duplicate leader
		{Handle: "dead", WorktreePath: "/ws", Harness: "claude", Connected: false}, // not connected
		{Handle: "codex", WorktreePath: "/ws", Harness: "codex", Connected: true},  // not the leader harness
		{Handle: "worker", WorktreePath: "/ws/proj/worktrees/story-a", Harness: "claude", Connected: true},
	}
	got := leaderTerminalsIn(terms, "/ws", "claude")
	if len(got) != 2 {
		t.Fatalf("want the two connected claude leader terminals in the root, got %v", got)
	}
	// A cox workspace nested under a repo checkout: the root is inside the terminal's worktree.
	nested := []backend.Terminal{
		{Handle: "a", WorktreePath: "/repo", Harness: "claude", Connected: true},
		{Handle: "b", WorktreePath: "/repo", Harness: "claude", Connected: true},
	}
	if len(leaderTerminalsIn(nested, "/repo/ops", "claude")) != 2 {
		t.Error("a workspace root inside the terminal's worktree must still match")
	}
	// harness "" (policy unknown): any connected terminal that runs an agent in the root counts.
	if len(leaderTerminalsIn(terms, "/ws", "")) < 2 {
		t.Error("with no known leader harness, connected agent terminals in the root must still count")
	}
}

// Item 4: doctor fails when more than one connected leader terminal runs in a workspace root that has an active epic,
// naming the recorded .cox/leader as the one to keep; a single leader raises nothing.
func TestDuplicateLeaderIssues(t *testing.T) {
	restore := epicTerminals
	t.Cleanup(func() { epicTerminals = restore })

	reps := []doctor.WorkspaceReport{{Root: "/ws", Epics: []doctor.EpicReport{{Slug: "e", Path: "/ws/proj/epics/e"}}}}

	epicTerminals = func(string) ([]backend.Terminal, bool) {
		return []backend.Terminal{
			{Handle: "term_leader", WorktreePath: "/ws", Harness: "claude", Connected: true},
			{Handle: "term_dupe", WorktreePath: "/ws", Harness: "claude", Connected: true},
		}, true
	}
	got := duplicateLeaderIssues(reps)
	if len(got) != 1 || !strings.Contains(got[0], "2 connected") || !strings.Contains(got[0], "/ws") {
		t.Fatalf("want one duplicate-leader issue for /ws, got %v", got)
	}

	// One leader: no issue.
	epicTerminals = func(string) ([]backend.Terminal, bool) {
		return []backend.Terminal{{Handle: "term_leader", WorktreePath: "/ws", Harness: "claude", Connected: true}}, true
	}
	if got := duplicateLeaderIssues(reps); len(got) != 0 {
		t.Fatalf("a single leader must raise nothing, got %v", got)
	}

	// A closed-only workspace is skipped.
	closed := []doctor.WorkspaceReport{{Root: "/ws", Epics: []doctor.EpicReport{{Slug: "e", Path: "/ws/proj/epics/e", Closed: true}}}}
	epicTerminals = func(string) ([]backend.Terminal, bool) {
		return []backend.Terminal{
			{Handle: "a", WorktreePath: "/ws", Harness: "claude", Connected: true},
			{Handle: "b", WorktreePath: "/ws", Harness: "claude", Connected: true},
		}, true
	}
	if got := duplicateLeaderIssues(closed); len(got) != 0 {
		t.Fatalf("a workspace with no active epic must raise nothing, got %v", got)
	}
}

// A closed epic raises no watcher issue (nothing to deliver), so doctor never prints it as "active ... watcher dead"
// (finding 12).
func TestWatcherIssuesSkipsClosedEpic(t *testing.T) {
	closed := []doctor.WorkspaceReport{{Epics: []doctor.EpicReport{{Path: t.TempDir(), WatcherAlive: false, Closed: true}}}}
	if len(watcherIssuesForWorkspaces(closed)) != 0 {
		t.Error("a closed epic must raise no watcher issue")
	}
}

// A path-backed repo whose checkout is missing or is not a git checkout makes doctor fail (finding 7): the issue is
// surfaced and folds into the exit code.
func TestWorkspaceRepoIssues(t *testing.T) {
	reps := []doctor.WorkspaceReport{{Root: "/ws", RepoIssues: []string{`repo "api" path /gone does not exist`}}}
	got := workspaceRepoIssues(reps)
	if len(got) != 1 || !strings.Contains(got[0], "/ws") || !strings.Contains(got[0], "api") {
		t.Fatalf("workspaceRepoIssues = %v, want the repo issue prefixed with the workspace root", got)
	}
	if len(workspaceRepoIssues([]doctor.WorkspaceReport{{Root: "/ok"}})) != 0 {
		t.Error("a workspace with no repo issues must contribute nothing")
	}
}

// doctor renders one capability card per implemented harness (registry-driven), each tagged adapter=yes, with the
// card's real fields. The count tracks the registry so a new adapter (pi) is covered without editing this assertion.
func TestDoctorHarnessCards(t *testing.T) {
	cards := harnessCards(false)
	if len(cards) != len(registry.Names()) {
		t.Fatalf("got %d harness cards, want %d (one per registry adapter: %v)", len(cards), len(registry.Names()), registry.Names())
	}
	byName := map[string]harnessCard{}
	for _, c := range cards {
		if !c.Adapter {
			t.Errorf("%s: adapter must be true (it is in the registry)", c.Name)
		}
		byName[c.Name] = c
	}
	if byName["claude"].Wake != "push" || byName["claude"].Checkpoint != "auto" || !byName["claude"].Telemetry {
		t.Errorf("claude card wrong: %+v", byName["claude"])
	}
	if byName["codex"].Wake != "pull" || byName["codex"].Checkpoint != "manual" || byName["codex"].Telemetry {
		t.Errorf("codex card wrong: %+v", byName["codex"])
	}
	if byName["pi"].Wake != "push" || byName["pi"].Checkpoint != "auto" || !byName["pi"].Telemetry {
		t.Errorf("pi card wrong: %+v", byName["pi"])
	}
}

// When the cox codex leader hooks are installed, doctor shows codex wake=push (the Stop hook delivers wakes), not the
// card default of pull.
func TestDoctorCodexWakePushWhenHooksInstalled(t *testing.T) {
	cardWake := func(cards []harnessCard) string {
		for _, c := range cards {
			if c.Name == "codex" {
				return c.Wake
			}
		}
		return ""
	}
	if w := cardWake(harnessCards(false)); w != "pull" {
		t.Fatalf("no hooks: codex wake = %q, want pull", w)
	}
	if w := cardWake(harnessCards(true)); w != "push" {
		t.Fatalf("hooks installed: codex wake = %q, want push", w)
	}

	// codexCoxHooksInstalled detects the stop-rewake command in root/.codex/hooks.json.
	root := t.TempDir()
	if codexCoxHooksInstalled(root) {
		t.Error("no file: must report not installed")
	}
	mustWrite(t, filepath.Join(root, ".codex", "hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"command":"cox hook stop-rewake --harness codex --epic /e"}]}]}}`)
	if !codexCoxHooksInstalled(root) {
		t.Error("stop-rewake command present: must report installed")
	}
}

// The policy-options column marks each declared harness adapter yes|no. The seed template lists only adaptered
// harnesses (claude, codex) so a minimal install passes doctor clean (arena round 1, adversary claim 1, accepted); a
// non-adaptered option, when a user adds one, still resolves as adapter=false.
func TestDoctorPolicyOptions(t *testing.T) {
	pol, err := workspace.LoadPolicyFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, o := range policyOptionsFrom(pol) {
		got[o.Name] = o.Adapter
	}
	for name, want := range map[string]bool{"claude": true, "codex": true} {
		if got[name] != want {
			t.Errorf("option %q adapter=%v, want %v", name, got[name], want)
		}
	}
	if _, ok := got["omp"]; ok {
		t.Errorf("seed template must list only adaptered harnesses in worker.options, got %v", got)
	}
	// A user-added non-adaptered option still resolves as adapter=false.
	pol.Harness.Worker.Options = append(pol.Harness.Worker.Options, "omp")
	adapters := map[string]bool{}
	for _, o := range policyOptionsFrom(pol) {
		adapters[o.Name] = o.Adapter
	}
	if adapters["omp"] {
		t.Errorf("omp must resolve adapter=false")
	}
}

// watcherInfo reports a live pidfile as alive and reads the last-tick file; a stale pid is present but dead. When the
// watcher is not alive and a story is still working, watcherIssue fires (the dogfood gap: a dead watcher nobody noticed).
func TestWatcherInfoAndIssue(t *testing.T) {
	epic := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	// No pidfile: not present, and no issue even with a working story.
	if wi := watcherInfo(epic); wi.Present {
		t.Error("no pidfile must be Present=false")
	}

	// A live pid (this process) plus a recent tick: alive, tick age read.
	writeCoxFile(epic, "watch.pid", "")
	if err := os.WriteFile(watchPidPath(epic), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	tickDir := filepath.Join(epic, controlDir, "watch")
	os.MkdirAll(tickDir, 0o755)
	os.WriteFile(filepath.Join(tickDir, "lasttick"), []byte(now.Add(-30*time.Second).Format(time.RFC3339)), 0o644)
	wi := watcherInfo(epic)
	if !wi.Present || !wi.Alive || !wi.HasTick {
		t.Fatalf("live watcher wrong: %+v", wi)
	}
	if line := watcherLine(wi, now); !strings.Contains(line, "alive") || !strings.Contains(line, "30s ago") {
		t.Errorf("watcher line wrong: %q", line)
	}
	// Alive + working story => no issue.
	if err := state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: "s", Attempt: 1, Actor: state.Leader, From: state.Submitted, To: state.Working, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	if iss := watcherIssue(epic, wi); iss != "" {
		t.Errorf("alive watcher must raise no issue, got %q", iss)
	}

	// A dead pid with a story still working => issue.
	os.WriteFile(watchPidPath(epic), []byte("999999"), 0o644)
	dead := watcherInfo(epic)
	if dead.Alive {
		t.Skip("pid 999999 happens to be alive on this host")
	}
	if iss := watcherIssue(epic, dead); iss == "" || !strings.Contains(iss, "not alive") {
		t.Fatalf("dead watcher with a working story must raise an issue, got %q", iss)
	}
}

// resolveWorkerModel falls back to the template default for a harness the workspace policy does not map, instead of
// returning "" (which would launch with no --model).
func TestResolveWorkerModelTemplateFallback(t *testing.T) {
	// An empty policy maps no codex model; resolveWorkerModel borrows the template default gpt-5.6-sol.
	if got := resolveWorkerModel(&workspace.Policy{}, "codex", ""); got != "gpt-5.6-sol" {
		t.Errorf("codex fallback = %q, want gpt-5.6-sol", got)
	}
	// Same fallback for pi: an empty policy borrows the template default openai-codex/gpt-5.6-sol (item 6, B-47).
	if got := resolveWorkerModel(&workspace.Policy{}, "pi", ""); got != "openai-codex/gpt-5.6-sol" {
		t.Errorf("pi fallback = %q, want openai-codex/gpt-5.6-sol", got)
	}
	// An explicit model still wins.
	if got := resolveWorkerModel(&workspace.Policy{}, "codex", "gpt-x"); got != "gpt-x" {
		t.Errorf("explicit model = %q, want gpt-x", got)
	}
}

// item 2: cox doctor lists a live cox-watch process whose epic dir is outside every known workspace root (B-37), via
// the injectable process lister.
func TestDoctorListsOrphanWatcher(t *testing.T) {
	orig := doctor.ListWatchProcs
	doctor.ListWatchProcs = func() []doctor.WatchProc {
		return []doctor.WatchProc{{Pid: 4242, Epic: "/tmp/pi-dogfood.abc/epic"}}
	}
	defer func() { doctor.ListWatchProcs = orig }()

	issues := doctor.OrphanWatchers([]string{t.TempDir()})
	if len(issues) != 1 || !strings.Contains(issues[0], "orphan watcher pid 4242") {
		t.Fatalf("expected the orphan watcher to be listed, got %v", issues)
	}
}

// item 3: cox doctor raises an ISSUE for an epic whose leader doorbell has failed DoorbellFailAlarm+ consecutive times.
func TestDoorbellFailIssues(t *testing.T) {
	epic := t.TempDir()
	failDir := filepath.Join(epic, controlDir, "watch", "doorbell-fail")
	if err := os.MkdirAll(failDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failDir, "term_dead"), []byte("3"), 0o644); err != nil {
		t.Fatal(err)
	}
	reps := []doctor.WorkspaceReport{{
		Root:  "/ws",
		Valid: true,
		Epics: []doctor.EpicReport{{Path: epic, Slug: "e1"}},
	}}
	issues := doorbellFailIssues(reps)
	if len(issues) != 1 || !strings.Contains(issues[0], "leader doorbell failed 3") {
		t.Fatalf("expected a doorbell-fail ISSUE, got %v", issues)
	}
	// Below the threshold: no issue.
	if err := os.WriteFile(filepath.Join(failDir, "term_dead"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := doorbellFailIssues(reps); len(got) != 0 {
		t.Fatalf("below the alarm threshold must raise no issue, got %v", got)
	}
}

// Finding 3 / dogfood AC6: `cox doctor --root <B>` run from inside another workspace A checks B's OWN Pi leader
// extension and prints it under B's header. On beedd57 the check ran once, for the cwd workspace only, so a tampered
// extension in B was never reported.
func TestDoctorRootChecksEachWorkspacePiExtension(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCA_WORKSPACES", "")
	t.Setenv("COX_ROOTS", "")
	t.Setenv("ORCA_RUN_ID", "")
	tpl, err := os.ReadFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	mkWs := func() string {
		ws := t.TempDir()
		mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"schema":"coxswain.workspace.v1"}`)
		mustWrite(t, filepath.Join(ws, "cox", "policy.json"), string(tpl))
		if _, err := pi.InstallExtension(ws, ""); err != nil {
			t.Fatal(err)
		}
		return ws
	}
	a, b := mkWs(), mkWs()
	if err := os.WriteFile(filepath.Join(b, pi.ExtensionRelDir, pi.ExtensionEntry), []byte("// tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(a)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	code := cmdDoctor([]string{"--root", b})
	os.Stdout = prev
	_ = w.Close()
	outB, _ := io.ReadAll(r)
	out := string(outB)

	section := func(ws string) string {
		i := strings.Index(out, "workspace "+ws+" ")
		if i < 0 {
			t.Fatalf("no header for workspace %s:\n%s", ws, out)
		}
		rest := out[i+1:]
		if j := strings.Index(rest, "\nworkspace "); j >= 0 {
			rest = rest[:j]
		}
		if j := strings.Index(rest, "\nchecks:"); j >= 0 {
			rest = rest[:j]
		}
		return rest
	}
	if s := section(b); !strings.Contains(s, "pi leader extension    fail  stale") || !strings.Contains(s, filepath.Join(b, pi.ExtensionRelDir)) {
		t.Fatalf("workspace B (--root) must report its own stale extension under its header:\n%s", s)
	}
	if s := section(a); !strings.Contains(s, "pi leader extension    pass") {
		t.Fatalf("workspace A must report its own current extension under its header:\n%s", s)
	}
	if code != 1 {
		t.Fatalf("a stale extension in any workspace must fail doctor, exit %d", code)
	}
}
