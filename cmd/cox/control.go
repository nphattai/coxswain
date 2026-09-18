package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/control"
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

func readWorktree(epicDir, story string) string { return readTrimmed(wtFilePath(epicDir, story)) }

func saveWorktree(epicDir, story, path string) error {
	dir := filepath.Join(epicDir, controlDir, "wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(wtFilePath(epicDir, story), []byte(path), 0o644)
}

// cmdControl implements `cox control <story> interrupt|park|relaunch [--note <progress>] --epic <dir>`.
func cmdControl(args []string) int {
	story, rest := onePositional(args)
	verb, rest := onePositional(rest)
	fs := flag.NewFlagSet("control", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	note := fs.String("note", "", "progress note (relaunch)")
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
		spec := backend.HarnessSpec{Name: hname, Model: resolveWorkerModel(pol, hname, meta.Model), LaunchFlags: pol.LaunchFlags(hname)}
		prior, _ := loadSession(*epicDir, story) // previous attempt's terminal, closed before the new spawn (zero value when none)
		sess, err := ctl.Relaunch(story, readWorktree(*epicDir, story), *note, prior, spec, nil)
		if err != nil {
			return fail("%v", err)
		}
		if err := saveSession(*epicDir, story, sess); err != nil {
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
