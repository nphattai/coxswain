// Package lavish is the optional review-surface adapter (M13, ADR 0008): it wraps the external lavish-axi CLI so a
// generated artifact (internal/artifact) can be opened in a browser and the reviewer's feedback recorded. cox owns the
// artifact and the record; lavish is never forked and never authoritative (DESIGN decision after arena round 1).
//
// Open runs `lavish-axi <file>`; Poll runs a single bounded `lavish-axi poll <file>`, parses its TOON output, and turns
// every delivered feedback item into an inbox.v1 record for story _leader plus a wake (review_feedback routine, or
// review_decision urgent for a decision/answer tag). Lavish acks on delivery and clears its queue (session-store.js
// `session.prompts = []`), so there is no second ack: the record is written synchronously in this process, and if the
// write fails the raw feedback is surfaced so the loss is reported, never silent (arena round-1 adversary claim).
package lavish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/arena/synth"
	"github.com/nphattai/coxswain/internal/artifact"
	"github.com/nphattai/coxswain/internal/boundexec"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/wake"
)

// Binary is the lavish-axi executable name looked up on PATH when policy sets no override.
const Binary = "lavish-axi"

// LeaderStory is the inbox owner for review feedback (advisory commentary to the leader, ADR 0012).
const LeaderStory = "_leader"

// Poll exit codes (each terminal case gets its own, so a caller can branch): feedback delivered, timed out, session
// ended, or the browser disconnected (resumable).
const (
	ExitFeedback     = 0
	ExitTimeout      = 3
	ExitEnded        = 4
	ExitDisconnected = 5
)

// pollBuffer is added to the caller's --max so the process context outlives lavish's own --timeout-ms (cold start plus
// server spin-up), letting lavish return its own timeout status instead of being killed.
var pollBuffer = 30 * time.Second

// NPXOptIn is the explicit policy opt-in to run lavish-axi via npx when no binary is installed (policy review.npx): an
// exact version and integrity value, same rule as the quota adapter. null means npx is never used.
type NPXOptIn struct {
	Version   string
	Integrity string
}

// Config is the policy-derived adapter config (policy review section). Binary overrides the PATH lookup; NPX is the
// opt-in fallback.
type Config struct {
	Binary string
	NPX    *NPXOptIn
}

// Available reports whether lavish-axi can be run under this config (a binary override, one on PATH, or an npx opt-in).
func (c Config) Available() bool {
	_, _, err := c.command()
	return err == nil
}

// command resolves the executable and leading args: the policy binary override, else lavish-axi on PATH, else the
// explicit npx opt-in, else an error. It never uses a shell.
func (c Config) command(extra ...string) (string, []string, error) {
	if c.Binary != "" {
		return c.Binary, extra, nil
	}
	if bin, err := exec.LookPath(Binary); err == nil {
		return bin, extra, nil
	}
	if c.NPX != nil && c.NPX.Version != "" {
		npx, err := exec.LookPath("npx")
		if err != nil {
			return "", nil, fmt.Errorf("lavish-axi not found and npx missing for the policy opt-in")
		}
		// ponytail: npm has no per-run SRI check, so review.npx.integrity is recorded but not enforced here; the
		// installed binary is the trustworthy default, same ceiling as quota.npx.
		return npx, append([]string{"-y", Binary + "@" + c.NPX.Version}, extra...), nil
	}
	return "", nil, fmt.Errorf("lavish-axi not on PATH (install it or set policy review.binary; npx is opt-in via review.npx)")
}

