package watch

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/state"
)

// fakeProc writes a /proc/<pid>/{stat,cmdline} pair whose comm field holds spaces and a paren (firstmate
// fm-watcher-lock.test.sh write_fake_proc_identity).
func fakeProc(t *testing.T, root string, pid int, start string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (watcher ) with spaces) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 " + start + " 20 21 22\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("bash\x00/path with spaces/fm-watch.sh\x00--flag\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fm: tests/fm-watcher-lock.test.sh:1100@a8572f6 (R12)
func TestProcIdentityProcIgnoresWallClockAndDetectsReuse(t *testing.T) {
	root := t.TempDir()
	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("btime 1784094040\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeProc(t, root, 4242, "987654")
	before, err := ProcIdentity(4242)
	if err != nil {
		t.Fatal(err)
	}
	key := "proc-starttime"
	if runtime.GOOS == "linux" {
		key = "linux-starttime"
	}
	want := key + "=987654 cmdline-hex=62617368002f706174682077697468207370616365732f666d2d77617463682e7368002d2d666c616700"
	if before != want {
		t.Fatalf("identity = %q, want %q", before, want)
	}
	_ = os.WriteFile(filepath.Join(root, "stat"), []byte("btime 1784094016\n"), 0o644) // a wall-clock step
	if after, _ := ProcIdentity(4242); after != before {
		t.Fatalf("identity changed with btime: %q -> %q", before, after)
	}
	fakeProc(t, root, 4242, "987655") // the pid reused by a new process
	if reused, _ := ProcIdentity(4242); reused == before {
		t.Fatal("identity missed a reused pid")
	}
}

// fm: tests/fm-watcher-lock.test.sh:1005@a8572f6 (R12): the ps fallback pins LC_ALL=C whatever the caller's locale.
func TestProcIdentityPsFallbackIsLocaleInvariant(t *testing.T) {
	oldRoot, oldRun := procRoot, psRun
	procRoot = filepath.Join(t.TempDir(), "no-proc")
	defer func() { procRoot, psRun = oldRoot, oldRun }()
	var seen []string
	psRun = func(env []string, args ...string) ([]byte, error) {
		lc := "<unset>"
		for _, kv := range env {
			if strings.HasPrefix(kv, "LC_ALL=") {
				lc = strings.TrimPrefix(kv, "LC_ALL=") // the last assignment wins, as in exec
			}
		}
		seen = append(seen, lc)
		return []byte("   Mon Jul 28 20:00:00 2026 sleep 300\n"), nil
	}
	t.Setenv("LC_ALL", "ko_KR.UTF-8")
	t.Setenv("LC_TIME", "ko_KR.UTF-8")
	id, err := ProcIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if id != "Mon Jul 28 20:00:00 2026 sleep 300" {
		t.Fatalf("identity = %q (leading space must be trimmed)", id)
	}
	if len(seen) != 1 || seen[0] != "C" {
		t.Fatalf("ps ran without LC_ALL=C (saw %v)", seen)
	}
}

// The real process identity is stable for a live process and fails for a dead one. The stability half reads this test
// process (fully started): a child read the instant cmd.Start returns can briefly show an empty /proc cmdline on Linux,
// which is correctly unreadable, not an identity.
func TestProcIdentityRealProcess(t *testing.T) {
	a, err := ProcIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := ProcIdentity(os.Getpid()); b != a {
		t.Fatalf("identity not stable: %q vs %q", a, b)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, err := ProcIdentity(pid); err == nil {
		t.Fatal("a dead pid still has an identity")
	}
}

// R13: Healthy is alive + a matching recorded identity + a fresh beacon (fm fm_watcher_healthy).
func TestHealthy(t *testing.T) {
	epic := t.TempDir()
	now := time.Now()
	if Healthy(epic, now, 0) {
		t.Fatal("no pidfile reads healthy")
	}
	_ = os.MkdirAll(filepath.Dir(PidPath(epic)), 0o755)
	_ = os.WriteFile(PidPath(epic), []byte(strconv.Itoa(os.Getpid())), 0o644)
	w := &Watcher{EpicDir: epic, Now: func() time.Time { return now }}
	w.markTick()
	if Healthy(epic, now, 0) {
		t.Fatal("an identityless live pid reads healthy (fm: an unrecorded identity is not a watcher)")
	}
	if err := RecordIdentity(epic, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if pid, id := ReadPid(epic); pid != os.Getpid() || id == "" {
		t.Fatalf("ReadPid = %d %q", pid, id)
	}
	if !Healthy(epic, now, 0) {
		t.Fatal("a live pid with a matching identity and a fresh beacon reads unhealthy")
	}
	if Healthy(epic, now.Add(DefaultGrace+time.Second), 0) {
		t.Fatal("a stale beacon reads healthy")
	}
	_ = os.WriteFile(IdentityPath(epic), []byte("someone else\n"), 0o644)
	if Healthy(epic, now, 0) {
		t.Fatal("a reused pid (identity mismatch) reads healthy")
	}
	_ = os.Remove(IdentityPath(epic))
	_ = os.WriteFile(PidPath(epic), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	if pid, _ := ReadPid(epic); pid != os.Getpid() {
		t.Fatalf("a bare pid with a trailing newline did not parse: %d", pid)
	}
}

// fm: docs/watcher-continuity.md:121@a8572f6 (R19) and fm_poll_derived_grace.
func TestExitSignalsAndGrace(t *testing.T) {
	want := map[os.Signal]bool{syscall.SIGHUP: true, syscall.SIGINT: true, syscall.SIGTERM: true}
	if len(ExitSignals) != len(want) {
		t.Fatalf("ExitSignals = %v", ExitSignals)
	}
	for _, s := range ExitSignals {
		if !want[s] {
			t.Fatalf("unexpected exit signal %v", s)
		}
	}
	if PollGrace(15*time.Second) != 300*time.Second || PollGrace(400*time.Second) != 460*time.Second {
		t.Fatalf("grace = %v / %v", PollGrace(15*time.Second), PollGrace(400*time.Second))
	}
}

// fm: tests/fm-watcher-lock.test.sh:560@a8572f6 (R17): a pidfile naming another process evicts; our own or none does not.
func TestEvictOnPidfileTakeover(t *testing.T) {
	epic := t.TempDir()
	_ = os.MkdirAll(filepath.Join(epic, state.ControlDir), 0o755)
	w := &Watcher{EpicDir: epic}
	if r := w.evictReason(); r != "" {
		t.Fatalf("no pidfile evicted: %s", r)
	}
	_ = os.WriteFile(PidPath(epic), []byte(strconv.Itoa(os.Getpid())), 0o644)
	if r := w.evictReason(); r != "" {
		t.Fatalf("own pidfile evicted: %s", r)
	}
	_ = os.WriteFile(PidPath(epic), []byte("1\n"), 0o644)
	if r := w.evictReason(); !strings.Contains(r, "taken over") {
		t.Fatalf("takeover not evicted: %q", r)
	}
}

// A process caught mid-exec reads an empty cmdline for a moment; the identity waits it out (bounded), while a cmdline
// that stays empty is still unreadable.
func TestProcIdentityWaitsOutAnExecInProgress(t *testing.T) {
	root := t.TempDir()
	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()
	fakeProc(t, root, 4243, "5555")
	cmdline := filepath.Join(root, "4243", "cmdline")
	if err := os.WriteFile(cmdline, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = os.WriteFile(cmdline, []byte("sleep\x0060\x00"), 0o644)
	}()
	if id, err := ProcIdentity(4243); err != nil || !strings.Contains(id, "=5555 ") {
		t.Fatalf("an exec in progress was not waited out: %q %v", id, err)
	}
	if err := os.WriteFile(cmdline, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcIdentity(4243); err == nil {
		t.Fatal("a permanently empty cmdline was given an identity")
	}
}

// fm: tests/fm-watcher-lock.test.sh:1064@a8572f6 (test_pid_identity_is_terminal_width_invariant, e1d6cf9): an identity
// recorded from a wide shell must equal the one recomputed inside a narrow-COLUMNS hook, so the ps fallback pins a
// wide COLUMNS (after LC_ALL=C) and carries the whole command.
func TestProcIdentityPsFallbackIsTerminalWidthInvariant(t *testing.T) {
	t.Run("FM/fm-watcher-lock/pid_identity_is_terminal_width_invariant", func(t *testing.T) {
		oldRoot, oldRun := procRoot, psRun
		procRoot = filepath.Join(t.TempDir(), "no-proc")
		defer func() { procRoot, psRun = oldRoot, oldRun }()

		// Stub leg: the env ps runs under ends with the wide pin, whatever the caller's COLUMNS.
		var cols []string
		psRun = func(env []string, args ...string) ([]byte, error) {
			c := "<unset>"
			for _, kv := range env {
				if strings.HasPrefix(kv, "COLUMNS=") {
					c = strings.TrimPrefix(kv, "COLUMNS=") // the last assignment wins, as in exec
				}
			}
			cols = append(cols, c)
			return []byte("Mon Jul 28 20:00:00 2026 sleep 300\n"), nil
		}
		t.Setenv("COLUMNS", "20")
		if _, err := ProcIdentity(os.Getpid()); err != nil {
			t.Fatal(err)
		}
		if len(cols) != 1 || cols[0] != "10000" {
			t.Fatalf("ps ran without COLUMNS=10000 (saw %v)", cols)
		}

		// Real-ps leg (skipped where ps -o lstart= is unsupported): a long command reads identically narrow and wide.
		psRun = oldRun
		if _, err := exec.Command("ps", "-p", strconv.Itoa(os.Getpid()), "-o", "lstart=", "-o", "command=").Output(); err != nil {
			t.Skip("ps -o lstart= unsupported here")
		}
		long := "300." + strings.Repeat("0", 100)
		cmd := exec.Command("sleep", long)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		t.Setenv("COLUMNS", "20")
		narrow, err := ProcIdentity(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("COLUMNS", "1000")
		wide, err := ProcIdentity(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(wide, "sleep "+long) {
			t.Errorf("identity dropped the full command under a wide COLUMNS: %q", wide)
		}
		if narrow != wide {
			t.Errorf("identity varied with COLUMNS: narrow %q, wide %q", narrow, wide)
		}
	})
}
