package artifact

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nphattai/coxswain/board"
)

// artifactCSS layers a few rules on the shared board CSS (board.CSS) for the review pages: readable prose width, a claim
// answer input, and a "missing" marker for the comparison. No CDN, inlined like the board.
const artifactCSS = `
main { max-width: 1100px; margin: 0 auto; }
h1, h2, h3 { color: var(--fg); }
h2 { border-bottom: 1px solid var(--border); padding-bottom: 6px; margin-top: 28px; }
p, li { color: var(--fg); }
blockquote { border-left: 3px solid var(--border); margin: 8px 0; padding: 2px 12px; color: var(--muted); }
pre { background: var(--panel); border: 1px solid var(--border); border-radius: 6px; padding: 10px 12px; overflow-x: auto; }
a { color: var(--accent); }
.claim { border: 1px solid var(--border); border-radius: 8px; padding: 10px 14px; margin: 10px 0; background: var(--panel); }
.claim .head { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-bottom: 6px; }
.claim .cid { font-family: ui-monospace, Menlo, monospace; font-size: 12px; color: var(--accent); }
.answer { width: 100%; margin-top: 8px; padding: 6px 8px; background: var(--bg); color: var(--fg); border: 1px solid var(--border); border-radius: 6px; }
.miss { color: var(--bad); font-weight: 600; }
.have { color: var(--ok); }
.verdict-accepted { color: var(--ok); }
.verdict-rejected { color: var(--bad); }
.verdict-captain_decision, .verdict-unresolved { color: var(--warn); }
`

// Page wraps body HTML in the self-contained shell: the shared board CSS plus artifactCSS inlined, a header with a title,
// meta line, and a read-only badge. It opens with no network and loads no external resource (DESIGN: self-contained like
// the board, no CDN).
func Page(title, meta, badge, body string) []byte {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n<style>%s\n%s</style>\n</head>\n<body>\n", html.EscapeString(title), board.CSS, artifactCSS)
	fmt.Fprintf(&b, "<header><h1>%s</h1><span class=\"meta\">%s</span><span class=\"readonly\">%s</span></header>\n<main>\n",
		html.EscapeString(title), html.EscapeString(meta), html.EscapeString(badge))
	b.WriteString(body)
	b.WriteString("</main>\n</body>\n</html>\n")
	return []byte(b.String())
}

