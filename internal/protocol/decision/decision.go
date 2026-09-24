// Package decision ports firstmate's status-line grammar and keyed decision fold (bin/fm-classify-lib.sh, pinned
// 1e0e773) so every cox reader of a worker status line agrees on one statement of the rules. It is pure: lines in,
// verdicts out, no I/O. A worker's status history is an append-only event log; reading it last-event-wins cannot
// represent "an earlier decision is still open after a later, unrelated event", so Fold is the one authoritative
// open set: a needs-decision/blocked line OPENS a keyed decision, an explicit resolved or captain-held line naming
// that key CLOSES it, and a ship/scout terminal declaration (done:/failed:) supersedes everything open.
//
// Grammar (fm-classify-lib.sh:420-560): "<verb> [name=value]...: <note>". Tags before the first colon may come in any
// order: [key=<slug>] names the decision (a complete token at the head of the note is an equivalent position),
// [corr=<16 hex>] or a bare corr=<16 hex> after the verb is correlation metadata, [at=<epoch>] is the optional
// emission time. A line with no key token folds under "default". A malformed key is rejected, never rewritten.
package decision

import (
	"regexp"
	"strings"
	"sync"
)

// Verbs of the status-line vocabulary (fm-classify-lib.sh:103,131-132).
const (
	ResolveVerb     = "resolved"
	CaptainHeldVerb = "captain-held"
	PausedVerb      = "paused"
	DefaultKey      = "default"
)

// DefaultCaptainRE is FM_CLASSIFY_CAPTAIN_RE_DEFAULT (fm-classify-lib.sh:77): the captain-relevant set. The free-text
// tokens exist only for legacy lines that lack a standard terminal verb.
const DefaultCaptainRE = `done:|needs-decision:|blocked:|failed:|PR ready|checks green|ready in branch|merged`

// ReservedKeyPrefixes is FM_CLASSIFY_RESERVED_KEY_PREFIXES_DEFAULT (fm-classify-lib.sh:644): a reserved key may be
// opened or closed only by a line whose note speaks that namespace's own vocabulary.
var ReservedKeyPrefixes = []string{"pending-reply-"}

// Kind is the task kind the terminal-supersession rule reads (fm-classify-lib.sh:669 _fm_status_kind).
type Kind string

const (
	KindShip       Kind = "ship"
	KindScout      Kind = "scout"
	KindSecondmate Kind = "secondmate"
	KindUnknown    Kind = "unknown"
)

// ParseKind maps a recorded kind to a Kind; anything but ship/scout/secondmate is unknown.
func ParseKind(s string) Kind {
	switch k := Kind(strings.TrimSpace(s)); k {
	case KindShip, KindScout, KindSecondmate:
		return k
	}
	return KindUnknown
}

// Verbs names the two transition-closing verbs a caller may override (FM_CLASSIFY_RESOLVE_VERB,
// FM_CLASSIFY_CAPTAIN_HELD_VERB). The zero value means the defaults.
type Verbs struct{ Resolve, Held string }

func (v Verbs) resolve() string {
	if v.Resolve != "" {
		return v.Resolve
	}
	return ResolveVerb
}

func (v Verbs) held() string {
	if v.Held != "" {
		return v.Held
	}
	return CaptainHeldVerb
}

// Decision is one still-open record of the fold: its key, the opening verb, the note, and Line, the 1-based index of
// the line that opened it (firstmate's decision origin, fm-classify-lib.sh:2091).
type Decision struct {
	Key  string
	Verb string
	Note string
	Line int
}

// isSpace mirrors [[:space:]].
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}

func trimLeft(s string) string  { return strings.TrimLeftFunc(s, isSpace) }
func trimSpace(s string) string { return strings.TrimFunc(s, isSpace) }

// head returns the text before the first colon, or the whole line when it has none (bash ${line%%:*}).
func head(line string) string {
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return line[:i]
	}
	return line
}

// hasTag reports whether s holds "<open>...]" (the glob *\[<name>=*\]*).
func hasTag(s, open string) bool {
	i := strings.Index(s, open)
	return i >= 0 && strings.Contains(s[i+len(open):], "]")
}

// Unstamped strips every [at=...]-shaped run a worker could have written as the stamp, however malformed, while
// nothing before it holds a colon (fm-classify-lib.sh:388 _fm_status_unstamped). It is the shared head-boundary rule:
// a time tag never decides where the head ends, which note or key a line carries, or whether a decision moves.
func Unstamped(line string) string {
	rest, keep := line, ""
	for hasTag(rest, "[at=") {
		i := strings.Index(rest, "[at=")
		before := rest[:i]
		if strings.Contains(before, ":") {
			break
		}
		keep += strings.TrimSuffix(before, " ")
		rest = rest[i+len("[at="):]
		rest = rest[strings.Index(rest, "]")+1:]
	}
	return keep + rest
}

