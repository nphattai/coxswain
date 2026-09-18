// Package check machine-verifies an arena role's claim report. It reads the report with the report package (v2 or v3
// schema), confirms every evidence citation resolves to a real line at its pinned sha, and validates each severity.
// A report with any broken citation or bad severity fails, naming the offending source line, so a claim cannot cite
// something that does not exist (G10: citations checked by machine, not trusted by eye).
package check

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/arena/cite"
	"github.com/nphattai/coxswain/internal/arena/report"
)

// Severities is the allowed set, in report order.
var Severities = []string{"epic-blocking", "significant", "minor"}

// checkedSeverities are the severities that require a runnable `check` and are verified by cox arena verify (ADR 0013).
var checkedSeverities = map[string]bool{"epic-blocking": true, "significant": true}

func validSeverity(s string) bool {
	for _, v := range Severities {
		if v == s {
			return true
		}
	}
	return false
}

// Report verifies one report file. counts is the claim count per severity (for a clean report); errs is one message per
// problem, each naming the source line; warnings carries non-fatal notices (a v2-schema report, ADR 0013). err is
// non-nil only when the file cannot be read.
func Report(epicDir, reportPath string) (counts map[string]int, errs, warnings []string, err error) {
	b, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read report: %w", err)
	}
	rep := report.Parse(string(b))
	v3 := rep.Version == report.V3
	if !v3 {
		warnings = append(warnings, "v2 report (no evidence tiers or verifiable checks); rewrite as coxswain.arena.v3")
	}
	// The known repo aliases (from the epic repos file) drive two format checks: a citation whose leading segment is not
	// an alias needs a `<alias>/` prefix (an error), and a command check whose path carries an alias prefix is stripped at
	// verify time (a warning, not a fatal). Best-effort: an unreadable repos file yields an empty set and neither fires.
	aliases, _ := cite.Aliases(epicDir)
	aliasList := sortedKeys(aliases)
	counts = map[string]int{}
	for _, c := range rep.Claims {
		if !validSeverity(c.Severity) {
			errs = append(errs, fmt.Sprintf("line %d: invalid severity %q (want %s)", c.Line, c.Severity, strings.Join(Severities, "/")))
		} else {
			counts[c.Severity]++
		}
		if v3 {
			errs = append(errs, validateV3(c)...)
		}
		errs = append(errs, verifyEvidence(epicDir, c, aliasList)...)
		warnings = append(warnings, checkPrefixWarnings(c, aliases)...)
	}
	return counts, errs, warnings, nil
}

// sortedKeys returns the map keys in stable order (for the known-aliases hint).
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkPrefixWarnings warns (never fails) when a command check carries the `<alias>/` prefix on a path: verify runs a
// command at the repo root and strips a leading alias, so the report is still usable, but the author should drop the
// prefix. An assertion check keeps its `alias/path:line@sha` syntax and is skipped (M13 round 1 format finding).
func checkPrefixWarnings(c report.Claim, aliases map[string]bool) []string {
	check := strings.TrimSpace(c.Check)
	if check == "" || strings.Contains(check, `== "`) {
		return nil
	}
	for _, f := range strings.Fields(check) {
		alias, rest, ok := strings.Cut(f, "/")
		if ok && rest != "" && aliases[alias] {
			return []string{fmt.Sprintf("line %d: check commands run at the repo root; alias prefix stripped (%q)", c.Line, f)}
		}
	}
	return nil
}

// validateV3 applies the ADR 0013 claim rules to one v3 claim: tier in 1-5, confidence in 0-100, a tier-5 claim may not
// be epic-blocking, and an epic-blocking or significant claim must carry a runnable check. Each failure names the source
// line. These rules run only for a v3 report; a v2 report has none of these columns and only earns the schema warning.
func validateV3(c report.Claim) []string {
	var errs []string
	tier, tierOK := intInRange(c.Tier, 1, 5)
	if !tierOK {
		errs = append(errs, fmt.Sprintf("line %d: invalid tier %q (want 1-5)", c.Line, c.Tier))
	}
	if _, ok := intInRange(c.Confidence, 0, 100); !ok {
		errs = append(errs, fmt.Sprintf("line %d: invalid confidence %q (want 0-100)", c.Line, c.Confidence))
	}
	if tierOK && tier == 5 && c.Severity == "epic-blocking" {
		errs = append(errs, fmt.Sprintf("line %d: a tier-5 (reasoned inference) claim cannot be epic-blocking (ADR 0013)", c.Line))
	}
	if checkedSeverities[c.Severity] && strings.TrimSpace(c.Check) == "" {
		errs = append(errs, fmt.Sprintf("line %d: %s claim needs a check (a command or `alias/path:line@sha == \"text\"`)", c.Line, c.Severity))
	}
	return errs
}

// intInRange parses s as an integer and reports whether it is within [lo, hi]. A blank or non-integer s is out of range
// (the column is required in a v3 report), so the caller flags it.
func intInRange(s string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return n, n >= lo && n <= hi
}

// verifyEvidence checks every citation in a claim's evidence cell. Arena evidence must be pinned with @sha (a claim is
// pinned to the commit it read), so an unpinned or unparseable citation is an error. A citation whose leading segment is
// not a repo alias (the author dropped the `<alias>/` prefix) gets a format hint naming the known aliases, rather than the
// raw "unknown repo alias" from RepoDir (M13 round 1 format finding). knownAliases is the sorted repo-alias list for that
// hint (empty when the repos file is unreadable, in which case the alias check is skipped so a bare test still resolves
// via the epic symlink).
func verifyEvidence(epicDir string, c report.Claim, knownAliases []string) []string {
	var errs []string
	fields := strings.Split(c.Evidence, ";")
	sawOne := false
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		sawOne = true
		cit, ok := cite.Parse(f)
		if !ok {
			errs = append(errs, fmt.Sprintf("line %d: unparseable evidence %q (want alias/path:line@sha)", c.Line, f))
			continue
		}
		if cit.SHA == "" {
			errs = append(errs, fmt.Sprintf("line %d: evidence %q is not pinned with @sha", c.Line, f))
			continue
		}
		if len(knownAliases) > 0 && !contains(knownAliases, cit.Alias) {
			errs = append(errs, fmt.Sprintf("line %d: citation %q needs `<alias>/`: known aliases %s", c.Line, f, strings.Join(knownAliases, ", ")))
			continue
		}
		if e := cite.Verify(epicDir, cit); e != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", c.Line, e))
		}
	}
	if !sawOne {
		errs = append(errs, fmt.Sprintf("line %d: claim has no evidence", c.Line))
	}
	return errs
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// FormatCounts renders a severity summary in a stable order for the CLI.
func FormatCounts(counts map[string]int) string {
	var parts []string
	for _, sev := range Severities {
		parts = append(parts, fmt.Sprintf("%s=%d", sev, counts[sev]))
	}
	// Any unexpected severities that slipped through (should not happen after validation) are listed too.
	var extra []string
	for k := range counts {
		if !validSeverity(k) {
			extra = append(extra, fmt.Sprintf("%s=%d", k, counts[k]))
		}
	}
	sort.Strings(extra)
	return strings.Join(append(parts, extra...), " ")
}