// writePage writes the page and its sidecar under <epic>/reports/visual and returns the page path.
func writePage(epicDir, name string, page []byte, s Sidecar) (string, error) {
	dir := VisualDir(epicDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create visual dir: %w", err)
	}
	pagePath := filepath.Join(dir, name)
	if err := os.WriteFile(pagePath, page, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	if s.GeneratedAt == "" {
		s.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if s.Generator == "" {
		s.Generator = GenCox
	}
	if err := Write(pagePath, s); err != nil {
		return "", err
	}
	return pagePath, nil
}

// sourcesFor builds Sources for a set of existing files (missing files are skipped).
func sourcesFor(epicDir string, paths ...string) ([]Source, error) {
	var out []Source
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		src, err := NewSource(epicDir, p)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, nil
}

// GenerateDesign renders <epic>/DESIGN.md (designMD), the decisions still in force (decisionsDir, best-effort), and the
// latest arena synthesis (synthesisMD, when present) into reports/visual/design.html with a design sidecar.
func GenerateDesign(epicDir, designMD, synthesisMD, decisionsDir string) (string, error) {
	dm, err := os.ReadFile(designMD)
	if err != nil {
		return "", fmt.Errorf("read DESIGN.md: %w", err)
	}
	var body strings.Builder
	body.WriteString(Markdown(string(dm)))

	if section := decisionsInForce(decisionsDir); section != "" {
		body.WriteString("<h2>Decisions in force</h2>\n")
		body.WriteString(section)
	}
	if synthesisMD != "" {
		if sm, err := os.ReadFile(synthesisMD); err == nil {
			body.WriteString("<h2>Latest arena synthesis</h2>\n")
			body.WriteString(Markdown(string(sm)))
		}
	}
	sources, err := sourcesFor(epicDir, designMD, synthesisMD)
	if err != nil {
		return "", err
	}
	page := Page("Design - "+filepath.Base(epicDir), "epic "+filepath.Base(epicDir),
		"read-only source render - decisions and answers go through cox", body.String())
	return writePage(epicDir, "design.html", page, Sidecar{Kind: KindDesign, Sources: sources})
}

var adrTitleRe = regexp.MustCompile(`^#\s+(\d{4}.*)$`)
var adrStatusRe = regexp.MustCompile(`(?i)^-?\s*status:\s*(.*)$`)

// decisionsInForce lists the ADRs under decisionsDir whose Status begins "Accepted" (superseded/rejected are dropped),
// as an HTML list. Best-effort: an unreadable dir yields "" and the caller drops the section.
func decisionsInForce(decisionsDir string) string {
	if decisionsDir == "" {
		return ""
	}
	entries, err := os.ReadDir(decisionsDir)
	if err != nil {
		return ""
	}
	type adr struct{ title, status string }
	var list []adr
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(decisionsDir, e.Name()))
		if err != nil {
			continue
		}
		var title, status string
		for _, line := range strings.Split(string(b), "\n") {
			if title == "" {
				if m := adrTitleRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
					title = m[1]
				}
			}
			if status == "" {
				if m := adrStatusRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
					status = m[1]
				}
			}
			if title != "" && status != "" {
				break
			}
		}
		if title != "" && strings.HasPrefix(strings.ToLower(status), "accepted") {
			list = append(list, adr{title, status})
		}
	}
	if len(list) == 0 {
		return ""
	}
	sort.Slice(list, func(i, j int) bool { return list[i].title < list[j].title })
	var b strings.Builder
	b.WriteString("<ul>\n")
	for _, a := range list {
		fmt.Fprintf(&b, "<li>%s <span class=\"muted\">(%s)</span></li>\n", inline(a.title), html.EscapeString(a.status))
	}
	b.WriteString("</ul>\n")
	return b.String()
}

var phaseFileRe = regexp.MustCompile(`^phase-\d`)

// GeneratePlan renders planDir/plan.md and each phase-*.md (with its status) into reports/visual/plan-<slug>.html with a
// plan sidecar.
func GeneratePlan(epicDir, planDir string) (string, error) {
	var body strings.Builder
	var sources []string
	if planMD := filepath.Join(planDir, "plan.md"); fileExists(planMD) {
		b, err := os.ReadFile(planMD)
		if err != nil {
			return "", err
		}
		body.WriteString(Markdown(string(b)))
		sources = append(sources, planMD)
	}
	phases, err := phaseFiles(planDir)
	if err != nil {
		return "", err
	}
	if len(phases) > 0 {
		body.WriteString("<h2>Phases</h2>\n")
	}
	for _, p := range phases {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		title, status, rest := phaseHead(string(b))
		fmt.Fprintf(&body, "<div class=\"claim\"><div class=\"head\"><b>%s</b> <span class=\"pill state-%s\">%s</span></div>\n",
			inline(title), statusClass(status), html.EscapeString(orUnknown(status)))
		body.WriteString(Markdown(rest))
		body.WriteString("</div>\n")
		sources = append(sources, p)
	}
	if body.Len() == 0 {
		return "", fmt.Errorf("no plan.md or phase-*.md under %s", planDir)
	}
	srcs, err := sourcesFor(epicDir, sources...)
	if err != nil {
		return "", err
	}
	page := Page("Plan - "+filepath.Base(planDir), "plan "+filepath.Base(planDir),
		"read-only source render", body.String())
	return writePage(epicDir, "plan-"+slug(filepath.Base(planDir))+".html", page, Sidecar{Kind: KindPlan, Sources: srcs})
}

