package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/boundexec"
	"github.com/nphattai/coxswain/internal/workspace"
)

// orcaRepoBound bounds each `orca repo show|add` call (doctor's `orca status` uses 10s too).
const orcaRepoBound = 10 * time.Second

// orcaRepoState is whether Orca has a checkout registered (B-34b): an epic worktree for an unregistered repo fails late
// in `orca worktree create` with repo_not_found.
type orcaRepoState int

const (
	orcaRepoUnknown      orcaRepoState = iota // orca missing, unreachable, or an answer cox cannot read
	orcaRepoRegistered                        // `orca repo show` found it
	orcaRepoUnregistered                      // `orca repo show` answered repo_not_found
)

// orcaRepoCall runs `orca <args> --json` bounded and returns Orca's envelope (ok, error code/message). A launch failure,
// a timeout or unparseable output is an error.
func orcaRepoCall(args ...string) (ok bool, code, msg string, err error) {
	var out bytes.Buffer
	cmd := exec.Command("orca", append(args, "--json")...)
	cmd.Stdout = &out
	if _, err := boundexec.Run(context.Background(), orcaRepoBound, cmd); err != nil {
		return false, "", "", err
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		return false, "", "", fmt.Errorf("orca %s: unreadable output %q", strings.Join(args, " "), strings.TrimSpace(out.String()))
	}
	return env.OK, env.Error.Code, env.Error.Message, nil
}

// orcaRepoStatus asks Orca whether the checkout at path is registered.
func orcaRepoStatus(path string) orcaRepoState {
	ok, code, _, err := orcaRepoCall("repo", "show", "--repo", "path:"+path)
	switch {
	case err != nil:
		return orcaRepoUnknown
	case ok:
		return orcaRepoRegistered
	case code == "repo_not_found":
		return orcaRepoUnregistered
	default:
		return orcaRepoUnknown
	}
}

// orcaRepoAdd registers the checkout at path with Orca.
func orcaRepoAdd(path string) error {
	ok, code, msg, err := orcaRepoCall("repo", "add", "--path", path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("orca repo add: %s (%s)", msg, code)
	}
	return nil
}

// orcaRepoFix is the command that registers path, named in every refusal and doctor issue.
func orcaRepoFix(path string) string { return "orca repo add --path " + path }

// unregisteredOrcaRepos names each path-backed repo of the workspace that Orca positively reports unregistered, with
// the fix. A repo addressed by a backend name, or an unknown answer (no orca, unreachable), is not reported.
func unregisteredOrcaRepos(repos []workspace.Repo) []string {
	var out []string
	for _, r := range repos {
		if r.Path == "" {
			continue
		}
		if orcaRepoStatus(r.Path) == orcaRepoUnregistered {
			out = append(out, fmt.Sprintf("repo %s (%s) is not registered with Orca; run: %s", r.Alias, r.Path, orcaRepoFix(r.Path)))
		}
	}
	return out
}
