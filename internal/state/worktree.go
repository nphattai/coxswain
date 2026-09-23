package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorktreeRecord is the on-disk form of <epic>/.cox/wt/<story>: the checkout path plus the attempt that wrote it (DESIGN
// wave-2 item 7), so a late writer from a prior incarnation can be dropped. A legacy record is the bare path text.
type WorktreeRecord struct {
	Path    string `json:"path"`
	Attempt int    `json:"attempt"`
}

// WorktreePath is <epic>/.cox/wt/<story>.
func WorktreePath(epicDir, story string) string {
	return filepath.Join(epicDir, ControlDir, "wt", story)
}

// ReadWorktreeRecord is the ONE parser of the worktree record: every reader (dispatch, control, story, arena, close)
// routes through it so the JSON/legacy format can never drift between them. A JSON record yields its path and attempt;
// a legacy plain-path file yields the trimmed text with attempt 0; an absent or empty file yields the zero record. A
// record that cannot be read or parsed is an error, so a fail-closed caller (epic close) can refuse it rather than skip.
func ReadWorktreeRecord(epicDir, story string) (WorktreeRecord, error) {
	p := WorktreePath(epicDir, story)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return WorktreeRecord{}, nil
		}
		return WorktreeRecord{}, err
	}
	raw := strings.TrimSpace(string(b))
	if !strings.HasPrefix(raw, "{") {
		return WorktreeRecord{Path: raw}, nil
	}
	var r WorktreeRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return WorktreeRecord{}, fmt.Errorf("malformed worktree record %s: %w", p, err)
	}
	if r.Path == "" {
		return WorktreeRecord{}, fmt.Errorf("malformed worktree record %s: no path", p)
	}
	return r, nil
}

// ReadWorktree returns the recorded worktree path, or "" when there is none or the record is malformed: the lenient
// form for readers that only skip a story without a worktree.
func ReadWorktree(epicDir, story string) string {
	r, _ := ReadWorktreeRecord(epicDir, story)
	return r.Path
}
