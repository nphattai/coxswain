package routing

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// BaselineRow is one data row of a docs/baselines/<harness>-<date>.md table: which story was replayed, the sha it was
// replayed before, the harness and condition (bare|v2), the test result, and the leader-fix count. LeaderFixes is -1
// when the cell is blank or "-" (not recorded).
type BaselineRow struct {
	Story       string
	BeforeSha   string
	Harness     string
	Condition   string
	Result      string
	LeaderFixes int
}

// Measured reports whether this row is a real measurement, not a plan. A dry-run row (Result "dry-run", "-", or blank)
// records intent, not an outcome, so it never counts toward the routing bar (ADR 0011: a single replay is not
// evidence). Only a row whose result begins with "pass" or "fail" is a measurement.
func (r BaselineRow) Measured() bool {
	res := strings.ToLower(strings.TrimSpace(r.Result))
	return strings.HasPrefix(res, "pass") || strings.HasPrefix(res, "fail")
}

// Cite renders the row for a routing decision's cited_rows: story | harness | condition | <first word of result>.
func (r BaselineRow) Cite() string {
	res := strings.Fields(r.Result)
	first := ""
	if len(res) > 0 {
		first = res[0]
	}
	return strings.Join([]string{r.Story, r.Harness, r.Condition, first}, " | ")
}

// Measured returns only the measured rows from a set (dry-run and blank rows dropped).
func Measured(rows []BaselineRow) []BaselineRow {
	var out []BaselineRow
	for _, r := range rows {
		if r.Measured() {
			out = append(out, r)
		}
	}
	return out
}

// ParseBaselines reads every docs/baselines/*.md file in dir and returns their data rows. A missing dir is not an error
// (no baseline yet); it returns an empty slice.
func ParseBaselines(dir string) ([]BaselineRow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rows []BaselineRow
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		rows = append(rows, parseTable(string(b))...)
	}
	return rows, nil
}

var sepCell = regexp.MustCompile(`^:?-+:?$`)

// parseTable extracts the data rows of a markdown table with the baseline columns (story, before sha, harness,
// condition, test result, leader fixes). The header row and the |---| separator are skipped; any table line with fewer
// than six cells is ignored, so prose or a different table cannot inject a row.
func parseTable(md string) []BaselineRow {
	var rows []BaselineRow
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") {
			continue
		}
		cells := splitCells(t)
		if len(cells) < 6 {
			continue
		}
		if strings.EqualFold(cells[0], "story") || isSeparator(cells) {
			continue
		}
		fixes := -1
		if n, err := strconv.Atoi(cells[5]); err == nil {
			fixes = n
		}
		rows = append(rows, BaselineRow{
			Story: cells[0], BeforeSha: cells[1], Harness: cells[2],
			Condition: cells[3], Result: cells[4], LeaderFixes: fixes,
		})
	}
	return rows
}

// splitCells strips the outer pipes and returns the trimmed inner cells.
func splitCells(row string) []string {
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	parts := strings.Split(row, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		if !sepCell.MatchString(c) {
			return false
		}
	}
	return true
}
