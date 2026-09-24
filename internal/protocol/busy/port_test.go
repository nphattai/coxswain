//go:build port

// Port tests (wave 1, cox-supervision-port-busy-wake): firstmate's busy-state suites fm-busy-state,
// fm-busy-adapter-wiring and fm-tmux-submit-busy translated case by case against cox's harness-owned busy record
// (busy.Arm/Apply/Read/Retire and the capability-card trust table), the worker hooks dispatch writes, and the Pi
// extension. Firstmate pinned at 1e0e773 (references/firstmate, read only). Every case is t.Run("FM/<suite>/<case>")
// with a `// fm: tests/<file>:<line>` citation. A failure names its cox mechanism as red[<mechanism>] (behaviour differs)
// or notImplemented[<mechanism>] (cox has no such mechanism); red is the deliverable (DESIGN translation contract).
//
// Name map (firstmate -> cox): fm-busy-event.sh arm/apply/retire -> busy.Arm/Apply/Retire; fm_busy_classify
// "<state> <source>" -> busy.Read + ReadRecord().Source; the <id>.busy-gen sidecar -> the record's Gen field plus
// COX_BUSY_GEN in the launch env; source fm-spawn -> dispatch, fm-interrupt -> interrupt, fm-recovery -> recovery;
// fm_busy_sources_for_harness -> registry.Card(h).BusySources; `--current-gen` -> ReadRecord().Gen (the leader-side
// idiom control.retireBusy uses); fm_busy_is_busy -> backend.BusyComposer; the tmux composer verdict -> BusyComposer;
// fm_busy_classify -> busy.Classify(...).String(); fm_busy_classify_live -> busy.ClassifyLive; fm-busy-event.sh progress
// -> busy.Progress; <id>.progress -> busy.ProgressPath.
package busy_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/state"
)

// notImplemented fails a case whose firstmate mechanism cox does not have, naming the gap (contract rule 3).
func notImplemented(t *testing.T, mechanism, detail string) {
	t.Helper()
	t.Fatalf("notImplemented[%s]: %s", mechanism, detail)
}

// red records a behaviour that differs from the firstmate case, tagged with the cox mechanism for the red list.
func red(t *testing.T, mechanism, format string, args ...any) {
	t.Helper()
	t.Errorf("red[%s]: %s", mechanism, fmt.Sprintf(format, args...))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// arm arms story on epic with the trust table the harness's capability card carries (what armWorkerBusy does).
func arm(t *testing.T, epic, story, harnessName string) string {
	t.Helper()
	gen, err := busy.Arm(epic, story, harnessName, registry.Card(harnessName).BusySources)
	must(t, err)
	return gen
}

// view is fm_busy_classify's "<state> <source>" in cox terms: busy.Read plus the record's source when it classifies.
// Unknown renders bare here; wantUnknown checks the reason through busy.Classify.
func view(epic, story string) string {
	st := busy.Read(epic, story)
	if st == busy.Unknown {
		return busy.Unknown
	}
	rec, _ := busy.ReadRecord(epic, story)
	return st + " " + rec.Source
}

// wantView checks the classification against the translated firstmate verdict.
func wantView(t *testing.T, mechanism, epic, story, want string) {
	t.Helper()
	if got := view(epic, story); got != want {
		red(t, mechanism, "classify = %q, want %q", got, want)
	}
}

// wantUnknown checks the classification for the story's harness is unknown with firstmate's reason (missing,
// malformed, gen-mismatch, source-mismatch, <harness>-unverified, launch-prompt, or an applied unknown's source).
func wantUnknown(t *testing.T, epic, story, harnessName, reason string) {
	t.Helper()
	if got := busy.Read(epic, story); got != busy.Unknown {
		red(t, "busy.read", "Read = %q, want unknown %s", got, reason)
	}
	if got, want := busy.Classify(epic, story, harnessName, "").String(), busy.Unknown+" "+reason; got != want {
		red(t, "busy.unknown-reason", "classify = %q, want %q", got, want)
	}
}

// classify is fm_busy_classify for harness h with the captured tail.
func classify(epic, story, h, tail string) string {
	return busy.Classify(epic, story, h, tail).String()
}

func wantClassify(t *testing.T, mechanism, got, want string) {
	t.Helper()
	if got != want {
		red(t, mechanism, "classify = %q, want %q", got, want)
	}
}

// rawRecord writes the record file directly (a torn or foreign writer that bypassed Apply).
func rawRecord(t *testing.T, epic, story, body string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(busy.Path(epic, story)), 0o755))
	must(t, os.WriteFile(busy.Path(epic, story), []byte(body), 0o600))
}

func recJSON(t *testing.T, r busy.Record) string {
	t.Helper()
	b, err := json.Marshal(r)
	must(t, err)
	return string(b) + "\n"
}

