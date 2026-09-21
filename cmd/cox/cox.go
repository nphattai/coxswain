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
