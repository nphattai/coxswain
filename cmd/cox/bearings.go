package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/orca"
	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/github"
	"github.com/nphattai/coxswain/internal/bearings"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

const bearingsUsage = `usage:
  cox bearings [--reemit] [--source startup|resume|compact|clear] [--harness claude|codex|pi] [--root <ws>] [--json]
  cox bearings curate [--reinforce "<entry>" ...] [--root <ws>] [--json]
  cox bearings deferred [--root <ws>] [--leader <id>]   (the detached forge worker a locked start launches)`

// ghAuthBound bounds the deferred gh auth probe; deferredBound bounds the whole detached worker.
const (
	ghAuthBound   = 30 * time.Second
	deferredBound = 10 * time.Minute
)

// cmdBearings implements `cox bearings`: the leader's one-command session start (internal/bearings), its notes curation
// pass, and the detached deferred worker.
func cmdBearings(args []string) int {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("bearings "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, bearingsUsage) }
	// parse returns the exit code to stop with (-1 = go on): -h is 0, a bad flag or a stray argument is a usage error.
	parse := func() int {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "cox bearings: unexpected argument %q\n%s\n", fs.Arg(0), bearingsUsage)
			return 2
		}
		return -1
	}
	root := fs.String("root", "", "workspace root (default: the workspace above the cwd)")
	asJSON := fs.Bool("json", false, "print JSON")
	switch sub {
	case "":
		reemit := fs.Bool("reemit", false, "this session already took the helm and only lost its context (compact, clear)")
		source := fs.String("source", "", "the harness session-start source: startup | resume | compact | clear")
		harness := fs.String("harness", "claude", "leader harness: claude | codex | pi")
		timeout := fs.Duration("timeout", bearings.DefaultTimeout, "runtime bound for the whole digest")
		if code := parse(); code >= 0 {
			return code
		}
		return runBearings(bearingsWorkspace(*root), *source, *harness, *reemit, *timeout, *asJSON, os.Stdout)
	case "curate":
		var reinforce repoList // a repeatable string flag
		fs.Var(&reinforce, "reinforce", "an entry (or its leading words) this session evidenced; repeatable")
		if code := parse(); code >= 0 {
			return code
		}
		return runCurate(bearingsWorkspace(*root), reinforce, *asJSON)
	case "deferred":
		harness := fs.String("harness", "claude", "leader harness")
		leader := fs.String("leader", "", "the leader identity that started this stage (its results publish only while it holds the lease)")
		if code := parse(); code >= 0 {
			return code
		}
		ws := bearingsWorkspace(*root)
		// Single flight: one deferred worker per workspace; a second one exits quietly.
		unlock, ok := deferredLock(ws)
		if !ok {
			return 0
		}
		defer unlock()
		// A hard backstop past the stage's own wait, so a wedged probe can never outlive the worker's bound for long. It
		// leaves the failed record the next digest prints (firstmate 5842d42: a worker ended at its bound says so and how
		// to rerun), never a silent exit.
		time.AfterFunc(deferredBound+30*time.Second, func() {
			if err := writeDeferredBackstop(ws, deferredBound+30*time.Second); err != nil {
				fmt.Fprintf(os.Stderr, "cox bearings deferred: failed record: %v\n", err)
			}
			os.Exit(3)
		})
		o := bearingsOpts(ws, "", *harness, false)
		if *leader != "" {
			o.LeaderID = *leader
		}
		rep, err := bearings.RunDeferred(o, deferredBound)
		if err != nil {
			return fail("bearings deferred: %v", err)
		}
		fmt.Println(rep)
		return 0
	default:
		fmt.Fprintln(os.Stderr, bearingsUsage)
		return 2
	}
}

