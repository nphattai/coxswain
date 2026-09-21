package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/orca"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/watch"
	"github.com/nphattai/coxswain/internal/workspace"
)

// controlDir is the per-epic control directory (mirrors state.ControlDir).
const controlDir = ".cox"

// modelAlias maps a bare model alias to its concrete id. `opus` resolves to claude-opus-4-8 (captain ruling
// 2026-09-03: bare "opus" is NOT Opus 5; claude workers run Opus 4.8). Any other value passes through unchanged.
func modelAlias(m string) string {
	if m == "opus" {
		return "claude-opus-4-8"
	}
	return m
}

// resolveWorkerModel resolves the --model a worker of harness `h` launches with. The alias (opus -> claude-opus-4-8) is
// a claude-only convenience, so it is applied only for claude; a codex "opus" is left untouched. It then defers to
// policy WorkerModel and warns to stderr when no default is found, so the launch omits --model and the harness picks
// its own default rather than being handed another harness's model (M10c).
func resolveWorkerModel(pol *workspace.Policy, h, explicit string) string {
	if h == "claude" {
		explicit = modelAlias(explicit)
	}
	model, ok := pol.WorkerModel(h, explicit)
	if !ok {
		// The workspace policy maps no default for this harness (it predates harness.worker.models). Rather than launch
		// with no --model (M10c let the harness pick its own, which drifted from the intended model), fall back to the
		// template's default for the harness and print a note; `cox doctor` shows the underlying policy drift (M14).
		if fallback := workspace.TemplateWorkerModel(h); fallback != "" {
			fmt.Fprintf(os.Stderr, "cox: note: policy has no harness.worker.models.%s; using the template default %q (workspace policy drift - run cox doctor)\n", h, fallback)
			return fallback
		}
		fmt.Fprintf(os.Stderr, "cox: warning: no default model for harness %q; launching without --model (harness picks its own default)\n", h)
	}
	return model
}

// modelVendor classifies a model id by its vendor prefix: "claude" for claude-*, "codex" for gpt-* and the o-series
// (o1, o3-mini, ...), else "" for a model whose vendor we do not recognise (no guard is applied to it).
func modelVendor(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "claude-"):
		return "claude"
	case strings.HasPrefix(m, "gpt-"), len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9':
		return "codex"
	default:
		return ""
	}
}

// piPreSpawnValidate runs Pi's provider/model and thinking validation before spawn (DESIGN section 2), alongside
// modelHarnessMismatch (which applies no check to a pi model, since modelVendor returns "" for it). It requires an
// explicit provider/model and rejects an unsupported thinking level; it applies only to the pi harness, so every other
// harness passes unchanged.
func piPreSpawnValidate(harnessName, model, effort string) error {
	if harnessName != "pi" {
		return nil
	}
	if err := pi.ValidateModel(model); err != nil {
		return err
	}
	return pi.ValidateThinking(effort)
}

// authorizeWorker runs the single card-notice authorization gate for a worker launch and, for pi, resolves the
// out-of-tree packaged extension. It is the one place every launch path (dispatch, resume/reroute, control relaunch,
// baseline) shares, so none can bypass the unsandboxed gate or silently omit the Pi extension. It returns the -e
// extension path (empty for a non-pi harness or a reduced-mode downgrade), the notices to print, the recorded
// unsandboxed authority ("flag"|"standing-ack"|""), and an error when the launch is refused (no adapter, role not
// allowed, or an unauthorized unsandboxed harness).
func authorizeWorker(harnessName, epic, story string, allowUnsandboxed, installExtension bool) (extension string, notices []string, authority string, err error) {
	// Gate pi to the terminal plane: on the orchestration plane Orca's Spawn ignores HarnessSpec.Argv (it passes only
	// agent+model), so the adapter-owned launch config (extension, --approve, model/thinking flags) is silently dropped
	// and a worker would run without push/checkpoint while dispatch recorded the static card. Refuse before spawn.
	if harnessName == "pi" && resolveOrcaPlane(epic) != workspace.PlaneTerminal {
		return "", nil, "", fmt.Errorf("harness %q requires the terminal plane (backend.orca.plane=terminal): the orchestration plane drops the adapter-owned launch config (extension, --approve, model flags)", harnessName)
	}
	n, authority, err := registry.Notices(harnessName, harness.RoleWorker, allowUnsandboxed)
	if err != nil {
		return "", nil, "", err
	}
	notices = n
	// installExtension is false for a bare baseline (which promises no cox hooks): the extension must not be installed or
	// passed there, or it would infer leader, start wake supervision, and use the _leader checkpoint, contaminating the
	// measurement.
	if harnessName == "pi" && installExtension {
		ext, extNotices := resolvePiExtension(piExtDir(epic, story), epic)
		extension = ext
		notices = append(notices, extNotices...)
	}
	return extension, notices, authority, nil
}

