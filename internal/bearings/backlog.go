package bearings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// backlogRow is one BACKLOG.md table row with its status class.
type backlogRow struct {
	line  string
	class string // in-epic | gated | open | closed
}

// classifyStatus maps a BACKLOG.md status cell to its class: fixed/wontfix rows are done; in-epic is in flight; an
// open row carrying a hold or blocked-by marker is held or blocked; any other open row is queued.
func classifyStatus(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.HasPrefix(s, "fixed"), strings.HasPrefix(s, "wontfix"):
		return "closed"
	case strings.HasPrefix(s, "in-epic"):
		return "in-epic"
	case strings.Contains(s, "hold:"), strings.Contains(s, "hold-kind:"), strings.Contains(s, "blocked-by:"):
		return "gated"
	}
	return "open"
}

// parseBacklog reads the first markdown table of a BACKLOG.md: its header lines and its rows. Prose outside the table
// (the evidence bodies) is never returned.
func parseBacklog(text string) (header []string, rows []backlogRow) {
	inTable := false
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "|") {
			if inTable {
				break
			}
			continue
		}
		// The table's first line is its header, and a following |---| line its separator.
		if !inTable || (len(header) == 1 && len(rows) == 0 && strings.Trim(t, "|-: ") == "") {
			inTable = true
			header = append(header, t)
			continue
		}
		cells := strings.Split(strings.Trim(t, "|"), "|")
		rows = append(rows, backlogRow{line: t, class: classifyStatus(cells[len(cells)-1])})
	}
	return header, rows
}

// backlogFiles are the workspace's BACKLOG.md files: <ws>/BACKLOG.md and each project's <ws>/<project>/BACKLOG.md.
func backlogFiles(ws string) []string {
	var out []string
	if _, err := os.Stat(filepath.Join(ws, "BACKLOG.md")); err == nil {
		out = append(out, filepath.Join(ws, "BACKLOG.md"))
	}
	m, _ := filepath.Glob(filepath.Join(ws, "*", "BACKLOG.md"))
	return append(out, m...)
}

// BacklogOpenRows counts every row that is not done: in-epic, held or blocked, and queued.
func BacklogOpenRows(ws string) (int, error) {
	n := 0
	for _, p := range backlogFiles(ws) {
		b, err := os.ReadFile(p)
		if err != nil {
			return 0, err
		}
		_, rows := parseBacklog(string(b))
		for _, r := range rows {
			if r.class != "closed" {
				n++
			}
		}
	}
	return n, nil
}

// printBacklogCompact is fm-session-start.sh's compact backlog listing over cox's BACKLOG.md table: closed rows are
// omitted, every in-epic, held and blocked row prints in full, only plain open rows are bounded, and the remainder is
// disclosed exactly. The evidence prose below the table is never printed.
func printBacklogCompact(p *printer, ws string, limit int) {
	files := backlogFiles(ws)
	if len(files) == 0 {
		p.sub("BACKLOG.md")
		p.line("ABSENT")
		return
	}
	for _, path := range files {
		rel, _ := filepath.Rel(ws, path)
		p.sub(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			p.line(fmt.Sprintf("unreadable: %v", err))
			continue
		}
		if len(b) == 0 {
			p.line("(present, empty)")
			continue
		}
		header, rows := parseBacklog(string(b))
		p.line(fmt.Sprintf("compact backlog listing (done rows omitted; every in-epic, held, and blocked row shown in full; other open rows bounded to %d; evidence prose omitted)", limit))
		if len(rows) == 0 {
			p.line("(no backlog rows found)")
		} else {
			for _, h := range header {
				p.line(h)
			}
			var inEpic, gated, open, shown, closed int
			for _, r := range rows {
				switch r.class {
				case "closed":
					closed++
				case "in-epic":
					inEpic++
					p.line(r.line)
				case "gated":
					gated++
					p.line(r.line)
				default:
					open++
					if shown < limit {
						shown++
						p.line(r.line)
					}
				}
			}
			p.line(fmt.Sprintf("(shown %d in-epic, %d held or blocked, %d of %d other open row(s); %d closed row(s) omitted)", inEpic, gated, shown, open, closed))
			if open > shown {
				p.line(fmt.Sprintf("(%d more open - read %s)", open-shown, rel))
			}
		}
		p.line("Full rows and evidence remain on demand: " + rel)
	}
}
