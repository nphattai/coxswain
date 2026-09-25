package routing

// The task text the typed router sends the model (firstmate 795e4b5, bin/fm-dispatch-resolve.sh task_sections +
// bin/fm-brief-heading-lib.sh @a8572f6). The model sees only the story's task-specific sections, because the rest of a
// scaffolded story (Read first, Verification, Defaults, Working rules) is the same boilerplate on every story and its
// safety language reads as high stakes on every task. Firstmate's `# Task` > `## Captain's intent` / `## Firstmate
// spec` map to the cox story template's H1 (`# <title>`) > `## Goal` / `## Scope` / `## Acceptance criteria`; the
// scout tag comes from frontmatter `kind: scout` (cox's scout contract), and the delivery mode is never sent.

import "strings"

// TaskSections are the story template headings whose bodies reach the router, in this order.
var TaskSections = []string{"## Goal", "## Scope", "## Acceptance criteria"}

// ScoutKindLine prefixes the task text of a scout story (fm brief_kind).
const ScoutKindLine = "Brief kind: scout (report only)"

// TaskText returns what the router sends as the brief: the task sections of the story's first H1 (each as its heading,
// a newline and its body, separated by a blank line), led by ScoutKindLine for a scout story; the whole brief when the
// story has none of them.
func TaskText(brief, kind string) string {
	lines := strings.Split(strings.TrimRight(brief, "\n"), "\n")
	lines = skipFrontmatter(lines)
	// The task sections live under the story's H1, as firstmate's live under `# Task`: a section before it is not one.
	var h1 []string
	for i, l := range unfencedHeadings(lines) {
		if l.level == 1 {
			h1 = headingBody(lines[i+1:], 1)
			break
		}
	}
	var parts []string
	if h1 != nil {
		for _, h := range TaskSections {
			if body, ok := sectionBody(h1, h); ok {
				parts = append(parts, h+"\n"+strings.TrimRight(strings.Join(body, "\n"), "\n"))
			}
		}
	}
	if len(parts) == 0 {
		return brief
	}
	text := strings.Join(parts, "\n\n") + "\n"
	if strings.TrimSpace(kind) == "scout" {
		text = ScoutKindLine + "\n\n" + text
	}
	return text
}

func skipFrontmatter(lines []string) []string {
	if len(lines) == 0 || lines[0] != "---" {
		return lines
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return lines[i+1:]
		}
	}
	return lines
}

// sectionBody finds heading (an exact line match outside a fence) and returns its body through the next unfenced heading
// at the same or a higher level (fm_brief_heading_parse body mode).
func sectionBody(lines []string, heading string) ([]string, bool) {
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	for i, l := range unfencedHeadings(lines) {
		if l.exact == heading {
			return headingBody(lines[i+1:], level), true
		}
	}
	return nil, false
}

// headingBody returns lines up to the first unfenced heading of level <= level.
func headingBody(lines []string, level int) []string {
	for i, l := range unfencedHeadings(lines) {
		if l.level > 0 && l.level <= level {
			return lines[:i]
		}
	}
	return lines
}

type lineInfo struct {
	exact string // the raw line when it sits outside a fence (a heading candidate), else ""
	level int    // ATX heading level of an unfenced line, 0 when it is not a heading
}

// unfencedHeadings classifies each line with the fence rule of fm_brief_heading_parse: up to three leading spaces, a run
// of three or more ` or ~ opens a fence; only the same marker, at least as long, with nothing but blanks after it closes
// it. Fence lines and fenced lines are never headings.
func unfencedHeadings(lines []string) []lineInfo {
	out := make([]lineInfo, len(lines))
	fenced, fenceMarker, fenceLen := false, byte(0), 0
	for i, line := range lines {
		scan := line
		for n := 0; n < 3 && strings.HasPrefix(scan, " "); n++ {
			scan = scan[1:]
		}
		markerLen := 0
		if scan != "" && (scan[0] == '`' || scan[0] == '~') {
			for markerLen < len(scan) && scan[markerLen] == scan[0] {
				markerLen++
			}
		}
		isFence := markerLen >= 3
		wasFenced := fenced
		if isFence {
			if !fenced {
				fenced, fenceMarker, fenceLen = true, scan[0], markerLen
			} else if scan[0] == fenceMarker && markerLen >= fenceLen && strings.TrimSpace(scan[markerLen:]) == "" {
				fenced = false
			}
		}
		if isFence || wasFenced {
			continue
		}
		out[i].exact = line
		level := len(scan) - len(strings.TrimLeft(scan, "#"))
		if level > 0 && (level == len(scan) || scan[level] == ' ' || scan[level] == '\t') {
			out[i].level = level
		}
	}
	return out
}
