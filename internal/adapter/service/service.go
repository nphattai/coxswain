// Package service is the adapter that runs a project's service script (<ws>/cox/services/<alias>.sh) with the four
// verbs preflight | start | health | stop. cox never contains service logic (plan 5.3, F13): the project owns the
// script, cox only invokes it and interprets its exit code. health is tri-state - a script that is missing or cannot be
// executed is Unknown, not Fail, so a broken adapter is never reported as an unhealthy service.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Health is a tri-state readiness result.
type Health int

const (
	Unknown Health = iota // the script is missing, not executable, or could not run
	Pass                  // the script ran and reported healthy (exit 0)
	Fail                  // the script ran and reported unhealthy (non-zero exit)
)

func (h Health) String() string {
	switch h {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	default:
		return "unknown"
	}
}

// Adapter runs one project's service script.
type Adapter struct {
	Script string   // path to <ws>/cox/services/<alias>.sh
	Env    []string // extra environment (KEY=value) passed to the script, e.g. from the story env file
	run    func(script, verb string, env []string) (int, []byte, error)
}

// New returns an Adapter that shells out to the script.
func New(script string, env []string) *Adapter {
	return &Adapter{Script: script, Env: env, run: runScript}
}

// runnable reports whether the script exists and is a regular executable file. A missing or non-executable script is the
// Unknown case for health, and a real error for the action verbs.
func (a *Adapter) runnable() error {
	fi, err := os.Stat(a.Script)
	if err != nil {
		return fmt.Errorf("service script %s: %w", a.Script, err)
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return fmt.Errorf("service script %s is not executable", a.Script)
	}
	return nil
}

// Preflight, Start, Stop run their verb and return a real error on any non-zero exit or exec failure.
func (a *Adapter) Preflight() error { return a.action("preflight") }
func (a *Adapter) Start() error     { return a.action("start") }
func (a *Adapter) Stop() error      { return a.action("stop") }

func (a *Adapter) action(verb string) error {
	if err := a.runnable(); err != nil {
		return err
	}
	code, out, err := a.run(a.Script, verb, a.Env)
	if err != nil {
		return fmt.Errorf("service %s: %w: %s", verb, err, strings.TrimSpace(string(out)))
	}
	if code != 0 {
		return fmt.Errorf("service %s exited %d: %s", verb, code, strings.TrimSpace(string(out)))
	}
	return nil
}

// Health runs the health verb and maps it to the tri-state: a missing/unrunnable script or an exec error is Unknown;
// exit 0 is Pass; any other exit is Fail.
func (a *Adapter) Health() Health {
	if err := a.runnable(); err != nil {
		return Unknown
	}
	code, _, err := a.run(a.Script, "health", a.Env)
	if err != nil {
		return Unknown
	}
	if code == 0 {
		return Pass
	}
	return Fail
}

// runScript executes the script and returns its exit code, combined output, and an exec error (for a failure to launch
// the process at all, distinct from a non-zero exit).
func runScript(script, verb string, env []string) (int, []byte, error) {
	cmd := exec.Command(script, verb)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, out, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out, nil // ran, non-zero exit
	}
	return -1, out, err // could not run
}
