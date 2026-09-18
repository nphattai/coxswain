// Package verify runs the `check` an arena role attached to an epic-blocking or significant claim (ADR 0013) and records
// pass, fail, or unknown. A check is either a runnable command (from a tight allowlist: go test/vet, git show/log/grep/
// diff, grep, rg, and cox's read-only verbs) or an assertion `alias/path:line@sha == "<text>"`. Commands run in the
// role's read-only worktree, or a temporary detached worktree at the cited sha when that worktree is already closed,
// under a 5-minute timeout with the network off (GOPROXY=off). A command outside the allowlist, a timeout, or a worktree
// that cannot be built is `unknown`, never a silent pass: the leader still has to look. Results are written to
// reports/arena/verify-round-N.json for synth to fold into the synthesis `verified` column.
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/arena/cite"
	"github.com/nphattai/coxswain/internal/arena/report"
)

// timeout bounds one check. ponytail: a flat 5-minute ceiling per check (ADR 0013); a per-check budget is not worth it
// until a real check needs longer.
const timeout = 5 * time.Minute

// Status is a check outcome.
const (
	Pass    = "pass"
	Fail    = "fail"
	Unknown = "unknown"
)

// Result is one verified claim.
type Result struct {
	Role   string `json:"role"`
	Claim  string `json:"claim"`
	Check  string `json:"check"`
	Status string `json:"status"` // pass | fail | unknown
	Detail string `json:"detail"`
}

// Round is the written verify-round-N.json.
type Round struct {
	Round      int      `json:"round"`
	VerifiedAt string   `json:"verified_at"`
	Results    []Result `json:"results"`
}

// roundFileRe matches reports/arena/round-<n>-<role>.md and captures the role.
var roundFileRe = regexp.MustCompile(`^round-(\d+)-([a-z]+)\.md$`)

// Run verifies every claim that carries a check across the round's role reports, writes reports/arena/verify-round-N.json,
// and returns the round. A claim with no check is skipped (only epic-blocking/significant claims must carry one). Run
// returns an error only when the reports dir cannot be read or the JSON cannot be written; a failing or unbuildable check
// is a Result, not an error.
func Run(epicDir string, round int) (Round, error) {
	dir := filepath.Join(epicDir, "reports", "arena")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Round{}, fmt.Errorf("read arena reports: %w", err)
	}
	out := Round{Round: round, VerifiedAt: time.Now().UTC().Format(time.RFC3339)}
	prefix := fmt.Sprintf("round-%d-", round)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		m := roundFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		role := m[2]
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return Round{}, err
		}
		for _, c := range report.Parse(string(b)).Claims {
			if strings.TrimSpace(c.Check) == "" {
				continue
			}
			status, detail := verifyClaim(epicDir, role, c)
			out.Results = append(out.Results, Result{
				Role: role, Claim: c.Claim, Check: c.Check, Status: status, Detail: detail,
			})
		}
	}
	if err := writeJSON(filepath.Join(dir, fmt.Sprintf("verify-round-%d.json", round)), out); err != nil {
		return Round{}, err
	}
	return out, nil
}

// verifyClaim runs one claim's check and returns its status and a short detail. A command check always runs in a
// temporary detached worktree at the cited sha, never in the leader checkout (captain ruling 2026-09-16): the leader's
// working tree must not be mutated or read by an arena check. A worktree that cannot be built is unknown.
func verifyClaim(epicDir, role string, c report.Claim) (status, detail string) {
	// An assertion needs no worktree: it reads a blob at a sha with git show.
	if cit, want, ok := parseAssertion(c.Check); ok {
		return verifyAssertion(epicDir, cit, want)
	}
	dir, cleanup, st, dt := detachedWorktree(epicDir, c)
	if st != "" {
		return st, dt
	}
	defer cleanup()
	alias := ""
	if cit, ok := firstCitation(c.Evidence); ok {
		alias = cit.Alias
	}
	return runCommand(dir, c.Check, alias)
}

// assertionRe matches `<citation> == "<expected text>"`.
var assertionRe = regexp.MustCompile(`^\s*(\S+)\s*==\s*"(.*)"\s*$`)

// parseAssertion recognizes the assertion check form and returns the citation and expected text.
func parseAssertion(check string) (cite.Citation, string, bool) {
	m := assertionRe.FindStringSubmatch(check)
	if m == nil {
		return cite.Citation{}, "", false
	}
	cit, ok := cite.Parse(m[1])
	if !ok || cit.SHA == "" {
		return cite.Citation{}, "", false
	}
	return cit, m[2], true
}

// verifyAssertion resolves the cited line at its sha and compares it (trimmed) to the expected text.
func verifyAssertion(epicDir string, cit cite.Citation, want string) (status, detail string) {
	repo, err := cite.RepoDir(epicDir, cit.Alias)
	if err != nil {
		return Unknown, err.Error()
	}
	out, err := exec.Command("git", "-C", repo, "show", cit.SHA+":"+cit.Path).Output()
	if err != nil {
		return Unknown, fmt.Sprintf("git show %s:%s failed", shortSHA(cit.SHA), cit.Path)
	}
	lines := strings.Split(string(out), "\n")
	if cit.Line < 1 || cit.Line > len(lines) {
		return Unknown, fmt.Sprintf("line %d out of range", cit.Line)
	}
	got := strings.TrimSpace(lines[cit.Line-1])
	if got == strings.TrimSpace(want) {
		return Pass, "assertion holds"
	}
	return Fail, fmt.Sprintf("want %q, got %q", want, got)
}

