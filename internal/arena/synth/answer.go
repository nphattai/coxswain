package synth

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// roundLinkRe captures the round from a synthesis.md symlink target synthesis-round-<N>.md.
var roundLinkRe = regexp.MustCompile(`synthesis-round-(\d+)\.md$`)

// Answer records the captain's answer to one synthesis claim, by its stable id. It is the single write path for the
// captain cells (M13): it fills the row's `captain agrees` cell (a captain_decision's answer goes there too, which is
// what the sign gate reads). answer is a `yes`/`no` or free text; by, when given, is appended as `(by <name>)`.
//
// It refuses when the synthesis was re-synthesized since it was built - the recorded content sha in
// .cox/arena/synthesis-<round>.sha (written at synth) must match synthesis.md now - so an id never lands on a shifted row.
// A successful write re-records the sha, so consecutive answers keep matching. It returns the round it wrote.
func Answer(epicDir, id, answer, by string) (int, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(answer) == "" {
		return 0, fmt.Errorf("answer needs a claim id and an answer")
	}
	link := filepath.Join(epicDir, "reports", "arena", "synthesis.md")
	target, err := os.Readlink(link)
	if err != nil {
		return 0, fmt.Errorf("synthesis.md is not a per-round synthesis (run cox arena synth first): %w", err)
	}
	m := roundLinkRe.FindStringSubmatch(target)
	if m == nil {
		return 0, fmt.Errorf("cannot read the round from synthesis.md -> %s", target)
	}
	round, _ := strconv.Atoi(m[1])
	roundFile := filepath.Join(filepath.Dir(link), target)

	content, err := os.ReadFile(roundFile)
	if err != nil {
		return 0, err
	}
	recorded := ReadSha(epicDir, round)
	if recorded == "" {
		return 0, fmt.Errorf("no recorded synthesis sha for round %d; re-run cox arena synth to enable answering by id", round)
	}
	if got := ContentSha(content); got != recorded {
		return 0, fmt.Errorf("synthesis.md changed since synth (sha %s != recorded %s); re-run cox arena synth before answering by id", got, recorded)
	}

	updated, err := setCaptainAgrees(string(content), id, formatAnswer(answer, by))
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(roundFile, []byte(updated), 0o644); err != nil {
		return 0, err
	}
	if err := WriteSha(epicDir, round, []byte(updated)); err != nil {
		return 0, err
	}
	return round, nil
}

// formatAnswer renders the captain-agrees cell: the answer, plus `(by <name>)` when a name is given. Any pipe in the
// text is escaped so it cannot split the table cell.
func formatAnswer(answer, by string) string {
	v := strings.ReplaceAll(strings.TrimSpace(answer), "|", `\|`)
	if b := strings.TrimSpace(by); b != "" {
		v += " (by " + strings.ReplaceAll(b, "|", `\|`) + ")"
	}
	return v
}

// setCaptainAgrees finds the claims-table row whose `id` cell equals id and writes value into its `captain agrees` cell,
// returning the updated content. It errors when the table has no id or captain-agrees column, or no row carries the id.
func setCaptainAgrees(content, id, value string) (string, error) {
	lines := strings.Split(content, "\n")
	idCol, agreesCol := -1, -1
	inTable := false
	found := false
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if !strings.HasPrefix(trimmed, "|") {
			inTable, idCol, agreesCol = false, -1, -1
			continue
		}
		cells := tableCells(trimmed)
		if !inTable {
			for j, c := range cells {
				switch strings.ToLower(strings.TrimSpace(c)) {
				case "id":
					idCol = j
				case "captain agrees":
					agreesCol = j
				}
			}
			if idCol >= 0 && agreesCol >= 0 {
				inTable = true
			}
			continue
		}
		if isDashRow(cells) {
			continue
		}
		if idCol >= len(cells) || strings.TrimSpace(cells[idCol]) != id {
			continue
		}
		for len(cells) <= agreesCol {
			cells = append(cells, "")
		}
		cells[agreesCol] = " " + value + " "
		lines[i] = "|" + strings.Join(cells, "|") + "|"
		found = true
		break
	}
	if !found {
		return "", fmt.Errorf("no claim with id %q in the synthesis (is it the current round?)", id)
	}
	return strings.Join(lines, "\n"), nil
}