func TestPortBusyState(t *testing.T) {
	const s = "t1"

	// fm: tests/fm-busy-state.test.sh:31
	t.Run("FM/fm-busy-state/arm_seeds_busy_spawn", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		rec, ok := busy.ReadRecord(epic, s)
		if !ok || rec.Gen != gen || rec.Seq != 1 {
			red(t, "busy.arm", "armed record = %+v ok=%v, want gen=%s seq=1", rec, ok, gen)
		}
		wantView(t, "busy.arm", epic, s, "busy dispatch")
	})

	// fm: tests/fm-busy-state.test.sh:42
	t.Run("FM/fm-busy-state/apply_advances_seq_and_source", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		must(t, busy.Apply(epic, s, busy.Idle, gen, "claude-hook", "stop"))
		wantView(t, "busy.apply", epic, s, "idle claude-hook")
		must(t, busy.Apply(epic, s, busy.Busy, gen, "claude-hook", "user-prompt-submit"))
		wantView(t, "busy.apply", epic, s, "busy claude-hook")
		if rec, _ := busy.ReadRecord(epic, s); rec.Seq != 3 {
			red(t, "busy.apply", "seq = %d after seed + two applies, want 3", rec.Seq)
		}
	})

	// fm: tests/fm-busy-state.test.sh:59
	t.Run("FM/fm-busy-state/apply_current_gen_reset", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "pi")
		cur, _ := busy.ReadRecord(epic, s) // --current-gen
		must(t, busy.Apply(epic, s, busy.Idle, cur.Gen, "interrupt", "interrupt"))
		wantView(t, "busy.apply", epic, s, "idle interrupt")
		cur, _ = busy.ReadRecord(epic, s)
		must(t, busy.Apply(epic, s, busy.Unknown, cur.Gen, "recovery", "relaunch"))
		wantUnknown(t, epic, s, "pi", "recovery")
	})

	// fm: tests/fm-busy-state.test.sh:74
	t.Run("FM/fm-busy-state/apply_unarmed_refused", func(t *testing.T) {
		epic := t.TempDir()
		if err := busy.Apply(epic, s, busy.Busy, "g1.2.3", "claude-hook", "x"); err == nil {
			red(t, "busy.apply", "apply against an unarmed story must be refused")
		}
		if _, err := os.Stat(busy.Path(epic, s)); err == nil {
			red(t, "busy.apply", "refused apply wrote a record")
		}
	})

	// fm: tests/fm-busy-state.test.sh:84
	t.Run("FM/fm-busy-state/retire_serializes_and_rejects_stale_gen", func(t *testing.T) {
		epic := t.TempDir()
		oldGen := arm(t, epic, s, "claude")
		lock := busy.Path(epic, s) + ".lock"
		must(t, os.Mkdir(lock, 0o700))
		done := make(chan error, 1)
		go func() { done <- busy.Retire(epic, s, oldGen) }()
		time.Sleep(200 * time.Millisecond)
		if _, err := os.Stat(busy.Path(epic, s)); err != nil {
			red(t, "busy.retire", "retire bypassed the writer lock")
		}
		must(t, os.Remove(lock))
		if err := <-done; err != nil {
			red(t, "busy.retire", "retire failed after acquiring the lock: %v", err)
		}
		if _, err := os.Stat(busy.Path(epic, s)); err == nil {
			red(t, "busy.retire", "retire left the record behind")
		}
		newGen := arm(t, epic, s, "claude")
		if err := busy.Retire(epic, s, oldGen); err == nil {
			red(t, "busy.retire", "retire accepted a superseded incarnation")
		}
		wantView(t, "busy.retire", epic, s, "busy dispatch")
		if rec, _ := busy.ReadRecord(epic, s); rec.Gen != newGen {
			red(t, "busy.retire", "stale retirement changed the new gen")
		}
	})

	// fm: tests/fm-busy-state.test.sh:122 (GNU stat stubs -> an old lock mtime; Go stats the lock natively)
	t.Run("FM/fm-busy-state/stale_lock_broken_under_gnu_stat", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		lock := busy.Path(epic, s) + ".lock"
		must(t, os.Mkdir(lock, 0o700))
		old := time.Unix(1000000000, 0)
		must(t, os.Chtimes(lock, old, old))
		if err := busy.Retire(epic, s, gen); err != nil {
			red(t, "busy.lock", "retire did not break a provably stale lock: %v", err)
		}
		if _, err := os.Stat(busy.Path(epic, s)); err == nil {
			red(t, "busy.lock", "retire left the record behind")
		}
		if _, err := os.Stat(lock); err == nil {
			red(t, "busy.lock", "retire left the stale lock behind")
		}
		if err := busy.Retire(epic, s, gen); err != nil {
			red(t, "busy.retire", "a repeated retire over cleaned state was not idempotent: %v", err)
		}
	})

	// fm: tests/fm-busy-state.test.sh:168
	t.Run("FM/fm-busy-state/retire_missing_sidecar_is_idempotent", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		must(t, os.Remove(busy.GenPath(epic, s)))
		if err := busy.Retire(epic, s, gen); err != nil {
			red(t, "busy.retire", "exact-gen retire rejected a missing sidecar: %v", err)
		}
		if _, err := os.Stat(busy.Path(epic, s)); err == nil {
			red(t, "busy.retire", "retire left an orphan record behind")
		}
		if err := busy.Retire(epic, s, gen); err != nil {
			red(t, "busy.retire", "repeated exact-gen retire was not idempotent: %v", err)
		}
		must(t, os.WriteFile(busy.GenPath(epic, s), []byte("malformed gen\n"), 0o600))
		rawRecord(t, epic, s, "orphan\n")
		if err := busy.Retire(epic, s, gen); err == nil {
			red(t, "busy.retire", "retire accepted a malformed existing sidecar")
		}
		if _, err := os.Stat(busy.Path(epic, s)); err != nil {
			red(t, "busy.retire", "retire removed the record for a malformed existing sidecar")
		}
	})

	// fm: tests/fm-busy-state.test.sh:190
	t.Run("FM/fm-busy-state/stale_gen_event_rejected", func(t *testing.T) {
		epic := t.TempDir()
		oldGen := arm(t, epic, s, "claude")
		newGen := arm(t, epic, s, "claude")
		if oldGen == newGen {
			red(t, "busy.arm", "re-arm must mint a fresh gen")
		}
		if err := busy.Apply(epic, s, busy.Idle, oldGen, "claude-hook", "stop"); err == nil {
			red(t, "busy.apply", "an event carrying a stale gen must be rejected")
		}
		wantView(t, "busy.apply", epic, s, "busy dispatch")
	})

	// fm: tests/fm-busy-state.test.sh:204 (the armed gen lives in a sidecar apart from the record; cox keeps it only
	// inside the record, so a record carrying a superseded gen cannot be told from the live one)
	t.Run("FM/fm-busy-state/stale_gen_record_unknown", func(t *testing.T) {
		epic := t.TempDir()
		oldGen := arm(t, epic, s, "claude")
		arm(t, epic, s, "claude") // the live incarnation
		rec, _ := busy.ReadRecord(epic, s)
		rec.Gen, rec.State, rec.Source, rec.Seq = oldGen, busy.Idle, "claude-hook", 2
		rawRecord(t, epic, s, recJSON(t, rec)) // a record left behind by the superseded incarnation
		if got := busy.Read(epic, s); got != busy.Unknown {
			red(t, "busy.armed-gen-binding", "a record from a superseded incarnation classifies %q, want unknown gen-mismatch", got)
		}
		wantClassify(t, "busy.armed-gen-binding", classify(epic, s, "claude", ""), "unknown gen-mismatch")
	})

	// fm: tests/fm-busy-state.test.sh:218
	t.Run("FM/fm-busy-state/missing_record_unknown_not_idle", func(t *testing.T) {
		epic := t.TempDir()
		for _, h := range []string{"claude", "pi"} {
			if got := busy.Read(epic, s+h); got != busy.Unknown {
				red(t, "busy.read", "%s with no record = %q, want unknown missing", h, got)
			}
		}
		for _, h := range []string{"claude", "pi"} {
			wantUnknown(t, epic, s, h, "missing")
		}
		wantUnknown(t, epic, s, "codex", "codex-unverified")
		if registry.Card("codex").BusyRecord {
			red(t, "busy.codex-gate", "codex card reports busy by default; firstmate keeps it unknown codex-unverified")
		}
	})

	// fm: tests/fm-busy-state.test.sh:230
	t.Run("FM/fm-busy-state/malformed_record_unknown", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "claude")
		good, _ := busy.ReadRecord(epic, s)
		good.State, good.Source, good.Event = busy.Busy, "claude-hook", "x"
		base := strings.TrimSuffix(recJSON(t, good), "\n")
		bad := map[string]string{
			"garbage":        "garbage\n",
			"schema v0":      strings.Replace(base, busy.Schema, "busy.v0", 1) + "\n",
			"seq NaN":        strings.Replace(base, `"seq":1`, `"seq":"NaN"`, 1) + "\n",
			"state frobbing": strings.Replace(base, `"state":"busy"`, `"state":"frobbing"`, 1) + "\n",
			"source spaced":  strings.Replace(base, `"source":"claude-hook"`, `"source":"bad source"`, 1) + "\n",
			"rogue field":    strings.TrimSuffix(base, "}") + `,"rogue":1}` + "\n",
			"multi-line":     base + "\nsecond line\n",
		}
		for name, body := range bad {
			rawRecord(t, epic, s, body)
			if got := classify(epic, s, "claude", ""); got != "unknown malformed" {
				red(t, "busy.strict-parse", "malformed record (%s) classifies %q, want unknown malformed", name, got)
			}
		}
	})

	// fm: tests/fm-busy-state.test.sh:251
	t.Run("FM/fm-busy-state/record_without_sidecar_unknown", func(t *testing.T) {
		epic := t.TempDir()
		rawRecord(t, epic, s, recJSON(t, busy.Record{Schema: busy.Schema, State: busy.Busy, Gen: "g1.1.1", Seq: 1, TS: 1,
			Source: "claude-hook", Event: "x"})) // never armed: no harness, no trust table
		if got := busy.Read(epic, s); got != busy.Unknown {
			red(t, "busy.read", "a record never armed classifies %q, want unknown", got)
		}
		wantClassify(t, "busy.read", classify(epic, s, "claude", ""), "unknown malformed")
	})

	// fm: tests/fm-busy-state.test.sh:262
	t.Run("FM/fm-busy-state/source_mismatch_cross_adapter", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		_ = busy.Apply(epic, s, busy.Busy, gen, "pi-ext", "agent-start") // cox refuses at write; fm refuses at read
		rec, _ := busy.ReadRecord(epic, s)
		rec.State, rec.Source, rec.Seq = busy.Busy, "pi-ext", rec.Seq+1
		rawRecord(t, epic, s, recJSON(t, rec))
		if got := busy.Read(epic, s); got != busy.Unknown {
			red(t, "busy.trust-table", "pi-ext record on a claude story classifies %q, want unknown source-mismatch", got)
		}
		wantClassify(t, "busy.trust-table", classify(epic, s, "claude", ""), "unknown source-mismatch")
		pgen := arm(t, epic, "p1", "pi")
		must(t, busy.Apply(epic, "p1", busy.Busy, pgen, "pi-ext", "agent-start"))
		wantView(t, "busy.trust-table", epic, "p1", "busy pi-ext")
		ggen := arm(t, epic, "g1", "grok") // no card: trusts nothing
		if err := busy.Apply(epic, "g1", busy.Busy, ggen, "pi-ext", "agent-start"); err == nil {
			red(t, "busy.trust-table", "a harness with no card must trust no source")
		}
		if got := busy.Read(epic, "g1"); got != busy.Unknown {
			red(t, "busy.trust-table", "grok record classifies %q, want unknown source-mismatch", got)
		}
	})

	// fm: tests/fm-busy-state.test.sh:276 (cox's reader takes no rendered text at all; BusyComposer never reads a pane)
	t.Run("FM/fm-busy-state/converted_adapters_ignore_footer_text", func(t *testing.T) {
		epic := t.TempDir()
		footer := "• Working (6s • esc to interrupt)\n   ■■■■⬝⬝⬝⬝  esc interrupt\nWorking...\nCtrl+c:cancel"
		for _, h := range []string{"claude", "pi", "codex"} {
			if cs, ok := backend.BusyComposer(epic, s+h); ok {
				red(t, "backend.busy-composer", "%s with no record resolved to %q; must defer to unknown", h, cs)
			}
			want := "unknown missing"
			if h == "codex" {
				want = "unknown codex-unverified"
			}
			wantClassify(t, "busy.classify", classify(epic, s, h, footer), want)
		}
	})

	// fm: tests/fm-busy-state.test.sh:295
	t.Run("FM/fm-busy-state/launch_prompt_claude_trust_dialog", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "claude")
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "claude", `Accessing workspace: /tmp/wt-a
Quick safety check: Is this a project you created or one you trust?
Claude Code'll be able to read, edit, and execute files here.
> No, exit
  Yes, I trust this folder
Enter to confirm . Esc to cancel`), "unknown launch-prompt")
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "claude", `Allow external CLAUDE.md file imports?
This project's CLAUDE.md imports files outside the current working directory.
> No, disable external imports
  Yes, allow external imports`), "unknown launch-prompt")
	})

	// fm: tests/fm-busy-state.test.sh:316
	t.Run("FM/fm-busy-state/launch_prompt_pi_trust_dialog", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "pi")
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "pi", ` Trust project folder?
 /tmp/fm-pi-trust-check/wt

 This allows pi to load .pi settings and resources, install missing project packages, and execute project extensions.

 > Trust
   Trust parent folder (/tmp/fm-pi-trust-check)
   Trust (this session only)
   Do not trust
   Do not trust (this session only)

 up/down navigate  enter select  escape/ctrl+c cancel`), "unknown launch-prompt")
	})

	// fm: tests/fm-busy-state.test.sh:339
	t.Run("FM/fm-busy-state/launch_prompt_pi_requires_both_markers", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "pi")
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "pi", "I trust this approach and will proceed."), "busy dispatch")
	})

	// fm: tests/fm-busy-state.test.sh:352
	t.Run("FM/fm-busy-state/launch_prompt_gemini_dialogs", func(t *testing.T) {
		// cox has no gemini card; the record is armed with firstmate's gemini trust set so the backstop itself is tested.
		gemini := []string{"gemini-hook", "dispatch", "interrupt", "recovery"}
		for _, tail := range []string{"Do you trust the files in this folder?\n● 1. Trust folder (worktree)\n  2. Trust parent folder (project)\n  3. Don't trust",
			"How would you like to authenticate for this project?\n● 2. Use Gemini API Key",
			"Enter Gemini API Key\n> "} {
			epic := t.TempDir()
			_, err := busy.Arm(epic, s, "gemini", gemini)
			must(t, err)
			wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "gemini", tail), "unknown launch-prompt")
		}
	})

	// fm: tests/fm-busy-state.test.sh:379
	t.Run("FM/fm-busy-state/launch_prompt_never_shortens_a_working_launch", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "claude")
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "claude", "• Working (6s • esc to interrupt)"), "busy dispatch")
	})

	// fm: tests/fm-busy-state.test.sh:392 (opencode has no cox card; translated to a card-less harness)
	t.Run("FM/fm-busy-state/launch_prompt_scoped_to_armed_harnesses", func(t *testing.T) {
		epic := t.TempDir()
		_, err := busy.Arm(epic, s, "opencode", []string{"opencode-plugin", "dispatch", "interrupt", "recovery"})
		must(t, err)
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "opencode",
			"Quick safety check: Is this a project you created or one you trust?"), "busy dispatch")
	})

	// fm: tests/fm-busy-state.test.sh:405
	t.Run("FM/fm-busy-state/launch_prompt_never_reclassifies_an_advanced_record", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		must(t, busy.Apply(epic, s, busy.Busy, gen, "claude-hook", "user-prompt-submit"))
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "claude",
			"Quick safety check: Is this a project you created or one you trust?"), "busy claude-hook")
	})

	// fm: tests/fm-busy-state.test.sh:417
	t.Run("FM/fm-busy-state/launch_prompt_requires_a_captured_tail", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "claude")
		wantClassify(t, "busy.launch-prompt-backstop", classify(epic, s, "claude", ""), "busy dispatch")
	})

	// n/a grok_regex_isolated fm:tests/fm-busy-state.test.sh:427 - grok has no cox harness card and cox never classifies busy from rendered pane text (captain ruling 2026-09-21)

	// fm: tests/fm-busy-state.test.sh:444 (the verification gate lives at arming in cox: card BusyRecord=false unless
	// policy harness.busy_verified, so an unverified codex is never armed and reads unknown)
	t.Run("FM/fm-busy-state/codex_unverified_gate", func(t *testing.T) {
		epic := t.TempDir()
		if registry.Card("codex").BusyRecord {
			red(t, "busy.codex-gate", "codex card arms the busy record without verification")
		}
		wantUnknown(t, epic, s, "codex", "codex-unverified")
		gen := arm(t, epic, s, "claude") // a record another adapter armed still reads unverified for codex
		must(t, busy.Apply(epic, s, busy.Busy, gen, "claude-hook", "user-prompt-submit"))
		wantClassify(t, "busy.codex-gate", classify(epic, s, "codex", ""), "unknown codex-unverified")
	})

	// n/a kimi_unverified_gate fm:tests/fm-busy-state.test.sh:456 - kimi has no cox harness card
	// n/a cursor_ignores_rendered_and_native_signals fm:tests/fm-busy-state.test.sh:468 - cursor has no cox harness card (transcript fold is cursor-only)

	// fm: tests/fm-busy-state.test.sh:495 (B-51: a busy record whose endpoint is gone must read dead, never busy)
	t.Run("FM/fm-busy-state/dead_endpoint_overrides", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "claude")
		if got := busy.Read(epic, s); got != busy.Busy {
			t.Fatalf("setup: armed record reads %q", got)
		}
		gone := func(string) bool { return false }
		there := func(string) bool { return true }
		wantClassify(t, "busy.classify-live", busy.ClassifyLive(epic, s, "claude", "w1", gone).String(), "dead endpoint-gone")
		wantClassify(t, "busy.classify-live", busy.ClassifyLive(epic, s, "claude", "w1", there).String(), "busy dispatch")
		wantClassify(t, "busy.classify-live", busy.ClassifyLive(epic, s, "claude", "", there).String(), "unknown no-target")
	})

	// n/a herdr_native_busy_only fm:tests/fm-busy-state.test.sh:513 - herdr native agent verdict (herdr is firstmate-only surface per DESIGN rule 5; cox's herdr Composer consults only the busy record)

	// fm: tests/fm-busy-state.test.sh:538 (the caller-shell half is bash-only; the translated half is the field check)
	t.Run("FM/fm-busy-state/record_read_leaves_caller_shell_intact", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, s, "claude")
		rec, _ := busy.ReadRecord(epic, s)
		rec.Source = "*"
		rawRecord(t, epic, s, recJSON(t, rec))
		if got := busy.Read(epic, s); got != busy.Unknown {
			red(t, "busy.strict-parse", "a glob-shaped source classifies %q, want unknown malformed", got)
		}
	})

	// fm: tests/fm-busy-state.test.sh:561
	t.Run("FM/fm-busy-state/boolean_view_never_promotes_unknown", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		if cs, ok := backend.BusyComposer(epic, s); !ok || cs != backend.ComposerBusy {
			red(t, "backend.busy-composer", "busy record must read busy, got %q ok=%v", cs, ok)
		}
		must(t, busy.Apply(epic, s, busy.Idle, gen, "claude-hook", "stop"))
		if cs, _ := backend.BusyComposer(epic, s); cs == backend.ComposerBusy {
			red(t, "backend.busy-composer", "idle record must not read busy")
		}
		rawRecord(t, epic, s, "garbage\n")
		if cs, ok := backend.BusyComposer(epic, s); ok && cs == backend.ComposerBusy {
			red(t, "backend.busy-composer", "malformed record must not read busy")
		}
	})

	// fm: tests/fm-busy-state.test.sh:577
	t.Run("FM/fm-busy-state/progress_is_generation_bound_and_not_semantic_state", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, s, "claude")
		before, _ := os.ReadFile(busy.Path(epic, s))
		if err := busy.Progress(epic, s, gen); err != nil {
			red(t, "busy.progress-marker", "current progress was refused: %v", err)
		}
		if _, ok := busy.ProgressAt(epic, s); !ok {
			red(t, "busy.progress-marker", "progress marker missing")
		}
		if after, _ := os.ReadFile(busy.Path(epic, s)); string(after) != string(before) {
			red(t, "busy.progress-marker", "progress changed semantic state")
		}
		replacement := arm(t, epic, s, "claude")
		if _, ok := busy.ProgressAt(epic, s); ok {
			red(t, "busy.progress-marker", "arm retained the previous incarnation's progress")
		}
		if err := busy.Progress(epic, s, gen); err == nil {
			red(t, "busy.progress-marker", "stale progress was accepted")
		}
		if _, ok := busy.ProgressAt(epic, s); ok {
			red(t, "busy.progress-marker", "stale progress wrote a marker")
		}
		if err := busy.Progress(epic, s, replacement); err != nil {
			red(t, "busy.progress-marker", "replacement progress was refused: %v", err)
		}
		must(t, busy.Retire(epic, s, replacement))
		if _, ok := busy.ProgressAt(epic, s); ok {
			red(t, "busy.progress-marker", "retire retained progress")
		}
	})
}

