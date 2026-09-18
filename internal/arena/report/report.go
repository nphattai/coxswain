// Package report parses an arena role's claim report into a structured form both `arena check` (validation) and `synth`
// (synthesis) read. It handles two schemas: v2 (decision 0004) is a bare 4-column table `claim | evidence | severity |
// proposal` with no frontmatter; v3 (coxswain.arena.v3, ADR 0013) adds a frontmatter block (recommendation, assumptions,
// checks_required, conflicts_with) and three columns (tier, confidence, check). Columns are located by header name, not
// position, so a v2 report reads with its tier/confidence/check fields empty ("unknown") and a v3 report reads them
// filled - one parser, both schemas.
package report

import (
	"regexp"
	"strings"
)

// Version is the report schema a report was parsed as.
type Version string

const (
	V2 Version = "v2" // decision 0004: 4-column table, no frontmatter, tier unknown
	V3 Version = "v3" // coxswain.arena.v3 (ADR 0013): frontmatter + tier/confidence/check columns
)

// Claim is one parsed claim-table row. Tier, Confidence, and Check are "" for a v2 report (the columns are absent).
// Line is the 1-based source line in the whole file (frontmatter included) for error messages.
type Claim struct {
	Id         string // stable claim id "<role>-<round>-<n>"; "" when the report has no id column (cox assigns at synth)
	Claim      string
	Evidence   string
	Tier       string // "1".."5", or "" when the column is absent (v2, unknown)
	Severity   string
	Confidence string // "0".."100", or "" when absent
	Check      string // a command or `alias/path:line@sha == "<text>"`; "" when absent
	Proposal   string
	Line       int
}

// Report is a parsed role report: its schema version, the v3 frontmatter fields (empty for v2), and its claims.
type Report struct {
	Version        Version
	Recommendation string
	Assumptions    []string
	ChecksRequired []string
	ConflictsWith  []string
	Claims         []Claim
}

// Parse reads a report's markdown into a Report. It splits an optional leading `---` frontmatter block, then reads the
// claim table by header name. Version is v3 when a frontmatter block is present or the table carries a `tier` column,
// else v2. A report with no claim table yields no claims and no error (an empty review is valid).
func Parse(content string) Report {
	var r Report
	fm, body, hadFM := splitFrontmatter(content)
	fmLines := 0
	if hadFM {
		parseFrontmatter(fm, &r)
		fmLines = strings.Count(fm, "\n") + 2 // the two `---` fence lines wrap the frontmatter block
	}

	lines := strings.Split(body, "\n")
	cols := newColumns()
	inTable := false
	tierCol := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			inTable = false
			continue
		}
		cells := splitRow(line)
		if !inTable {
			if c, ok := headerColumns(cells); ok {
				cols = c
				inTable = true
				tierCol = c.tier >= 0
			}
			continue
		}
		if isSeparator(cells) {
			continue
		}
		r.Claims = append(r.Claims, Claim{
			Id:         cols.id.get(cells),
			Claim:      cols.claim.get(cells),
			Evidence:   cols.evidence.get(cells),
			Tier:       cols.tier.get(cells),
			Severity:   cols.severity.get(cells),
			Confidence: cols.confidence.get(cells),
			Check:      cols.check.get(cells),
			Proposal:   cols.proposal.get(cells),
			Line:       i + 1 + fmLines,
		})
	}

	if hadFM || tierCol {
		r.Version = V3
	} else {
		r.Version = V2
	}
	return r
}

// columns holds the located index of each claim-table column (-1 when absent).
type columns struct {
	id, claim, evidence, tier, severity, confidence, check, proposal col
}

// newColumns returns a columns with every index absent (-1).
func newColumns() columns {
	return columns{-1, -1, -1, -1, -1, -1, -1, -1}
}

type col int

// get returns the trimmed cell for this column, or "" when the column is absent or the row is short.
func (c col) get(cells []string) string {
	if c < 0 || int(c) >= len(cells) {
		return ""
	}
	return strings.TrimSpace(cells[c])
}

// headerColumns maps a header row's cells to column indices when it is a claim-table header (names claim, evidence, and
// severity). Unknown columns are ignored so a report may add columns without breaking the parser.
func headerColumns(cells []string) (columns, bool) {
	c := newColumns()
	for i, cell := range cells {
		switch strings.ToLower(strings.TrimSpace(cell)) {
		case "id":
			c.id = col(i)
		case "claim":
			c.claim = col(i)
		case "evidence":
			c.evidence = col(i)
		case "tier":
			c.tier = col(i)
		case "severity":
			c.severity = col(i)
		case "confidence":
			c.confidence = col(i)
		case "check":
			c.check = col(i)
		case "proposal":
			c.proposal = col(i)
		}
	}
	if c.claim >= 0 && c.evidence >= 0 && c.severity >= 0 {
		return c, true
	}
	return c, false
}

// fenceRe matches a `---` frontmatter fence line (only whitespace around it).
var fenceRe = regexp.MustCompile(`^\s*---\s*$`)

// splitFrontmatter returns the frontmatter block (without the fences), the body after it, and whether one was present.
// A frontmatter block is only recognized at the very start of the content (its first non-empty line is `---`).
func splitFrontmatter(content string) (fm, body string, ok bool) {
	lines := strings.Split(content, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || !fenceRe.MatchString(lines[start]) {
		return "", content, false
	}
	for i := start + 1; i < len(lines); i++ {
		if fenceRe.MatchString(lines[i]) {
			return strings.Join(lines[start+1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", content, false // an unterminated fence is not frontmatter
}

// scalarRe matches a `key: value` frontmatter line; itemRe matches a `- value` list item.
var (
	scalarRe = regexp.MustCompile(`^([A-Za-z_]+):\s*(.*)$`)
	itemRe   = regexp.MustCompile(`^\s*-\s+(.*)$`)
)

// parseFrontmatter fills r's v3 frontmatter fields from a YAML-subset block: `key: value` scalars and `key:` followed by
// `- item` lists. Only the four known keys are read; anything else is ignored. Go stdlib only, so no YAML dependency.
func parseFrontmatter(fm string, r *Report) {
	key := ""
	for _, raw := range strings.Split(fm, "\n") {
		if m := itemRe.FindStringSubmatch(raw); m != nil {
			appendItem(r, key, strings.TrimSpace(m[1]))
			continue
		}
		if m := scalarRe.FindStringSubmatch(strings.TrimSpace(raw)); m != nil {
			key = strings.ToLower(m[1])
			if v := strings.TrimSpace(m[2]); v != "" {
				appendItem(r, key, v)
			}
		}
	}
}

func appendItem(r *Report, key, val string) {
	switch key {
	case "recommendation":
		if r.Recommendation == "" {
			r.Recommendation = val
		}
	case "assumptions":
		r.Assumptions = append(r.Assumptions, val)
	case "checks_required":
		r.ChecksRequired = append(r.ChecksRequired, val)
	case "conflicts_with":
		r.ConflictsWith = append(r.ConflictsWith, val)
	}
}

// splitRow splits a markdown table row into cells, dropping the empty edges from the leading/trailing pipe.
func splitRow(line string) []string {
	parts := strings.Split(line, "|")
	if len(parts) >= 2 {
		parts = parts[1 : len(parts)-1]
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

// isSeparator reports whether a row is the `---|---` header separator.
func isSeparator(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}
