package bearings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ControlDir is the workspace-level runtime directory holding the leader lease and the digest's own records (never
// committed; the epic-level .cox/ is a different directory).
const ControlDir = ".cox"

const (
	leaseFile     = "leader-lease"
	leaseLockFile = "leader-lease.lock"
)

// leaseResult is one lease acquisition: ok, the line the digest prints, and whether the refusal was a live holder
// (as opposed to a lease that could not be written or verified).
type leaseResult struct {
	ok       bool
	line     string
	liveHeld bool
}

// ErrLeaseWrite is a lease that could not be published; the session operates read-only.
var ErrLeaseWrite = errors.New("cannot write leader lease")

// Acquire takes the workspace's leader lease for id (firstmate's fleet lock, bin/fm-lock.sh): a free lease, a lease
// id already holds, or a lease whose holder is no longer live is taken; a live competing holder refuses. Acquisition
// is serialised by flock, so concurrent acquirers produce exactly one winner. live nil treats every holder as live.
func Acquire(ws, id string, live func(string) bool) (bool, error) {
	r := acquire(ws, id, live)
	if !r.ok && !r.liveHeld {
		return false, fmt.Errorf("%w: %s", ErrLeaseWrite, r.line)
	}
	return r.ok, nil
}

func acquire(ws, id string, live func(string) bool) leaseResult {
	if id == "" {
		return leaseResult{line: "error: cannot write leader lease - this session has no leader identity (ORCA_TERMINAL_HANDLE); operate read-only until resolved"}
	}
	fail := func(err error) leaseResult {
		return leaseResult{line: fmt.Sprintf("error: cannot write leader lease (%v); operate read-only until resolved", err)}
	}
	dir := filepath.Join(ws, ControlDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(err)
	}
	lf, err := os.OpenFile(filepath.Join(dir, leaseLockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fail(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fail(err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	holder := leaseHolder(ws)
	if holder != "" && holder != id && (live == nil || live(holder)) {
		return leaseResult{liveHeld: true, line: fmt.Sprintf("error: another live leader holds the lease (%s); operate read-only until resolved", holder)}
	}
	if holder != id {
		if err := writeAtomic(filepath.Join(dir, leaseFile), id+"\n"); err != nil {
			return fail(err)
		}
	}
	return leaseResult{ok: true, line: "lease acquired: " + id}
}

// leaseHolder is the identity recorded in the lease, "" when none.
func leaseHolder(ws string) string {
	b, err := os.ReadFile(filepath.Join(ws, ControlDir, leaseFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}