// coxBin builds the cox binary once per test binary into dir, so the wiring cases run the real dispatch path and the
// real hook commands it writes.
var (
	coxOnce sync.Once
	coxPath string
	coxErr  error
)

// TestMain removes the built binary's temp dir once every case has run.
func TestMain(m *testing.M) {
	code := m.Run()
	if coxPath != "" {
		_ = os.RemoveAll(filepath.Dir(coxPath))
	}
	os.Exit(code)
}

func coxBin(t *testing.T) string {
	t.Helper()
	coxOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cox-port-busy-")
		if err != nil {
			coxErr = err
			return
		}
		root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
		coxPath = filepath.Join(dir, "cox")
		cmd := exec.Command("go", "build", "-o", coxPath, "./cmd/cox")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			coxErr = fmt.Errorf("go build cox: %v\n%s", err, out)
		}
	})
	if coxErr != nil {
		t.Fatal(coxErr)
	}
	return coxPath
}

// fakeOrca answers the Orca calls a worker launch makes (terminal create succeeds, the typed launch line fails so no
// harness starts), the same shape cmd/cox/launch_model_test.go uses.
const fakeOrca = `#!/bin/sh
case "$1 $2" in
'terminal create') echo '{"ok":true,"result":{"terminal":{"handle":"h1"}}}' ;;
'terminal close') echo '{"ok":true,"result":{}}' ;;
*) echo '{"ok":false,"error":{"message":"fake orca"}}'; exit 1 ;;
esac
`

