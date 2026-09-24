package wake

import "github.com/nphattai/coxswain/internal/protocol/decision"

// OpenDecisionLine renders one still-open decision the way the drain's OPEN DECISIONS section prints it
// (fm-wake-drain.sh:444 print_open_decisions_section): "<story> [key=<k>] <verb>: <note>", with the key segment
// omitted for the default key.
func OpenDecisionLine(story string, d decision.Decision) string {
	line := story
	if d.Key != decision.DefaultKey {
		line += " [key=" + d.Key + "]"
	}
	return line + " " + d.Verb + ": " + d.Note
}
