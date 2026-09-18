package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// oppRow is one prior-round claim shown to a round-2 role: its role label, claim, evidence, tier, severity, the machine
// `verified` result, and the leader's provisional `verdict` (either may be blank).
type oppRow struct {
	Role, Claim, Evidence, Tier, Severity, Verified, Verdict string
}

// opposition renders the round-2 addition to the pack: the prior round's verified claim table plus an "Answer these"
// section. The rows come from reports/arena/synthesis-round-(round-1).md, read by header name so a v2 or a not-yet-filled
// synthesis (blank verified/verdict) still renders. A missing prior synthesis is an error: round 2 has nothing to oppose.
func opposition(epicDir string, round int) (string, error) {
	prior := round - 1
	path := filepath.Join(epicDir, "reports", "arena", fmt.Sprintf("synthesis-round-%d.md", prior))
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("round %d needs the round-%d synthesis at %s: %w", round, prior, path, err)
	}
	rows := parseOpposition(string(b))
	if len(rows) == 0 {
		return "", fmt.Errorf("round %d: no claims in %s to oppose", round, filepath.Base(path))
	}
	var w strings.Builder
	fmt.Fprintf(&w, "## Round %d claims (verified)\n\n", prior)
	w.WriteString("The previous round raised these claims. `verified` is the machine check result (pass/fail/unknown); ")
	w.WriteString("`verdict` is the leader's provisional call (may be blank).\n\n")
	w.WriteString("| role | claim | evidence | tier | severity | verified | verdict |\n")
	w.WriteString("|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&w, "| %s | %s | %s | %s | %s | %s | %s |\n",
			r.Role, r.Claim, r.Evidence, r.Tier, r.Severity, r.Verified, r.Verdict)
	}
	w.WriteString("\n## Answer these\n\n")
	w.WriteString("For each claim above, in your report:\n")
	w.WriteString("- agree or reject it, with your reason\n")
	w.WriteString("- which evidence would change the conclusion\n")
	w.WriteString("- what stays unresolved\n\n")
	w.WriteString("Then add any new claim of your own in the same v3 table. Do not defer to the previous round; the ")
	w.WriteString("verdicts are provisional.\n")
	return w.String(), nil
}

// parseOpposition extracts claim rows from a synthesis table by header name, so it survives column reorders and a v2
// synthesis (tier/verified absent -> blank).
func parseOpposition(content string) []oppRow {
	var rows []oppRow
	cols := map[string]int{}
	inTable := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			inTable = false
			cols = map[string]int{}
			continue
		}
		cells := tableCells(line)
		if !inTable {
			for i, c := range cells {
				cols[strings.ToLower(strings.TrimSpace(c))] = i
			}
			if _, ok := cols["claim"]; ok {
				if _, ok2 := cols["severity"]; ok2 {
					inTable = true
				}
			}
			continue
		}
		if isDashRow(cells) {
			continue
		}
		rows = append(rows, oppRow{
			Role:     at(cells, cols, "role"),
			Claim:    at(cells, cols, "claim"),
			Evidence: at(cells, cols, "evidence"),
			Tier:     at(cells, cols, "tier"),
			Severity: at(cells, cols, "severity"),
			Verified: at(cells, cols, "verified"),
			Verdict:  at(cells, cols, "verdict"),
		})
	}
	return rows
}

func at(cells []string, cols map[string]int, name string) string {
	i, ok := cols[name]
	if !ok || i < 0 || i >= len(cells) {
		return ""
	}
	return strings.TrimSpace(cells[i])
}

func tableCells(line string) []string {
	parts := strings.Split(line, "|")
	if len(parts) >= 2 {
		parts = parts[1 : len(parts)-1]
	}
	return parts
}

func isDashRow(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}