// launchCase builds a workspace with one epic, story s1 on harnessName, a git worktree recorded for it, and a fake orca
// on PATH, then runs the real `cox story resume` (the dispatch path that arms the busy record and writes the worker
// hooks). It returns the epic dir, the worktree, and the env the worker would run with.
func launchCase(t *testing.T, harnessName string) (epic, wt string, env []string) {
	t.Helper()
	cox := coxBin(t)
	ws := t.TempDir()
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	tpl, err := os.ReadFile(filepath.Join(root, "templates", "policy.json"))
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(ws, "cox"), 0o755))
	must(t, os.WriteFile(filepath.Join(ws, "cox", "workspace.json"), []byte(`{"schema":"coxswain.workspace.v1"}`), 0o644))
	must(t, os.WriteFile(filepath.Join(ws, "cox", "policy.json"), tpl, 0o644))
	epic = filepath.Join(ws, "epics", "e1")
	must(t, os.MkdirAll(filepath.Join(epic, "stories"), 0o755))
	must(t, os.WriteFile(filepath.Join(epic, "stories", "s1.md"), []byte("---\nid: s1\nharness: "+harnessName+"\n---\nbody\n"), 0o644))
	must(t, state.Append(epic, state.Event{Epic: "e1", Story: "s1", Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true}))
	wt = t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}, {"checkout", "-q", "-b", "story/s1"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	must(t, os.MkdirAll(filepath.Dir(state.WorktreePath(epic, "s1")), 0o755))
	must(t, os.WriteFile(state.WorktreePath(epic, "s1"), []byte(wt), 0o644))
	bin := t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "orca"), []byte(fakeOrca), 0o755))
	env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "ORCA_RUN_ID=run-fake",
		"COX_PLANE=terminal", "ORCA_TERMINAL_HANDLE=", "COX_BIN="+cox)
	cmd := exec.Command(cox, "story", "resume", "s1", "--epic", epic, "--allow-unsandboxed")
	cmd.Env = env
	_, _ = cmd.CombinedOutput() // the fake launch line fails after arming; the arm and hook write are what matter
	return epic, wt, env
}

