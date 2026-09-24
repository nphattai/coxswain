//go:build port

// Port tests (wave 1, cox-supervision-port-busy-wake): firstmate's wake-queue and drain suites (fm-wake-queue,
// fm-wake-drain-unread-status, fm-wake-drain-open-decisions, fm-wake-drain-open-decisions-cursor,
// fm-wake-drain-outcome-backstop, fm-wake-daemon-lifecycle-e2e) and docs/wedge-alarm.md translated case by case against
// cox's wake queue (wake.Append/Drain/AckThrough/Load), the `cox wake` verbs, worker reports and questions, and the
// watcher's leader-doorbell alarm. Firstmate pinned at 1e0e773 (references/firstmate, read only). Every case is
// t.Run("FM/<suite>/<case>") with a `// fm: <path>:<line>` citation. A failure names its cox mechanism as
// red[<mechanism>] or notImplemented[<mechanism>]; red is the deliverable (DESIGN translation contract). This is an
// external test package so it can drive the watcher (which imports wake) without an import cycle.
//
// Name map (firstmate -> cox): .wake-queue row -> a coxswain.wake.v1 line in .cox/wake.jsonl; sequence -> Gen;
// fm-wake-drain.sh -> wake.Drain / `cox wake drain`; --ack-through <seq> --recovery-generation <g> -> wake.AckThrough /
// `cox wake ack-through <gen>` (a monotonic gen cursor, no recovery episode); a worker status-log line -> one report
// wake (report.Report); a pending reply / needs-decision -> a question (report.Question) answered by `cox reply`; OPEN
// DECISIONS -> the open questions (question.List); the watcher's stale wake -> the stalePass unknown_probe wake and its
// heartbeat suppressor; inject_wedge_alarm -> Watcher.recordDoorbellFailure/fireAlarm; config/wedge-alarm ->
// policy alerts.channel (Watcher.AlarmChannel); FM_WEDGE_ALARM_EXEC -> Watcher.AlarmRun.
package wake_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/protocol/decision"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/protocol/question"
	"github.com/nphattai/coxswain/internal/protocol/report"
	"github.com/nphattai/coxswain/internal/protocol/status"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
	"github.com/nphattai/coxswain/internal/watch"
	"github.com/nphattai/coxswain/internal/workspace"
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

const story = "s1"

// newEpic is a temp epic with story s1 working, a recorded worker session, and a leader handle.
func newEpic(t *testing.T) string {
	t.Helper()
	epic := t.TempDir()
	must(t, state.Append(epic, state.Event{Epic: filepath.Base(epic), Story: story, Attempt: 1, Actor: state.Leader,
		From: state.Submitted, To: state.Working, ExternalConfirmed: true}))
	dir := filepath.Join(epic, state.ControlDir, "sessions")
	must(t, os.MkdirAll(dir, 0o755))
	b, err := json.Marshal(backend.Session{Kind: "fake", ID: "ctx_" + story, Handle: "term_" + story, Story: story})
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, story+".json"), b, 0o644))
	must(t, os.WriteFile(filepath.Join(epic, state.ControlDir, "leader"), []byte("term_leader"), 0o644))
	return epic
}

func appendN(t *testing.T, epic string, kind wake.Kind, notes ...string) []int {
	t.Helper()
	var gens []int
	for _, n := range notes {
		g, err := wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: story, Kind: kind, Note: n})
		must(t, err)
		gens = append(gens, g)
	}
	return gens
}

func drain(t *testing.T, epic string) []wake.Wake {
	t.Helper()
	w, err := wake.Drain(epic, false)
	must(t, err)
	return w
}

func notes(ws []wake.Wake) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Note)
	}
	return out
}

func queuePath(epic string) string { return filepath.Join(epic, wake.ControlDir, "wake.jsonl") }

// coxBin builds the cox binary once per test binary, so the verb-level cases run the real `cox wake` commands.
var (
	coxOnce sync.Once
	coxPath string
	coxErr  error
)

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
		dir, err := os.MkdirTemp("", "cox-port-wake-")
		if err != nil {
			coxErr = err
			return
		}
		root, _ := filepath.Abs(filepath.Join("..", ".."))
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

// cox runs the real binary and returns stdout, stderr and the exit code.
func cox(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(coxBin(t), args...)
	cmd.Env = append(os.Environ(), "COX_PLANE=terminal", "ORCA_TERMINAL_HANDLE=")
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run cox: %v", err)
	}
	return so.String(), se.String(), code
}