// writeDeferredBackstop writes the deferred worker's failed record (internal/bearings DeferredFailed reads and the next
// digest prints it) for a worker the hard backstop is about to end: what happened and the rerun command. The format is
// the one internal/bearings writes for a missed publish.
func writeDeferredBackstop(ws string, bound time.Duration) error {
	path := filepath.Join(ws, bearings.RuntimeDir, bearings.DeferredFailedFile)
	body := strings.Join([]string{
		"state=failed",
		fmt.Sprintf("reason: the deferred worker (pid %d) was still running at its hard bound (%s) and was ended; its results were not published.", os.Getpid(), bound),
		"rerun: cox bearings deferred --root " + ws,
	}, "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// bearingsSessionStart is the leader session-start hook's entry point (cox hook session-start calls it): a startup or
// resume is a full start, a compact or clear re-emits the digest for a session that already took the helm.
func bearingsSessionStart(wsRoot, source, harness string, out io.Writer) int {
	reemit := source == "compact" || source == "clear"
	return runBearings(wsRoot, source, harness, reemit, bearings.DefaultTimeout, false, out)
}

// bearingsWorkspace resolves the workspace: an explicit root, else the cox workspace above the cwd, else the cwd.
func bearingsWorkspace(root string) string {
	if root != "" {
		return root
	}
	if ws, err := findWorkspaceRoot("."); err == nil {
		return ws
	}
	cwd, _ := os.Getwd()
	return cwd
}

// leaderIdentity is this session's leader id: the Orca terminal handle, else the pid of the harness process this
// command runs under (the nearest ancestor named after the harness, firstmate's fm_harness_ancestry_pid). With neither
// there is no stable identity and the digest is read-only.
func leaderIdentity(harness string) string {
	if h := os.Getenv("ORCA_TERMINAL_HANDLE"); h != "" {
		return h
	}
	if pid := harnessAncestor(os.Getppid(), harness); pid > 0 {
		return "pid:" + strconv.Itoa(pid)
	}
	return ""
}

// harnessAncestor walks at most 16 parents up from pid for a process whose command name is the harness.
func harnessAncestor(pid int, harness string) int {
	for i := 0; i < 16 && pid > 1; i++ {
		out, err := exec.Command("ps", "-o", "ppid=", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0
		}
		f := strings.Fields(string(out))
		if len(f) < 2 {
			return 0
		}
		if filepath.Base(f[1]) == harness {
			return pid
		}
		if pid, err = strconv.Atoi(f[0]); err != nil {
			return 0
		}
	}
	return 0
}

// deferredLock takes the workspace's single-flight lock for the deferred worker, non-blocking.
func deferredLock(ws string) (unlock func(), ok bool) {
	dir := filepath.Join(ws, bearings.RuntimeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(filepath.Join(dir, "bearings-deferred.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true
}

// leaderLive reports whether a lease holder is still live: a pid identity by a signal-0 probe, a terminal handle by the
// Orca terminal listing (a handle missing from it, or disconnected, is gone). When Orca cannot be asked at all a handle
// counts as live, so an unverifiable lease makes a read-only session rather than a second writer; the refusal line names
// the lease file to remove once that leader is gone.
func leaderLive() func(string) bool {
	// Orca's terminal listing is run-independent, so it answers even with no active epic.
	terms, err := orca.New("").Terminals()
	listed := err == nil
	return func(id string) bool {
		if pid, ok := strings.CutPrefix(id, "pid:"); ok {
			n, err := strconv.Atoi(pid)
			return err == nil && bearings.PidAlive(n)
		}
		if !listed {
			return true
		}
		for _, t := range terms {
			if t.Handle == id {
				return t.Connected
			}
		}
		return false
	}
}

// bearingsOpts wires the digest to cox's live observers: the backend liveness probe for each story's endpoint, gh auth
// for the forge check, and the forge's merged verdict for a still-working story.
func bearingsOpts(ws, source, harness string, reemit bool) bearings.Opts {
	return bearings.Opts{
		Workspace: ws,
		LeaderID:  leaderIdentity(harness),
		Live:      leaderLive(),
		Harness:   harness,
		Source:    source,
		Reemit:    reemit,
		Endpoint: func(epicDir, story string) (bool, string) {
			b, _ := newBackend(epicDir)
			sess, err := loadSession(epicDir, story)
			if b == nil || err != nil {
				return false, ""
			}
			switch probeLiveness(b, epicDir, story) {
			case backend.Alive:
				return true, sess.Handle
			case backend.Unknown:
				return false, ""
			}
			return false, sess.Handle
		},
		Forge: func() error {
			code, err := bearings.RunBounded(ghAuthBound, "gh", "auth", "status")
			if err != nil {
				return fmt.Errorf("gh auth status: %w", err)
			}
			if code != 0 {
				return fmt.Errorf("gh auth status exited %d (run gh auth login)", code)
			}
			return nil
		},
		StateRead: func(epicDir, story string) (string, error) {
			events, _, err := state.Load(epicDir)
			if err != nil {
				return "", err
			}
			s := state.Fold(events).Stories[story]
			if s == nil {
				return "", nil
			}
			fo := &forgeObserver{now: time.Now(), cache: map[string]state.Observation{}, newForge: func(dir string) forge.Forge { return github.New(dir) }}
			if v, ok := fo.observe(epicDir, s).Value.(forgeVal); ok && v.PR != nil && v.Merged == "true" {
				return fmt.Sprintf("PR #%d merged while the story is still %s - reconcile it", *v.PR, s.State), nil
			}
			return "", nil
		},
	}
}

func runBearings(ws, source, harness string, reemit bool, timeout time.Duration, asJSON bool, out io.Writer) int {
	o := bearingsOpts(ws, source, harness, reemit)
	o.Timeout = timeout
	// The deferred stage runs in a detached worker, so the hook that runs this digest never waits for the network.
	o.Forge, o.StateRead = nil, nil
	o.Detach = func() error { return detachDeferred(ws, harness, o.LeaderID) }
	d, err := bearings.Compose(o)
	if err != nil {
		return fail("bearings: %v", err)
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"text": d.Text, "read_only": d.ReadOnly, "truncated": d.Truncated})
		return 0
	}
	fmt.Fprint(out, d.Text)
	return 0
}

// detachDeferred starts `cox bearings deferred` in its own session, detached from this process and its output. The
// binary is $COX_BIN when set, else this executable. Under a test binary it refuses: re-executing cox.test would run
// the whole suite again as an orphan that spawns more of itself.
func detachDeferred(ws, harness, leader string) error {
	if testing.Testing() {
		return errors.New("deferred worker not started under a test binary")
	}
	self := os.Getenv("COX_BIN")
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			return err
		}
	}
	cmd := exec.Command(self, "bearings", "deferred", "--root", ws, "--harness", harness, "--leader", leader)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func runCurate(ws string, reinforce []string, asJSON bool) int {
	r, err := bearings.Curate(ws, time.Now(), reinforce)
	if err != nil {
		return fail("bearings curate: %v", err)
	}
	if asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		return 0
	}
	fmt.Printf("startup memory before: %d of %d estimated tokens (%s)\n", r.Before.Total, r.Before.Budget, r.Before.Status)
	fmt.Printf("startup memory after:  %d of %d estimated tokens (%s)\n", r.After.Total, r.After.Budget, r.After.Status)
	for _, f := range workspace.NotesFiles {
		fmt.Printf("%s: %s\n", filepath.Join(workspace.ControlDir, workspace.NotesDir, f), strings.Join(r.Actions[f], ", "))
	}
	for _, a := range r.Archived {
		fmt.Println("archived " + a)
	}
	for _, e := range r.Exceptions {
		fmt.Println("exception: " + e)
	}
	if r.Decision != "" {
		fmt.Println("captain decision: " + r.Decision)
	}
	if r.ResetSafe {
		fmt.Println("reset-safe: yes - nothing this session knew has been lost from startup memory (this is not a claim that the durable records are correct)")
	} else {
		fmt.Println("reset-safe: no - resolve the exception or decision above first")
	}
	if len(r.Exceptions) > 0 {
		return 1
	}
	return 0
}
