package backend

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LaunchLine builds the shell line a terminal-plane backend types into a fresh worker terminal: the cox env prefix
// (COX_EPIC/COX_STORY derived from the story path, COX_PLANE=terminal) followed by the adapter-owned harness argv,
// shell-quoted token by token. The argv (executable, model/provider syntax, trust/resource flags, prompt) is composed
// by cmd/cox/internal-arena via registry.LaunchArgs and threaded in as HarnessSpec.Argv, so both terminal-plane
// backends (orca, herdr) type the command without importing the harness layer (decision 0002). Shared so the env
// contract and quoting cannot drift between backends. Quoting is uniform per token: the resulting process argv is
// byte-for-byte what the adapter returned (only shell-safe quoting is added), never the harness's ambient default.
func LaunchLine(h HarnessSpec, brief Brief) string {
	var b strings.Builder
	if epic := EpicFromPath(brief.StoryPath); epic != "" {
		fmt.Fprintf(&b, "COX_EPIC=%s ", shellQuote(epic))
	}
	if story := StoryFromPath(brief.StoryPath); story != "" {
		fmt.Fprintf(&b, "COX_STORY=%s ", shellQuote(story))
	}
	b.WriteString("COX_PLANE=terminal")
	for _, a := range h.Argv {
		if a == "" {
			continue
		}
		fmt.Fprintf(&b, " %s", shellQuote(a))
	}
	return b.String()
}

// PromptFromBrief renders the single prompt argument the worker harness receives, matching harness.LaunchArgs for the
// worker role: the story-file instruction when a story path is set (with the inline Text appended as a progress note),
// else the inline Text alone.
func PromptFromBrief(brief Brief) string {
	if brief.StoryPath != "" {
		p := "Your task is the story file " + brief.StoryPath + " - read it in full and follow its Working rules exactly."
		if brief.Text != "" {
			p += " Progress note from your previous attempt: " + brief.Text
		}
		return p
	}
	return brief.Text
}

// StoryFromPath returns the story id from a story path <epic>/stories/<id>.md, or "".
func StoryFromPath(storyPath string) string {
	if storyPath == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(storyPath), ".md")
}

// EpicFromPath returns the epic dir from a story path <epic>/stories/<id>.md (the parent of the stories dir), or "".
func EpicFromPath(storyPath string) string {
	if storyPath == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(storyPath))
}

// shellQuote wraps s in single quotes for safe interpolation into a typed shell line, escaping embedded single quotes
// as the standard POSIX '\” idiom. An empty string becomes ”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