// GenerateArenaSynth renders the round's synthesis claims into reports/visual/arena-synth-round-N.html: each claim by
// role with tier, verified, and verdict, and a data-claim-id input for a captain_decision or an unanswered row. The
// sidecar records the synthesis content sha so the answer path can refuse a stale artifact.
func GenerateArenaSynth(epicDir string, round int, synthContentSha func([]byte) string) (string, error) {
	dir := filepath.Join(epicDir, "reports", "arena")
	file := filepath.Join(dir, fmt.Sprintf("synthesis-round-%d.md", round))
	if !fileExists(file) {
		file = filepath.Join(dir, "synthesis.md") // fall back to the latest symlink
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read synthesis: %w", err)
	}
	rows := parseSynthClaims(string(content))
	if len(rows) == 0 {
		return "", fmt.Errorf("no claims table in %s", file)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "<p class=\"muted\">%d claims. To answer a captain_decision or fill a captain-agrees cell, annotate the row in lavish or send the prompt <code>answer &lt;id&gt; &lt;yes|no|text&gt;</code> (playbook input); cox arena answer writes the cell after a sha check.</p>\n", len(rows))
	role := ""
	for _, r := range rows {
		if r.Role != role {
			role = r.Role
			fmt.Fprintf(&body, "<h2>%s</h2>\n", inline(role))
		}
		body.WriteString("<div class=\"claim\">\n<div class=\"head\">")
		fmt.Fprintf(&body, "<span class=\"cid\">%s</span>", html.EscapeString(r.ID))
		if r.Tier != "" {
			fmt.Fprintf(&body, "<span class=\"pill\">tier %s</span>", html.EscapeString(r.Tier))
		}
		if r.Severity != "" {
			fmt.Fprintf(&body, "<span class=\"pill\">%s</span>", html.EscapeString(r.Severity))
		}
		if r.Verified != "" {
			fmt.Fprintf(&body, "<span class=\"pill\">verified: %s</span>", html.EscapeString(r.Verified))
		}
		if r.Verdict != "" {
			fmt.Fprintf(&body, "<span class=\"pill verdict-%s\">%s</span>", statusClass(r.Verdict), html.EscapeString(r.Verdict))
		}
		body.WriteString("</div>\n")
		fmt.Fprintf(&body, "<p>%s</p>\n", inline(r.Claim))
		if r.Question != "" {
			fmt.Fprintf(&body, "<p><b>captain decision:</b> %s</p>\n", inline(r.Question))
		}
		if r.CaptainAgrees != "" {
			fmt.Fprintf(&body, "<p class=\"muted\">captain agrees: %s</p>\n", inline(r.CaptainAgrees))
		} else if r.Verdict == "captain_decision" || r.Question != "" {
			fmt.Fprintf(&body, "<input class=\"answer\" data-claim-id=\"%s\" placeholder=\"answer %s &lt;yes|no|text&gt;\">\n", html.EscapeString(r.ID), html.EscapeString(r.ID))
		}
		body.WriteString("</div>\n")
	}
	src, err := NewSource(epicDir, file)
	if err != nil {
		return "", err
	}
	page := Page(fmt.Sprintf("Arena synthesis round %d - %s", round, filepath.Base(epicDir)),
		"epic "+filepath.Base(epicDir), "answers go through cox arena answer (sha-checked)", body.String())
	return writePage(epicDir, fmt.Sprintf("arena-synth-round-%d.html", round), page,
		Sidecar{Kind: KindArena, Sources: []Source{src}, SynthesisSHA: synthContentSha(content)})
}

