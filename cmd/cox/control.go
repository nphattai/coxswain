package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/control"
	"github.com/nphattai/coxswain/internal/state"
)

// harnessFor returns the adapter for a recorded harness name on the control path. An unknown name is refused, never
// guessed at (firstmate fm-control.sh: "no verified control mechanics"); an empty name is a story dispatched before
// harness frontmatter existed, which dispatch launched as claude.
func harnessFor(story, name string) (harness.Harness, error) {
	if h, ok := registry.Adapter(nonEmpty(name, "claude")); ok {
		return h, nil
	}
	return nil, fmt.Errorf("story %s records harness '%s', which has no verified control mechanics; cox control refuses to guess an interrupt key or stop", story, name)
}

// resumeRefusal is firstmate's reason `resume` is not a control verb, with cox's two deterministic alternatives.
const resumeRefusal = "'resume' is not a control verb: resuming an exited agent is not deterministic across the verified adapters (codex needs a session id printed at exit, and claude and pi have no verified pane-resume contract). Use 'relaunch' (cox control <story> relaunch), which carries the brief plus a progress note into a fresh agent on any adapter, or `cox story resume`."

func wtFilePath(epicDir, story string) string { return state.WorktreePath(epicDir, story) }

// readWorktree returns the story's recorded worktree path, or "" when unset. It accepts both the JSON worktree record
// and the legacy plain-path text through the one shared reader (state.ReadWorktreeRecord), so an in-flight epic is
// never blocked.
func readWorktree(epicDir, story string) string {
	return state.ReadWorktree(epicDir, story)
}

