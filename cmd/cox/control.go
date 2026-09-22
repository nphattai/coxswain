package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/control"
	"github.com/nphattai/coxswain/internal/state"
)

// harnessFor returns the adapter for a harness name for the control path, falling back to claude for an unknown name
// (a control verb runs against a story whose harness was already validated at dispatch). Dispatch itself enforces the
// adapter via registry.Notices, so an unadaptered harness never reaches here.
func harnessFor(name string) harness.Harness {
	if h, ok := registry.Adapter(name); ok {
		return h
	}
	return registry.Default()
}

func wtFilePath(epicDir, story string) string {
	return filepath.Join(epicDir, controlDir, "wt", story)
}

// worktreeFile is the on-disk worktree record: the checkout path plus the attempt that wrote it (DESIGN wave-2 item 7),
// so a late writer from a prior incarnation can be dropped. A legacy file is the bare path text, which readWorktree
// still parses.
type worktreeFile struct {
	Path    string `json:"path"`
	Attempt int    `json:"attempt"`
}

// readWorktree returns the story's recorded worktree path, or "" when unset. It accepts both the JSON worktree record
// and the legacy plain-path text (a file written before this change), so an in-flight epic is never blocked.
func readWorktree(epicDir, story string) string {
	wt, _ := readWorktreeRecord(epicDir, story)
	return wt
}

// readWorktreeRecord returns the worktree path and the attempt that wrote it (0 for a legacy plain-path file), and
// whether a record was read at all.
func readWorktreeRecord(epicDir, story string) (path string, attempt int) {
	raw := readTrimmed(wtFilePath(epicDir, story))
	if raw == "" {
		return "", 0
	}
	if strings.HasPrefix(raw, "{") {
		var f worktreeFile
		if json.Unmarshal([]byte(raw), &f) == nil && f.Path != "" {
			return f.Path, f.Attempt
		}
		return "", 0
	}
	return raw, 0
}

// saveWorktree writes the story's worktree record by temp + rename, stamped with the attempt. A write whose attempt is
// LOWER than the attempt already on disk is dropped with a stderr note, so a stale writer never clobbers the newer
// worktree record (DESIGN wave-2 item 7).
func saveWorktree(epicDir, story, path string, attempt int) error {
	if _, cur := readWorktreeRecord(epicDir, story); cur > 0 && attempt < cur {
		fmt.Fprintf(os.Stderr, "cox: note: dropped worktree write for %s (attempt %d < %d on disk)\n", story, attempt, cur)
		return nil
	}
	dir := filepath.Join(epicDir, controlDir, "wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(worktreeFile{Path: path, Attempt: attempt})
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
	b, _ := newBackend(*epicDir)
	if b == nil {
		return fail("control needs a live backend: set ORCA_RUN_ID or %s/.cox/run", *epicDir)
	}
	meta := readStoryMeta(*epicDir, story)
	ctl := &control.Controller{EpicDir: *epicDir, Backend: b, Harness: harnessFor(meta.Harness)}

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
		// Re-arm the busy record for the relaunched incarnation (DESIGN wave-2 item 6): a fresh gen invalidates any late
		// event from the prior attempt and reaches the new worker via COX_BUSY_GEN, and re-writes its worker busy hooks.
		g, err := armWorkerBusy(*epicDir, story, hname, wtPath, pol)
		if err != nil {
			return fail("arm busy state: %v", err)
		}
		spec.BusyGen = g
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
	default:
		return usageErr("cox control <story> interrupt|park|relaunch")
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
