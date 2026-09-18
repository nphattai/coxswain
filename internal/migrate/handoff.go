package migrate

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
)

// HandoffWrap is one v1 handoff to carry into a checkpoint.v1. WT is the story worktree path from .run (wt.<story>),
// used to resolve the head sha; Wrapped is false when the file already has checkpoint.v1 frontmatter (nothing to do).
type HandoffWrap struct {
	Story   string
	Path    string
	WT      string
	Wrapped bool
}

// gitHead is the head-sha resolver, injectable so the wrap logic is unit-tested without a real worktree.
var gitHead = func(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// handoffWraps scans <epic>/handoffs for *.md files (ignoring the *.md.v1 backups) and returns one HandoffWrap each,
// with Wrapped set when the file lacks checkpoint.v1 frontmatter. A missing handoffs dir yields no wraps.
func handoffWraps(epicDir string, wt map[string]string) ([]HandoffWrap, error) {
	dir := filepath.Join(epicDir, "handoffs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read handoffs dir: %w", err)
	}
	var out []HandoffWrap
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		story := strings.TrimSuffix(name, ".md")
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, HandoffWrap{
			Story:   story,
			Path:    path,
			WT:      wt[story],
			Wrapped: !hasCheckpointFrontmatter(b),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Story < out[j].Story })
	return out, nil
}

// hasCheckpointFrontmatter reports whether b already opens with a --- frontmatter block carrying
// schema: coxswain.checkpoint.v1, i.e. it is already a v2 checkpoint and must not be re-wrapped.
func hasCheckpointFrontmatter(b []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return false
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "---" {
			return false
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "schema" {
			return strings.TrimSpace(v) == checkpoint.Schema
		}
	}
	return false
}

// wrapOne wraps a single v1 handoff into a checkpoint.v1: it backs the original up to <path>.v1 (never overwriting an
// existing backup), then rewrites <path> as the frontmatter block followed by the original bytes unchanged. head comes
// from git rev-parse HEAD of the story worktree when it still exists, else "unknown"; base is "unknown" (v1 recorded
// neither); reason is migrated-v1.
func wrapOne(hw HandoffWrap, now string) error {
	orig, err := os.ReadFile(hw.Path)
	if err != nil {
		return err
	}
	backup := hw.Path + ".v1"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := os.WriteFile(backup, orig, 0o644); err != nil {
			return fmt.Errorf("back up %s: %w", hw.Path, err)
		}
	} else if err != nil {
		return err
	}
	head := "unknown"
	if hw.WT != "" {
		if _, err := os.Stat(hw.WT); err == nil {
			if h := gitHead(hw.WT); h != "" {
				head = h
			}
		}
	}
	fm := fmt.Sprintf("---\nschema: %s\nstory: %s\nattempt: 1\nhead: %s\nbase: unknown\nwritten_at: %s\nreason: migrated-v1\n---\n",
		checkpoint.Schema, hw.Story, head, now)
	return os.WriteFile(hw.Path, append([]byte(fm), orig...), 0o644)
}

// wrapHandoffs wraps every handoff that needs it, at one shared written_at timestamp.
func wrapHandoffs(wraps []HandoffWrap) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, hw := range wraps {
		if !hw.Wrapped {
			continue
		}
		if err := wrapOne(hw, now); err != nil {
			return err
		}
	}
	return nil
}