// runHook runs the command cox wrote for a Claude hook event, with the worker's launch env and the given gen, the way
// Claude Code runs it (sh -c). ok is false when no command is wired for the event.
func runHook(t *testing.T, wt string, env []string, event, gen string) (ok bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wt, ".claude", "settings.local.json"))
	if err != nil {
		return false
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	must(t, json.Unmarshal(b, &doc))
	groups := doc.Hooks[event]
	if len(groups) == 0 || len(groups[0].Hooks) == 0 {
		return false
	}
	cmd := exec.Command("sh", "-c", groups[0].Hooks[0].Command)
	cmd.Env = append(env, "COX_BUSY_GEN="+gen)
	if out, err := cmd.CombinedOutput(); err != nil {
		red(t, "worker-hooks.claude", "%s hook exited non-zero (must never break the harness lifecycle): %v %s", event, err, out)
	}
	return true
}

// driveScript loads the installed Pi extension in a plain Node host and fires one lifecycle mode, like firstmate's
// drive_pi_ext. Busy applies are async children; the COX_BIN wrapper logs each finished one so the driver waits for
// exactly WANT of them (or 5s), and parks any other cox child (the interrupt watcher) harmlessly.
const driveScript = `import { pathToFileURL } from "node:url";
import { existsSync, readFileSync } from "node:fs";
const mod = await import(pathToFileURL(process.env.EXT_PATH).href);
const handlers = {};
mod.default({ on: (n, fn) => { handlers[n] = fn; }, sendUserMessage: () => {} });
const ctx = { isIdle: () => process.env.MODE !== "settle-continuing", cwd: process.cwd(), ui: { notify() {} }, abort() {} };
switch (process.env.MODE) {
  case "agent-start": await handlers["agent_start"]({}, ctx); break;
  case "settle-idle": case "settle-continuing": await handlers["agent_settled"]({}, ctx); break;
  case "settle-then-start": await handlers["agent_settled"]({}, ctx); await handlers["agent_start"]({}, ctx); break;
  default: throw new Error("unknown mode " + process.env.MODE);
}
const want = Number(process.env.WANT);
const deadline = Date.now() + (want > 0 ? 5000 : 300);
const count = () => existsSync(process.env.DONE_LOG) ? readFileSync(process.env.DONE_LOG, "utf8").split("\n").filter(Boolean).length : 0;
while (Date.now() < deadline && count() < want) await new Promise((r) => setTimeout(r, 20));
process.exit(0);
`

