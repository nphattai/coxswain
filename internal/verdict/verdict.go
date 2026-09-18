// Package verdict computes the three-state results (pass | fail | unknown) for a story PR audit and an epic ship
// readiness check. Unknown is never promoted to pass: any retrieval error, pending CI, or unverifiable fetch yields
// unknown with a reason (P5, F12). Verdicts are bound to a head sha so a later push makes them stale.
package verdict

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/forge"
)

// Verdict is the three-state result.
type Verdict string

const (
	Pass    Verdict = "pass"
	Fail    Verdict = "fail"
	Unknown Verdict = "unknown"
)

// credRules is the v1 bin/audit-pr.sh credential scan, one named rule per pattern so a match reports the rule (and a
// count) rather than the secret itself. RE2 (Go's regexp) supports every pattern here.
var credRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"vn-phone", regexp.MustCompile(`0[35789][0-9]{8}`)},
	{"insurance-id", regexp.MustCompile(`INSU[0-9]{8,}`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}`)},
	{"password-literal", regexp.MustCompile(`password *[:=] *["'][^"']{3,}`)},
	{"otp-request-id", regexp.MustCompile(`requestId *[:=] *["'][A-Za-z0-9]{8,}`)},
	{"pin-code", regexp.MustCompile(`x-pin-code[^\n]*[0-9]{4,6}`)},
}

// scanCredentials returns the names of credential rules that matched text, sorted and de-duplicated. The matched
// substrings are never returned or logged, only the rule names, so audit output carries no secret.
func scanCredentials(text string) []string {
	hit := map[string]bool{}
	for _, r := range credRules {
		if r.re.MatchString(text) {
			hit[r.name] = true
		}
	}
	out := make([]string, 0, len(hit))
	for k := range hit {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CI aggregates check runs into a three-state verdict (pass | fail | unknown), the exported entry point cox state's
// forge block and the scorecard share so the aggregation rule lives in one place.
func CI(checks []forge.Check) Verdict { return ciStatus(checks) }

// ciStatus aggregates check runs into pass | fail | pending. Any not-completed run (queued/in_progress/pending) makes
// the whole set pending; any completed run with a non-success, non-neutral conclusion makes it fail; an empty set is
// pending (we cannot confirm CI ran, so we never call it a pass).
func ciStatus(checks []forge.Check) Verdict {
	if len(checks) == 0 {
		return Unknown // treated as pending: no evidence CI passed
	}
	pending := false
	for _, c := range checks {
		if strings.ToLower(c.Status) != "completed" {
			pending = true
			continue
		}
		switch strings.ToLower(c.Conclusion) {
		case "success", "neutral", "skipped", "":
			// ok
		default:
			return Fail
		}
	}
	if pending {
		return Unknown
	}
	return Pass
}

// reviewerVerdict scans review comments for a bot reviewer's verdict marker (v1 rs-pr-reviewer). It returns the
// strongest negative found: "changes-requested" (blocks), "review-required" (warns), or "" when none is negative.
func reviewerVerdict(comments []forge.Comment) string {
	worst := ""
	for _, c := range comments {
		b := c.Body
		switch {
		case strings.Contains(b, "Changes Requested") || strings.Contains(b, "❌"):
			return "changes-requested"
		case strings.Contains(b, "Review Required") || strings.Contains(b, "⚠️"):
			worst = "review-required"
		}
	}
	return worst
}

// reason formats a "key: detail" reason line.
func reason(key, detail string) string { return fmt.Sprintf("%s: %s", key, detail) }
