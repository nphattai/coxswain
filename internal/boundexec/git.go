package boundexec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Git call bounds, one per verb class (fm_exec_timed at every external call, ac2ed3b). Every git exec cox makes on a
// dispatch or supervision path (the orca and herdr adapters, internal/worktree) goes through Git, so a git blocked on a
// lock, a stalled remote or a credential helper never hangs the caller. They are variables only so a test can shorten
// them.
var (
	// GitReadBound bounds a local read (rev-parse, rev-list, show-ref, merge-base, status, log, diff, show): 10s, the
	// orca adapter's ReadBound.
	GitReadBound = 10 * time.Second
	// GitActBound bounds a local ref write or any verb not listed (branch, config, update-ref, ...): 30s, the orca
	// adapter's ActBound.
	GitActBound = 30 * time.Second
	// GitTreeBound bounds a verb that talks to a remote or rewrites a whole tree (fetch, ls-remote, switch, reset,
	// worktree, checkout, ...): 120s, the orca adapter's WorktreeBound.
	GitTreeBound = 120 * time.Second
)

var gitReads = map[string]bool{
	"rev-parse": true, "rev-list": true, "show-ref": true, "merge-base": true, "status": true, "log": true,
	"diff": true, "show": true, "cat-file": true, "for-each-ref": true,
}

var gitTree = map[string]bool{
	"fetch": true, "ls-remote": true, "pull": true, "push": true, "clone": true, "switch": true, "checkout": true,
	"reset": true, "worktree": true, "merge": true, "rebase": true,
}

// GitBound is the bound of one `git <args>` by its verb class; leading global options (-C <dir>, -c <k=v>) are skipped.
func GitBound(args []string) time.Duration {
	switch v := gitVerb(args); {
	case gitReads[v]:
		return GitReadBound
	case gitTree[v]:
		return GitTreeBound
	}
	return GitActBound
}

// Git runs `git <args>` bounded by GitBound and returns its stdout. git never prompts (GIT_TERMINAL_PROMPT=0): cox runs
// non-interactively, so a credential prompt must fail rather than wait for the bound.
func Git(args ...string) ([]byte, error) {
	var out bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdout = &out
	bound := GitBound(args)
	code, err := Run(context.Background(), bound, cmd)
	switch {
	case err != nil:
		return out.Bytes(), err
	case code == ExitTimeout:
		return out.Bytes(), fmt.Errorf("git %s: timed out after %s", gitVerb(args), bound)
	case code != 0:
		return out.Bytes(), fmt.Errorf("exit status %d", code)
	}
	return out.Bytes(), nil
}

// gitVerb is the git verb: the first argument past the global options, "" when there is none.
func gitVerb(args []string) string {
	for len(args) >= 2 && (args[0] == "-C" || args[0] == "-c") {
		args = args[2:]
	}
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