func TestPortWakeQueue(t *testing.T) {
	// fm: tests/fm-wake-queue.test.sh:22
	t.Run("FM/fm-wake-queue/concurrent_append_and_drain", func(t *testing.T) {
		epic := newEpic(t)
		var wg sync.WaitGroup
		errs := make(chan error, 41)
		for i := 1; i <= 40; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := wake.Append(epic, wake.Wake{Epic: "e", Story: story, Kind: wake.KindStatus, Note: fmt.Sprintf("status-%d", i)})
				errs <- err
			}(i)
		}
		wg.Add(1)
		go func() { defer wg.Done(); _, err := wake.Drain(epic, false); errs <- err }()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				red(t, "wake.queue", "concurrent append/drain failed: %v", err)
			}
		}
		ws := drain(t, epic)
		seen := map[string]bool{}
		gens := map[int]bool{}
		for _, w := range ws {
			seen[w.Note], gens[w.Gen] = true, true
		}
		if len(ws) != 40 || len(seen) != 40 || len(gens) != 40 {
			red(t, "wake.queue", "final replay = %d records, %d unique notes, %d unique gens; want 40/40/40", len(ws), len(seen), len(gens))
		}
		max := 0
		for g := range gens {
			if g > max {
				max = g
			}
		}
		must(t, wake.AckThrough(epic, max))
		if left := drain(t, epic); len(left) != 0 {
			red(t, "wake.queue", "acknowledged concurrent records remained queued: %d", len(left))
		}
	})

	// fm: tests/fm-wake-queue.test.sh:57 (a worker report written while no watcher runs is durable at once on the
	// terminal plane: report.Report appends the wake itself, so the next drain catches it up)
	t.Run("FM/fm-wake-queue/signal_catchup_without_running_watcher", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, story, 1, report.KindStuck, "blocked: first", nil)
		must(t, err)
		if ws := drain(t, epic); len(ws) != 1 {
			red(t, "wake.queue", "first report was not queued: %v", notes(ws))
		}
		must(t, wake.AckThrough(epic, g))
		_, err = report.Report(epic, story, 1, report.KindDone, "done: second", nil)
		must(t, err)
		ws := drain(t, epic)
		if len(ws) != 1 || ws[0].Kind != wake.KindWorkerDone {
			red(t, "wake.queue", "a report written with no watcher was not caught: %v", notes(ws))
		}
	})

	// fm: tests/fm-wake-queue.test.sh:89 (the stale wake is the stalePass unknown_probe wake; its suppressor is the
	// heartbeat bump, which must not advance when the enqueue failed)
	t.Run("FM/fm-wake-queue/stale_enqueue_before_suppressor", func(t *testing.T) {
		staleEnqueueBeforeSuppressor(t, nil)
	})

	// fm: tests/fm-wake-queue.test.sh:123 (not provably working: the probe itself errors)
	t.Run("FM/fm-wake-queue/not_working_stale_enqueue_before_suppressor", func(t *testing.T) {
		staleEnqueueBeforeSuppressor(t, errors.New("probe: terminal gone"))
	})

	// n/a check_output_is_queued fm:tests/fm-wake-queue.test.sh:157 - registered custom checks (fm-check-register.sh) have no cox counterpart

	// fm: tests/fm-wake-queue.test.sh:181
	t.Run("FM/fm-wake-queue/atomic_double_drain", func(t *testing.T) {
		epic := newEpic(t)
		appendN(t, epic, wake.KindStatus, "heartbeat", "signal: task", "stale: s:fm-task")
		var a, b []wake.Wake
		var ea, eb error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); a, ea = wake.Drain(epic, false) }()
		go func() { defer wg.Done(); b, eb = wake.Drain(epic, false) }()
		wg.Wait()
		must(t, ea)
		must(t, eb)
		if len(a) != 3 || len(b) != 3 || strings.Join(notes(a), "|") != strings.Join(notes(b), "|") {
			red(t, "wake.queue", "unacknowledged concurrent drains did not replay the same three records: %v / %v", notes(a), notes(b))
		}
		must(t, wake.AckThrough(epic, a[len(a)-1].Gen))
		if left := drain(t, epic); len(left) != 0 {
			red(t, "wake.queue", "acknowledged records replayed again: %v", notes(left))
		}
	})

	// fm: tests/fm-wake-queue.test.sh:213
	t.Run("FM/fm-wake-queue/drain_dedupes_obvious_duplicates", func(t *testing.T) {
		epic := newEpic(t)
		for _, n := range []string{"phase 2 building", "phase 2 building", "phase 2 building (turn ended)"} {
			_, err := report.Report(epic, story, 1, report.KindStatus, n, nil)
			must(t, err)
		}
		appendN(t, epic, wake.KindStuck, "watcher heartbeat", "watcher heartbeat")
		ws := drain(t, epic)
		if len(ws) != 2 {
			red(t, "wake.drain-dedupe", "drain presented %d records for 2 distinct (kind, story) keys, want the duplicates collapsed to the latest payload: %v", len(ws), notes(ws))
		}
	})

	// n/a secondmate_foreign_queue_stall_tracks_progress_and_alerts_once fm:tests/fm-wake-queue.test.sh:265 - secondmates are firstmate-only (DESIGN rule 5)
	// n/a secondmate_declared_pause_rows_do_not_feed_stall_escalation fm:tests/fm-wake-queue.test.sh:339 - secondmates are firstmate-only
	// n/a secondmate_reprovisioned_queue_starts_a_fresh_interval fm:tests/fm-wake-queue.test.sh:387 - secondmates are firstmate-only
	// n/a secondmate_active_turn_defers_stall_until_the_turn_ends fm:tests/fm-wake-queue.test.sh:449 - secondmates are firstmate-only
	// n/a secondmate_long_lived_mate_mid_turn_is_not_a_stall fm:tests/fm-wake-queue.test.sh:516 - secondmates are firstmate-only
	// n/a secondmate_proven_idle_ring_lets_the_child_drain fm:tests/fm-wake-queue.test.sh:625 - secondmates are firstmate-only
	// n/a secondmate_busy_and_unknown_panes_are_not_rung fm:tests/fm-wake-queue.test.sh:687 - secondmates are firstmate-only
	// n/a secondmate_genuine_stall_after_idle_ring_still_alarms fm:tests/fm-wake-queue.test.sh:746 - secondmates are firstmate-only
	// n/a secondmate_stall_marker_rejects_symlink fm:tests/fm-wake-queue.test.sh:805 - secondmates are firstmate-only
	// n/a acknowledged_stall_publication_survives_pre_marker_crash fm:tests/fm-wake-queue.test.sh:844 - secondmate wake-loop stall publication, firstmate-only
	// n/a empty_prefix_mate_preserves_other_mate_receipt fm:tests/fm-wake-queue.test.sh:883 - secondmate homes, firstmate-only

	// fm: tests/fm-wake-queue.test.sh:934 (work in flight and no live watcher: the drain warns; a live watcher whose
	// beacon (.cox/watch/lasttick) is fresh keeps it silent)
	t.Run("FM/fm-wake-queue/drain_asserts_watcher_liveness", func(t *testing.T) {
		epic := newEpic(t)
		_, se, code := cox(t, "wake", "drain", "--epic", epic)
		if code != 0 {
			t.Fatalf("drain exit %d: %s", code, se)
		}
		if !strings.Contains(strings.ToLower(se), "watcher") || !strings.Contains(se, "WATCHER DOWN") {
			red(t, "wake.drain-liveness", "drain with a working story and no live watcher printed no watcher-down warning (stderr %q)", se)
		}
		must(t, os.WriteFile(filepath.Join(epic, state.ControlDir, "watch.pid"), []byte(fmt.Sprint(os.Getpid())), 0o644))
		must(t, os.MkdirAll(filepath.Join(epic, state.ControlDir, "watch"), 0o755))
		must(t, os.WriteFile(filepath.Join(epic, state.ControlDir, "watch", "lasttick"), nil, 0o644))
		if _, se, _ := cox(t, "wake", "drain", "--epic", epic); strings.Contains(se, "WATCHER DOWN") {
			red(t, "wake.drain-liveness", "drain false-alarmed with a live watcher and fresh beacon: %q", se)
		}
	})

	// n/a structural_signal_enrichment_preserves_raw_rows fm:tests/fm-wake-queue.test.sh:959 - drain-time enrichment from firstmate status files; a cox wake carries its report body itself
	// n/a enrichment_preserves_all_unread_lines_and_status_file_failures fm:tests/fm-wake-queue.test.sh:1017 - status-file annotation, firstmate-only
	// n/a slow_annotation_does_not_block_append_and_deleted_file_fails_open fm:tests/fm-wake-queue.test.sh:1073 - status-file annotation, firstmate-only
	// n/a branch_actor_scoped_ack_never_swallows_a_main_owned_row fm:tests/fm-wake-queue.test.sh:1107 - per-actor grants for firstmate's Pi supervision branch; cox has one leader consumer per epic
	// n/a main_drain_excludes_rows_already_granted_to_branch fm:tests/fm-wake-queue.test.sh:1165 - per-actor grants, firstmate-only
	// n/a main_is_never_told_to_drain_rows_only_the_branch_owns fm:tests/fm-wake-queue.test.sh:1209 - per-actor grants, firstmate-only

	// fm: tests/fm-wake-queue.test.sh:1267 (a queue that cannot be read is never reported as empty)
	t.Run("FM/fm-wake-queue/uncountable_queue_still_raises_the_pending_alarm", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads mode-000 files (environment)")
		}
		epic := newEpic(t)
		appendN(t, epic, wake.KindStuck, "stale: fleet:w2:p3")
		must(t, os.Chmod(queuePath(epic), 0o000))
		defer os.Chmod(queuePath(epic), 0o600)
		if ws, err := wake.Drain(epic, true); err == nil {
			red(t, "wake.queue", "an unreadable queue drained as %d records with no error", len(ws))
		}
		must(t, os.Chmod(queuePath(epic), 0o600))
		must(t, os.WriteFile(queuePath(epic), nil, 0o600))
		if ws, err := wake.Drain(epic, true); err != nil || len(ws) != 0 {
			red(t, "wake.queue", "a provably empty queue did not drain clean: %v %v", ws, err)
		}
	})

	// fm: tests/fm-wake-queue.test.sh:1314
	t.Run("FM/fm-wake-queue/unconsumable_rows_are_retired_instead_of_wedging_the_queue", func(t *testing.T) {
		epic := newEpic(t)
		appendN(t, epic, wake.KindStatus, "signal: task-a")
		f, err := os.OpenFile(queuePath(epic), os.O_APPEND|os.O_WRONLY, 0o644)
		must(t, err)
		_, _ = f.WriteString(`{"schema":"coxswain.wake.v1","gen":"not-a-sequence","kind":"stale"}` + "\n" + `{"schema":"coxswain.wake.v1","gen":57` + "\n")
		must(t, f.Close())
		ws, err := wake.Drain(epic, false)
		if err != nil {
			red(t, "wake.row-retirement", "two unusable rows wedge the whole queue (drain error: %v); they must be retired and reported, the usable row presented", err)
			return
		}
		if len(ws) != 1 || ws[0].Note != "signal: task-a" {
			red(t, "wake.row-retirement", "usable row not presented alone: %v", notes(ws))
		}
		if _, err := wake.Append(epic, wake.Wake{Epic: "e", Story: story, Kind: wake.KindStatus, Note: "after"}); err != nil {
			red(t, "wake.row-retirement", "the queue stays wedged for appends: %v", err)
		}
	})

	// n/a branch_grant_refuses_rows_already_claimed_by_main fm:tests/fm-wake-queue.test.sh:1362 - per-actor grants, firstmate-only
	// n/a actor_filter_precedes_same_key_deduplication fm:tests/fm-wake-queue.test.sh:1381 - per-actor grants, firstmate-only
	// n/a main_reclaims_a_grant_whose_branch_owner_exited fm:tests/fm-wake-queue.test.sh:1412 - per-actor grants, firstmate-only
	// n/a branch_actor_without_eligible_snapshot_refuses fm:tests/fm-wake-queue.test.sh:1448 - per-actor grants, firstmate-only

	// fm: tests/fm-wake-queue.test.sh:1461 (a failed publish leaves no durable row and a retry is recovered by the drain;
	// cox has no recovery marker, the append itself is the publish)
	t.Run("FM/fm-wake-queue/wake_publish_requires_atomic_recovery_evidence", func(t *testing.T) {
		epic := newEpic(t)
		must(t, os.MkdirAll(filepath.Join(epic, wake.ControlDir), 0o755))
		must(t, os.Mkdir(queuePath(epic), 0o755)) // the publish target cannot be written
		if _, err := wake.Append(epic, wake.Wake{Epic: "e", Story: story, Kind: wake.KindStatus, Note: "publish failure"}); err == nil {
			red(t, "wake.queue", "a failed publish reported success")
		}
		must(t, os.Remove(queuePath(epic)))
		appendN(t, epic, wake.KindStatus, "recovered retry")
		if ws := drain(t, epic); len(ws) != 1 || ws[0].Note != "recovered retry" {
			red(t, "wake.queue", "retried wake was not recovered by the drain: %v", notes(ws))
		}
	})

	// fm: tests/fm-wake-queue.test.sh:1500 (a row written before the current schema/gen format)
	t.Run("FM/fm-wake-queue/legacy_generationless_wake_is_adopted", func(t *testing.T) {
		epic := newEpic(t)
		must(t, os.MkdirAll(filepath.Join(epic, wake.ControlDir), 0o755))
		must(t, os.WriteFile(queuePath(epic), []byte(`{"gen":7,"story":"s1","kind":"stuck","note":"legacy process-event"}`+"\n"), 0o644))
		ws := drain(t, epic)
		if len(ws) != 1 {
			red(t, "wake.legacy-adoption", "a legacy row without the schema tag is silently skipped (presented %d); it must be adopted, presented and acknowledgeable", len(ws))
			return
		}
		must(t, wake.AckThrough(epic, ws[0].Gen))
		if left := drain(t, epic); len(left) != 0 {
			red(t, "wake.legacy-adoption", "acknowledged legacy wake replayed")
		}
	})

	// fm: tests/fm-wake-queue.test.sh:1535
	t.Run("FM/fm-wake-queue/stale_recovery_generation_cannot_touch_a_newer_episode", func(t *testing.T) {
		epic := newEpic(t)
		g := appendN(t, epic, wake.KindStatus, "first", "second")
		must(t, wake.AckThrough(epic, g[1]))
		g3 := appendN(t, epic, wake.KindStuck, "newer")
		must(t, wake.AckThrough(epic, g[0])) // a stale acknowledgement from the earlier episode
		if ws := drain(t, epic); len(ws) != 1 || ws[0].Gen != g3[0] {
			red(t, "wake.ack", "a stale acknowledgement touched the newer wake: %v", notes(ws))
		}
	})

	// fm: tests/fm-wake-queue.test.sh:1622
	t.Run("FM/fm-wake-queue/stale_ack_that_consumes_nothing_names_the_current_wake", func(t *testing.T) {
		epic := newEpic(t)
		g := appendN(t, epic, wake.KindStatus, "first", "current")
		must(t, wake.AckThrough(epic, g[0]))
		_, se, code := cox(t, "wake", "ack-through", fmt.Sprint(g[0]), "--epic", epic)
		want := fmt.Sprintf("ack-through %d", g[1])
		if code == 0 && !strings.Contains(se, want) {
			red(t, "wake.ack", "an acknowledgement that consumed nothing exited 0 silently; it must say so and name %q (stderr %q)", want, se)
		}
	})

	// n/a branch_stale_ack_that_consumes_nothing_names_its_granted_wake fm:tests/fm-wake-queue.test.sh:1672 - per-actor grants, firstmate-only

	// fm: tests/fm-wake-queue.test.sh:1714 (an acknowledgement whose write fails is explicit and retryable)
	t.Run("FM/fm-wake-queue/recovery_ack_failure_is_reported", func(t *testing.T) {
		epic := newEpic(t)
		g := appendN(t, epic, wake.KindStatus, "fixture")
		tmp := filepath.Join(epic, wake.ControlDir, "wake.ack.tmp")
		must(t, os.Mkdir(tmp, 0o755)) // the ack write cannot land
		if err := wake.AckThrough(epic, g[0]); err == nil {
			red(t, "wake.ack", "an acknowledgement write failure was reported as success")
		}
		if n, _ := wake.Acked(epic); n != 0 {
			red(t, "wake.ack", "failed acknowledgement moved the cursor to %d", n)
		}
		must(t, os.Remove(tmp))
		if err := wake.AckThrough(epic, g[0]); err != nil {
			red(t, "wake.ack", "acknowledgement did not succeed on retry: %v", err)
		}
	})

	// fm: tests/fm-wake-queue.test.sh:1757 (a drain never consumes, so an interruption at any point keeps the row
	// until the post-handling acknowledgement)
	t.Run("FM/fm-wake-queue/interruption_before_and_after_raw_commit", func(t *testing.T) {
		epic := newEpic(t)
		appendN(t, epic, wake.KindStatus, "done: interruption fixture")
		cmd := exec.Command(coxBin(t), "wake", "wait", "--epic", epic, "--max", "1s")
		must(t, cmd.Start())
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		ws := drain(t, epic)
		if len(ws) != 1 {
			red(t, "wake.queue", "an interrupted drain lost or duplicated the durable row: %d", len(ws))
		}
		must(t, wake.AckThrough(epic, ws[0].Gen))
		if len(drain(t, epic)) != 0 {
			red(t, "wake.queue", "acknowledged row replayed")
		}
	})

	// n/a self_announced_append_guards fm:tests/fm-wake-queue.test.sh:1820 - self-announced status-file appends and seen-signature gate, firstmate-only
	// n/a separate_self_announced_answers_after_fold_are_owned fm:tests/fm-wake-queue.test.sh:1912 - owned-append ledger over status files, firstmate-only
	// n/a unreadable_status_is_not_owned fm:tests/fm-wake-queue.test.sh:1970 - owned-append ledger over status files, firstmate-only
	// n/a folded_worker_resolved_is_not_owned_lag fm:tests/fm-wake-queue.test.sh:2016 - owned-append ledger over status files, firstmate-only
	// n/a self_held_lock_reclaims_instead_of_deadlocking fm:tests/fm-wake-queue.test.sh:2052 - bash trap re-entry into a mkdir lock; cox holds flock on an fd the kernel releases
	// n/a subshell_lock_ownership_without_bashpid fm:tests/fm-wake-queue.test.sh:2078 - bash subshell/BASHPID lock ownership
	// n/a bounded_lock_handoff_after_contention fm:tests/fm-wake-queue.test.sh:2108 - bash helper-process lock handoff

	// fm: tests/fm-wake-queue.test.sh:2181 (a stuck lock holder never strands the drain, while acknowledgement keeps its
	// blocking all-or-nothing contract)
	t.Run("FM/fm-wake-queue/live_presentation_holder_is_deadlined_without_weakening_ack", func(t *testing.T) {
		epic := newEpic(t)
		g := appendN(t, epic, wake.KindStuck, "needs-decision: presentation remains retriable")
		f, err := os.OpenFile(filepath.Join(epic, wake.ControlDir, "wake.lock"), os.O_CREATE|os.O_RDWR, 0o644)
		must(t, err)
		must(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))
		start := time.Now()
		ws, err := wake.Drain(epic, false)
		if err != nil || len(ws) != 1 || time.Since(start) > 2*time.Second {
			red(t, "wake.queue", "drain was stranded by a live lock holder: %v %v %s", notes(ws), err, time.Since(start))
		}
		acked := make(chan error, 1)
		go func() { acked <- wake.AckThrough(epic, g[0]) }()
		select {
		case <-acked:
			red(t, "wake.ack", "acknowledgement did not wait for the queue lock")
		case <-time.After(300 * time.Millisecond):
		}
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		if err := <-acked; err != nil {
			red(t, "wake.ack", "acknowledgement failed after the holder released: %v", err)
		}
	})

	// n/a malformed_presentation_lock_reports_acquire_failure fm:tests/fm-wake-queue.test.sh:2324 - cox has no presentation lock file (flock on an fd, no pid payload to malform)
	// n/a owned_growth_still_annotates_turn_ended fm:tests/fm-wake-queue.test.sh:2354 - owned-append ledger and turn-ended annotation over status files, firstmate-only
	// n/a historical_annotation_skips_announced_status fm:tests/fm-wake-queue.test.sh:2396 - status-file historical annotation, firstmate-only
}