// saveWorktree writes the story's worktree record by temp + rename, stamped with the attempt. A write whose attempt is
// LOWER than the attempt already on disk is dropped with a stderr note, so a stale writer never clobbers the newer
// worktree record (DESIGN wave-2 item 7).
func saveWorktree(epicDir, story, path string, attempt int) error {
	if cur, _ := state.ReadWorktreeRecord(epicDir, story); cur.Attempt > 0 && attempt < cur.Attempt {
		fmt.Fprintf(os.Stderr, "cox: note: dropped worktree write for %s (attempt %d < %d on disk)\n", story, attempt, cur.Attempt)
		return nil
	}
	dir := filepath.Join(epicDir, controlDir, "wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(state.WorktreeRecord{Path: path, Attempt: attempt})
	if err != nil {
		return err
	}
	return state.AtomicWrite(wtFilePath(epicDir, story), b, 0o644)
}

// cmdControl implements `cox control <story> interrupt|park|relaunch [--note <progress>] --epic <dir>`.
func cmdControl(args []string) int {
	story, rest := onePositional(args)
	verb, rest := onePositional(rest)
	fs := flag.NewFlagSet("control", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	note := fs.String("note", "", "progress note (relaunch)")
	allowUnsandboxed := fs.Bool("allow-unsandboxed", false, "authorize relaunching an unsandboxed harness (not a sandbox)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || story == "" || verb == "" {
		return usageErr("cox control <story> interrupt|park|relaunch [--note <progress>] --epic <dir>")
	}
	// Refusals happen before any effect (firstmate fm-control.sh argument parsing): an unknown verb, resume, and a
	// relaunch-only flag on another verb never reach the backend.
	switch verb {
	case "interrupt", "park", "relaunch":
	case "resume":
		fmt.Fprintln(os.Stderr, "cox: "+resumeRefusal)
		return 2
	default:
		return usageErr("cox control <story> interrupt|park|relaunch")
	}
	if verb != "relaunch" {
		relaunchOnly := false
		fs.Visit(func(f *flag.Flag) { relaunchOnly = relaunchOnly || f.Name == "note" || f.Name == "allow-unsandboxed" })
		if relaunchOnly {
			return fail("--note and --allow-unsandboxed apply to 'relaunch' only")
		}
	}
	meta := readStoryMeta(*epicDir, story)
	h, err := harnessFor(story, meta.Harness)
	if err != nil {
		return fail("%v", err)
	}
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("control needs a live backend: set ORCA_RUN_ID or %s/.cox/run", *epicDir)
	}
	ctl := &control.Controller{EpicDir: *epicDir, Backend: b, Harness: h}

	switch verb {
	case "interrupt":
		sess, err := loadSession(*epicDir, story)
		if err != nil {
			return fail("no session for %s: %v", story, err)
		}
		if err := ctl.Interrupt(story, sess); err != nil {
			return fail("%v", err)
		}
	case "park":
		sess, err := loadSession(*epicDir, story)
		if err != nil {
			return fail("no session for %s: %v", story, err)
		}
		if err := ctl.Park(story, readWorktree(*epicDir, story), sess); err != nil {
			return fail("%v", err)
		}
	case "relaunch":
		pol := loadPolicyQuiet(*epicDir)
		hname := nonEmpty(meta.Harness, "claude")
		model := resolveWorkerModel(pol, hname, meta.Model)
		wtPath := readWorktree(*epicDir, story)
		storyPath := filepath.Join(*epicDir, "stories", story+".md")
		if err := piPreSpawnValidate(hname, model, ""); err != nil {
			return fail("%v", err)
		}
		// Same authorization + extension flow as dispatch: an unsandboxed relaunch is refused without
		// --allow-unsandboxed, and a pi relaunch keeps push/auto via its verified extension (or downgrades via notice).
		extension, notices, authority, err := authorizeWorker(hname, *epicDir, story, *allowUnsandboxed, true)
		if err != nil {
			return fail("%v", err)
		}
		for _, n := range notices {
			fmt.Println(n)
		}
		if err := registry.PrepareWorktree(hname, wtPath); err != nil {
			return fail("prepare worktree trust: %v", err)
		}
		argv, err := registry.LaunchArgs(hname, harness.Launch{
			Role: harness.RoleWorker, Worktree: wtPath, Model: model, Extension: extension,
			Flags: pol.LaunchFlags(hname), Brief: harness.Brief{StoryPath: storyPath, Note: *note},
		})
		if err != nil {
			return fail("compose launch argv: %v", err)
		}
		var extra map[string]any
		if authority == "flag" {
			extra = map[string]any{"unsandboxed": map[string]any{"authorized_by": "--allow-unsandboxed", "harness": hname}}
		}
		spec := backend.HarnessSpec{Name: hname, Model: model, LaunchFlags: pol.LaunchFlags(hname), Argv: argv}
		// Retire the prior incarnation's worker hooks, then re-arm the busy record for the relaunched one (DESIGN wave-2
		// item 6), in that order inside Relaunch after the prior agent settled: a fresh gen invalidates any late event
		// from the prior attempt and reaches the new worker via COX_BUSY_GEN.
		ctl.Unwire = func() error { return unwireWorkerBusy(hname, wtPath) }
		ctl.Arm = func() (string, error) { return armWorkerBusy(*epicDir, story, hname, wtPath, pol) }
		prior, _ := loadSession(*epicDir, story) // previous attempt's terminal, closed before the new spawn (zero value when none)
		sess, err := ctl.Relaunch(story, wtPath, *note, prior, spec, extra)
		if err != nil {
			return fail("%v", err)
		}
		if _, notice := confirmPiActivation(hname, extension, piExtDir(*epicDir, story)); notice != "" {
			fmt.Println(notice)
		}
		// The relaunch appended the working event at the new attempt; stamp the session with it so a late write from the
		// prior attempt is dropped (item 7).
		if err := saveSession(*epicDir, story, sess, currentAttempt(*epicDir, story)); err != nil {
			return fail("save session: %v", err)
		}
	}
	fmt.Printf("%s: %s ok\n", verb, story)
	return 0
}

func nonEmpty(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
