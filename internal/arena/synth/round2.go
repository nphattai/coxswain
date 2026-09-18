package synth

import "strings"

// Round2 reports whether a second arena round is required, read from a synthesis table. Decision 0004 opens round 2 only
// for an epic-blocking claim the leader left `unresolved`; a role conflict the leader cannot adjudicate is recorded as an
// unresolved epic-blocking claim, so it flows through the same rule. A blank verdict means "not yet adjudicated" (the
// sign gate's blank-verdict check catches that), not "unresolved", so it does not open round 2. reasons name the
// offending "<role>: <claim>" rows.
//
// ponytail: the mechanical rule is severity=epic-blocking AND verdict=unresolved. Automated "two roles disagree on the
// same fact" detection needs claim semantics; the leader surfaces such a conflict by marking the disputed claim
// unresolved, which this rule then catches.
func Round2(content string) (need bool, reasons []string) {
	roleCol, claimCol, sevCol, verdictCol := -1, -1, -1, -1
	header := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			header, roleCol, claimCol, sevCol, verdictCol = false, -1, -1, -1, -1
			continue
		}
		cells := tableCells(line)
		if !header {
			for i, c := range cells {
				switch strings.ToLower(strings.TrimSpace(c)) {
				case "role":
					roleCol = i
				case "claim":
					claimCol = i
				case "severity":
					sevCol = i
				case "verdict":
					verdictCol = i
				}
			}
			if sevCol >= 0 && verdictCol >= 0 {
				header = true
			}
			continue
		}
		if isDashRow(cells) {
			continue
		}
		if sevCol >= len(cells) || verdictCol >= len(cells) {
			continue
		}
		sev := strings.ToLower(strings.TrimSpace(cells[sevCol]))
		verdict := strings.ToLower(strings.TrimSpace(cells[verdictCol]))
		if sev == "epic-blocking" && verdict == "unresolved" {
			need = true
			reasons = append(reasons, cell(cells, roleCol)+": "+cell(cells, claimCol))
		}
	}
	return need, reasons
}

// HasAdjudication reports whether a synthesis table already carries any non-blank verdict or captain-agrees cell, i.e.
// the leader or captain has started adjudicating. Rebuilding over it would clobber that work, so Build refuses without
// --force when this is true.
func HasAdjudication(content string) bool {
	verdictCol, agreesCol := -1, -1
	header := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			header, verdictCol, agreesCol = false, -1, -1
			continue
		}
		cells := tableCells(line)
		if !header {
			for i, c := range cells {
				switch strings.ToLower(strings.TrimSpace(c)) {
				case "verdict":
					verdictCol = i
				case "captain agrees":
					agreesCol = i
				}
			}
			if verdictCol >= 0 || agreesCol >= 0 {
				header = true
			}
			continue
		}
		if isDashRow(cells) {
			continue
		}
		if cell(cells, verdictCol) != "" || cell(cells, agreesCol) != "" {
			return true
		}
	}
	return false
}

func cell(cells []string, i int) string {
	if i < 0 || i >= len(cells) {
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
