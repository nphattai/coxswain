// Package synth assembles the per-role claim reports into one synthesis skeleton the leader fills with verdicts. It
// re-verifies each report (a synthesis must never be built from unchecked claims) and renders reports/arena/synthesis.md
// with every claim filled and the verdict columns left blank for the leader (and the captain-agrees column for
// calibration, P7).
package synth

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/nphattai/coxswain/internal/arena/check"
	"github.com/nphattai/coxswain/internal/arena/report"
	"github.com/nphattai/coxswain/templates"
)

// Row is one synthesis claim: a parsed claim tagged with the role that raised it. Tier and Verified are filled from the
// report and the verify-round-N.json automatically (ADR 0013); the leader fills the verdict columns.
type Row struct {
	ID       string // stable claim id "<role>-<round>-<n>" (from the report, or assigned here) - the answer key
	Role     string
	Claim    string
	Evidence string
	Tier     string
	Severity string
	Verified string // pass | fail | unknown | "" (no check ran)
}

// synthData renders the synthesis template.
type synthData struct {
	Slug   string
	Claims []Row
}

// roundFileRe matches reports/arena/round-<n>-<role>.md and captures the role.
var roundFileRe = regexp.MustCompile(`^round-\d+-([a-z]+)\.md$`)

// Build reads every round-<round>-<role>.md report, re-checks it, and writes reports/arena/synthesis-round-<round>.md,
// then points reports/arena/synthesis.md at it as a symlink (so sign and check always read the latest round). It returns
// the per-round synthesis path. If any report has a broken citation or bad severity, Build returns an error listing them
// and writes nothing: a synthesis is only built over checked-clean reports. The F1 --force guard applies per file: when
// the round's own synthesis file already carries adjudication (any non-blank verdict or captain-agrees cell), Build
// refuses unless force is set, so a rebuild never clobbers the captain's work - and a different round's adjudication is
// never at risk because it lives in its own file.
func Build(epicDir string, round int, force bool) (string, error) {
	dir := filepath.Join(epicDir, "reports", "arena")
	link := filepath.Join(dir, "synthesis.md")
	out := filepath.Join(dir, fmt.Sprintf("synthesis-round-%d.md", round))
	if !force {
		if b, err := os.ReadFile(out); err == nil && HasAdjudication(string(b)) {
			return "", fmt.Errorf("%s already has adjudication (a verdict or captain-agrees cell); pass --force to overwrite", out)
		}
		// Protect a pre-per-round synthesis.md: a plain file (not yet a symlink) that still carries adjudication holds a
		// past round's verdicts and would be lost when synthesis.md is relinked. Refuse until it is preserved.
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			if b, err := os.ReadFile(link); err == nil && HasAdjudication(string(b)) {
				return "", fmt.Errorf("%s is a plain file with adjudication (a past round); rename it to synthesis-round-<N>.md before per-round synth, or pass --force", link)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read arena reports: %w", err)
	}
	prefix := fmt.Sprintf("round-%d-", round)
	var rows []Row
	var problems []string
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		m := roundFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		found = true
		role := m[1]
		reportPath := filepath.Join(dir, e.Name())
		_, errs, _, err := check.Report(epicDir, reportPath)
		if err != nil {
			return "", err
		}
		for _, msg := range errs {
			problems = append(problems, e.Name()+": "+msg)
		}
		b, err := os.ReadFile(reportPath)
		if err != nil {
			return "", err
		}
		n := 0
		for _, c := range report.Parse(string(b)).Claims {
			n++
			id := strings.TrimSpace(c.Id)
			if id == "" {
				id = fmt.Sprintf("%s-%d-%d", role, round, n) // cox assigns a stable id when the role did not write one
			}
			rows = append(rows, Row{ID: id, Role: role, Claim: c.Claim, Evidence: c.Evidence, Tier: c.Tier, Severity: c.Severity})
		}
	}
	if !found {
		return "", fmt.Errorf("no round-%d-<role>.md reports in %s", round, dir)
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("synthesis aborted: reports not clean:\n  %s", strings.Join(problems, "\n  "))
	}
	// Fold in the machine verify results (reports/arena/verify-round-N.json) so the leader adjudicates against the checked
	// status, not a remembered one. A claim with no verify result (no check ran) keeps a blank verified cell.
	verified := loadVerified(dir, round)
	for i := range rows {
		if st, ok := verified[verifyKey(rows[i].Role, rows[i].Claim)]; ok {
			rows[i].Verified = st
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Role < rows[j].Role })

	tmplBytes, err := templates.File("arena/synthesis.md")
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("synthesis").Parse(string(tmplBytes))
	if err != nil {
		return "", fmt.Errorf("parse synthesis template: %w", err)
	}
	// round2 is not seeded into the file (a static seed can go stale and lie). cox arena check computes it live with
	// synth.Round2 over the current table, and the sign gate recomputes it over the adjudicated table.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, synthData{Slug: filepath.Base(epicDir), Claims: rows}); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := relinkLatest(link, filepath.Base(out)); err != nil {
		return "", err
	}
	// Record the sha of the synthesis just written, so cox arena answer can refuse to write a captain cell by id against a
	// synthesis that was re-synthesized (its ids may have shifted) since (ADR 0013, M13 answer path).
	if err := WriteSha(epicDir, round, buf.Bytes()); err != nil {
		return "", err
	}
	return out, nil
}

// shaPath is where synth records the content sha of a round's synthesis (.cox/arena/synthesis-<round>.sha).
func shaPath(epicDir string, round int) string {
	return filepath.Join(epicDir, ".cox", "arena", fmt.Sprintf("synthesis-%d.sha", round))
}

// ContentSha is the synthesis fingerprint used for the answer guard: sha256 of the content, first 12 hex, matching the
// synthesis_sha epic sign records.
func ContentSha(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum)[:12]
}

// WriteSha records the content sha of a round's synthesis under .cox/arena, creating the dir. cox arena answer reads it.
func WriteSha(epicDir string, round int, content []byte) error {
	p := shaPath(epicDir, round)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(ContentSha(content)+"\n"), 0o644)
}

// ReadSha returns the recorded synthesis sha for a round, or "" when none was recorded (synth predates the answer path).
func ReadSha(epicDir string, round int) string {
	b, err := os.ReadFile(shaPath(epicDir, round))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// loadVerified reads reports/arena/verify-round-N.json and returns a map from (role, claim) to the check status. A
// missing or unreadable file yields an empty map (verify has not run yet; the verified cells stay blank), never an error:
// synth still builds so the leader can adjudicate and run verify after.
func loadVerified(dir string, round int) map[string]string {
	b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("verify-round-%d.json", round)))
	if err != nil {
		return map[string]string{}
	}
	var r struct {
		Results []struct {
			Role, Claim, Status string
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(r.Results))
	for _, res := range r.Results {
		out[verifyKey(res.Role, res.Claim)] = res.Status
	}
	return out
}

// verifyKey joins a role and claim into a stable map key (both come from the same parsed report, so the text matches).
func verifyKey(role, claim string) string {
	return role + "\x00" + strings.TrimSpace(claim)
}

// relinkLatest points synthesis.md at the round file just written, as a relative symlink so sign and check read the
// latest round. Any existing synthesis.md (a symlink from a prior round, or a plain file the --force path replaced) is
// removed first. The target is a bare basename since the link sits in the same directory.
func relinkLatest(link, target string) error {
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace synthesis.md: %w", err)
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("link synthesis.md -> %s: %w", target, err)
	}
	return nil
}
