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

// fm: tests/fm-watcher-lock.test.sh:1071 (R12)
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

// fm: tests/fm-watcher-lock.test.sh:1005 (R12): the ps fallback pins LC_ALL=C whatever the caller's locale.
func TestProcIdentityPsFallbackIsLocaleInvariant(t *testing.T) {
	old := procRoot
	procRoot = filepath.Join(t.TempDir(), "no-proc")
	defer func() { procRoot = old }()
	bin := t.TempDir()
	log := filepath.Join(bin, "observed")
	script := "#!/bin/sh\nprintf '%s\\n' \"${LC_ALL-<unset>}\" >> " + log + "\nprintf '   Mon Jul 28 20:00:00 2026 sleep 300\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "ko_KR.UTF-8")
	t.Setenv("LC_TIME", "ko_KR.UTF-8")
	id, err := ProcIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if id != "Mon Jul 28 20:00:00 2026 sleep 300" {
		t.Fatalf("identity = %q (leading space must be trimmed)", id)
	}
	b, _ := os.ReadFile(log)
	for _, l := range strings.Fields(string(b)) {
		if l != "C" {
			t.Fatalf("ps ran without LC_ALL=C (saw %q)", l)
		}
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

// fm: docs/watcher-continuity.md:117 (R19) and fm_poll_derived_grace.
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

// fm: tests/fm-watcher-lock.test.sh:560 (R17): a pidfile naming another process evicts; our own or none does not.
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