// GenerateCompare renders a plan-vs-architecture coverage table into reports/visual/plan-compare.html: architecture
// sections marked with the plan phases that reference them, and plan phases with no architecture home. It also lists
// architecture sections no phase references. Missing on either side is flagged.
//
// ponytail: coverage is a keyword/token heuristic (a phase's milestone token like "M13" in a section, or a shared
// significant word between phase title and section heading), a review aid the captain confirms visually - not a proof.
func GenerateCompare(epicDir, planDir, archHTML string) (string, error) {
	phases, err := phaseFiles(planDir)
	if err != nil {
		return "", err
	}
	if len(phases) == 0 {
		return "", fmt.Errorf("no phase-*.md under %s", planDir)
	}
	ab, err := os.ReadFile(archHTML)
	if err != nil {
		return "", fmt.Errorf("read architecture html: %w", err)
	}
	sections := archSections(string(ab))
	archText := strings.ToLower(stripTags(string(ab)))

	type ph struct {
		num, title, status, file string
		covered                  bool
	}
	var ps []ph
	var sources []string
	for _, f := range phases {
		b, _ := os.ReadFile(f)
		title, status, _ := phaseHead(string(b))
		ps = append(ps, ph{num: phaseNum(f), title: title, status: status, file: f})
		sources = append(sources, f)
	}

	var body strings.Builder
	body.WriteString("<h2>Architecture sections vs plan phases</h2>\n<table>\n<thead><tr><th>section</th><th>heading</th><th>covered by phase(s)</th></tr></thead>\n<tbody>\n")
	for _, s := range sections {
		var hits []string
		for i := range ps {
			if covers(ps[i].title, ps[i].num, s.heading) {
				hits = append(hits, "M"+ps[i].num)
				ps[i].covered = true
			}
		}
		cell := "<span class=\"miss\">MISSING (no plan phase)</span>"
		if len(hits) > 0 {
			cell = "<span class=\"have\">" + html.EscapeString(strings.Join(hits, ", ")) + "</span>"
		}
		fmt.Fprintf(&body, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n", html.EscapeString(s.id), inline(s.heading), cell)
	}
	body.WriteString("</tbody>\n</table>\n")

	body.WriteString("<h2>Plan phases</h2>\n<table>\n<thead><tr><th>phase</th><th>title</th><th>status</th><th>in architecture</th></tr></thead>\n<tbody>\n")
	for _, p := range ps {
		inArch := "<span class=\"miss\">MISSING (no section references it)</span>"
		// A phase is "in architecture" if a section covered it, or its milestone token appears in the architecture text.
		if p.covered || strings.Contains(archText, "m"+p.num) || strings.Contains(archText, "phase "+p.num) {
			inArch = "<span class=\"have\">yes</span>"
		}
		fmt.Fprintf(&body, "<tr><td>M%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(p.num), inline(p.title), html.EscapeString(orUnknown(p.status)), inArch)
	}
	body.WriteString("</tbody>\n</table>\n")

	srcs, err := sourcesFor(epicDir, append(sources, archHTML)...)
	if err != nil {
		return "", err
	}
	page := Page("Plan vs architecture - "+filepath.Base(planDir), "plan "+filepath.Base(planDir),
		"read-only coverage heuristic - confirm visually", body.String())
	return writePage(epicDir, "plan-compare.html", page, Sidecar{Kind: KindComparison, Sources: srcs})
}

// --- helpers ---

type archSection struct{ id, heading string }

var archH2Re = regexp.MustCompile(`(?is)<h2[^>]*\bid="([^"]+)"[^>]*>(.*?)</h2>`)

// archSections extracts each <h2 id="..."> section id and its heading text (tags stripped) from the architecture html.
func archSections(htmlSrc string) []archSection {
	var out []archSection
	for _, m := range archH2Re.FindAllStringSubmatch(htmlSrc, -1) {
		out = append(out, archSection{id: m[1], heading: strings.TrimSpace(stripTags(m[2]))})
	}
	return out
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string { return tagRe.ReplaceAllString(s, " ") }

var stopwords = map[string]bool{"the": true, "and": true, "cho": true, "với": true, "và": true, "của": true, "plan": true, "phase": true}

// covers reports whether a phase (title + milestone num) covers an architecture section heading: the milestone token in
// the heading, or a shared significant word (len>=4, not a stopword).
func covers(title, num, heading string) bool {
	h := strings.ToLower(heading)
	if num != "" && strings.Contains(h, "m"+num) {
		return true
	}
	titleWords := words(title)
	for w := range words(heading) {
		if len(w) >= 4 && !stopwords[w] && titleWords[w] {
			return true
		}
	}
	return false
}

var wordRe = regexp.MustCompile(`[a-zA-Z0-9]+`)

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		out[w] = true
	}
	return out
}

// phaseFiles returns the phase-*.md files in a plan dir, sorted by name.
func phaseFiles(planDir string) ([]string, error) {
	entries, err := os.ReadDir(planDir)
	if err != nil {
		return nil, fmt.Errorf("read plan dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && phaseFileRe.MatchString(e.Name()) && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, filepath.Join(planDir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

var phaseNumRe = regexp.MustCompile(`phase-0*(\d+)`)

func phaseNum(file string) string {
	if m := phaseNumRe.FindStringSubmatch(filepath.Base(file)); m != nil {
		return m[1]
	}
	return ""
}

// phaseHead splits a phase file into its title, status (from frontmatter), and the body after the frontmatter.
func phaseHead(content string) (title, status, body string) {
	fm, rest := splitFM(content)
	body = rest
	for _, line := range strings.Split(fm, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
		switch strings.TrimSpace(k) {
		case "title":
			title = v
		case "status":
			status = v
		}
	}
	if title == "" {
		title = "(untitled phase)"
	}
	return title, status, body
}

// splitFM returns a leading `---` frontmatter block (without fences) and the body after it, or "" and the whole content.
func splitFM(content string) (fm, body string) {
	s := strings.TrimLeft(content, "\n")
	if !strings.HasPrefix(s, "---\n") {
		return "", content
	}
	rest := s[4:]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		after := rest[i+4:]
		return rest[:i], strings.TrimPrefix(after, "\n")
	}
	return "", content
}

func statusClass(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(s, "done") || strings.Contains(s, "complete") || strings.Contains(s, "accepted"), strings.Contains(s, "agreed"):
		return "completed"
	case strings.Contains(s, "progress"):
		return "working"
	case strings.Contains(s, "pending") || strings.Contains(s, "todo"):
		return "parked"
	}
	return ""
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// synthRow is one parsed synthesis claim row (the columns the artifact renders).
type synthRow struct {
	ID, Role, Claim, Tier, Severity, Verified, Verdict, Question, CaptainAgrees string
}

// parseSynthClaims parses the synthesis.md claims table into rows, locating columns by header name so a column reorder
// does not shift the read (same rule as report.Parse and synth.setCaptainAgrees).
func parseSynthClaims(content string) []synthRow {
	lines := strings.Split(content, "\n")
	idx := map[string]int{}
	inTable := false
	var out []synthRow
	for _, raw := range lines {
		t := strings.TrimSpace(raw)
		if !strings.HasPrefix(t, "|") {
			if inTable {
				break // table ended
			}
			continue
		}
		cells := tableCells(t)
		if !inTable {
			has := 0
			for i, c := range cells {
				name := strings.ToLower(strings.TrimSpace(c))
				idx[name] = i
				if name == "claim" || name == "role" || name == "verdict" {
					has++
				}
			}
			if has == 3 {
				inTable = true
			}
			continue
		}
		if isDashCells(cells) {
			continue
		}
		get := func(name string) string {
			i, ok := idx[name]
			if !ok || i >= len(cells) {
				return ""
			}
			return strings.TrimSpace(cells[i])
		}
		out = append(out, synthRow{
			ID: get("id"), Role: get("role"), Claim: get("claim"), Tier: get("tier"),
			Severity: get("severity"), Verified: get("verified"), Verdict: get("verdict"),
			Question: get("question"), CaptainAgrees: get("captain agrees"),
		})
	}
	return out
}

func isDashCells(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(strings.TrimSpace(c), "-: ") != "" {
			return false
		}
	}
	return true
}
