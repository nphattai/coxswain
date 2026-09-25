package epic

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/arena/synth"
	"github.com/nphattai/coxswain/internal/state"
)

// verdictValues are the allowed synthesis verdicts. captain_decision (ADR 0013) marks a question only the captain can
// settle; sign refuses while one is unanswered.
var verdictValues = map[string]bool{"accepted": true, "rejected": true, "unresolved": true, "captain_decision": true}

// v3Sections are the six leader-authored sections a v3 synthesis must fill before signing (ADR 0013), in template order.
var v3Sections = []string{"Adopted decision", "Decisive evidence", "Rejected alternatives", "Preserved locked decisions", "Remaining uncertainty", "Verification gates"}

// Sign records a design_signed event for an epic. It requires reports/arena/synthesis.md to exist with a non-empty valid
// verdict on every claim row and no epic-blocking claim left unresolved (round 2). A coxswain.arena.v3 synthesis is held
// to the stricter ADR 0013 gates: the six sections must be filled, an accepted epic-blocking claim must be verified pass,
// an open captain_decision must be answered (a captain-agrees cell), and the round must be <= 3. A legacy v2 synthesis
// keeps its P7 gate (a captain-agrees cell on every row). The event carries the DESIGN.md and synthesis content shas, and
// `by` when given. An already-signed design is never re-signed: record a post-signature change with `--amend --reason`.
func Sign(epicDir, by string) error {
	synthPath := filepath.Join(epicDir, "reports", "arena", "synthesis.md")

	if _, err := os.Stat(synthPath); err != nil {
		return fmt.Errorf("cannot sign: no synthesis at %s (run cox arena synth first, or sign the captain's no-arena ruling with --no-arena --reason)", synthPath)
	}
	synthContent, err := os.ReadFile(synthPath)
	if err != nil {
		return err
	}
	if blanks, err := blankColumn(synthPath, "verdict", func(v string) bool { return verdictValues[strings.ToLower(v)] }); err != nil {
		return err
	} else if len(blanks) > 0 {
		return fmt.Errorf("cannot sign: %d synthesis claim(s) have no verdict (lines %s)", len(blanks), strings.Join(blanks, ", "))
	}
	if isV3Synthesis(string(synthContent)) {
		if err := signGatesV3(synthPath, string(synthContent)); err != nil {
			return err
		}
	} else if blanks, err := blankColumn(synthPath, "captain agrees", func(v string) bool { return v != "" }); err != nil {
		return err
	} else if len(blanks) > 0 {
		return fmt.Errorf("cannot sign: %d synthesis claim(s) have an empty 'captain agrees' cell (lines %s)", len(blanks), strings.Join(blanks, ", "))
	}
	if need, reasons := synth.Round2(string(synthContent)); need {
		return fmt.Errorf("cannot sign: round 2 required (epic-blocking claim(s) unresolved): %s", strings.Join(reasons, "; "))
	}

	synthSha, err := fileSha(synthPath)
	if err != nil {
		return err
	}
	return appendSigned(epicDir, by, map[string]any{"synthesis_sha": synthSha})
}

// SignNoArena records a design_signed event on the captain's ruling that the epic needs no arena (B-04: a lite epic,
// arena level `none`), so no synthesis is required. The ruling is the reason and is mandatory; the event carries
// no_arena and the reason in place of the synthesis sha. Readers that only look at the event type (board, doctor) treat
// it as any other signature.
func SignNoArena(epicDir, by, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("cannot sign without an arena: --reason is required (the captain's no-arena ruling)")
	}
	return appendSigned(epicDir, by, map[string]any{"no_arena": true, "reason": strings.TrimSpace(reason)})
}

// appendSigned refuses an already-signed design, then appends design_signed with the DESIGN.md sha plus evidence.
func appendSigned(epicDir, by string, evidence map[string]any) error {
	alreadySigned, err := isSigned(epicDir)
	if err != nil {
		return err
	}
	if alreadySigned {
		return fmt.Errorf("DESIGN.md is already signed; use cox epic design --amend --reason to record a change")
	}
	designSha, err := fileSha(filepath.Join(epicDir, "DESIGN.md"))
	if err != nil {
		return err
	}
	evidence["design_sha"] = designSha
	if strings.TrimSpace(by) != "" {
		evidence["by"] = strings.TrimSpace(by)
	}
	// design_signed is durable epic history: write it to the committed ledger, not the machine-local .cox log, so the
	// signature travels with git clone and a re-attach never loses it (finding 2).
	return state.AppendLedger(epicDir, state.Event{
		Type: state.DesignSigned, Epic: filepath.Base(epicDir), Story: state.EpicStory,
		Actor: state.Captain, Evidence: evidence, ExternalConfirmed: true,
	})
}

// Amend records a design_amended event: DESIGN.md changed after a signature. A reason is required so the change is never
// undocumented. The event carries the new DESIGN.md sha.
func Amend(epicDir, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("cannot amend: --reason is required (why DESIGN.md changed after signing)")
	}
	designSha, err := fileSha(filepath.Join(epicDir, "DESIGN.md"))
	if err != nil {
		return err
	}
	// design_amended is durable epic history too: append it to the committed ledger (finding 2).
	return state.AppendLedger(epicDir, state.Event{
		Type: state.DesignAmended, Epic: filepath.Base(epicDir), Story: state.EpicStory,
		Actor: state.Captain, Evidence: map[string]any{"design_sha": designSha, "reason": strings.TrimSpace(reason)},
		ExternalConfirmed: true,
	})
}