// runCommand tokenizes and runs a command check under the allowlist, timeout, and no-network env. It returns pass on
// exit 0, fail on a non-zero exit, and unknown when the command is outside the allowlist, cannot be tokenized, or times
// out. A check runs at the cited repo's root, so a path argument carrying the claim's own `<alias>/` prefix (the alias a
// citation needs, but a command does not) is stripped first: `grep -n x cox/internal/a.go` runs as `grep -n x
// internal/a.go` (first live v3 run, 2026-09-16). Only the claim's own alias is stripped; another repo's prefix is left
// alone (the check would not resolve in this worktree anyway, and the leader should see that).
func runCommand(dir, check, alias string) (status, detail string) {
	fields, err := shellFields(check)
	if err != nil {
		return Unknown, "unparseable check: " + err.Error()
	}
	if alias != "" {
		prefix := alias + "/"
		for i, f := range fields {
			if strings.HasPrefix(f, prefix) {
				fields[i] = strings.TrimPrefix(f, prefix)
			}
		}
	}
	if !allowed(fields) {
		return Unknown, "command not in allowlist: " + fields[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	cmd.Dir = dir
	cmd.Env = noNetworkEnv()
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return Unknown, "timeout after 5m"
	}
	if err != nil {
		return Fail, snippet(out)
	}
	return Pass, "exit 0"
}

// allowed reports whether a tokenized command is on the ADR 0013 read-only allowlist. The first token (and, for go/git/
// cox, its subcommand) is matched; anything else is refused so the check runs nothing the allowlist did not name.
func allowed(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "grep", "rg":
		return true
	case "go":
		return len(fields) >= 2 && (fields[1] == "test" || fields[1] == "vet")
	case "git":
		return len(fields) >= 2 && oneOf(fields[1], "show", "log", "grep", "diff")
	case "cox":
		if len(fields) >= 3 && fields[1] == "arena" && fields[2] == "check" {
			return true
		}
		return len(fields) >= 2 && oneOf(fields[1], "state", "doctor", "route", "quota")
	}
	return false
}

func oneOf(s string, opts ...string) bool {
	for _, o := range opts {
		if s == o {
			return true
		}
	}
	return false
}

// detachedWorktree builds a temporary git worktree detached at the sha of the claim's first evidence citation (removed
// by cleanup), so a command check runs against exactly the commit the claim cites and never touches the leader checkout
// (captain ruling 2026-09-16). It returns an unknown status when there is no cited sha or the worktree cannot be built.
// cleanup is always safe to call (a no-op when nothing was created).
func detachedWorktree(epicDir string, c report.Claim) (dir string, cleanup func(), status, detail string) {
	noop := func() {}
	cit, ok := firstCitation(c.Evidence)
	if !ok || cit.SHA == "" {
		return "", noop, Unknown, "no cited sha to build a worktree from"
	}
	repo, err := cite.RepoDir(epicDir, cit.Alias)
	if err != nil {
		return "", noop, Unknown, err.Error()
	}
	tmp, err := os.MkdirTemp("", "arena-verify-")
	if err != nil {
		return "", noop, Unknown, err.Error()
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", tmp, cit.SHA).CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		return "", noop, Unknown, "cannot build worktree at " + shortSHA(cit.SHA) + ": " + snippet(out)
	}
	return tmp, func() {
		exec.Command("git", "-C", repo, "worktree", "remove", "--force", tmp).Run()
		os.RemoveAll(tmp)
	}, "", ""
}

// firstCitation returns the first parseable citation in a `;`-separated evidence cell.
func firstCitation(evidence string) (cite.Citation, bool) {
	for _, f := range strings.Split(evidence, ";") {
		if cit, ok := cite.Parse(strings.TrimSpace(f)); ok {
			return cit, true
		}
	}
	return cite.Citation{}, false
}

// noNetworkEnv is the process env with the module network turned off, so a check cannot reach out (ADR 0013). GOFLAGS is
// cleared so a stray -mod=mod cannot re-enable downloads; GOPROXY=off makes go fail closed rather than fetch.
func noNetworkEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOPROXY=") || strings.HasPrefix(kv, "GOFLAGS=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOPROXY=off", "GOFLAGS=")
}

// snippet trims command output to its last 400 bytes for the JSON detail (the tail carries the failing assertion or the
// go test summary).
func snippet(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 400 {
		s = "..." + s[len(s)-400:]
	}
	return s
}

func writeJSON(path string, r Round) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// shellFields splits a command check into argv, honoring single quotes, double quotes, and backslash escapes, so a
// grep pattern with spaces is one arg. It runs the argv directly (no shell), so a `;`, `|`, or `&&` in a check is a
// literal argument that fails rather than chaining a second command past the allowlist. An unterminated quote errors.
func shellFields(s string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	inWord := false
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				fields = append(fields, cur.String())
				cur.Reset()
				inWord = false
			}
			i++
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case c == '"':
			inWord = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++ // closing quote
		case c == '\\' && i+1 < len(s):
			inWord = true
			cur.WriteByte(s[i+1])
			i += 2
		default:
			inWord = true
			cur.WriteByte(c)
			i++
		}
	}
	if inWord {
		fields = append(fields, cur.String())
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return fields, nil
}