// staleEnqueueBeforeSuppressor drives the watcher's stale probe over a silent worker while the queue cannot be written:
// the heartbeat (the suppressor) must not advance, so the next tick with a writable queue still raises the wake.
func staleEnqueueBeforeSuppressor(t *testing.T, probeErr error) {
	t.Helper()
	epic := newEpic(t)
	hb := filepath.Join(epic, state.ControlDir, "watch", "hb", "ctx_"+story)
	must(t, os.MkdirAll(filepath.Dir(hb), 0o755))
	must(t, os.WriteFile(hb, nil, 0o644))
	old := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(hb, old, old))
	b := fake.New()
	b.Liveness = backend.Unknown
	w := &watch.Watcher{EpicDir: epic, Backend: b, StaleMin: time.Minute}
	must(t, os.Mkdir(queuePath(epic), 0o755)) // enqueue fails
	if probeErr != nil {
		b.FailNext("Probe", probeErr)
	}
	_, _ = w.Tick()
	if info, err := os.Stat(hb); err == nil && time.Since(info.ModTime()) < time.Minute {
		red(t, "watch.stale-enqueue-order", "the stale suppressor (heartbeat) advanced although the wake was never enqueued")
	}
	must(t, os.Remove(queuePath(epic)))
	if probeErr != nil {
		b.FailNext("Probe", probeErr)
	}
	_, _ = w.Tick()
	found := false
	for _, wk := range drain(t, epic) {
		found = found || wk.Kind == wake.KindUnknownProbe
	}
	if !found {
		red(t, "watch.stale-enqueue-order", "the stale wake lost to a failed enqueue never surfaced on the next tick")
	}
}

// ask asks a question the way `cox story report question` does and returns its id.
func ask(t *testing.T, epic, body string) string {
	t.Helper()
	id, err := report.Question(epic, story, 1, body)
	must(t, err)
	return id
}