const coxWrapper = `#!/bin/sh
if [ "$1" = busy ]; then "$REAL_COX" "$@"; rc=$?; echo "$*" >> "$DONE_LOG"; exit $rc; fi
exec sleep 5
`

type piCase struct {
	t              *testing.T
	epic, ext, dir string
	cox, gen       string
	log            string
}

func newPiCase(t *testing.T) *piCase {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH (environment, not a cox gap)")
	}
	p := &piCase{t: t, epic: t.TempDir(), dir: t.TempDir(), cox: coxBin(t)}
	p.gen = arm(t, p.epic, "s1", "pi")
	ext, err := pi.InstallExtension(t.TempDir(), p.epic)
	must(t, err)
	p.ext = ext
	must(t, os.WriteFile(filepath.Join(p.dir, "drive.mjs"), []byte(driveScript), 0o644))
	must(t, os.WriteFile(filepath.Join(p.dir, "cox"), []byte(coxWrapper), 0o755))
	p.log = filepath.Join(p.dir, "done.log")
	return p
}

// drive fires mode with the given gen, waiting for want busy applies to finish.
func (p *piCase) drive(mode, gen string, want int) {
	p.t.Helper()
	_ = os.Remove(p.log)
	cmd := exec.Command("node", filepath.Join(p.dir, "drive.mjs"))
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "EXT_PATH="+p.ext, "MODE="+mode, fmt.Sprintf("WANT=%d", want), "DONE_LOG="+p.log,
		"REAL_COX="+p.cox, "COX_BIN="+filepath.Join(p.dir, "cox"), "COX_EPIC="+p.epic, "COX_STORY=s1",
		"COX_ROLE=worker", "COX_BUSY_GEN="+gen)
	if out, err := cmd.CombinedOutput(); err != nil {
		p.t.Fatalf("drive %s: %v\n%s", mode, err, out)
	}
}