// AtEpoch returns the well-formed emission time of a line (fm-classify-lib.sh:301 _fm_status_at_epoch): one
// [at=<epoch>] tag before the first colon, canonical unsigned decimal, at most 12 digits. Missing, malformed or
// duplicate tags mean unknown time. Time describes history only and never decides state or closure.
func AtEpoch(line string) (string, bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", false
	}
	h := line[:i]
	if !hasTag(h, "[at=") {
		return "", false
	}
	rest := h[strings.Index(h, "[at=")+len("[at="):]
	j := strings.Index(rest, "]")
	value := rest[:j]
	if strings.Contains(rest[j+1:], "[at=") {
		return "", false
	}
	if value == "" || strings.TrimLeft(value, "0123456789") != "" || len(value) > 12 {
		return "", false
	}
	if len(value) > 1 && value[0] == '0' {
		return "", false
	}
	return value, true
}

// corrTokenRe is exactly the unbracketed correlation token firstmate's tooling writes (fm-classify-lib.sh:496).
var corrTokenRe = regexp.MustCompile(`^corr=[0-9A-Fa-f]{16}$`)

// Verb returns the leading verb word of a line (fm-classify-lib.sh:509 status_line_verb): the text before the first
// colon, ended at the first bracket tag, trimmed. When that prefix holds corr=, the first word is retained and only
// whole-word correlation tokens after it are dropped, so a token-first line keeps its token and its following word
// cannot impersonate a transition; an unrecognised word stays, so prose still matches no verb.
func Verb(line string) string {
	v := head(line)
	if i := strings.IndexByte(v, '['); i >= 0 {
		v = v[:i]
	}
	v = trimSpace(v)
	if !strings.Contains(v, "corr=") {
		return v
	}
	words := strings.FieldsFunc(v, isSpace)
	out := words[0]
	for _, w := range words[1:] {
		if corrTokenRe.MatchString(w) {
			continue
		}
		out += " " + w
	}
	return out
}

// keyBeforeColon reports a complete [key=...] token in the documented position before the first colon.
func keyBeforeColon(line string) bool { return hasTag(head(line), "[key=") }

// keyAtNoteHead returns the raw slug of a complete [key=<slug>] token at the head of the note.
func keyAtNoteHead(line string) (string, bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", false
	}
	rest := trimLeft(line[i+1:])
	if !strings.HasPrefix(rest, "[key=") || !strings.Contains(rest, "]") {
		return "", false
	}
	rest = rest[len("[key="):]
	return rest[:strings.Index(rest, "]")], true
}

var slugRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Note returns the text after the first colon, left-trimmed, read on the unstamped copy (fm-classify-lib.sh:573). A
// note-head key token that states the line's key is key metadata and is stripped, so both positions yield one note.
func Note(line string) string {
	u := Unstamped(line)
	i := strings.IndexByte(u, ':')
	if i < 0 {
		return u
	}
	n := trimLeft(u[i+1:])
	if !keyBeforeColon(u) {
		if k, ok := keyAtNoteHead(u); ok && slugRe.MatchString(k) {
			n = trimLeft(strings.TrimPrefix(n, "[key="+k+"]"))
		}
	}
	return n
}

// Key returns the decision key a line states (fm-classify-lib.sh:590 _fm_decision_key): the before-colon token wins,
// a note-head token is equivalent, no token is "default", and a malformed slug is rejected (ok=false).
func Key(line string) (string, bool) {
	u := Unstamped(line)
	var k string
	if keyBeforeColon(u) {
		k = head(u)
		k = k[strings.Index(k, "[key=")+len("[key="):]
		k = k[:strings.Index(k, "]")]
	} else if nk, ok := keyAtNoteHead(u); ok {
		k = nk
	} else {
		return DefaultKey, true
	}
	if !slugRe.MatchString(k) {
		return "", false
	}
	return k, true
}

// transitionAllowed is _fm_decision_key_transition_allowed (fm-classify-lib.sh:646).
func transitionAllowed(key, note string) bool {
	for _, p := range ReservedKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return strings.HasPrefix(note, p) && strings.Contains(note[len(p):], ":")
		}
	}
	return true
}

func isTerminalFor(verb string, kind Kind) bool {
	return (verb == "done" || verb == "failed") && (kind == KindShip || kind == KindScout)
}

func drop(open []Decision, key string) []Decision {
	out := open[:0:0]
	for _, d := range open {
		if d.Key != key {
			out = append(out, d)
		}
	}
	return out
}

