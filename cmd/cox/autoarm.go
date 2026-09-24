package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/watch"
)

// The Claude turn-end contract, ported from firstmate bin/fm-turnend-guard.sh --claude, bin/fm-claude-stop-autoarm.sh
// and the fm_autoarm_* / fm_failure_episode_reset helpers of bin/fm-wake-lib.sh (pinned 1e0e773;
// docs/turnend-guard.md "Harness integrations"). Firstmate runs two Stop hooks - the guard and the asyncRewake
// auto-arm - over one state dir; cox's stop-rewake is both in one process, over the epic's control dir:
//
//	firstmate state/.claude-autoarm-epoch            -> <epic>/.cox/claude-autoarm-epoch   (the generation ledger)
//	firstmate state/.claude-autoarm.lock             -> <epic>/.cox/claude-autoarm.lock    (ledger micro-mutex)
//	firstmate state/.claude-autoarm-failure-notified -> <epic>/.cox/claude-autoarm-failure-notified
//	firstmate state/.claude-autoarm-failure-alarmed  -> <epic>/.cox/claude-autoarm-failure-alarmed
//	firstmate state/.turnend-claude-blocks(.lock)    -> <epic>/.cox/turnend-claude-blocks(.lock)
//	firstmate session_id (Stop payload)              -> the payload session_id, else ORCA_TERMINAL_HANDLE
//	firstmate bin/fm-watch-arm.sh                    -> launchWatcher (restart + confirmed first tick)
//	firstmate "N task(s) in flight"                  -> "N story(ies) in flight"
const (
	autoarmEpochName   = "claude-autoarm-epoch"
	autoarmLockName    = "claude-autoarm.lock"
	failureNoticeName  = "claude-autoarm-failure-notified"
	failureAlarmName   = "claude-autoarm-failure-alarmed"
	turnendBudgetName  = "turnend-claude-blocks"
	turnendBudgetLockN = "turnend-claude-blocks.lock"
)

// Firstmate defaults: FM_CLAUDE_TURNEND_BLOCK_BUDGET 3 (below Claude's 8-block override), FM_CLAUDE_AUTOARM_EPOCH_FRESH
// 15s, FM_CLAUDE_AUTOARM_ATTEMPTS 2.
const (
	autoarmEpochFresh = 15 * time.Second
	autoarmAttempts   = 2
)

// rewakeBlockBudget is the re-block budget (FM_CLAUDE_TURNEND_BLOCK_BUDGET, default 3).
const rewakeBlockBudget = 3

func coxPath(epic, name string) string { return filepath.Join(epic, controlDir, name) }

func beaconPath(epic string) string { return filepath.Join(epic, controlDir, "watch", "lasttick") }