func TestPortWakeDrainUnreadStatus(t *testing.T) {
	// fm: tests/fm-wake-drain-unread-status.test.sh:28
	t.Run("FM/fm-wake-drain-unread-status/incident_note_answer_buried_under_routine_note_surfaces_both", func(t *testing.T) {
		epic := newEpic(t)
		for _, n := range []string{"captain said use REST not RPC", "re-read acknowledgement"} {
			_, err := report.Report(epic, story, 1, report.KindStatus, n, nil)
			must(t, err)
		}
		got := strings.Join(notes(drain(t, epic)), "|")
		if !strings.Contains(got, "captain said use REST not RPC") || !strings.Contains(got, "re-read acknowledgement") {
			red(t, "wake.drain", "a buried status was dropped: %q", got)
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:50
	t.Run("FM/fm-wake-drain-unread-status/already_presented_notes_are_not_replayed", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, story, 1, report.KindStatus, "captain said use REST not RPC", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g))
		if ws := drain(t, epic); len(ws) != 0 {
			red(t, "wake.drain", "an already-presented status replayed: %v", notes(ws))
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:77
	t.Run("FM/fm-wake-drain-unread-status/brand_new_note_after_presentation_is_surfaced", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, story, 1, report.KindStatus, "first answer", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g))
		_, err = report.Report(epic, story, 1, report.KindStatus, "follow-up after ack", nil)
		must(t, err)
		if got := notes(drain(t, epic)); len(got) != 1 || got[0] != "follow-up after ack" {
			red(t, "wake.drain", "brand-new status after presentation = %v", got)
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:100 (every unread report is presented beside the queued signal)
	t.Run("FM/fm-wake-drain-unread-status/signal_annotation_surfaces_every_unread_note_not_only_the_newest", func(t *testing.T) {
		epic := newEpic(t)
		for _, n := range []string{"captain said use REST not RPC", "re-read acknowledgement"} {
			_, err := report.Report(epic, story, 1, report.KindStatus, n, nil)
			must(t, err)
		}
		ask(t, epic, "ship it?")
		if ws := drain(t, epic); len(ws) != 3 {
			red(t, "wake.drain", "drain presented %d of 3 unread records: %v", len(ws), notes(ws))
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:126 (a pending reply resolves once; B-53: a reply the worker already
	// consumed through `cox question wait` must not keep counting as an unread inbox steer, or the runaway rule fires)
	t.Run("FM/fm-wake-drain-unread-status/pending_reply_resolution_surfaces_once", func(t *testing.T) {
		epic := newEpic(t)
		id := ask(t, epic, "ship it?")
		must(t, wake.AckThrough(epic, drain(t, epic)[0].Gen))
		// The leader answers with `cox reply` (answer file + reply inbox record), the worker consumes it.
		_, se, code := cox(t, "reply", story, id, "ship it", "--epic", epic)
		if code != 0 {
			t.Fatalf("cox reply: %d %s", code, se)
		}
		ans, timedOut, err := question.Wait(epic, story, id, 5*time.Second)
		if err != nil || timedOut || ans == "" {
			t.Fatalf("question wait: %q %v %v", ans, timedOut, err)
		}
		b := fake.New()
		b.Liveness = backend.Alive
		clock := time.Now().Add(2 * time.Hour)
		w := &watch.Watcher{EpicDir: epic, Backend: b, InboxGrace: time.Second, RunawayMin: time.Minute, Now: func() time.Time { return clock }}
		_, _ = w.Tick()
		for _, wk := range drain(t, epic) {
			if wk.Kind == wake.KindRunaway {
				red(t, "watch.runaway-consumed-reply", "B-53: a reply already consumed by `cox question wait` counted as unread and raised a runaway (%s)", wk.Note)
			}
		}
		recs, _ := inbox.List(epic, story)
		if len(recs) != 0 {
			red(t, "watch.runaway-consumed-reply", "the consumed reply still sits unread in the inbox (%d records)", len(recs))
		}
	})

	// n/a self_announced_pending_reply_close_still_surfaces fm:tests/fm-wake-drain-unread-status.test.sh:165 - watcher-authored pending-reply escalation close over a status file; cox never escalates a pending reply

	// fm: tests/fm-wake-drain-unread-status.test.sh:222 (the 200-char note is a cap on the line, the full body survives)
	t.Run("FM/fm-wake-drain-unread-status/unread_output_over_cap_remains_recoverable", func(t *testing.T) {
		epic := newEpic(t)
		long := strings.Repeat("x", 20000)
		_, err := report.Report(epic, story, 1, report.KindStatus, long, nil)
		must(t, err)
		ws := drain(t, epic)
		full := ws[0].Full
		if full == "" {
			full = ws[0].Note
		}
		if full != long {
			red(t, "wake.drain", "the over-cap body is not recoverable in full (%d of %d chars)", len(full), len(long))
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:247
	t.Run("FM/fm-wake-drain-unread-status/snapshot_does_not_ack_a_later_append", func(t *testing.T) {
		epic := newEpic(t)
		g := appendN(t, epic, wake.KindStatus, "presented")
		snap := drain(t, epic)
		appendN(t, epic, wake.KindStatus, "appended after the snapshot")
		must(t, wake.AckThrough(epic, snap[len(snap)-1].Gen))
		if got := notes(drain(t, epic)); len(got) != 1 || got[0] != "appended after the snapshot" || g[0] != snap[0].Gen {
			red(t, "wake.ack", "ack past the captured snapshot: left %v", got)
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:275 (a re-dispatched story id starts unread)
	t.Run("FM/fm-wake-drain-unread-status/retired_task_id_starts_new_status_unread", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, story, 1, report.KindDone, "old incarnation done", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g))
		_, err = report.Report(epic, story, 2, report.KindStatus, "replacement starts", nil)
		must(t, err)
		if got := notes(drain(t, epic)); len(got) != 1 || got[0] != "replacement starts" {
			red(t, "wake.drain", "replacement incarnation's first status not unread: %v", got)
		}
	})

	// n/a weak_identity_still_presents_and_advances fm:tests/fm-wake-drain-unread-status.test.sh:333 - status-file inode identity fallback, firstmate-only

	// fm: tests/fm-wake-drain-unread-status.test.sh:352
	t.Run("FM/fm-wake-drain-unread-status/snapshot_failure_is_visible", func(t *testing.T) {
		epic := newEpic(t)
		must(t, os.MkdirAll(filepath.Join(epic, wake.ControlDir), 0o755))
		must(t, os.WriteFile(filepath.Join(epic, wake.ControlDir, "wake.ack"), []byte("garbage\n"), 0o644))
		appendN(t, epic, wake.KindStatus, "x")
		_, se, code := cox(t, "wake", "drain", "--epic", epic)
		if code == 0 || se == "" {
			red(t, "wake.drain", "a snapshot failure was not visible (exit %d, stderr %q)", code, se)
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:364 (the open-decision fold runs independently of unread status: a
	// buried decision and an unread note surface together, and a resolution clears the decision everywhere)
	t.Run("FM/fm-wake-drain-unread-status/open_decisions_fold_is_unchanged", func(t *testing.T) {
		epic := newEpic(t)
		id := ask(t, epic, "pick REST or RPC")
		must(t, wake.AckThrough(epic, drain(t, epic)[0].Gen))
		_, err := report.Report(epic, story, 1, report.KindStatus, "routine note", nil)
		must(t, err)
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "s1 [key="+id+"] needs-decision: pick REST or RPC", "OPEN DECISIONS no longer surfaces a buried question")
		wantLine(t, out, "routine note", "the unread note was not surfaced alongside the still-open decision")
		wantLine(t, out, "OPEN DECISIONS: close one by answering it: cox reply", "OPEN DECISIONS lost its answerer-closes hint")

		for _, l := range []string{"needs-decision [key=api-shape]: pick REST or RPC", "working: continuing other work", "note: re-read acknowledgement"} {
			_, err := report.Report(epic, "task6", 1, report.KindStatus, l, nil)
			must(t, err)
		}
		out, _, _ = cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "task6 [key=api-shape] needs-decision: pick REST or RPC", "OPEN DECISIONS no longer surfaces a buried needs-decision")
		wantLine(t, out, "task6: note: re-read acknowledgement", "the unread note was not surfaced alongside the still-open decision")

		_, err = report.Report(epic, "task6", 1, report.KindStatus, "resolved [key=api-shape]: went with REST", nil)
		must(t, err)
		_, se, code := cox(t, "reply", story, id, "REST", "--epic", epic)
		if code != 0 {
			t.Fatalf("cox reply: %s", se)
		}
		ws := drain(t, epic)
		must(t, wake.AckThrough(epic, ws[len(ws)-1].Gen))
		out, _, _ = cox(t, "wake", "drain", "--epic", epic)
		if strings.Contains(out, "OPEN DECISIONS") || strings.Contains(out, "pick REST or RPC") {
			red(t, "wake.drain-open-decisions", "an explicitly resolved decision still printed as open: %s", out)
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:393
	t.Run("FM/fm-wake-drain-unread-status/empty_queue_does_not_swallow_later_signal_annotation", func(t *testing.T) {
		epic := newEpic(t)
		if ws := drain(t, epic); len(ws) != 0 {
			t.Fatalf("setup: queue not empty")
		}
		_, err := report.Report(epic, story, 1, report.KindStatus, "later status", nil)
		must(t, err)
		if got := notes(drain(t, epic)); len(got) != 1 {
			red(t, "wake.drain", "an empty-queue drain swallowed the later status: %v", got)
		}
	})

	// fm: tests/fm-wake-drain-unread-status.test.sh:415
	t.Run("FM/fm-wake-drain-unread-status/routine_working_and_covered_done_stay_silent_on_the_empty_queue", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, story, 1, report.KindDone, "covered done", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g))
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		if strings.TrimSpace(out) != "" {
			red(t, "wake.drain", "an empty-queue drain printed %q", out)
		}
	})
}

// statusLine logs a worker status event with no wake row, the cox shape of a firstmate status-file line no queue row
// carries (status.Report with no mailbox: the event lands, nothing is queued).
func statusLine(t *testing.T, epic, story, line string) {
	t.Helper()
	must(t, status.Report(epic, story, 1, "status", line, nil, ""))
}

// storyFile writes stories/<story>.md with the given frontmatter kind (firstmate: <task>.meta kind=).
func storyFile(t *testing.T, epic, story, kind string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Join(epic, "stories"), 0o755))
	must(t, os.WriteFile(filepath.Join(epic, "stories", story+".md"), []byte("---\nid: "+story+"\nkind: "+kind+"\n---\n"), 0o644))
}

// historyOf returns one story's folded status history.
func historyOf(t *testing.T, epic, story string) wake.History {
	t.Helper()
	hs, err := wake.Histories(epic)
	must(t, err)
	for _, h := range hs {
		if h.Story == story {
			return h
		}
	}
	return wake.History{Story: story}
}

// wantLine asserts the drain output carries sub.
func wantLine(t *testing.T, out, sub, msg string) {
	t.Helper()
	if !strings.Contains(out, sub) {
		red(t, "wake.drain", "%s: %q not in:\n%s", msg, sub, out)
	}
}

// lineStarting returns the first output line that starts with prefix.
func lineStarting(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// backstopBody is the STATUS OUTCOME BACKSTOP section's items (firstmate backstop_body).
func backstopBody(out string) string {
	var b []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "STATUS OUTCOME BACKSTOP ("):
			in = true
		case in && (strings.HasPrefix(l, "OPEN DECISIONS") || strings.HasPrefix(l, "STATUS OUTCOME BACKSTOP:")):
			in = false
		case in:
			b = append(b, l)
		}
	}
	return strings.Join(b, "\n")
}

// failWriter is an output consumer that fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("consumer closed") }

// openQuestionOnDrain reports whether the real `cox wake drain` output names an open question after its wake was acked.
func openQuestionOnDrain(t *testing.T, epic, text string) bool {
	t.Helper()
	out, _, _ := cox(t, "wake", "drain", "--epic", epic)
	return strings.Contains(out, text)
}

func TestPortWakeDrainOpenDecisions(t *testing.T) {
	// fm: tests/fm-wake-drain-open-decisions.test.sh:18
	t.Run("FM/fm-wake-drain-open-decisions/buried_decision_still_surfaces", func(t *testing.T) {
		epic := newEpic(t)
		ask(t, epic, "pick REST or RPC")
		for _, n := range []string{"working: routine", "note: other key"} {
			_, err := report.Report(epic, story, 1, report.KindStatus, n, nil)
			must(t, err)
		}
		ws := drain(t, epic)
		must(t, wake.AckThrough(epic, ws[len(ws)-1].Gen))
		if !openQuestionOnDrain(t, epic, "pick REST or RPC") {
			red(t, "wake.drain-open-decisions", "an unanswered question buried under later reports did not report as open")
		}
		for _, l := range []string{"needs-decision [key=api-shape]: pick REST or RPC", "working: continuing other work", "resolved [key=other]: unrelated decision closed"} {
			statusLine(t, epic, "task1", l)
		}
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "OPEN DECISIONS", "buried decision produced no OPEN DECISIONS section")
		wantLine(t, out, "task1 [key=api-shape] needs-decision: pick REST or RPC", "buried needs-decision was not surfaced with its task, key, and note")
		wantLine(t, out, "OPEN DECISIONS: close one by answering it:", "open section is missing the answerer-closes hint")
	})

	// fm: tests/fm-wake-drain-open-decisions.test.sh:40 (answering closes it: the question leaves the open set)
	t.Run("FM/fm-wake-drain-open-decisions/explicit_resolution_closes_it", func(t *testing.T) {
		epic := newEpic(t)
		id := ask(t, epic, "pick REST or RPC")
		_, _ = question.Answer(epic, story, id, "REST", false)
		_, _, _ = question.Wait(epic, story, id, time.Second)
		if ids, _ := question.List(epic, story); len(ids) != 0 {
			red(t, "question.open-set", "an answered and consumed question stays open: %v", ids)
		}
		if openQuestionOnDrain(t, epic, "OPEN") {
			red(t, "wake.drain-open-decisions", "a resolved question still printed as open")
		}
	})

	// n/a reserved_key_namespace_is_owned_by_its_library fm:tests/fm-wake-drain-open-decisions.test.sh:57 - reserved decision keys owned by firstmate libraries (pending-reply-*)

	// fm: tests/fm-wake-drain-open-decisions.test.sh:88 (no story file: kind unknown, so done: supersedes nothing)
	t.Run("FM/fm-wake-drain-open-decisions/later_unrelated_terminal_line_does_not_close_it", func(t *testing.T) {
		epic := newEpic(t)
		ask(t, epic, "pick REST or RPC")
		_, err := report.Report(epic, story, 1, report.KindDone, "done: unrelated", nil)
		must(t, err)
		ws := drain(t, epic)
		must(t, wake.AckThrough(epic, ws[len(ws)-1].Gen))
		if ids, _ := question.List(epic, story); len(ids) != 1 {
			red(t, "question.open-set", "a later done report closed the open question: %v", ids)
		}
		if !openQuestionOnDrain(t, epic, "pick REST or RPC") {
			red(t, "wake.drain-open-decisions", "the still-open question did not print on the drain after a later terminal report")
		}
		statusLine(t, epic, "task3", "needs-decision [key=api-shape]: pick REST or RPC")
		statusLine(t, epic, "task3", "done: unrelated later milestone")
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "task3 [key=api-shape] needs-decision: pick REST or RPC", "a later unrelated terminal line incorrectly cleared the open decision")
	})

	// fm: tests/fm-wake-drain-open-decisions.test.sh:105
	t.Run("FM/fm-wake-drain-open-decisions/no_open_decisions_prints_nothing", func(t *testing.T) {
		epic := newEpic(t)
		if out, _, _ := cox(t, "wake", "drain", "--epic", epic); strings.TrimSpace(out) != "" {
			red(t, "wake.drain", "no open questions and an empty queue printed %q", out)
		}
	})

	// fm: tests/fm-wake-drain-open-decisions.test.sh:122
	t.Run("FM/fm-wake-drain-open-decisions/open_decision_surfaces_even_with_an_unrelated_queued_wake", func(t *testing.T) {
		epic := newEpic(t)
		ask(t, epic, "pick REST or RPC")
		must(t, wake.AckThrough(epic, drain(t, epic)[0].Gen))
		appendN(t, epic, wake.KindStatus, "unrelated")
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "unrelated", "the unrelated queued wake was not presented")
		wantLine(t, out, "pick REST or RPC", "the open-question section is not epic-wide")
	})

	// fm: tests/fm-wake-drain-open-decisions.test.sh:144
	t.Run("FM/fm-wake-drain-open-decisions/buried_decision_surfaces_on_the_empty_queue_fast_path", func(t *testing.T) {
		epic := newEpic(t)
		ask(t, epic, "pick REST or RPC")
		must(t, wake.AckThrough(epic, drain(t, epic)[0].Gen))
		if len(drain(t, epic)) != 0 {
			t.Fatal("setup: queue not empty")
		}
		if !openQuestionOnDrain(t, epic, "pick REST or RPC") {
			red(t, "wake.drain-open-decisions", "an open question did not print when the wake queue is empty")
		}
	})

	// n/a status_symlink_is_not_followed fm:tests/fm-wake-drain-open-decisions.test.sh:161 - fleet-wide status-file scan, firstmate-only

	// fm: tests/fm-wake-drain-open-decisions.test.sh:186 (the item line is cut to 219 characters with the shared
	// " [truncated]" marker, its lede intact; a short item is untouched)
	t.Run("FM/fm-wake-drain-open-decisions/over_long_decision_note_is_capped_with_a_marker", func(t *testing.T) {
		epic := newEpic(t)
		statusLine(t, epic, "task-long", "needs-decision [key=api-shape]: pick REST or RPC"+strings.Repeat(" and-then-some", 200))
		id := ask(t, epic, "pick REST or RPC"+strings.Repeat(" and-then-some", 200))
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		for _, lede := range []string{"task-long [key=api-shape] needs-decision: pick REST or RPC", "s1 [key=" + id + "] needs-decision: pick REST or RPC"} {
			line := lineStarting(out, lede)
			if line == "" || !strings.HasSuffix(line, " [truncated]") || len([]rune(line)) > 219 {
				red(t, "wake.drain", "an over-long decision note was not capped with its lede intact (%d chars): %q", len([]rune(line)), line)
			}
		}
		statusLine(t, epic, "task-short", "needs-decision [key=short]: brief enough to keep whole")
		out, _, _ = cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "task-short [key=short] needs-decision: brief enough to keep whole", "a decision note already under the cap was altered")
		if strings.Contains(out, "brief enough to keep whole [truncated]") {
			red(t, "wake.drain", "a decision note already under the cap was marked truncated")
		}
	})
}

func TestPortWakeDrainOpenDecisionsCursor(t *testing.T) {
	// fm: tests/fm-wake-drain-open-decisions-cursor.test.sh:42
	t.Run("FM/fm-wake-drain-open-decisions-cursor/buried_decision_survives_many_growing_drains_and_resolution_clears_it", func(t *testing.T) {
		epic := newEpic(t)
		id := ask(t, epic, "pick REST or RPC")
		for i := 0; i < 20; i++ {
			_, err := report.Report(epic, story, 1, report.KindStatus, fmt.Sprintf("working: filler %d", i), nil)
			must(t, err)
			ws := drain(t, epic)
			must(t, wake.AckThrough(epic, ws[len(ws)-1].Gen))
			if !openQuestionOnDrain(t, epic, "pick REST or RPC") {
				red(t, "wake.drain-open-decisions", "a buried open question was lost after growing drain %d", i)
				break
			}
		}
		if _, err := question.Answer(epic, story, id, "REST", false); err != nil {
			t.Fatal(err)
		}
		if openQuestionOnDrain(t, epic, "OPEN DECISIONS") {
			red(t, "wake.drain-open-decisions", "the resolution did not clear the buried question")
		}
	})

	// n/a truncated_log_falls_back_to_a_full_refold_not_a_dropped_decision fm:tests/fm-wake-drain-open-decisions-cursor.test.sh:113 - status-log rewrite and fold cursor, firstmate-only
	// n/a same_size_rewrite_is_detected_via_inode_identity fm:tests/fm-wake-drain-open-decisions-cursor.test.sh:146 - status-log inode rotation, firstmate-only

	// fm: tests/fm-wake-drain-open-decisions-cursor.test.sh:187 (a failed presentation read keeps the cursor for retry)
	t.Run("FM/fm-wake-drain-open-decisions-cursor/read_failure_preserves_state_for_retry", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads mode-000 files (environment)")
		}
		epic := newEpic(t)
		appendN(t, epic, wake.KindStuck, "needs-decision: retry me")
		must(t, os.Chmod(queuePath(epic), 0o000))
		_, _ = wake.Drain(epic, false)
		must(t, os.Chmod(queuePath(epic), 0o644))
		if n, _ := wake.Acked(epic); n != 0 {
			red(t, "wake.ack", "a failed read moved the cursor to %d", n)
		}
		if ws := drain(t, epic); len(ws) != 1 {
			red(t, "wake.drain", "the retry did not present the row: %v", notes(ws))
		}
	})

	// n/a cursor_cache_read_failure_refolds_without_replaying_unread_status fm:tests/fm-wake-drain-open-decisions-cursor.test.sh:224 - fold cache over status files, firstmate-only
	// n/a pre_fix_cursor_refolds_corr_tagged_decision fm:tests/fm-wake-drain-open-decisions-cursor.test.sh:276 - fold cache versioning, firstmate-only
	// n/a previous_fold_cache_is_refolded_under_current_semantics fm:tests/fm-wake-drain-open-decisions-cursor.test.sh:309 - fold cache versioning, firstmate-only

	// fm: tests/fm-wake-drain-open-decisions-cursor.test.sh:350 (a ship/scout terminal report supersedes the story's open
	// decisions; a secondmate's does not; a reopening after it surfaces again. The cache-migration pass is n/a: cox
	// keeps no fold cache)
	t.Run("FM/fm-wake-drain-open-decisions-cursor/terminal_supersession_reaches_cached_drains", func(t *testing.T) {
		for _, kind := range []string{"scout", "ship", "secondmate"} {
			for _, terminal := range []string{"done", "failed"} {
				epic := newEpic(t)
				storyFile(t, epic, "task", kind)
				statusLine(t, epic, "task", "blocked [key=access]: waiting")
				out, _, _ := cox(t, "wake", "drain", "--epic", epic)
				wantLine(t, out, "task [key=access] blocked: waiting", "initial blocker must surface")
				statusLine(t, epic, "task", terminal+": report saved")
				statusLine(t, epic, "task", "note: cleanup complete")
				out, _, _ = cox(t, "wake", "drain", "--epic", epic)
				hist := historyOf(t, epic, "task")
				ev, _ := decision.Actionable(hist.Lines, hist.Kind, "")
				span := strings.Join(ev, "\n")
				if kind == "secondmate" {
					wantLine(t, out, "task [key=access] blocked: waiting", "secondmate blocker must survive "+terminal)
					wantLine(t, span, "blocked [key=access]: waiting", "secondmate opening must remain actionable")
				} else {
					if strings.Contains(out, "OPEN DECISIONS") {
						red(t, "wake.drain-open-decisions", "%s pre-terminal blocker resurfaced after %s: %s", kind, terminal, out)
					}
					if strings.Contains(span, "waiting") {
						red(t, "wake.drain-open-decisions", "%s superseded opening remained actionable in a captured span", kind)
					}
				}
				for _, l := range []string{"blocked [key=access]: reopened", "needs-decision [key=new]: a new decision", "note: more cleanup"} {
					statusLine(t, epic, "task", l)
				}
				out, _, _ = cox(t, "wake", "drain", "--epic", epic)
				wantLine(t, out, "task [key=access] blocked: reopened", "post-terminal reopening must surface")
				wantLine(t, out, "task [key=new] needs-decision: a new decision", "post-terminal new key must surface")
				for _, l := range []string{"resolved [key=access]: answered", "resolved [key=new]: answered", "note: final cleanup"} {
					statusLine(t, epic, "task", l)
				}
				if out, _, _ = cox(t, "wake", "drain", "--epic", epic); strings.Contains(out, "OPEN DECISIONS") {
					red(t, "wake.drain-open-decisions", "matching resolutions did not close reopened decisions: %s", out)
				}
			}
		}
		// The cox question channel under the same rule: a ship story's done report supersedes its open question.
		epic := newEpic(t)
		storyFile(t, epic, story, "ship")
		ask(t, epic, "pick REST or RPC")
		_, err := report.Report(epic, story, 1, report.KindDone, "done: shipped with REST", nil)
		must(t, err)
		ws := drain(t, epic)
		must(t, wake.AckThrough(epic, ws[len(ws)-1].Gen))
		if openQuestionOnDrain(t, epic, "pick REST or RPC") {
			red(t, "wake.drain-open-decisions", "a ship story's terminal report did not supersede its open question")
		}
	})

	// n/a kind_changes_invalidate_folded_decisions fm:tests/fm-wake-drain-open-decisions-cursor.test.sh:397 - task-kind evidence cache, firstmate-only
}

func TestPortWakeDrainOutcomeBackstop(t *testing.T) {
	const mech = "wake.outcome-backstop"
	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:32 (a captain-facing latest status event whose wake was never
	// handled - its row lost, or acknowledged past without being named - resurfaces on the next drain)
	t.Run("FM/fm-wake-drain-outcome-backstop/uncovered_keyless_captain_events_surface_on_the_next_main_drain", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, story, 1, report.KindDone, "done: PR 900", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g+5)) // an acknowledgement past what was handled
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		if !strings.Contains(backstopBody(out), "s1 done: PR 900") {
			red(t, mech, "a done report acknowledged past without handling did not resurface: %s", out)
		}
		statusLine(t, epic, "done-task", "done: PR https://example.test/3346 checks green")
		statusLine(t, epic, "blocked-task", "blocked: release credential unavailable")
		statusLine(t, epic, "decision-task", "needs-decision: choose REST or RPC")
		out, _, _ = cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, backstopBody(out), "done-task done: PR https://example.test/3346 checks green", "keyless done event did not surface in the backstop")
		wantLine(t, out, "blocked-task blocked: release credential unavailable", "keyless blocked event did not surface through OPEN DECISIONS")
		wantLine(t, out, "decision-task needs-decision: choose REST or RPC", "keyless needs-decision event did not surface through OPEN DECISIONS")
	})

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:59 (cox: a handled wake is the covering outcome)
	t.Run("FM/fm-wake-drain-outcome-backstop/newer_task_outcome_and_routine_latest_events_stay_silent", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, "covered", 1, report.KindDone, "done: already delivered completion", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g))
		statusLine(t, epic, "working", "working: rebased onto merged #76")
		statusLine(t, epic, "paused", "paused: waiting for the scheduled release window")
		if out, _, _ := cox(t, "wake", "drain", "--epic", epic); out != "" {
			red(t, mech, "covered and routine latest events broke the silent drain contract: %q", out)
		}
	})

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:81
	t.Run("FM/fm-wake-drain-outcome-backstop/older_or_other_task_outcome_cannot_hide_a_new_captain_event", func(t *testing.T) {
		epic := newEpic(t)
		for _, s := range []string{"same-task", "unrelated-task"} {
			g, err := report.Report(epic, s, 1, report.KindDone, "done: "+s+" completion", nil)
			must(t, err)
			must(t, wake.AckThrough(epic, g))
		}
		statusLine(t, epic, "same-task", "failed: a later attempt failed")
		statusLine(t, epic, "no-task-outcome", "PR ready for review")
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		body := backstopBody(out)
		wantLine(t, body, "same-task failed: a later attempt failed", "an older same-task outcome hid a later failure")
		wantLine(t, body, "no-task-outcome PR ready for review", "another task's newer outcome hid a captain-facing event")
	})

	// n/a branch_annotation_cannot_consume_the_main_resurfacing_backstop fm:tests/fm-wake-drain-outcome-backstop.test.sh:103 - Pi supervision-branch actor, firstmate-only

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:141 (every event here lands in the same second; the history
	// position, not the timestamp, separates handled from later)
	t.Run("FM/fm-wake-drain-outcome-backstop/same_second_outcome_uses_status_causal_position", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, "same-second", 1, report.KindDone, "done: first completion", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g))
		if out, _, _ := cox(t, "wake", "drain", "--epic", epic); out != "" {
			red(t, mech, "a same-second handled status was re-presented: %q", out)
		}
		statusLine(t, epic, "same-second", "failed: genuinely later same-second event")
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "same-second failed: genuinely later same-second event", "a later same-second status was hidden by the older outcome")
	})

	// n/a drain_does_not_scan_append_only_outcome_history fm:tests/fm-wake-drain-outcome-backstop.test.sh:168 - cost bound of firstmate's append-only outcome-history store

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:189
	t.Run("FM/fm-wake-drain-outcome-backstop/successful_backstop_is_idempotent_without_consuming_delayed_annotation", func(t *testing.T) {
		epic := newEpic(t)
		statusLine(t, epic, "receipt-task", "done: keyless completion awaiting recovery")
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "receipt-task done: keyless completion awaiting recovery", "first drain did not surface the keyless completion")
		if out, _, _ := cox(t, "wake", "drain", "--epic", epic); out != "" {
			red(t, mech, "a successful backstop presentation repeated unchanged: %q", out)
		}
		_, err := wake.Append(epic, wake.Wake{Epic: filepath.Base(epic), Story: "receipt-task", Kind: wake.KindWorkerDone, Note: "done: keyless completion awaiting recovery"})
		must(t, err)
		out, _, _ = cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "worker_done receipt-task: done: keyless completion awaiting recovery", "the backstop receipt consumed the delayed wake")
	})

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:217 (the output consumer fails: wake.Present's writer)
	t.Run("FM/fm-wake-drain-outcome-backstop/output_failure_does_not_commit_the_backstop_receipt", func(t *testing.T) {
		epic := newEpic(t)
		statusLine(t, epic, "output-task", "done: retry after the output consumer fails")
		if err := wake.Present(epic, failWriter{}, io.Discard, wake.PresentOptions{}); err == nil {
			red(t, mech, "a failed output consumer was reported as a successful presentation")
		}
		var retry strings.Builder
		must(t, wake.Present(epic, &retry, io.Discard, wake.PresentOptions{}))
		wantLine(t, retry.String(), "output-task done: retry after the output consumer fails", "the output failure consumed the backstop receipt")
	})

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:248 (the receipt's atomic write cannot land)
	t.Run("FM/fm-wake-drain-outcome-backstop/receipt_commit_failure_repeats_the_already_presented_backstop", func(t *testing.T) {
		epic := newEpic(t)
		statusLine(t, epic, "atomic-task", "done: presentation precedes its durable receipt")
		blocker := filepath.Join(epic, wake.ControlDir, fmt.Sprintf("wake.backstop.tmp.%d", os.Getpid()))
		must(t, os.MkdirAll(blocker, 0o755))
		var first, retry, final strings.Builder
		must(t, wake.Present(epic, &first, io.Discard, wake.PresentOptions{}))
		wantLine(t, first.String(), "atomic-task done: presentation precedes its durable receipt", "receipt failure prevented the prepared backstop presentation")
		must(t, os.Remove(blocker))
		must(t, wake.Present(epic, &retry, io.Discard, wake.PresentOptions{}))
		wantLine(t, retry.String(), "atomic-task done: presentation precedes its durable receipt", "the uncommitted backstop did not retry after storage recovered")
		must(t, wake.Present(epic, &final, io.Discard, wake.PresentOptions{}))
		if final.String() != "" {
			red(t, mech, "the successfully committed retry repeated: %q", final.String())
		}
	})

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:284
	t.Run("FM/fm-wake-drain-outcome-backstop/rejected_decision_line_surfaces_once_through_backstop", func(t *testing.T) {
		epic := newEpic(t)
		statusLine(t, epic, "rejected", "blocked [key=bad/value]: credential missing")
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "rejected blocked [key=bad/value]: credential missing", "captain-facing rejected decision was lost")
		if strings.Contains(out, "OPEN DECISIONS") {
			red(t, mech, "malformed decision key entered the open-decision fold: %s", out)
		}
		if out, _, _ := cox(t, "wake", "drain", "--epic", epic); out != "" {
			red(t, mech, "rejected decision backstop repeated unchanged: %q", out)
		}
	})

	// n/a missing_index_self_heals_on_first_drain fm:tests/fm-wake-drain-outcome-backstop.test.sh:306 - outcome index store, firstmate-only

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:331 (a fresh epic: status history, no receipts)
	t.Run("FM/fm-wake-drain-outcome-backstop/uncovered_event_surfaces_on_first_drain_without_index", func(t *testing.T) {
		epic := newEpic(t)
		statusLine(t, epic, "fresh", "done: uncovered completion with no index")
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		wantLine(t, out, "STATUS OUTCOME BACKSTOP (", "first drain skipped the backstop")
		wantLine(t, backstopBody(out), "fresh done: uncovered completion with no index", "uncovered status did not surface on the first drain")
	})

	// n/a malformed_outcome_store_fails_closed_without_pi_advice fm:tests/fm-wake-drain-outcome-backstop.test.sh:358 - outcome store and Pi restart advice, firstmate-only
	// n/a held_lock_mode_rejects_an_unlocked_caller fm:tests/fm-wake-drain-outcome-backstop.test.sh:383 - bash process-tree lock inheritance
	// n/a held_lock_mode_accepts_a_lock_owner_descendant fm:tests/fm-wake-drain-outcome-backstop.test.sh:402 - bash process-tree lock inheritance
	// n/a index_self_heal_runs_under_the_outcome_lock fm:tests/fm-wake-drain-outcome-backstop.test.sh:423 - outcome index store, firstmate-only

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:469
	t.Run("FM/fm-wake-drain-outcome-backstop/overbound_routine_event_stays_silent", func(t *testing.T) {
		epic := newEpic(t)
		statusLine(t, epic, "oversized", "working: "+strings.Repeat("x", 70000))
		if out, _, _ := cox(t, "wake", "drain", "--epic", epic); out != "" {
			red(t, mech, "unclassifiable over-bound routine event was presented (%d bytes)", len(out))
		}
	})

	// fm: tests/fm-wake-drain-outcome-backstop.test.sh:483
	t.Run("FM/fm-wake-drain-outcome-backstop/backstop_output_is_bounded", func(t *testing.T) {
		epic := newEpic(t)
		payload := strings.Repeat("0", 300)
		for i := 1; i <= 30; i++ {
			statusLine(t, epic, fmt.Sprintf("task-%d", i), fmt.Sprintf("done: completion-%02d %s", i, payload))
		}
		out, _, _ := cox(t, "wake", "drain", "--epic", epic)
		if !strings.Contains(out, "STATUS OUTCOME BACKSTOP: ") || !strings.Contains(out, "more omitted (byte cap)") {
			red(t, mech, "an over-budget backstop did not report bounded omission: %s", out)
		}
		count, longest := 0, 0
		for _, l := range strings.Split(backstopBody(out), "\n") {
			if strings.HasPrefix(l, "task-") {
				count++
				if n := len([]rune(l)); n > longest {
					longest = n
				}
			}
		}
		if count == 0 || count >= 30 {
			red(t, mech, "backstop byte cap presented an unexpected task count: %d", count)
		}
		if longest > 219 {
			red(t, mech, "a backstop item exceeded its 219-character budget: %d", longest)
		}
	})
}

