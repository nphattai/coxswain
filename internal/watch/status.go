package watch

import (
	"regexp"
	"strings"
	"time"
)

// Status-line grammar the watcher needs to triage a quiet worker, ported from firstmate bin/fm-classify-lib.sh
// (status_line_verb, status_is_captain_relevant, status_is_paused, status_is_captain_held, status_paused_until; pinned
// 1e0e773). A worker's status line is a status mail's subject (or a report's note); its verb is the word before the
// first colon with any [tag] stripped. The decision fold ([key=] open/close) is wave 2b and not read here.

const (
	verbPaused      = "paused"       // FM_CLASSIFY_PAUSED_VERB_DEFAULT
	verbCaptainHeld = "captain-held" // FM_CLASSIFY_CAPTAIN_HELD_VERB_DEFAULT
)

// captainRE is FM_CLASSIFY_CAPTAIN_RE_DEFAULT, matched case-insensitively against a line with no verb.
var captainRE = regexp.MustCompile(`(?i)done:|needs-decision:|blocked:|failed:|PR ready|checks green|ready in branch|merged`)

// untilRE is status_paused_until's token: `until <YYYY-MM-DDTHH:MM[:SS]Z>` after whitespace, UTC only.
var untilRE = regexp.MustCompile(`(?i)\suntil\s+(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?Z)`)

// corrRE is a correlation token (corr=<16 hex>) that status_line_verb drops from the verb words.
var corrRE = regexp.MustCompile(`^\[?corr=[0-9a-f]{16}\]?$`)

// tagRE is one before-colon [tag] token with its leading space (the emission-time stamp and friends).
var tagRE = regexp.MustCompile(`\s*\[[^\]]*\]`)

// statusVerb returns the leading verb of a status line: the text before the first colon, cut at the first '[', trimmed,
// with correlation tokens dropped (status_line_verb).
func statusVerb(line string) string {
	v := line
	if i := strings.Index(v, ":"); i >= 0 {
		v = v[:i]
	}
	if i := strings.Index(v, "["); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "corr=") {
		return v
	}
	words := strings.Fields(v)
	out := words[:1]
	for _, w := range words[1:] {
		if !corrRE.MatchString(w) {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

// captainRelevant reports whether a status line is captain-relevant under the default captain regex.
func captainRelevant(line string) bool { return captainRelevantRE(line, nil) }

// captainRelevantRE is status_is_captain_relevant: working, resolved, captain-held and paused are never relevant; with
// no override the terminal verbs done, needs-decision, blocked and failed always are; any other line matches the regex
// (the override FM_CAPTAIN_RE, else the default) against the line with its before-colon tags removed. An override
// replaces the default verb set entirely.
func captainRelevantRE(line string, override *regexp.Regexp) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	verb := statusVerb(line)
	switch verb {
	case "working", "resolved", verbCaptainHeld, verbPaused:
		return false
	}
	re := override
	if re == nil {
		switch verb {
		case "done", "needs-decision", "blocked", "failed":
			return true
		}
		re = captainRE
	}
	return re.MatchString(unstamped(line))
}

// unstamped drops the [tag] tokens before a line's first colon, so a stamped event still matches the regex.
func unstamped(line string) string {
	i := strings.Index(line, ":")
	if i < 0 {
		return line
	}
	head := tagRE.ReplaceAllString(line[:i], "")
	return head + line[i:]
}

func statusPaused(line string) bool      { return statusVerb(line) == verbPaused }
func statusCaptainHeld(line string) bool { return statusVerb(line) == verbCaptainHeld }
func statusDeclaredWait(line string) bool {
	return statusPaused(line) || statusCaptainHeld(line)
}

// statusPausedUntil returns the declared clearing time of a `paused:` line, or false when the line is not a pause or
// names no well-formed UTC time (a bad token falls back to the flat cadence rather than silencing the wait).
func statusPausedUntil(line string) (time.Time, bool) {
	if !statusPaused(line) {
		return time.Time{}, false
	}
	m := untilRE.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	layout := "2006-01-02T15:04Z"
	if len(m[1]) == len("2006-01-02T15:04:05Z") {
		layout = "2006-01-02T15:04:05Z"
	}
	t, err := time.Parse(layout, m[1])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
