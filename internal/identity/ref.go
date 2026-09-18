// Package identity binds a worker to a story by attempt id and canonical worktree path, never by path prefix or
// directory name (F07). CanonicalPath resolves symlinks and makes the path absolute; SamePath compares two paths for
// exact equality only after both are canonicalized, so "/x/story-a" never matches "/x/story-a-2".
package identity

import "path/filepath"

// StoryRef identifies a specific attempt of a story in an epic. Attempt increments on resume from parked; a resume
// hook rejects a checkpoint whose attempt differs, so identity is exact rather than inferred.
type StoryRef struct {
	Epic    string
	Story   string
	Attempt int
}

// CanonicalPath returns the absolute, symlink-resolved form of p. It is the single normalization every path
// comparison goes through, so a symlink and its target resolve equal and a prefix can never masquerade as a match.
func CanonicalPath(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

// SamePath reports whether a and b are the same path after canonicalization. Comparison is exact string equality of
// the canonical forms: no prefix, no suffix, no directory-name heuristics.
func SamePath(a, b string) (bool, error) {
	ca, err := CanonicalPath(a)
	if err != nil {
		return false, err
	}
	cb, err := CanonicalPath(b)
	if err != nil {
		return false, err
	}
	return ca == cb, nil
}