// isV3Synthesis reports whether a synthesis was rendered from the coxswain.arena.v3 template (its frontmatter schema).
// The stricter ADR 0013 gates apply only to v3, so a legacy v2 synthesis keeps its original behavior.
func isV3Synthesis(content string) bool {
	return strings.Contains(content, "coxswain.arena.v3")
}

// signGatesV3 runs the ADR 0013 sign gates over a v3 synthesis: the six sections filled, an accepted epic-blocking claim
// verified pass, an open captain_decision answered, and the round <= 3. Each failure names the offending line(s).
func signGatesV3(synthPath, content string) error {
	if empty := emptySections(content); len(empty) > 0 {
		return fmt.Errorf("cannot sign: synthesis section(s) not filled: %s", strings.Join(empty, ", "))
	}
	if err := claimGatesV3(content); err != nil {
		return err
	}
	if r := synthesisRound(synthPath); r > 3 {
		return fmt.Errorf("cannot sign: round %d exceeds the 3-round maximum (ADR 0013)", r)
	}
	return nil
}

// emptySections returns the v3 sections whose body is empty. A section runs from its `## <name>` heading to the next
// heading; it is empty when it has no non-blank line that is not a `<...>` placeholder.
func emptySections(content string) []string {
	lines := strings.Split(content, "\n")
	var empty []string
	for _, name := range v3Sections {
		start := -1
		for i, raw := range lines {
			if strings.TrimSpace(raw) == "## "+name {
				start = i + 1
				break
			}
		}
		if start < 0 {
			empty = append(empty, name+" (missing)")
			continue
		}
		filled := false
		for i := start; i < len(lines); i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "#") {
				break // next section
			}
			if t != "" && !strings.HasPrefix(t, "<") {
				filled = true
				break
			}
		}
		if !filled {
			empty = append(empty, name)
		}
	}
	return empty
}

// claimGatesV3 checks each claim row: an accepted epic-blocking claim must be verified pass, and a captain_decision must
// carry a captain-agrees answer. Columns are located by header name so a reorder does not slip a row past a gate.
func claimGatesV3(content string) error {
	cols := map[string]int{}
	inTable := false
	for lineNo, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			inTable, cols = false, map[string]int{}
			continue
		}
		cells := tableCells(line)
		if !inTable {
			for i, c := range cells {
				cols[strings.ToLower(strings.TrimSpace(c))] = i
			}
			_, hasVerdict := cols["verdict"]
			_, hasSeverity := cols["severity"]
			if hasVerdict && hasSeverity {
				inTable = true
			}
			continue
		}
		if isDashRow(cells) {
			continue
		}
		verdict := strings.ToLower(colVal(cells, cols, "verdict"))
		severity := strings.ToLower(colVal(cells, cols, "severity"))
		verified := strings.ToLower(colVal(cells, cols, "verified"))
		if verdict == "accepted" && severity == "epic-blocking" && verified != "pass" {
			got := verified
			if got == "" {
				got = "no check run"
			}
			return fmt.Errorf("cannot sign: accepted epic-blocking claim on line %d is not verified pass (verified=%s); run cox arena verify", lineNo+1, got)
		}
		if verdict == "captain_decision" && colVal(cells, cols, "captain agrees") == "" {
			return fmt.Errorf("cannot sign: captain_decision on line %d has no 'captain agrees' answer", lineNo+1)
		}
	}
	return nil
}

func colVal(cells []string, cols map[string]int, name string) string {
	i, ok := cols[name]
	if !ok || i < 0 || i >= len(cells) {
		return ""
	}
	return strings.TrimSpace(cells[i])
}

// roundLinkRe captures the round from a synthesis.md symlink target synthesis-round-<N>.md.
var roundLinkRe = regexp.MustCompile(`synthesis-round-(\d+)\.md$`)

// synthesisRound returns the round a synthesis.md points at (via its symlink target), or 0 when it is a plain file or the
// target does not name a round (a v2 synthesis has no round; the round gate then does not apply).
func synthesisRound(synthPath string) int {
	target, err := os.Readlink(synthPath)
	if err != nil {
		return 0
	}
	m := roundLinkRe.FindStringSubmatch(target)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// isSigned reports whether the epic has a design_signed event.
func isSigned(epicDir string) (bool, error) {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return false, err
	}
	for _, ev := range events {
		if ev.Type == state.DesignSigned {
			return true, nil
		}
	}
	return false, nil
}

// blankColumn returns the 1-based source lines of synthesis claim rows whose named column fails valid. The column is
// located by its header (case-insensitive), so a template column reorder does not silently pass a blank; valid receives
// the trimmed cell value. A row missing the column entirely counts as failing.
func blankColumn(synthPath, header string, valid func(string) bool) ([]string, error) {
	b, err := os.ReadFile(synthPath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	col := -1
	inTable := false
	var blanks []string
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			inTable = false
			col = -1
			continue
		}
		cells := tableCells(line)
		if col == -1 {
			for idx, c := range cells {
				if strings.EqualFold(strings.TrimSpace(c), header) {
					col = idx
					inTable = true
					break
				}
			}
			continue // header row
		}
		if !inTable || isDashRow(cells) {
			continue
		}
		if col >= len(cells) || !valid(strings.TrimSpace(cells[col])) {
			blanks = append(blanks, fmt.Sprintf("%d", i+1))
		}
	}
	return blanks, nil
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