func TestPortWakeDaemonLifecycle(t *testing.T) {
	// fm: tests/fm-wake-daemon-lifecycle-e2e.test.sh:61 (queue-facing half: a routine report queues, a terminal report
	// written while the watcher is down is caught on restart, exactly once, and a later watcher pass does not duplicate
	// it; the afk daemon's buffer and pane injection are n/a)
	t.Run("FM/fm-wake-daemon-lifecycle-e2e/routine_then_terminal_after_restart", func(t *testing.T) {
		epic := newEpic(t)
		g, err := report.Report(epic, story, 1, report.KindStatus, "working: building", nil)
		must(t, err)
		must(t, wake.AckThrough(epic, g))
		_, err = report.Report(epic, story, 1, report.KindDone, "done: PR https://example.test/pr/900", nil)
		must(t, err)
		w := &watch.Watcher{EpicDir: epic, Backend: fake.New()}
		_, _ = w.Tick()
		_, _ = w.Tick()
		dones := 0
		for _, wk := range drain(t, epic) {
			if wk.Kind == wake.KindWorkerDone {
				dones++
			}
		}
		if dones != 1 {
			red(t, "wake.queue", "terminal report across a watcher restart surfaced %d times, want exactly once", dones)
		}
	})

	// n/a stale_pane_transient_persistent_resume fm:tests/fm-wake-daemon-lifecycle-e2e.test.sh:122 - tmux pane-hash staleness and the afk daemon's escalation buffer, firstmate-only
}