// pathAge is fm_path_age: an absent path reads as ancient.
func pathAge(path string) time.Duration {
	info, err := os.Stat(path)
	if err != nil {
		return 1 << 62
	}
	return time.Since(info.ModTime())
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

// autoarmLedger is one parsed ledger entry (fm_autoarm_ledger_read).
type autoarmLedger struct {
	gen      int
	owner    int
	outcome  string
	identity string // line 2, and only line 2
}

func readLedger(epic string) (autoarmLedger, bool) {
	b, err := os.ReadFile(coxPath(epic, autoarmEpochName))
	if err != nil {
		return autoarmLedger{}, false
	}
	lines := strings.SplitN(string(b), "\n", 3)
	var l autoarmLedger
	var haveGen, haveOwner bool
	for _, tok := range strings.Fields(lines[0]) {
		k, v, _ := strings.Cut(tok, "=")
		switch k {
		case "epoch":
			n, err := strconv.Atoi(v)
			l.gen, haveGen = n, err == nil
		case "owner_pid":
			l.owner, _ = strconv.Atoi(v)
			haveOwner = v != ""
		case "outcome":
			l.outcome = v
		}
	}
	if !haveGen || !haveOwner || l.outcome == "" {
		return autoarmLedger{}, false
	}
	if len(lines) > 1 {
		l.identity = strings.TrimSpace(lines[1])
	}
	return l, true
}

// identityOf is the recomputed pid identity, "" when the process is gone or cannot be identified.
func identityOf(pid int) string {
	id, err := watch.ProcIdentity(pid)
	if err != nil {
		return ""
	}
	return id
}

// stuck is firstmate's stuck proof: both the entry and the watcher beacon are older than the guard grace.
func stuck(epic, entry string) bool {
	return pathAge(entry) >= watch.DefaultGrace && pathAge(beaconPath(epic)) >= watch.DefaultGrace
}

// claimOpen is fm_autoarm_claim_open: the current claim is arming, its owner is alive, its mandatory recorded identity
// recomputes and matches, and it is not stuck.
func claimOpen(epic string) bool {
	l, ok := readLedger(epic)
	if !ok || l.outcome != "arming" || l.owner <= 0 || !processAlive(l.owner) || l.identity == "" {
		return false
	}
	if cur := identityOf(l.owner); cur == "" || cur != l.identity {
		return false
	}
	return !stuck(epic, coxPath(epic, autoarmEpochName))
}

// writeLedger publishes a ledger entry through a temp file and a rename.
func writeLedger(epic string, gen int, outcome, identity string) error {
	line := fmt.Sprintf("epoch=%d owner_pid=%d outcome=%s updated_at=%d\n", gen, os.Getpid(), outcome, time.Now().Unix())
	if identity != "" {
		line += identity + "\n"
	}
	return writeFileAtomic(coxPath(epic, autoarmEpochName), []byte(line))
}

func writeFileAtomic(path string, b []byte) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// claimNext is fm_autoarm_claim_next: publish this process as the owner of generation N+1 under one micro-mutex
// hold. rc 0 = claimed, 2 = a competing claim is open, 1 = contention, no identity, or a failed write.
func claimNext(epic string) (gen, rc int) {
	identity := identityOf(os.Getpid())
	if identity == "" {
		return 0, 1
	}
	lock := coxPath(epic, autoarmLockName)
	if _, err := tryLock(lock, nil); err != nil {
		return 0, 1
	}
	defer releaseLock(lock)
	if claimOpen(epic) {
		return 0, 2
	}
	l, _ := readLedger(epic)
	gen = l.gen + 1
	if err := writeLedger(epic, gen, "arming", identity); err != nil {
		return 0, 1
	}
	return gen, 0
}

// writeOwned is fm_autoarm_write_owned: a new outcome for a generation this process still owns, re-verified under the
// micro-mutex; marker (when set) is created after the ledger write in the same hold. 0 committed, 2 refused, 1 unable.
func writeOwned(epic string, gen int, outcome, marker string) int {
	lock := coxPath(epic, autoarmLockName)
	acquired := false
	for i := 0; i <= 20; i++ {
		if _, err := tryLock(lock, nil); err == nil {
			acquired = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !acquired {
		return 1
	}
	defer releaseLock(lock)
	l, ok := readLedger(epic)
	if !ok || l.gen != gen || l.owner != os.Getpid() {
		return 2
	}
	if err := writeLedger(epic, gen, outcome, l.identity); err != nil {
		return 1
	}
	if marker != "" {
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			return 2
		}
	}
	return 0
}

// stillOwner is fm_autoarm_still_owner.
func stillOwner(epic string, gen int) bool {
	l, ok := readLedger(epic)
	return ok && l.gen == gen && l.owner == os.Getpid()
}

// failureEpisodeReset is fm_failure_episode_reset: clear the block budget, the failure notice and the attended alarm
// together under the budget lock (held = the caller already holds it). Contention or a failed removal preserves every
// file and reports false.
func failureEpisodeReset(epic string, held bool) bool {
	lock := coxPath(epic, turnendBudgetLockN)
	if held {
		if pid, _, _ := lockHolder(lock); pid != os.Getpid() {
			return false
		}
	} else {
		if _, err := tryLock(lock, nil); err != nil {
			return false
		}
		defer releaseLock(lock)
	}
	paths := []string{coxPath(epic, turnendBudgetName), coxPath(epic, failureNoticeName), coxPath(epic, failureAlarmName)}
	for _, p := range paths {
		if info, err := os.Lstat(p); err == nil && info.IsDir() {
			return false
		}
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return true
}

// resetOwned is fm_autoarm_reset_owned: 0 reset, 2 superseded or contended, 1 reset failed.
func resetOwned(epic string, gen int) int {
	lock := coxPath(epic, autoarmLockName)
	if _, err := tryLock(lock, nil); err != nil {
		return 2
	}
	defer releaseLock(lock)
	if !stillOwner(epic, gen) {
		return 2
	}
	if !failureEpisodeReset(epic, false) {
		return 1
	}
	return 0
}

// claimAbandoned is the legacy shim fm_autoarm_claim_abandoned: a lock-holding claim (the owner lock carrying the
// autoarm role) is abandoned when its recorded identity no longer matches its live pid, or when the ledger names that
// pid with a terminal outcome, or with "arming" that is stuck.
func claimAbandoned(epic string) bool {
	lock := coxPath(epic, autoarmLockName)
	pid, role, recorded, ok := lockRecord(lock)
	if !ok || role != "autoarm" || pid <= 0 {
		return false
	}
	if recorded != "" {
		if cur := identityOf(pid); cur != "" && cur != recorded {
			return true
		}
	}
	l, ok := readLedger(epic)
	if !ok || l.owner != pid {
		return false
	}
	if l.outcome == "arming" {
		return stuck(epic, coxPath(epic, autoarmEpochName))
	}
	return true
}

// releaseAbandoned is fm_autoarm_release_abandoned: re-prove abandonment under the steal mutex, retire a live owner
// only when its recorded identity verifies (never a bare or reused pid), then remove the lock.
func releaseAbandoned(epic string) bool {
	lock := coxPath(epic, autoarmLockName)
	if !claimAbandoned(epic) {
		return false
	}
	steal := lock + ".steal"
	if _, err := tryLock(steal, nil); err != nil {
		return false
	}
	defer releaseLock(steal)
	if !claimAbandoned(epic) {
		return false
	}
	pid, _, recorded, _ := lockRecord(lock)
	if recorded != "" && processAlive(pid) && identityOf(pid) == recorded {
		p, err := os.FindProcess(pid)
		if err != nil || p.Signal(syscall.SIGTERM) != nil {
			return false
		}
		for i := 0; i < 20 && processAlive(pid); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	_ = os.Remove(lock)
	return !exists(lock)
}

// claudeTurnend is one epic's --claude guard run (fm-turnend-guard.sh --claude after scope and need).
type claudeTurnend struct {
	epic         string
	session      string
	need         supervisionNeed // FM_SUP_IN_FLIGHT, FM_SUP_SOURCES, FM_SUP_CHECKS
	count        int
	initialized  bool
	chargedEpoch string
	chargedValid bool
}

func (g *claudeTurnend) notice() string { return coxPath(g.epic, failureNoticeName) }
func (g *claudeTurnend) alarm() string  { return coxPath(g.epic, failureAlarmName) }

// account is budget_account_current_epoch [observe|block]: it charges the ledger's epoch identity once per Stop, and a
// re-block against an epoch the previous re-block already charged is charged again (an inert auto-arm freezes the
// ledger; charging only epoch changes would let the guard re-block without limit).
func (g *claudeTurnend) account(block bool) bool {
	lock := coxPath(g.epic, turnendBudgetLockN)
	if _, err := tryLock(lock, nil); err != nil {
		return false
	}
	defer releaseLock(lock)
	current, outcome := "", ""
	if l, ok := readLedger(g.epic); ok {
		current, outcome = strconv.Itoa(l.gen), l.outcome
	}
	path := coxPath(g.epic, turnendBudgetName)
	oldSession, oldCount, oldEpoch, have := readBudget(path)
	initialized, charged := false, false
	g.count = 0
	if have && oldSession == g.session {
		g.count = oldCount
		if current != "" && oldEpoch == current {
			if block && (!g.chargedValid || g.chargedEpoch != current) {
				g.count++
				charged = true
			}
		} else {
			g.count++
			charged = true
		}
	}
	if !have || oldSession != g.session {
		charged = true
		switch outcome {
		case "failed", "failed-suppressed":
			if exists(g.notice()) {
				initialized = true
				g.count = 0
			} else {
				g.count = 1
			}
		default:
			g.count = 1
		}
	}
	if err := writeFileAtomic(path, []byte(fmt.Sprintf("session=%s\ncount=%d\nepoch=%s\n", g.session, g.count, current))); err != nil {
		return false
	}
	if charged {
		g.chargedEpoch, g.chargedValid = current, true
	}
	g.initialized = initialized
	return true
}

// readBudget reads the budget file (session, count, epoch lines).
func readBudget(path string) (session string, count int, epoch string, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "session":
			session = v
		case "count":
			count, _ = strconv.Atoi(v)
		case "epoch":
			epoch = v
		}
	}
	return session, count, epoch, true
}

// ownsRecovery is autoarm_owns_recovery: a healthy watcher, an open generation claim, a live legacy lock-holding
// owner, a fresh rewake, or a fresh first failed epoch owns this Stop's recovery.
func (g *claudeTurnend) ownsRecovery() bool {
	if watcherHealthy(g.epic, time.Now()) {
		return true
	}
	observe := func() {
		if exists(g.notice()) {
			g.account(false)
		}
	}
	if claimOpen(g.epic) {
		observe()
		return true
	}
	if pid, role, _ := lockHolder(coxPath(g.epic, autoarmLockName)); pid > 0 && processAlive(pid) && role == "autoarm" && !claimAbandoned(g.epic) {
		observe()
		return true
	}
	l, _ := readLedger(g.epic)
	fresh := pathAge(coxPath(g.epic, autoarmEpochName)) < autoarmEpochFresh
	switch l.outcome {
	case "rewake":
		if fresh {
			observe()
			return true
		}
	case "failed":
		if fresh && exists(g.notice()) && g.account(false) && g.initialized {
			return true
		}
	case "failed-suppressed":
		if fresh && exists(g.notice()) {
			g.account(false)
		}
	}
	return false
}

// verified is failure_episode_verified: the auto-arm recorded an exhausted failure and its one notice.
func (g *claudeTurnend) verified() bool {
	if !exists(g.notice()) {
		return false
	}
	l, _ := readLedger(g.epic)
	return l.outcome == "failed" || l.outcome == "failed-suppressed"
}

// terminalFailOpen is terminal_fail_open: 0 = take the one loud attended fail-open, 1 = keep blocking, 2 = step aside
// for a concurrent recovery decision. The owner lock is taken before the budget lock.
func (g *claudeTurnend) terminalFailOpen() int {
	if g.count <= rewakeBlockBudget || !g.verified() || exists(g.alarm()) {
		return 1
	}
	if claimOpen(g.epic) {
		return 2
	}
	owner := coxPath(g.epic, autoarmLockName)
	if _, err := tryLock(owner, nil); err != nil {
		pid, role, _ := lockHolder(owner)
		if pid > 0 && processAlive(pid) && role == "autoarm" && !claimAbandoned(g.epic) {
			return 2
		}
		if !releaseAbandoned(g.epic) {
			return 1
		}
		if _, err := tryLock(owner, nil); err != nil {
			return 1
		}
	}
	defer releaseLock(owner)
	if !setLockRole(owner, "terminal-check") {
		return 1
	}
	budgetLock := coxPath(g.epic, turnendBudgetLockN)
	if _, err := tryLock(budgetLock, nil); err != nil {
		return 1
	}
	defer releaseLock(budgetLock)
	session, count, _, _ := readBudget(coxPath(g.epic, turnendBudgetName))
	if _, role, _ := lockHolder(owner); role != "terminal-check" || session != g.session || count <= rewakeBlockBudget || !g.verified() || exists(g.alarm()) {
		return 1
	}
	if watcherHealthy(g.epic, time.Now()) {
		if !failureEpisodeReset(g.epic, true) {
			return 1
		}
		return 2
	}
	if claimOpen(g.epic) {
		return 2
	}
	f, err := os.OpenFile(g.alarm(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 1
	}
	f.Close()
	return 0
}

// needDesc is the NEED_DESC / banner need line.
func (g *claudeTurnend) needDesc() string { return g.need.desc() }

// blockText is block_stop's banner: firstmate's text with cox's repair line (name map).
func blockText(epic string, need supervisionNeed, claude bool) string {
	const rule = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
	var b strings.Builder
	fmt.Fprintf(&b, "●%s\n", rule)
	b.WriteString("●  TURN WOULD END BLIND - SUPERVISION IS OFF\n")
	fmt.Fprintf(&b, "●  %s, but no live watcher holds this epic's lock (last beat: %s).\n", need.desc(), beaconDesc(epic))
	if claude {
		b.WriteString("●  The Stop-owned auto-arm did not claim this epic either, so recovery is NOT already under way.\n")
	}
	fmt.Fprintf(&b, "●  Watcher for %s is not alive; run: cox watch --epic %s --replace\n", filepath.Base(epic), epic)
	fmt.Fprintf(&b, "●%s\n", rule)
	return b.String()
}

// beaconDesc is FM_SUP_BEACON_DESC: "never" when absent, else "<age>s ago".
func beaconDesc(epic string) string {
	info, err := os.Stat(beaconPath(epic))
	if err != nil {
		return "never"
	}
	return fmt.Sprintf("%ds ago", int(time.Since(info.ModTime()).Seconds()))
}

// failOpenMessage is the one attended fail-open's systemMessage.
func failOpenMessage(need string) string {
	return fmt.Sprintf(`{"systemMessage":"COX SUPERVISION IS GENUINELY DOWN: %s, the Stop-owned auto-arm exhausted its bounded retries and one failure notice, no watcher or automatic continuation exists, and the block budget is exhausted. Keep this session attended and diagnose the automatic Stop-hook and watcher startup before relying on unattended supervision."}`+"\n", need)
}

// runClaudeGuard is fm-turnend-guard.sh --claude for one epic that needs supervision. It writes the block banner to
// errw and a systemMessage to outw, and returns the Stop exit code (0 allow, 2 block).
func runClaudeGuard(epic, session string, need supervisionNeed, errw, outw io.Writer) int {
	g := &claudeTurnend{epic: epic, session: session, need: need}
	if watcherHealthy(epic, time.Now()) {
		if failureEpisodeReset(epic, false) {
			return 0
		}
		return 2 // reset contention: continue silently; every episode file is preserved for the retry
	}
	if g.ownsRecovery() {
		if watcherHealthy(epic, time.Now()) && !failureEpisodeReset(epic, false) {
			return 2
		}
		return 0
	}
	if !g.account(true) {
		fmt.Fprint(errw, blockText(epic, need, true))
		return 2
	}
	switch g.terminalFailOpen() {
	case 0:
		fmt.Fprint(outw, failOpenMessage(g.needDesc()))
		return 0
	case 2:
		return 0
	}
	fmt.Fprint(errw, blockText(epic, need, true))
	return 2
}

// runClaudeAutoarm is fm-claude-stop-autoarm.sh for one epic that needs supervision: take the next generation unless
// one is open, arm (launch = the watcher restart) a bounded number of times, and translate the outcome. It returns
// the exit code the auto-arm contributes (2 = a continuation: the failure notice or a retry) and whether it verified a
// healthy watcher.
func runClaudeAutoarm(epic string, launch func(string) error, errw io.Writer) (code int, healthy bool) {
	if claimOpen(epic) {
		return 0, false
	}
	gen, rc := claimNext(epic)
	if rc != 0 {
		if rc == 2 {
			return 0, false
		}
		if _, role, _ := lockHolder(coxPath(epic, autoarmLockName)); role == "" || !releaseAbandoned(epic) {
			return 0, false
		}
		if gen, rc = claimNext(epic); rc != 0 {
			return 0, false
		}
	}
	var lastErr error
	attempt := 0
	for attempt < autoarmAttempts {
		if !stillOwner(epic, gen) {
			return 0, false
		}
		attempt++
		lastErr = launch(epic)
		// A launch confirms its own watcher; a failed one is still benign when another verified watcher owns the epic.
		if lastErr == nil || watcherHealthy(epic, time.Now()) {
			healthy = true
			break
		}
	}
	notice, alarm := coxPath(epic, failureNoticeName), coxPath(epic, failureAlarmName)
	if healthy {
		switch resetOwned(epic, gen) {
		case 0:
			writeOwned(epic, gen, "clean", "")
			return 0, true
		case 2:
			return 0, true
		}
		if writeOwned(epic, gen, "failed-suppressed", "") == 0 && !exists(alarm) {
			return 2, true
		}
		return 0, true
	}
	if exists(alarm) {
		writeOwned(epic, gen, "failed-suppressed", "")
		return 0, false
	}
	if !exists(notice) {
		if !stillOwner(epic, gen) {
			return 0, false
		}
		var b strings.Builder
		fmt.Fprintf(&b, "cox watcher auto-arm FAILED - the Stop-owned automatic supervision mechanism is broken after %d bounded attempts, and no live watcher with a fresh beacon was verified.\n", attempt)
		if lastErr != nil {
			fmt.Fprintf(&b, "watcher: %v\n", lastErr)
		}
		// Firstmate's notice forbids a manual background arm (it would race the Stop-owned one); cox's remedy is the
		// foreground `cox watch --replace`, which shows why the automatic restart fails.
		fmt.Fprintf(&b, "Watcher for %s is not alive; run: cox watch --epic %s --replace to see why the automatic restart fails, and investigate the Stop hook and watcher startup before ending blind.\n", filepath.Base(epic), epic)
		if writeOwned(epic, gen, "failed", notice) == 0 {
			fmt.Fprint(errw, b.String())
			return 2, false
		}
		return 0, false
	}
	if writeOwned(epic, gen, "failed-suppressed", "") == 0 {
		return 2, false
	}
	return 0, false
}