// Open runs `lavish-axi <file>` so the reviewer can open the artifact in a browser. When lavish is not available it
// prints the file path and how to open it and returns nil (exit 0): the review degrades to path plus chat, never fails
// (DESIGN: without lavish everything still works by path).
func Open(cfg Config, file string, stdout, stderr io.Writer) error {
	bin, args, err := cfg.command(file)
	if err != nil {
		fmt.Fprintf(stdout, "lavish-axi not available: %v\nopen the artifact directly in a browser:\n  %s\nfeedback then goes through the leader chat.\n", err, file)
		return nil
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(stdout, &out)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if local := localSessionURL(out.String()); local != "" {
		fmt.Fprintf(stdout, "local: %s\n", local)
	}
	fmt.Fprintln(stdout, "feedback is delivered only after Send to Agent in the page")
	return nil
}

// localSessionURL converts lavish's advertised session URL to a loopback URL. Lavish may advertise a MagicDNS host,
// but the same server always listens on loopback; preserving the parsed port and session id gives the captain a local
// fallback without guessing either value.
func localSessionURL(raw string) string {
	doc := decodeTOON(raw)
	session, _ := doc["session"].(map[string]any)
	advertised := str(session["url"])
	u, err := url.Parse(advertised)
	if err != nil || u.Port() == "" || !strings.HasPrefix(u.Path, "/session/") {
		return ""
	}
	return "http://127.0.0.1:" + u.Port() + u.Path
}

// Share runs `lavish-axi share <file>` to publish the artifact on the third-party ht-ml.app host. It is outward-facing
// and the caller must gate it on policy review.share plus an explicit --share (cox never shares on its own); this
// function only runs the command once the caller has decided to.
func Share(cfg Config, file string, stdout, stderr io.Writer) error {
	bin, args, err := cfg.command("share", file)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// PollResult is the outcome of one bounded poll: the terminal status, the inbox records written, and the exit code.
type PollResult struct {
	Status   string
	Records  []string
	ExitCode int
}

// Poll runs one bounded `lavish-axi poll <file> --timeout-ms <max>`, parses its TOON output, and records every delivered
// feedback item as an inbox.v1 record for _leader plus a wake. It returns the terminal status and the exit code. A
// record-write failure is a hard error carrying the raw feedback, because lavish has already cleared its queue and the
// feedback cannot be re-polled (the known loss window). afterDeliver, when non-nil, runs right after lavish returns and
// before any record is written; it is a test seam for the fault-injection gate.
func Poll(cfg Config, epicDir, file string, max time.Duration, afterDeliver func(), stdout, stderr io.Writer) (PollResult, error) {
	return runPoll(cfg, epicDir, file, max, "", afterDeliver, stdout, stderr)
}

// Reply mirrors an agent response into the Lavish conversation, then waits for the next delivery with the same bounded
// poll, record, wake, and exit-code behavior as Poll.
func Reply(cfg Config, epicDir, file, reply string, max time.Duration, stdout, stderr io.Writer) (PollResult, error) {
	return runPoll(cfg, epicDir, file, max, reply, nil, stdout, stderr)
}

func runPoll(cfg Config, epicDir, file string, max time.Duration, agentReply string, afterDeliver func(), stdout, stderr io.Writer) (PollResult, error) {
	bin, base, err := cfg.command()
	if err != nil {
		fmt.Fprintf(stdout, "lavish-axi not available: %v\nnothing to poll; the review runs by path and chat.\n", err)
		return PollResult{Status: "unavailable", ExitCode: ExitFeedback}, nil
	}
	ms := int(max / time.Millisecond)
	args := append(base, "poll", file, "--timeout-ms", fmt.Sprint(ms))
	if agentReply != "" {
		args = append(args, "--agent-reply", agentReply)
	}

	cmd := exec.Command(bin, args...)
	cmd.Env = cleanEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = stderr
	bound := max + pollBuffer
	code, runErr := boundexec.Run(context.Background(), bound, cmd)
	switch {
	case runErr != nil:
	case code == boundexec.ExitTimeout:
		runErr = fmt.Errorf("timed out after %s", bound)
	case code != 0:
		runErr = fmt.Errorf("exit status %d", code)
	}

	if afterDeliver != nil {
		afterDeliver()
	}

	raw := out.String()
	doc := decodeTOON(raw)
	// A lavish error (no session, bad file) has no session block; surface it.
	if _, ok := doc["session"]; !ok {
		if msg, ok := doc["error"].(string); ok {
			return PollResult{Status: "error", ExitCode: 1}, fmt.Errorf("lavish-axi poll: %s", msg)
		}
		if runErr != nil {
			return PollResult{Status: "error", ExitCode: 1}, fmt.Errorf("lavish-axi poll failed: %v\n%s", runErr, raw)
		}
		return PollResult{Status: "error", ExitCode: 1}, fmt.Errorf("lavish-axi poll returned no session:\n%s", raw)
	}
	session, _ := doc["session"].(map[string]any)
	status, _ := session["status"].(string)

	res, err := record(epicDir, file, status, session, doc, raw, stdout)
	return res, err
}

// record turns the parsed poll document into inbox records and wakes and returns the result. Every feedback item is
// written before returning; a write failure surfaces the raw feedback (it is already lost from lavish).
func record(epicDir, file, status string, session, doc map[string]any, raw string, stdout io.Writer) (PollResult, error) {
	slug := filepath.Base(epicDir)
	res := PollResult{Status: status}

	switch status {
	case "feedback":
		prompts, _ := doc["prompts"].([]any)
		for i, item := range prompts {
			p, _ := item.(map[string]any)
			// Write the record first (synchronously); lavish has already cleared its queue, so a write failure is a hard
			// loss and must be reported with the raw feedback, never swallowed.
			path, err := inbox.Write(epicDir, LeaderStory, feedbackBody(file, p), inbox.FYI, "")
			if err != nil {
				fmt.Fprintf(stdout, "LOST: feedback item %d was delivered by lavish (which clears its queue on delivery) but could not be recorded; it cannot be re-polled.\nRaw poll output follows so it is not lost:\n%s\n", i+1, raw)
				return res, fmt.Errorf("record review feedback: %w", err)
			}
			res.Records = append(res.Records, path)
			note, kind := handlePrompt(epicDir, file, p, stdout)
			if _, werr := wake.Append(epicDir, wake.Wake{Epic: slug, Story: LeaderStory, Kind: kind, Note: note}); werr != nil {
				return res, fmt.Errorf("wake for review feedback: %w", werr)
			}
		}
		// A final delivery may carry both feedback and a session-end signal (Send & End, or a browser disconnect after
		// Lavish's grace period). Record both, and return ended so callers do not re-arm the poll.
		if b, _ := session["session_ended"].(bool); b {
			endedBy, _ := session["ended_by"].(string)
			note := "session ended by " + orUnknown(endedBy)
			if endedBy == "user" {
				note += " (Send & End)"
			}
			if err := terminalRecord(epicDir, slug, file, note); err != nil {
				return res, err
			}
			res.ExitCode = ExitEnded
			return res, nil
		}
		res.ExitCode = ExitFeedback
		return res, nil

	case "ended":
		endedBy, _ := session["ended_by"].(string)
		if err := terminalRecord(epicDir, slug, file, "review session ended by "+orUnknown(endedBy)); err != nil {
			return res, err
		}
		res.ExitCode = ExitEnded
		return res, nil

	case "browser_disconnected":
		if err := terminalRecord(epicDir, slug, file, "review browser disconnected (session resumable; ask the captain to reopen or end)"); err != nil {
			return res, err
		}
		res.ExitCode = ExitDisconnected
		return res, nil

	default: // waiting / timeout
		if err := terminalRecord(epicDir, slug, file, "review poll timed out with no feedback (re-run cox review poll to keep waiting)"); err != nil {
			return res, err
		}
		res.Status = "timeout"
		res.ExitCode = ExitTimeout
		return res, nil
	}
}

// handlePrompt decides the wake note and kind for a delivered prompt after its record is written. An answer prompt
// (tag answer, or a message `answer <id> <value>`) is executed through the single authoritative write path,
// cox arena answer (synth.Answer), guarded by the artifact sidecar's synthesis sha; a decision tag is urgent; anything
// else is routine review_feedback. The answer outcome (recorded, or refused with a reason) is the wake note, so one
// prompt yields exactly one wake.
func handlePrompt(epicDir, file string, p map[string]any, stdout io.Writer) (string, wake.Kind) {
	if id, value, ok := parseAnswer(p); ok {
		outcome := executeAnswer(epicDir, file, id, value)
		fmt.Fprintf(stdout, "review answer %s: %s\n", id, outcome)
		return "review answer " + id + ": " + outcome, wake.KindReviewDecision
	}
	if isDecisionTag(str(p["tag"])) {
		return feedbackNote(p), wake.KindReviewDecision
	}
	return feedbackNote(p), wake.KindReviewFeedback
}

// parseAnswer extracts a claim id and value from an answer prompt: a message shaped `answer <id> <value>` (the input
// playbook fallback when the SDK cannot transfer a form), or an explicit claim_id plus value field. It returns ok=false
// for any other prompt.
func parseAnswer(p map[string]any) (id, value string, ok bool) {
	if fields := strings.Fields(message(p)); len(fields) >= 3 && strings.EqualFold(fields[0], "answer") {
		return fields[1], strings.Join(fields[2:], " "), true
	}
	if cid := str(p["claim_id"]); cid != "" {
		if v := str(p["value"]); v != "" {
			return cid, v, true
		}
	}
	return "", "", false
}

// executeAnswer writes the captain's answer through cox arena answer (synth.Answer), the single write path for a synthesis
// cell. It first refuses when the artifact is stale: the arena sidecar's synthesis_sha must match the current
// synthesis.md content sha, so a captain never answers against a shifted table (arena round-1 reviewer, sha guard). It
// returns a human outcome string for the wake note; it never panics the poll.
func executeAnswer(epicDir, file, id, value string) string {
	if reason := staleReason(epicDir, file); reason != "" {
		return "refused (" + reason + "); re-generate the artifact with cox arena synth --html and answer again"
	}
	round, err := synth.Answer(epicDir, id, value, "captain")
	if err != nil {
		return "refused: " + err.Error()
	}
	return fmt.Sprintf("recorded in synthesis round %d", round)
}

// staleReason returns why the artifact is stale for answering, or "" when it is current: the arena sidecar's
// synthesis_sha must equal the current synthesis.md content sha. A missing sidecar or non-arena artifact returns ""
// (synth.Answer still applies its own recorded-sha guard).
func staleReason(epicDir, file string) string {
	b, err := os.ReadFile(artifact.SidecarPath(file))
	if err != nil {
		return ""
	}
	var s artifact.Sidecar
	if err := json.Unmarshal(b, &s); err != nil || s.Kind != artifact.KindArena || s.SynthesisSHA == "" {
		return ""
	}
	cur, err := os.ReadFile(filepath.Join(epicDir, "reports", "arena", "synthesis.md"))
	if err != nil {
		return "synthesis.md unreadable"
	}
	if got := synth.ContentSha(cur); got != s.SynthesisSHA {
		return "artifact synthesis sha " + s.SynthesisSHA + " != current " + got
	}
	return ""
}

// feedbackBody renders the record body: a key=value header the leader (or a later surface) can read back.
func feedbackBody(file string, p map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema=coxswain.review-feedback\n")
	fmt.Fprintf(&b, "artifact=%s\n", filepath.Base(file))
	fmt.Fprintf(&b, "tag=%s\n", orUnknown(str(p["tag"])))
	fmt.Fprintf(&b, "anchor=%s\n", anchor(p))
	if q := str(p["text"]); q != "" {
		fmt.Fprintf(&b, "quoted=%s\n", oneLine(q))
	}
	if m := message(p); m != "" {
		fmt.Fprintf(&b, "message=%s\n", oneLine(m))
	}
	if ids := claimID(p); ids != "" {
		fmt.Fprintf(&b, "claim_id=%s\n", ids)
	}
	return strings.TrimRight(b.String(), "\n")
}

// terminalRecord writes an fyi record noting a poll end (timeout, ended, disconnected, Send & End) and appends a routine
// review_feedback wake so the leader sees the poll closed.
func terminalRecord(epicDir, slug, file, note string) error {
	body := fmt.Sprintf("schema=coxswain.review-feedback\nartifact=%s\ntag=session\nmessage=%s", filepath.Base(file), note)
	if _, err := inbox.Write(epicDir, LeaderStory, body, inbox.FYI, ""); err != nil {
		return fmt.Errorf("record review end: %w", err)
	}
	if _, err := wake.Append(epicDir, wake.Wake{Epic: slug, Story: LeaderStory, Kind: wake.KindReviewFeedback, Note: note}); err != nil {
		return fmt.Errorf("wake for review end: %w", err)
	}
	return nil
}

// feedbackNote is the short wake note for one feedback item.
func feedbackNote(p map[string]any) string {
	note := "review " + orUnknown(str(p["tag"]))
	if m := message(p); m != "" {
		note += ": " + oneLine(m)
	}
	return note
}

// isDecisionTag reports whether a tag routes to the urgent review_decision wake: a captain decision or an answer to a
// synthesis claim (the authoritative answer path, executed in the answer step).
func isDecisionTag(tag string) bool {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "decision", "answer":
		return true
	}
	return false
}

// anchor renders the opaque element anchor: the CSS selector, plus the structured target's type when one is present.
func anchor(p map[string]any) string {
	sel := str(p["selector"])
	if t, ok := p["target"].(map[string]any); ok {
		if ty := str(t["type"]); ty != "" {
			if sel != "" {
				return sel + " (" + ty + ")"
			}
			return ty
		}
	}
	if sel == "" {
		if t := str(p["target"]); t != "" {
			return t
		}
		return "(none)"
	}
	return sel
}

// message returns the reviewer's typed message (lavish field `prompt`, falling back to `message`).
func message(p map[string]any) string {
	if m := str(p["prompt"]); m != "" {
		return m
	}
	return str(p["message"])
}

// claimID returns an explicit claim_id field when the prompt carries one (the input playbook may attach it).
func claimID(p map[string]any) string { return str(p["claim_id"]) }

func str(v any) string {
	s, _ := v.(string)
	return s
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// cleanEnv is the minimal environment lavish-axi runs under: PATH (Node shebang) and HOME (its state dir). No other
// inherited variable leaks into the subprocess (same rule as the quota adapter).
func cleanEnv() []string {
	var env []string
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	return env
}