// foldLine folds one line into the open set (fm-classify-lib.sh:681 _fm_decision_fold_line), the ONE place the
// per-line open/resolved rule is written. n is the line's 1-based index.
func foldLine(open []Decision, line string, n int, kind Kind, v Verbs) []Decision {
	u := Unstamped(line)
	// Declaration guard: a transition's verb ends at a colon or, colonless, at a complete [key=...] token.
	if !strings.Contains(u, ":") && !hasTag(u, "[key=") {
		return open
	}
	verb := Verb(line)
	if strings.Contains(u, ":") && isTerminalFor(verb, kind) {
		return nil
	}
	if verb != "needs-decision" && verb != "blocked" && verb != v.resolve() && verb != v.held() {
		return open
	}
	key, ok := Key(line)
	if !ok || !transitionAllowed(key, Note(line)) {
		return open
	}
	open = drop(open, key)
	if verb == "needs-decision" || verb == "blocked" {
		open = append(open, Decision{Key: key, Verb: verb, Note: Note(line), Line: n})
	}
	return open
}

// Fold folds a whole status history into the decisions still open, most-recently-opened last
// (fm-classify-lib.sh:742 status_open_decisions).
func Fold(lines []string, kind Kind, v Verbs) []Decision {
	var open []Decision
	for i, l := range lines {
		open = foldLine(open, l, i+1, kind, v)
	}
	return open
}

// OpenActivities folds a status history into the keyed activity phases still open, most-recently-opened last
// (fm-classify-lib.sh:1869 status_open_activities): a working or paused line opens (or replaces) its key's phase, and a
// done, failed, needs-decision, blocked, resolve or captain-held line under the same key closes it. A line with a
// malformed key is ignored; an unkeyed line is the default key.
func OpenActivities(lines []string, v Verbs) []Decision {
	var open []Decision
	for i, line := range lines {
		if trimSpace(line) == "" {
			continue
		}
		key, ok := Key(line)
		if !ok {
			continue
		}
		switch verb := Verb(line); verb {
		case "working", PausedVerb:
			open = append(drop(open, key), Decision{Key: key, Verb: verb, Note: Note(line), Line: i + 1})
		case "done", "failed", "needs-decision", "blocked", v.resolve(), v.held():
			open = drop(open, key)
		}
	}
	return open
}

// Open reports whether key has a record in an open set.
func Open(open []Decision, key string) (Decision, bool) {
	for _, d := range open {
		if d.Key == key {
			return d, true
		}
	}
	return Decision{}, false
}

// candidateRe is status_key_closing_verb's pre-select: a leading word that could be a fold verb, followed by
// whitespace, a colon or a bracket tag.
func candidateRe(v Verbs) *regexp.Regexp {
	return regexp.MustCompile(`^[[:space:]]*(needs-decision|blocked|done|failed|` + regexp.QuoteMeta(v.resolve()) + `|` +
		regexp.QuoteMeta(v.held()) + `)[[:space:]:[]`)
}

// ClosingVerb returns the verb that last moved key (fm-classify-lib.sh:852 status_key_closing_verb): the opening verb
// while it is still open, the closing verb (resolved, captain-held, or a ship/scout done/failed) once closed, and ""
// when no line ever stated a transition for it.
func ClosingVerb(lines []string, key string, kind Kind, v Verbs) string {
	if key == "" {
		return ""
	}
	re := candidateRe(v)
	var open []Decision
	verb := ""
	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}
		event := Verb(line)
		if !isTerminalFor(event, kind) {
			if event != "needs-decision" && event != "blocked" && event != v.resolve() && event != v.held() {
				continue
			}
			if key != DefaultKey && !strings.Contains(line, "[key="+key+"]") {
				continue
			}
		}
		_, was := Open(open, key)
		open = foldLine(open, line, i+1, kind, v)
		if _, now := Open(open, key); was && !now {
			verb = event
		}
	}
	if d, ok := Open(open, key); ok {
		return d.Verb
	}
	return verb
}

var (
	reMu    sync.Mutex
	reCache = map[string]*regexp.Regexp{}
)

// compile compiles a case-insensitive captain pattern once (firstmate matches under nocasematch).
func compile(pattern string) *regexp.Regexp {
	reMu.Lock()
	defer reMu.Unlock()
	if re, ok := reCache[pattern]; ok {
		return re
	}
	re := regexp.MustCompile(`(?i)` + pattern)
	reCache[pattern] = re
	return re
}

