package bearings

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const agentsBaselineFile = "bearings-agents-baseline"

// agentsHash is the workspace AGENTS.md's "sha256:<hex>", "" when it cannot be read.
func agentsHash(ws string) string {
	b, err := os.ReadFile(filepath.Join(ws, "AGENTS.md"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeAgentsBaseline records the instructions a true start began with, keyed to the leader identity, only after the
// digest completed. It describes the start, not the latest re-emit, so every later drifted compaction refreshes again.
func writeAgentsBaseline(ws, id, hash string) error {
	return writeAtomic(filepath.Join(ws, ControlDir, agentsBaselineFile), id+"\n"+hash+"\n")
}

// agentsDrifted: a missing baseline, a baseline for another leader identity, or a changed hash is drift.
func agentsDrifted(ws, id string) bool {
	fi, err := os.Lstat(filepath.Join(ws, ControlDir, agentsBaselineFile))
	if err != nil || !fi.Mode().IsRegular() {
		return true
	}
	b, err := os.ReadFile(filepath.Join(ws, ControlDir, agentsBaselineFile))
	if err != nil {
		return true
	}
	ls := strings.Split(string(b), "\n")
	cur := agentsHash(ws)
	return cur == "" || len(ls) < 2 || ls[0] != id || ls[1] != cur
}

// printAgentsRefresh re-emits the current AGENTS.md before the bulky digest when the harness rebuilt its context from
// a stale instruction cache: only Pi compaction (Claude re-reads on reset; Codex has no reset delivery path), and only
// when the baseline drifted. It compares against this session's own identity, so a read-only session refreshes too.
func printAgentsRefresh(p *printer, o Opts) {
	if o.Harness != "pi" || o.Source != "compact" || !agentsDrifted(o.Workspace, o.LeaderID) {
		return
	}
	p.section("CURRENT AGENTS.md - INSTRUCTION REFRESH")
	b, err := os.ReadFile(filepath.Join(o.Workspace, "AGENTS.md"))
	if err != nil {
		p.line("The original AGENTS.md baseline no longer matches, but the current file is absent.")
		return
	}
	p.lines("The complete on-disk AGENTS.md below supersedes the instruction copy this session",
		"started with. Apply it as the current leader instruction contract.", "")
	p.raw(strings.TrimRight(string(b), "\n") + "\n")
}
