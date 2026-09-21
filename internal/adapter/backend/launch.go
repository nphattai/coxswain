package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// coxSelf resolves the absolute path of the running cox binary once, so a dispatched worker's harness hooks call the
// SAME cox that launched it (via COX_BIN) rather than whatever is on the worker terminal's PATH - which Orca controls
// and which, during an epic, is the pinned driver that may predate a subcommand the launching candidate added
// (e.g. `cox busy`, `cox inbox interrupt-wait`). Empty when the path cannot be resolved, in which case the worker falls
// back to PATH `cox` as before. A var so tests can pin it.
var coxSelf = func() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}()

// LaunchLine builds the shell line a terminal-plane backend types into a fresh worker terminal: the cox env prefix
// (COX_EPIC/COX_STORY derived from the story path, COX_PLANE=terminal, COX_BIN so the worker's harness hooks use the
// launching cox, and COX_BUSY_GEN when the harness reports its own busy state) followed by the adapter-owned harness
// argv, shell-quoted token by token. The argv (executable, model/provider syntax, trust/resource flags, prompt) is
// composed by cmd/cox/internal-arena via registry.LaunchArgs and threaded in as HarnessSpec.Argv, so both terminal-plane
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
	if coxSelf != "" {
		fmt.Fprintf(&b, "COX_BIN=%s ", shellQuote(coxSelf))
	}
	b.WriteString("COX_PLANE=terminal")
	if h.BusyGen != "" {
		fmt.Fprintf(&b, " COX_BUSY_GEN=%s", shellQuote(h.BusyGen))
	}
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
