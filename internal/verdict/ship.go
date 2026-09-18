package verdict

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/forge"
)

// RepoTarget is one repo's ship inputs: where its epic worktree is checked out and the production/staging branch names.
type RepoTarget struct {
	Alias      string
	Dir        string
	EpicBranch string
	Production string
	Staging    string
}

// RepoFacts is the ship readiness of one repo. Verdict is Unknown whenever a fetch or a conflict computation failed:
// facts derived from a stale or failed fetch are not trustworthy, so ship never prints "conflicts: none" off a failed
// merge-tree (F12). Conflicts is nil when unknown, empty (non-nil) when confirmed none.
type RepoFacts struct {
	Alias     string   `json:"alias"`
	Verdict   Verdict  `json:"verdict"`
	Reasons   []string `json:"reasons,omitempty"`
	Ahead     int      `json:"ahead"`
	Behind    int      `json:"behind"`
	Conflicts []string `json:"conflicts"`
}

// ShipReport is the per-repo ship facts for an epic.
type ShipReport struct {
	Repos []RepoFacts `json:"repos"`
}

// Ship computes ship facts for each repo target using the injected git runner (git -C dir args). A fetch failure or a
// merge-tree failure yields Unknown for that repo with a reason, never a fabricated "no conflicts" (F12).
func Ship(targets []RepoTarget, git func(dir string, args ...string) (string, error)) ShipReport {
	var rep ShipReport
	for _, t := range targets {
		rep.Repos = append(rep.Repos, shipRepo(t, git))
	}
	return rep
}

// ShipForgeFacts is the forge readiness of one repo's epic-branch PR, bound to the PR head sha so a later push makes it
// stale. Verdict is fail when a check failed, pass when every check passed, and unknown when there is no PR or CI is
// pending - never a guessed pass (P5, F12). Merged is a three-state string.
type ShipForgeFacts struct {
	Alias   string   `json:"alias"`
	PR      int      `json:"pr"`
	Head    string   `json:"head,omitempty"`
	Checks  Verdict  `json:"checks"`
	Merged  string   `json:"merged"`
	Verdict Verdict  `json:"verdict"`
	Reasons []string `json:"reasons,omitempty"`
}

// ShipForge computes forge readiness for each repo target through the forge built for its dir. A repo with no PR is
// unknown (and says so), never fail: an unopened PR is a missing fact, not a failure.
func ShipForge(targets []RepoTarget, forgeFor func(dir string) forge.Forge) []ShipForgeFacts {
	var out []ShipForgeFacts
	for _, t := range targets {
		out = append(out, shipForgeRepo(t, forgeFor(t.Dir)))
	}
	return out
}

func shipForgeRepo(t RepoTarget, f forge.Forge) ShipForgeFacts {
	ff := ShipForgeFacts{Alias: t.Alias, Verdict: Unknown, Checks: Unknown, Merged: "unknown"}
	pr, err := f.PR(t.EpicBranch)
	if err != nil {
		ff.Reasons = append(ff.Reasons, reason("pr", "no PR for "+t.EpicBranch+": "+err.Error()))
		return ff
	}
	ff.PR, ff.Head = pr.Number, pr.Head
	if checks, err := f.Checks(pr); err != nil {
		ff.Reasons = append(ff.Reasons, reason("checks", "retrieval failed: "+err.Error()))
	} else {
		ff.Checks = CI(checks)
	}
	if merged, err := f.Merged(pr); err == nil {
		ff.Merged = strconv.FormatBool(merged)
	}
	switch ff.Checks {
	case Fail:
		ff.Verdict = Fail
		ff.Reasons = append(ff.Reasons, reason("ci", "a check failed"))
	case Pass:
		ff.Verdict = Pass
	default:
		ff.Verdict = Unknown
		ff.Reasons = append(ff.Reasons, reason("ci", "pending or no checks; cannot confirm green"))
	}
	return ff
}

func shipRepo(t RepoTarget, git func(dir string, args ...string) (string, error)) RepoFacts {
	f := RepoFacts{Alias: t.Alias, Verdict: Pass}
	prod := "origin/" + t.Production
	epic := "origin/" + t.EpicBranch

	if _, err := git(t.Dir, "fetch", "origin"); err != nil {
		f.Verdict = Unknown
		f.Reasons = append(f.Reasons, reason("fetch", "failed: "+err.Error()))
		return f // do not compute ahead/behind or conflicts off a failed fetch
	}

	if out, err := git(t.Dir, "rev-list", "--left-right", "--count", epic+"..."+prod); err == nil {
		if parts := strings.Fields(out); len(parts) == 2 {
			f.Ahead, _ = strconv.Atoi(parts[0])  // epic-only commits
			f.Behind, _ = strconv.Atoi(parts[1]) // production-only commits the epic lacks
		}
	}

	// merge-tree reports conflicting file names; a failure is unknown, not "none".
	out, err := git(t.Dir, "merge-tree", "--write-tree", "--name-only", epic, prod)
	if err != nil {
		// git merge-tree exits non-zero WITH a conflict listing on stdout; distinguish that from a real failure by
		// whether it produced any output at all.
		if strings.TrimSpace(out) == "" {
			f.Verdict = Unknown
			f.Reasons = append(f.Reasons, reason("merge-tree", "failed: "+err.Error()))
			return f
		}
	}
	f.Conflicts = parseConflicts(out)
	if len(f.Conflicts) > 0 {
		f.Reasons = append(f.Reasons, reason("conflicts", fmt.Sprintf("%d file(s) conflict with %s", len(f.Conflicts), t.Production)))
	}
	return f
}

// parseConflicts pulls the conflicting file names out of `git merge-tree --write-tree --name-only` output: the first
// line is the written tree oid, the remaining non-empty lines are file paths. It always returns a non-nil slice so a
// confirmed-none is distinguishable from an unknown (nil).
func parseConflicts(out string) []string {
	files := []string{}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if i == 0 || l == "" {
			continue // first line is the tree oid
		}
		files = append(files, l)
	}
	return files
}