// CaptainRelevant reports whether a line is work the leader must see (fm-classify-lib.sh:216
// status_is_captain_relevant). Verb-aware: working, resolved, captain-held and paused never match from prose; with no
// override the terminal verbs always match; otherwise the pattern (override, else DefaultCaptainRE) is matched on the
// unstamped line. override is FM_CAPTAIN_RE: "" means unset.
func CaptainRelevant(line, override string) bool {
	if line == "" {
		return false
	}
	switch Verb(line) {
	case "working", ResolveVerb, CaptainHeldVerb, PausedVerb:
		return false
	case "done", "needs-decision", "blocked", "failed":
		if override == "" {
			return true
		}
	}
	pattern := override
	if pattern == "" {
		pattern = DefaultCaptainRE
	}
	return compile(pattern).MatchString(Unstamped(line))
}

// IsTerminalVerb reports a real terminal captain verb (fm-classify-lib.sh:197); free-text tokens never count.
func IsTerminalVerb(line string) bool {
	switch Verb(line) {
	case "done", "needs-decision", "blocked", "failed":
		return line != ""
	}
	return false
}

// IsPaused / IsCaptainHeld read the verb only (fm-classify-lib.sh:238,251).
func IsPaused(line string) bool      { return line != "" && Verb(line) == PausedVerb }
func IsCaptainHeld(line string) bool { return line != "" && Verb(line) == CaptainHeldVerb }

// eventVerbs are the verbs the event scan recognises (fm-classify-lib.sh:165 _fm_status_event_scan).
var eventVerbs = map[string]bool{"working": true, "needs-decision": true, "blocked": true, "done": true, "failed": true,
	"note": true, PausedVerb: true, ResolveVerb: true, CaptainHeldVerb: true}

// IsEvent reports whether a line is a recognised status event: a known verb before a colon, or a bare legacy line a
// captain token LEADS, so continuation prose that merely mentions one cannot hide a declaration.
func IsEvent(line, override string) bool {
	if strings.Contains(line, ":") && eventVerbs[Verb(line)] {
		return true
	}
	pattern := override
	if pattern == "" {
		pattern = DefaultCaptainRE
	}
	return compile(`^[[:space:]]*(` + pattern + `)`).MatchString(Unstamped(line))
}

// Latest returns the latest recognised event of a history (fm-classify-lib.sh:146 last_status_line); a history with
// no event keeps its last nonblank line.
func Latest(lines []string, override string) string {
	last, fallback := "", ""
	for _, l := range lines {
		if trimSpace(l) == "" {
			continue
		}
		fallback = l
		if IsEvent(l, override) {
			last = l
		}
	}
	if last != "" {
		return last
	}
	return fallback
}

// Actionable returns every actionable event of a span, in order (fm-classify-lib.sh:2126
// status_span_first_actionable_record), and whether the span carries a captain decision (needsDecision). A keyed
// needs-decision/blocked counts only when it is the live origin of that key's open record in the span's own fold, so
// a decision the span already closed is not actionable; a malformed key is actionable as-is; a reserved key spoken by
// a foreign writer is actionable as "reconciliation-required: <line>". A captain-held line is not actionable but marks
// needsDecision.
func Actionable(lines []string, kind Kind, override string) (events []string, needsDecision bool) {
	var open []Decision
	folded := false
	for i, line := range lines {
		if trimSpace(line) == "" {
			continue
		}
		if IsCaptainHeld(line) {
			needsDecision = true
			continue
		}
		if !CaptainRelevant(line, override) {
			continue
		}
		verb := Verb(line)
		if verb != "needs-decision" && verb != "blocked" {
			events = append(events, line)
			continue
		}
		key, ok := Key(line)
		if !ok {
			events = append(events, line)
			needsDecision = needsDecision || verb == "needs-decision"
			continue
		}
		if !transitionAllowed(key, Note(line)) {
			events = append(events, "reconciliation-required: "+line)
			needsDecision = needsDecision || verb == "needs-decision"
			continue
		}
		if !folded {
			open, folded = Fold(lines, kind, Verbs{}), true
		}
		if d, ok := Open(open, key); !ok || d.Line != i+1 {
			continue
		}
		events = append(events, line)
		if verb == "needs-decision" || isPendingReplyEscalation(key, Note(line)) {
			needsDecision = true
		}
	}
	return events, needsDecision
}

// isPendingReplyEscalation is _fm_is_pending_reply_escalation (fm-classify-lib.sh:661).
func isPendingReplyEscalation(key, note string) bool {
	if !strings.HasPrefix(key, "pending-reply-") {
		return false
	}
	for _, p := range []string{"pending-reply-missed:", "pending-reply-delivery-unknown:",
		"pending-reply-recovery-delivery-failed:", "pending-reply-recovery-delivery-unknown:"} {
		if strings.HasPrefix(note, p) {
			return true
		}
	}
	return false
}