// failSend is a backend whose leader doorbell never lands (a dead leader terminal).
type failSend struct{ *fake.Backend }

func (failSend) Send(backend.Session, string) (bool, error) {
	return false, errors.New("terminal gone")
}

type alarm struct{ channel, summary string }

// wedgeRig ticks a watcher whose leader doorbell always fails over an urgent backlog, recording every alarm.
func wedgeRig(t *testing.T, channel string, ticks int) (string, []alarm) {
	t.Helper()
	epic := newEpic(t)
	appendN(t, epic, wake.KindStuck, "urgent backlog")
	clock := time.Now()
	var got []alarm
	w := &watch.Watcher{EpicDir: epic, Backend: failSend{fake.New()}, AlarmChannel: channel,
		AlarmRun: func(ch, s string) error { got = append(got, alarm{ch, s}); return nil },
		Now:      func() time.Time { return clock }}
	for i := 0; i < ticks; i++ {
		_, _ = w.Tick()
		clock = clock.Add(watch.DefaultNudgeWindow + time.Second)
	}
	return epic, got
}

func TestPortWedgeAlarm(t *testing.T) {
	// fm: docs/wedge-alarm.md:4 (delivery to the primary unconfirmed past a bound raises one out-of-band alarm)
	t.Run("FM/wedge-alarm/unconfirmed_delivery_past_bound_raises_alarm", func(t *testing.T) {
		_, got := wedgeRig(t, "command:true", watch.DoorbellFailAlarm)
		if len(got) != 1 || !strings.Contains(got[0].summary, "leader doorbell failed") {
			red(t, "watch.leader-alarm", "after %d failed leader doorbells the alarm fired %d times: %+v", watch.DoorbellFailAlarm, len(got), got)
		}
	})

	// fm: docs/wedge-alarm.md:11 (every listed non-off channel fires, best-effort)
	t.Run("FM/wedge-alarm/every_listed_channel_fires", func(t *testing.T) {
		_, got := wedgeRig(t, "osascript\ncommand:true", watch.DoorbellFailAlarm)
		if len(got) != 2 {
			red(t, "watch.leader-alarm-channels", "a two-channel list fired %d alarms (%+v); cox's alerts.channel is a single directive", len(got), got)
		}
	})

	// n/a env_override_single_directive fm:docs/wedge-alarm.md:12 - FM_WEDGE_ALARM_CHANNEL test override; cox's test seam is Watcher.AlarmRun (case test_seam_records_channel_and_summary)

	// fm: docs/wedge-alarm.md:14 (off silences the active alert and keeps the durable marker)
	t.Run("FM/wedge-alarm/off_disables_active_alerts_retaining_durable_marker", func(t *testing.T) {
		epic, got := wedgeRig(t, "off", watch.DoorbellFailAlarm)
		if len(got) != 0 {
			red(t, "watch.leader-alarm", "off still fired %d alarms", len(got))
		}
		marker := false
		for _, wk := range drain(t, epic) {
			marker = marker || (wk.Story == "_leader" && wk.Kind == wake.KindStuck)
		}
		if !marker {
			red(t, "watch.leader-alarm", "off dropped the durable _leader stuck wake")
		}
	})

	// fm: docs/wedge-alarm.md:15
	t.Run("FM/wedge-alarm/auto_resolves_to_osascript_on_macos", func(t *testing.T) {
		_, got := wedgeRig(t, "auto", watch.DoorbellFailAlarm)
		if len(got) != 1 || got[0].channel != "osascript" {
			red(t, "watch.leader-alarm-channels", "auto resolved to %+v, want one osascript alarm on macOS", got)
		}
	})

	// n/a other_platforms_need_command fm:docs/wedge-alarm.md:16 - guidance for non-macOS hosts, not a behaviour

	// fm: docs/wedge-alarm.md:17 (the osascript channel posts through osascript, outside any terminal pane)
	t.Run("FM/wedge-alarm/osascript_posts_outside_the_pane", func(t *testing.T) {
		argv := fakeOsascript(t)
		_, _ = wedgeRigReal(t, "osascript")
		if b, _ := os.ReadFile(argv); len(b) == 0 {
			red(t, "watch.leader-alarm", "the osascript channel never invoked osascript")
		}
	})

	// n/a herdr_notification_channel fm:docs/wedge-alarm.md:18 - herdr is firstmate-only (DESIGN rule 5)

	// fm: docs/wedge-alarm.md:19
	t.Run("FM/wedge-alarm/command_receives_summary_as_arg_and_stdin", func(t *testing.T) {
		dir := t.TempDir()
		ch := fmt.Sprintf(`command:printf '%%s' "$1" > %s/arg; cat > %s/stdin`, dir, dir)
		_, _ = wedgeRigReal(t, ch)
		arg, _ := os.ReadFile(filepath.Join(dir, "arg"))
		in, _ := os.ReadFile(filepath.Join(dir, "stdin"))
		if !strings.Contains(string(arg), "leader doorbell failed") || string(arg) != string(in) {
			red(t, "watch.leader-alarm", "command channel got $1=%q stdin=%q", arg, in)
		}
	})

	// fm: docs/wedge-alarm.md:21 (an absent config is auto: default-on, so N doorbell failures alarm out of band)
	t.Run("FM/wedge-alarm/absent_config_is_default_on", func(t *testing.T) {
		if ch := (&workspace.Policy{}).AlertsChannel(); ch == "off" {
			red(t, "watch.leader-alarm-default", "an absent alerts.channel resolves to %q: N failed leader doorbells raise no out-of-band alarm by default", ch)
		}
		_, got := wedgeRig(t, "", watch.DoorbellFailAlarm)
		if len(got) != 1 {
			red(t, "watch.leader-alarm-default", "an unset channel fired %d alarms after %d failed doorbells, want 1", len(got), watch.DoorbellFailAlarm)
		}
	})

	// fm: docs/wedge-alarm.md:22 (rate-limited to at most once per window)
	t.Run("FM/wedge-alarm/rate_limited_once_per_window", func(t *testing.T) {
		_, got := wedgeRig(t, "command:true", watch.DoorbellFailAlarm+3) // 3 more failures inside one alarm window
		if len(got) != 1 {
			red(t, "watch.leader-alarm", "%d alarms inside one window, want 1", len(got))
		}
	})

	// fm: docs/wedge-alarm.md:25 (a failing channel logs a warning and the loop carries on)
	t.Run("FM/wedge-alarm/failing_channel_logs_and_continues", func(t *testing.T) {
		epic := newEpic(t)
		appendN(t, epic, wake.KindStuck, "urgent backlog")
		clock := time.Now()
		w := &watch.Watcher{EpicDir: epic, Backend: failSend{fake.New()}, AlarmChannel: "command:false",
			AlarmRun: func(string, string) error { return errors.New("exit 1") }, Now: func() time.Time { return clock }}
		for i := 0; i < watch.DoorbellFailAlarm+1; i++ {
			if _, err := w.Tick(); err != nil {
				red(t, "watch.leader-alarm", "a failing alarm channel broke the watch loop: %v", err)
			}
			clock = clock.Add(watch.DefaultNudgeWindow + time.Second)
		}
		log, _ := os.ReadFile(filepath.Join(epic, state.ControlDir, "watch", "log"))
		if !strings.Contains(string(log), "alarm:command:false") {
			red(t, "watch.leader-alarm", "the failing channel left no warning in watch/log")
		}
	})

	// fm: docs/wedge-alarm.md:26 (every invocation is bounded by a 10s timeout)
	t.Run("FM/wedge-alarm/invocation_bounded_by_timeout", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		_, _ = wedgeRigReal(t, "command:sleep 30")
		if d := time.Since(start); d > 15*time.Second {
			red(t, "watch.leader-alarm", "the alarm invocation ran %s, past its 10s bound", d)
		}
	})

	// fm: docs/wedge-alarm.md:27 (on timeout the notifier's whole process group is terminated)
	t.Run("FM/wedge-alarm/timeout_terminates_the_process_group", func(t *testing.T) {
		t.Parallel()
		pidFile := filepath.Join(t.TempDir(), "pid")
		_, _ = wedgeRigReal(t, fmt.Sprintf("command:sleep 30 & echo $! > %s; wait", pidFile))
		b, _ := os.ReadFile(pidFile)
		var pid int
		fmt.Sscan(strings.TrimSpace(string(b)), &pid)
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			red(t, "watch.leader-alarm", "the timed-out notifier's child (pid %d) outlived the timeout: only the shell was killed, not its process group", pid)
		}
	})

	// fm: docs/wedge-alarm.md:28 (the summary reaches AppleScript as argv, never interpolated into the script)
	t.Run("FM/wedge-alarm/applescript_summary_is_argv", func(t *testing.T) {
		argv := fakeOsascript(t)
		_, _ = wedgeRigReal(t, "osascript")
		b, _ := os.ReadFile(argv)
		args := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		if len(args) == 0 || !strings.Contains(args[len(args)-1], "leader doorbell failed") {
			red(t, "watch.leader-alarm", "the summary was not the last argv item: %q", args)
		}
		for _, a := range args[:len(args)-1] {
			if strings.Contains(a, "leader doorbell failed") {
				red(t, "watch.leader-alarm", "the summary was interpolated into the script source: %q", a)
			}
		}
	})

	// n/a copyable_config_example fm:docs/wedge-alarm.md:29 - pointer to an example file, not a behaviour

	// fm: docs/wedge-alarm.md:33 (every notifier routes through a seam a test replaces with a recorder)
	t.Run("FM/wedge-alarm/test_seam_records_channel_and_summary", func(t *testing.T) {
		_, got := wedgeRig(t, "command:true", watch.DoorbellFailAlarm)
		if len(got) != 1 || got[0].channel != "command:true" || got[0].summary == "" {
			red(t, "watch.leader-alarm", "the AlarmRun seam did not record channel and summary: %+v", got)
		}
	})

	// n/a daemon_library_mode_discards fm:docs/wedge-alarm.md:34 - daemon sourced-as-library default, firstmate-only
	// n/a verification_pointers fm:docs/wedge-alarm.md:38 - pointers to fm-daemon.test.sh and manual verification
}

// wedgeRigReal is wedgeRig with the real channel runner (AlarmRun nil), so the actual osascript/command dispatch runs.
func wedgeRigReal(t *testing.T, channel string) (string, error) {
	t.Helper()
	epic := newEpic(t)
	appendN(t, epic, wake.KindStuck, "urgent backlog")
	clock := time.Now()
	w := &watch.Watcher{EpicDir: epic, Backend: failSend{fake.New()}, AlarmChannel: channel, Now: func() time.Time { return clock }}
	var err error
	for i := 0; i < watch.DoorbellFailAlarm; i++ {
		_, err = w.Tick()
		clock = clock.Add(watch.DefaultNudgeWindow + time.Second)
	}
	return epic, err
}

// fakeOsascript puts an osascript on PATH that records its argv one per line, so no real notification posts.
func fakeOsascript(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	argv := filepath.Join(bin, "argv")
	must(t, os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > "+argv+"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argv
}
