package pack

import (
	"regexp"
	"strings"
)

// Blinding removes what would let a reviewer defer to authority instead of judging the design: the discussion history,
// attribution lines, model/harness names, and commit-message links. It is deliberately conservative - it never touches
// citations (they are needed and already machine-verified) - and it is not a security redactor; its job is to make the
// review blind, not to scrub secrets.

// attributionRe drops a line that opens with an actor attribution ("captain:", "leader:"), with or without a leading
// list marker or blockquote.
var attributionRe = regexp.MustCompile(`(?im)^[-*>\s]*(captain|leader)\s*:.*$`)

// commitRe redacts a commit-message link: a "commit <sha>" phrase or a URL path segment "/commit/<sha>".
var commitRe = regexp.MustCompile(`(?i)(\bcommit\s+[0-9a-f]{7,40}\b|/commit/[0-9a-f]{7,40})`)

// nameRe redacts model/harness/vendor names as whole words. Kept short and specific so it does not maul ordinary prose.
var nameRe = regexp.MustCompile(`(?i)\b(claude|codex|opus|sonnet|haiku|fable|gpt-?[0-9.]*|anthropic|openai)\b`)

// headingRe matches a level-1 or level-2 markdown heading.
var headingRe = regexp.MustCompile(`^#{1,2}\s`)

// discussionHeadingRe matches a "## Discussion" (or "# Discussion") heading.
var discussionHeadingRe = regexp.MustCompile(`(?i)^#{1,2}\s+discussion\b`)

// blind applies the redactions in order and collapses the blank lines a dropped section leaves behind.
func blind(s string) string {
	s = dropDiscussion(s)
	s = attributionRe.ReplaceAllString(s, "")
	s = commitRe.ReplaceAllString(s, "[commit]")
	s = nameRe.ReplaceAllString(s, "[harness]")
	// Collapse 3+ newlines left by removed lines/sections into a paragraph break.
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimLeft(s, "\n")
}

// dropDiscussion removes each "## Discussion" section: the heading and everything until the next same-or-higher-level
// heading (or end of file). RE2 has no lookahead, so this is a line scan rather than one regex.
func dropDiscussion(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		if skipping {
			if headingRe.MatchString(line) && !discussionHeadingRe.MatchString(line) {
				skipping = false // a new section starts; keep this heading
			} else {
				continue
			}
		}
		if discussionHeadingRe.MatchString(line) {
			skipping = true
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
