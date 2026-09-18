package checkpoint

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/protocol/inbox"
)

// FactSet is the computable state of a story at a moment: what a worker cannot get wrong by hand. Control (park) uses
// Head to bind a checkpoint to the current HEAD; the worker appends Markdown() to the checkpoint body.
type FactSet struct {
	Head           string   // full HEAD sha of the worktree, "unknown" if not a repo
	Base           string   // origin/epic/<slug>@<full-sha>, "unknown" if the base ref is missing
	Dirty          []string // porcelain status lines
	InboxUnhandled []string // unhandled steer keys (NNN.msg)
	PR             string   // PR url from gh, "unknown" if gh is absent or found nothing
	CI             string   // "unknown" at M2 (no CI wiring yet)
}

// Facts gathers the FactSet for a story. worktree is the worker's checkout; epicDir holds the inbox. Git and forge
// failures degrade to "unknown" rather than erroring, so a facts call on a detached or offline checkout still returns
// what it can. It errors only when the inbox listing itself fails (a real disk problem).
func Facts(worktree, epicDir, story string) (FactSet, error) {
	slug := filepath.Base(epicDir)
	f := FactSet{Head: "unknown", Base: "unknown", PR: "unknown", CI: "unknown"}

	// Full shas (not --short): checkpointMatches/Inject compare heads by prefix, so a full HEAD here still matches an
	// older checkpoint that recorded a short sha (checkpoint.HeadMatches), while a full-sha checkpoint stays unambiguous.
	if head, err := git(worktree, "rev-parse", "HEAD"); err == nil {
		f.Head = head
	}
	baseRef := "origin/epic/" + slug
	if sha, err := git(worktree, "rev-parse", baseRef); err == nil {
		f.Base = baseRef + "@" + sha
	}
	if status, err := git(worktree, "status", "--porcelain"); err == nil && status != "" {
		f.Dirty = strings.Split(status, "\n")
	}

	recs, err := inbox.List(epicDir, story)
	if err != nil {
		return FactSet{}, err
	}
	for _, r := range recs {
		f.InboxUnhandled = append(f.InboxUnhandled, fmt.Sprintf("%03d.msg (%s)", r.Seq, r.Urgency))
	}

	if _, err := exec.LookPath("gh"); err == nil {
		if url, err := ghPR(worktree, story); err == nil && url != "" {
			f.PR = url
		}
	}
	return f, nil
}

// Markdown renders the "## Facts (máy tính)" block a worker appends to the end of its checkpoint.
func (f FactSet) Markdown() string {
	var b strings.Builder
	b.WriteString("## Facts (máy tính)\n")
	fmt.Fprintf(&b, "- head: %s\n", f.Head)
	fmt.Fprintf(&b, "- base: %s\n", f.Base)
	if len(f.Dirty) == 0 {
		b.WriteString("- dirty: (clean)\n")
	} else {
		fmt.Fprintf(&b, "- dirty (%d):\n", len(f.Dirty))
		for _, d := range f.Dirty {
			fmt.Fprintf(&b, "  - %s\n", d)
		}
	}
	if len(f.InboxUnhandled) == 0 {
		b.WriteString("- inbox unhandled: (none)\n")
	} else {
		fmt.Fprintf(&b, "- inbox unhandled: %s\n", strings.Join(f.InboxUnhandled, ", "))
	}
	fmt.Fprintf(&b, "- PR: %s\n", f.PR)
	fmt.Fprintf(&b, "- CI: %s\n", f.CI)
	return b.String()
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ghPR returns the PR url for branch story/<id>, or empty when none. gh infers the repo from the worktree cwd.
func ghPR(dir, story string) (string, error) {
	cmd := exec.Command("gh", "pr", "list", "--head", "story/"+story, "--json", "url", "--jq", ".[0].url")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
