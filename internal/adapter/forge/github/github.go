// Package github is the Forge adapter over the gh CLI. Every call uses gh's --json fields explicitly and returns a real
// error on any gh failure or unparseable output; nothing is guessed, so a gh error becomes an `unknown` verdict rather
// than a false pass (F12). The command runner is injectable so parsing is unit-tested without gh.
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/forge"
)

// Client runs gh in a repo working directory (the story worktree).
type Client struct {
	Dir string
	run func(dir string, args ...string) ([]byte, error)
}

// New returns a Client that shells out to gh in dir.
func New(dir string) *Client { return &Client{Dir: dir, run: runGH} }

func runGH(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w%s", strings.Join(args, " "), err, stderrTail(err))
	}
	return out, nil
}

// stderrTail returns gh's first stderr line as ": <line>", or "" when there is none. Output() populates
// ExitError.Stderr; without it a gh failure collapses to a bare "exit status 1" and the reason (e.g. "no pull requests
// found for branch", "not logged into any GitHub hosts") is lost, so cox state cannot classify it (M8 A0).
func stderrTail(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if line := strings.TrimSpace(string(ee.Stderr)); line != "" {
			if i := strings.IndexByte(line, '\n'); i >= 0 {
				line = line[:i]
			}
			return ": " + line
		}
	}
	return ""
}

// PR resolves the PR for a head branch via `gh pr view <head> --json ...`. A head with no PR is an error.
func (c *Client) PR(headRef string) (forge.PR, error) {
	out, err := c.run(c.Dir, "pr", "view", headRef, "--json", "number,headRefOid,headRefName,baseRefName,state,isDraft,mergeable")
	if err != nil {
		return forge.PR{}, err
	}
	var r struct {
		Number      int    `json:"number"`
		HeadRefOid  string `json:"headRefOid"`
		HeadRefName string `json:"headRefName"`
		BaseRefName string `json:"baseRefName"`
		State       string `json:"state"`
		IsDraft     bool   `json:"isDraft"`
		Mergeable   string `json:"mergeable"` // MERGEABLE | CONFLICTING | UNKNOWN
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return forge.PR{}, fmt.Errorf("parse pr view: %w", err)
	}
	if r.Number == 0 {
		return forge.PR{}, fmt.Errorf("no PR for head %q", headRef)
	}
	return forge.PR{
		Number:    r.Number,
		Head:      r.HeadRefOid,
		HeadRef:   r.HeadRefName,
		Base:      r.BaseRefName,
		State:     strings.ToLower(r.State),
		Draft:     r.IsDraft,
		Mergeable: strings.EqualFold(r.Mergeable, "MERGEABLE"), // only a known-clean state is mergeable; UNKNOWN fails closed
		// UNKNOWN (or an absent value) is not yet computed, so it is not a conflict either (B-60).
		MergeableUnknown: r.Mergeable == "" || strings.EqualFold(r.Mergeable, "UNKNOWN"),
	}, nil
}

// Diff returns the PR unified diff.
func (c *Client) Diff(pr forge.PR) (string, error) {
	out, err := c.run(c.Dir, "pr", "diff", strconv.Itoa(pr.Number))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Checks returns the PR head check runs via `gh pr checks --json`.
func (c *Client) Checks(pr forge.PR) ([]forge.Check, error) {
	out, err := c.run(c.Dir, "pr", "checks", strconv.Itoa(pr.Number), "--json", "name,state,bucket,startedAt,completedAt")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name        string `json:"name"`
		State       string `json:"state"`
		Bucket      string `json:"bucket"`
		StartedAt   string `json:"startedAt"`
		CompletedAt string `json:"completedAt"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse pr checks: %w", err)
	}
	checks := make([]forge.Check, 0, len(raw))
	for _, r := range raw {
		ck := mapCheck(r.Name, r.State, r.Bucket)
		ck.StartedAt, ck.CompletedAt = normalizeTS(r.StartedAt), normalizeTS(r.CompletedAt)
		checks = append(checks, ck)
	}
	return checks, nil
}

// normalizeTS drops gh's zero timestamp ("0001-01-01T00:00:00Z"), which it emits for a run that has not started or
// completed, to "" so the CI-wall computation treats it as absent rather than a real time.
func normalizeTS(ts string) string {
	if ts == "" || strings.HasPrefix(ts, "0001-01-01") {
		return ""
	}
	return ts
}

// mapCheck normalizes gh's check state/bucket into the forge Check status/conclusion. gh's `state` is already
// SUCCESS/FAILURE/PENDING/... and `bucket` is pass/fail/pending/skipping/cancel.
func mapCheck(name, state, bucket string) forge.Check {
	c := forge.Check{Name: name}
	switch strings.ToLower(bucket) {
	case "pass":
		c.Status, c.Conclusion = "completed", "success"
	case "fail", "cancel":
		c.Status, c.Conclusion = "completed", "failure"
	case "skipping":
		c.Status, c.Conclusion = "completed", "skipped"
	default: // pending
		c.Status, c.Conclusion = "in_progress", ""
	}
	return c
}

// Comments returns PR review comments via `gh pr view --json comments,reviews`.
func (c *Client) Comments(pr forge.PR) ([]forge.Comment, error) {
	out, err := c.run(c.Dir, "pr", "view", strconv.Itoa(pr.Number), "--json", "comments,reviews")
	if err != nil {
		return nil, err
	}
	var r struct {
		Comments []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body string `json:"body"`
		} `json:"comments"`
		Reviews []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body  string `json:"body"`
			State string `json:"state"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("parse pr comments: %w", err)
	}
	var out2 []forge.Comment
	for _, c := range r.Comments {
		out2 = append(out2, forge.Comment{Author: c.Author.Login, Body: c.Body})
	}
	for _, rv := range r.Reviews {
		body := rv.Body
		if rv.State == "CHANGES_REQUESTED" {
			body = "Changes Requested " + body
		}
		out2 = append(out2, forge.Comment{Author: rv.Author.Login, Body: body})
	}
	return out2, nil
}

// Merged reads the PR's state live by number (`gh pr view <n> --json state`). The passed struct is never trusted: it was
// read before the merge, so its State is stale (B-59). A failed read is an error, never a guessed answer.
func (c *Client) Merged(pr forge.PR) (bool, error) {
	out, err := c.run(c.Dir, "pr", "view", strconv.Itoa(pr.Number), "--json", "state")
	if err != nil {
		return false, err
	}
	var r struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return false, fmt.Errorf("parse pr state: %w", err)
	}
	if r.State == "" {
		return false, fmt.Errorf("pr view %d: no state", pr.Number)
	}
	return strings.EqualFold(r.State, "MERGED"), nil
}

// Merge merges the PR through `gh pr merge`, pinning the head with --match-head-commit so gh (and GitHub) reject the
// merge if the head moved since the read. method selects the strategy flag; an unknown method is an error before any gh
// call so a typo never falls through to gh's default.
func (c *Client) Merge(pr forge.PR, method string) error {
	var strategy string
	switch method {
	case "squash":
		strategy = "--squash"
	case "merge":
		strategy = "--merge"
	case "rebase":
		strategy = "--rebase"
	default:
		return fmt.Errorf("unknown merge method %q (want squash|merge|rebase)", method)
	}
	_, err := c.run(c.Dir, "pr", "merge", strconv.Itoa(pr.Number), strategy, "--match-head-commit", pr.Head)
	return err
}