// piReducedModeNotice is the downgrade notice printed when the pi extension is not verified: the effective card falls
// from push/auto to pull/manual, emitted through the notice path, never inferred from the static card (DESIGN AC6).
const piReducedModeNotice = "reduced mode: pi extension not verified (%s); effective card pull/manual - leader must run cox wake wait, worker must write cox checkpoint facts at each phase boundary"

// piExtDir is the out-of-tree install location for a worker's pi extension: under the epic's .cox, keyed by story. It is
// deliberately NOT inside the story worktree, so a Pi dispatch never leaves untracked .pi runtime files in the tree the
// worker commits from.
func piExtDir(epic, story string) string { return filepath.Join(epic, ".cox", "pi-ext", story) }

// resolvePiExtension installs the packaged pi extension into installDir (out-of-tree) with the epic binding and verifies
// it. On success it returns the -e entry path (push/auto). On install/hash failure it returns entry="" plus a
// reduced-mode downgrade notice, so pi launches without the extension in pull/manual mode rather than claiming the
// static push/auto card over an unverified extension.
func resolvePiExtension(installDir, epic string) (entry string, notices []string) {
	if _, err := pi.InstallExtension(installDir, epic); err != nil {
		return "", []string{fmt.Sprintf(piReducedModeNotice, "install failed: "+err.Error())}
	}
	entry, ok := pi.VerifyExtension(installDir)
	if !ok {
		return "", []string{fmt.Sprintf(piReducedModeNotice, "hash unverified")}
	}
	// Clear any stale activation marker so the post-spawn handshake confirms THIS launch, not a prior run's.
	_ = pi.ClearActivation(installDir)
	return entry, nil
}