func TestPortBusyAdapterWiring(t *testing.T) {
	// fm: tests/fm-busy-adapter-wiring.test.sh:82
	t.Run("FM/fm-busy-adapter-wiring/pi_extension_semantic_lifecycle", func(t *testing.T) {
		p := newPiCase(t)
		wantView(t, "pi-extension", p.epic, "s1", "busy dispatch")
		// fm's progress and turn_end notification edges have no cox counterpart (the progress marker is the
		// busy.progress-marker gap in fm-busy-state); the state edges below are the translated half.
		p.drive("settle-idle", p.gen, 1)
		wantView(t, "pi-extension", p.epic, "s1", "idle pi-ext")
		p.drive("agent-start", p.gen, 1)
		wantView(t, "pi-extension", p.epic, "s1", "busy pi-ext")
		p.drive("settle-continuing", p.gen, 0)
		wantView(t, "pi-extension", p.epic, "s1", "busy pi-ext")
		p.drive("settle-idle", p.gen, 1)
		wantView(t, "pi-extension", p.epic, "s1", "idle pi-ext")
		notImplemented(t, "busy.progress-marker", "native progress must write its own marker without changing semantic state, and turn_end stays a notification")
	})

	// fm: tests/fm-busy-adapter-wiring.test.sh:124
	t.Run("FM/fm-busy-adapter-wiring/pi_extension_serializes_settle_before_next_start", func(t *testing.T) {
		// The two applies are separate async children, so the order is a race; ten rounds keep the verdict stable.
		p := newPiCase(t)
		for i := 0; i < 10 && !t.Failed(); i++ {
			p.drive("settle-then-start", p.gen, 2)
			wantView(t, "pi-extension", p.epic, "s1", "busy pi-ext")
		}
	})

	// fm: tests/fm-busy-adapter-wiring.test.sh:139
	t.Run("FM/fm-busy-adapter-wiring/pi_extension_stale_incarnation_rejected", func(t *testing.T) {
		p := newPiCase(t)
		stale := p.gen
		arm(t, p.epic, "s1", "pi") // a re-arm supersedes the gen the running extension carries
		p.drive("settle-idle", stale, 1)
		wantView(t, "pi-extension", p.epic, "s1", "busy dispatch")
	})

	// n/a opencode_plugin_semantic_lifecycle fm:tests/fm-busy-adapter-wiring.test.sh:182 - opencode has no cox harness card or plugin

	// fm: tests/fm-busy-adapter-wiring.test.sh:237
	t.Run("FM/fm-busy-adapter-wiring/claude_hooks_semantic_lifecycle", func(t *testing.T) {
		epic, wt, env := launchCase(t, "claude")
		rec, ok := busy.ReadRecord(epic, "s1")
		if !ok {
			t.Fatalf("dispatch did not arm the busy record")
		}
		gen := rec.Gen
		for _, ev := range []string{"UserPromptSubmit", "Stop", "StopFailure", "SessionEnd"} {
			b, _ := os.ReadFile(filepath.Join(wt, ".claude", "settings.local.json"))
			if !strings.Contains(string(b), `"`+ev+`"`) {
				red(t, "worker-hooks.claude", "claude hook settings lack %s", ev)
			}
		}
		wantView(t, "worker-hooks.claude", epic, "s1", "busy dispatch")
		runHook(t, wt, env, "Stop", gen)
		wantView(t, "worker-hooks.claude", epic, "s1", "idle claude-hook")
		runHook(t, wt, env, "UserPromptSubmit", gen)
		wantView(t, "worker-hooks.claude", epic, "s1", "busy claude-hook")
		if !runHook(t, wt, env, "StopFailure", gen) {
			red(t, "worker-hooks.claude", "no StopFailure hook: an API error leaves the record busy (strands busy)")
		}
		wantView(t, "worker-hooks.claude", epic, "s1", "idle claude-hook")
		runHook(t, wt, env, "UserPromptSubmit", gen)
		runHook(t, wt, env, "SessionEnd", gen)
		wantView(t, "worker-hooks.claude", epic, "s1", "idle claude-hook")
	})

	// fm: tests/fm-busy-adapter-wiring.test.sh:275
	t.Run("FM/fm-busy-adapter-wiring/claude_hooks_stale_incarnation_harmless", func(t *testing.T) {
		epic, wt, env := launchCase(t, "claude")
		rec, _ := busy.ReadRecord(epic, "s1")
		arm(t, epic, "s1", "claude")
		runHook(t, wt, env, "UserPromptSubmit", rec.Gen)
		wantView(t, "worker-hooks.claude", epic, "s1", "busy dispatch")
	})

	// fm: tests/fm-busy-adapter-wiring.test.sh:291
	t.Run("FM/fm-busy-adapter-wiring/codex_unverified_until_a_semantic_source_exists", func(t *testing.T) {
		epic, wt, _ := launchCase(t, "codex")
		if _, ok := busy.ReadRecord(epic, "s1"); ok {
			red(t, "busy.codex-gate", "codex dispatch armed a busy record with no verified semantic source")
		}
		if _, err := os.Stat(filepath.Join(wt, ".codex", "hooks.json")); err == nil {
			red(t, "busy.codex-gate", "codex dispatch installed unverified busy hooks")
		}
		wantUnknown(t, epic, "s1", "codex", "codex-unverified")
	})

	// n/a gemini_hooks_semantic_lifecycle fm:tests/fm-busy-adapter-wiring.test.sh:319 - gemini has no cox harness card
	// n/a gemini_hooks_stale_incarnation_harmless fm:tests/fm-busy-adapter-wiring.test.sh:365 - gemini has no cox harness card
	// n/a raw_gemini_launch_has_no_semantic_wiring fm:tests/fm-busy-adapter-wiring.test.sh:381 - gemini has no cox harness card
	// n/a gemini_is_refused_as_a_secondmate fm:tests/fm-busy-adapter-wiring.test.sh:395 - secondmates are firstmate-only (DESIGN rule 5)
	// n/a kimi_and_grok_install_no_unverified_wiring fm:tests/fm-busy-adapter-wiring.test.sh:410 - kimi and grok have no cox harness card
}

func TestPortTmuxSubmitBusy(t *testing.T) {
	// fm: tests/fm-tmux-submit-busy.test.sh:67 (submit-while-busy: a busy worker's pending input is queued, not a
	// failed delivery; cox's composer verdict for a busy record is busy, so the ladder skips the ring and the durable
	// inbox record carries the message)
	t.Run("FM/fm-tmux-submit-busy/busy_pane_pending_returns_empty", func(t *testing.T) {
		epic := t.TempDir()
		arm(t, epic, "s1", "claude")
		if cs, ok := backend.BusyComposer(epic, "s1"); !ok || cs != backend.ComposerBusy {
			red(t, "backend.busy-composer", "busy record composer = %q ok=%v, want busy (queued)", cs, ok)
		}
	})

	// n/a idle_pane_pending_returns_pending fm:tests/fm-tmux-submit-busy.test.sh:90 - tmux Enter-retry verdict; cox confirms delivery by the inbox handled/ ack, not the composer after Enter
	// n/a wrapped_continuation_retries_swallowed_enter fm:tests/fm-tmux-submit-busy.test.sh:107 - tmux Enter-retry on wrapped input
	// n/a placeholder_like_bare_input_retries_swallowed_enter fm:tests/fm-tmux-submit-busy.test.sh:127 - tmux Enter-retry on placeholder text
	// n/a busy_pane_composer_clears_first_try fm:tests/fm-tmux-submit-busy.test.sh:147 - tmux Enter-retry
	// n/a idle_pane_composer_clears_first_try fm:tests/fm-tmux-submit-busy.test.sh:162 - tmux Enter-retry

	// fm: tests/fm-tmux-submit-busy.test.sh:177 (a busy worker never converts an unsafe verdict: an unknown record stays
	// unknown and defers to the backend's own classifier)
	t.Run("FM/fm-tmux-submit-busy/busy_pane_unknown_stays_unknown", func(t *testing.T) {
		epic := t.TempDir()
		rawRecord(t, epic, "s1", "garbage\n")
		if cs, ok := backend.BusyComposer(epic, "s1"); ok {
			red(t, "backend.busy-composer", "an unknown record resolved to %q; must defer", cs)
		}
	})

	// n/a failed_baseline_capture_keeps_busy_unknown_unconfirmed fm:tests/fm-tmux-submit-busy.test.sh:193 - tmux baseline capture before Enter
	// n/a busy_pane_ambiguous_pending_retries_without_conversion fm:tests/fm-tmux-submit-busy.test.sh:212 - tmux pending-unproven composer text

	// fm: tests/fm-tmux-submit-busy.test.sh:235 (an unrecognized record state is never converted by the busy view)
	t.Run("FM/fm-tmux-submit-busy/unrecognized_state_skips_busy_conversion", func(t *testing.T) {
		epic := t.TempDir()
		gen := arm(t, epic, "s1", "claude")
		rec, _ := busy.ReadRecord(epic, "s1")
		rec.State, rec.Gen = "future-state", gen
		rawRecord(t, epic, "s1", recJSON(t, rec))
		if cs, ok := backend.BusyComposer(epic, "s1"); ok {
			red(t, "backend.busy-composer", "an unrecognized state resolved to %q; must defer", cs)
		}
	})

	// n/a claude_busy_signature_uses_real_capture_shapes fm:tests/fm-tmux-submit-busy.test.sh:258 - pane-text busy signatures; cox never classifies busy from rendered text (captain ruling 2026-09-21)
}