// piActivateWait bounds the post-spawn extension-activation handshake. COX_PI_ACTIVATE_WAIT (a Go duration) overrides
// the 20s default; 0 checks once without waiting.
func piActivateWait() time.Duration {
	if v := strings.TrimSpace(os.Getenv("COX_PI_ACTIVATE_WAIT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 20 * time.Second
}

// confirmPiActivation is the runtime half of the extension guarantee: after spawn it waits (bounded) for the extension
// to signal it actually loaded (the activation marker). It returns confirmed=true for a non-pi launch or one already in
// reduced mode (extension==""), and otherwise whether Pi confirmed. When unconfirmed it returns a reduced-mode notice,
// so a load/version/API failure downgrades the effective card to pull/manual instead of silently claiming push/auto.
func confirmPiActivation(harnessName, extension, installDir string) (confirmed bool, notice string) {
	if harnessName != "pi" || extension == "" {
		return true, ""
	}
	if pi.WaitActivation(installDir, piActivateWait()) {
		return true, ""
	}
	return false, fmt.Sprintf(piReducedModeNotice, "extension did not confirm activation at startup")
}

// modelHarnessMismatch reports whether `model` belongs to a different vendor than `harness` expects (a claude-family
// model at a codex worker, or a codex-family model at a claude worker), with a message naming both. A model whose
// vendor is unrecognised, or a harness that is neither claude nor codex, is never flagged.
func modelHarnessMismatch(harness, model string) (bool, string) {
	v := modelVendor(model)
	if v == "" || v == harness {
		return false, ""
	}
	if (harness == "claude" && v == "codex") || (harness == "codex" && v == "claude") {
		return true, fmt.Sprintf("model %q is a %s model but harness is %q", model, v, harness)
	}
	return false, ""
}

// leaderPath / runPath / sessionPath live under <epic>/.cox.
func leaderPath(epicDir string) string   { return filepath.Join(epicDir, controlDir, "leader") }
func runPath(epicDir string) string      { return filepath.Join(epicDir, controlDir, "run") }
func watchPidPath(epicDir string) string { return filepath.Join(epicDir, controlDir, "watch.pid") }

func sessionPath(epicDir, story string) string {
	return filepath.Join(epicDir, controlDir, "sessions", story+".json")
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeCoxFile(epicDir, name, content string) error {
	dir := filepath.Join(epicDir, controlDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// readLeader returns the recorded leader terminal handle, or "".
func readLeader(epicDir string) string { return readTrimmed(leaderPath(epicDir)) }

// resolveRun returns the run id from .cox/run or the ORCA_RUN_ID env, else "".
func resolveRun(epicDir string) string {
	if r := readTrimmed(runPath(epicDir)); r != "" {
		return r
	}
	return os.Getenv("ORCA_RUN_ID")
}

// newBackend builds an Orca client for the epic's run. It returns (nil, "") when no run is configured, so callers that
// only need the durable event log still work without a live backend.
func newBackend(epicDir string) (backend.Backend, string) {
	run := resolveRun(epicDir)
	if run == "" {
		return nil, ""
	}
	c := orca.New(run)
	c.Plane = resolveOrcaPlane(epicDir)
	c.Epic = epicDir // so ringReady/Composer consult the harness-owned busy record (DESIGN wave-3)
	c.LaunchConfirmS = loadPolicyQuiet(epicDir).LaunchConfirmS()
	if h := os.Getenv("ORCA_TERMINAL_HANDLE"); h != "" {
		c.From = h
	}
	return c, run
}

// resolveOrcaPlane decides the Orca plane for a backend: the COX_PLANE env wins when set (a worker terminal is launched
// with COX_PLANE=terminal), else the epic's resolved policy, else the code default. A policy that cannot be loaded falls
// back to the default rather than blocking backend construction.
func resolveOrcaPlane(epicDir string) string {
	if p := strings.TrimSpace(os.Getenv("COX_PLANE")); p != "" {
		return p
	}
	return loadPolicyQuiet(epicDir).OrcaPlane()
}

func saveSession(epicDir, story string, s backend.Session) error {
	dir := filepath.Join(epicDir, controlDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(sessionPath(epicDir, story), b, 0o644)
}

func loadSession(epicDir, story string) (backend.Session, error) {
	b, err := os.ReadFile(sessionPath(epicDir, story))
	if err != nil {
		return backend.Session{}, err
	}
	var s backend.Session
	if err := json.Unmarshal(b, &s); err != nil {
		return backend.Session{}, err
	}
	s.Story = story // stamp the story so the backend can consult the busy record even for a session persisted before this field existed
	return s, nil
}

// loadAllSessions maps every story with a saved session to it. It delegates to watch.LoadSessions so the watcher's
// per-tick reload (item 1) and the dispatch/reconcile paths read sessions through one implementation.
func loadAllSessions(epicDir string) map[string]backend.Session {
	return watch.LoadSessions(epicDir)
}

// currentAttempt reads the attempt a dispatch should run under from the event log (default 1). After a terminal
// canceled|failed state, a re-dispatch is a fresh attempt, so it returns the folded attempt + 1 - the same N+1 a
// parked story gets on relaunch (control.Relaunch). Every other state (working, input_required, ...) returns the
// folded attempt unchanged, so a status/checkpoint report during a live turn keeps writing for the current attempt.
func currentAttempt(epicDir, story string) int {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return 1
	}
	s := state.Fold(events).Stories[story]
	if s == nil || s.Attempt < 1 {
		return 1
	}
	if s.State == state.Canceled || s.State == state.Failed {
		return s.Attempt + 1
	}
	return s.Attempt
}

// storyMeta is the subset of a story frontmatter dispatch needs.
type storyMeta struct {
	Repo    string
	Harness string
	Model   string
}

// readStoryMeta parses id/repo/agent/harness/model from stories/<id>.md frontmatter (simple key: value).
func readStoryMeta(epicDir, story string) storyMeta {
	var m storyMeta
	b, err := os.ReadFile(filepath.Join(epicDir, "stories", story+".md"))
	if err != nil {
		return m
	}
	inFM := false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "---" {
			if inFM {
				break
			}
			inFM = true
			continue
		}
		if !inFM {
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(strings.SplitN(v, "#", 2)[0])
		switch strings.TrimSpace(k) {
		case "repo":
			m.Repo = v
		case "agent", "harness":
			if m.Harness == "" {
				m.Harness = v
			}
		case "model":
			m.Model = v
		}
	}
	return m
}

// envDuration parses a Go duration (e.g. "120s", "5m") from env var name, falling back to def when unset or unparsable.
func envDuration(name string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "cox: "+format+"\n", args...)
	return 1
}

// processAlive reports whether a process with pid is running (signal 0 probe).
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
