package env

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeOps drives the external-failure paths without postgres/xcrun.
type fakeOps struct {
	dbs       map[string]bool
	createErr map[string]error // db name -> error DBCreate should return
	renameErr map[string]error // "from->to" -> error DBRename should return
	dropErr   map[string]error
	simErr    error
	sims      map[string]bool
	portOwner map[int]struct {
		pid int
		cmd string
	}
	portErr  error // when set, PortOwner returns it (simulates lsof missing / unreadable)
	signaled map[int]syscall.Signal
	alive    map[int]bool
}

func newFakeOps() *fakeOps {
	return &fakeOps{
		dbs: map[string]bool{}, createErr: map[string]error{}, renameErr: map[string]error{},
		dropErr: map[string]error{}, sims: map[string]bool{},
		portOwner: map[int]struct {
			pid int
			cmd string
		}{},
		signaled: map[int]syscall.Signal{}, alive: map[int]bool{},
	}
}

func (f *fakeOps) DBExists(name string) (bool, error) { return f.dbs[name], nil }
func (f *fakeOps) DBCreate(name, template string) error {
	if err := f.createErr[name]; err != nil {
		return err
	}
	f.dbs[name] = true
	return nil
}
func (f *fakeOps) DBDrop(name string) error {
	if err := f.dropErr[name]; err != nil {
		return err
	}
	delete(f.dbs, name)
	return nil
}
func (f *fakeOps) DBRename(from, to string) error {
	if err := f.renameErr[from+"->"+to]; err != nil {
		return err
	}
	if f.dbs[from] {
		delete(f.dbs, from)
		f.dbs[to] = true
	}
	return nil
}
func (f *fakeOps) SimClone(base, name string) (string, error) {
	if f.simErr != nil {
		return "", f.simErr
	}
	udid := "UDID-" + name
	f.sims[udid] = true
	return udid, nil
}
func (f *fakeOps) SimDelete(udid string) error { delete(f.sims, udid); return nil }
func (f *fakeOps) PortOwner(port int) (int, string, bool, error) {
	if f.portErr != nil {
		return 0, "", false, f.portErr
	}
	if o, ok := f.portOwner[port]; ok {
		return o.pid, o.cmd, true, nil
	}
	return 0, "", false, nil
}
func (f *fakeOps) Signal(pid int, sig syscall.Signal) error {
	if sig == 0 {
		if f.alive[pid] {
			return nil
		}
		return syscall.ESRCH
	}
	f.signaled[pid] = sig
	return nil
}

func writeEpic(t *testing.T, dir, epicEnv, storyFM string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "stories"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "epic.env"), []byte(epicEnv), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stories", "s1.md"), []byte(storyFM), 0o644); err != nil {
		t.Fatal(err)
	}
}

const backendEpicEnv = "EPIC=demo\nPROJECT=p\nBACKEND=api\nAPI_PORT=3333\nSTORY_PORT_BASE=3400\nDB_NAME=demo\nSIM_BASE=\n"

func TestAllocatePublishesCompleteBackendEnv(t *testing.T) {
	dir := t.TempDir()
	writeEpic(t, dir, backendEpicEnv, "---\nid: s1\nrepo: api\ndevice: false\n---\n")
	f := newFakeOps()
	f.dbs["demo_tpl"] = true
	a := &Allocator{EpicDir: dir, Ops: f}
	sr, err := a.Allocate("s1", "")
	if err != nil {
		t.Fatal(err)
	}
	if sr.Port != 3400 || sr.DB != "demo_s1" {
		t.Fatalf("bad resource: %+v", sr)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".env.s1"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(b)
	for _, k := range []string{"STORY=s1", "PORT=3400", "DB_NAME=demo_s1", "TYPEORM_DATABASE=demo_s1", "REDIS_PREFIX=demo:s1:", "TEMPORAL_HOST=localhost:7234"} {
		if !strings.Contains(env, k) {
			t.Errorf("env missing %q\n%s", k, env)
		}
	}
}

// A missing snapshot template makes the clone fail; no env file must be written (F13: never a half env).
func TestAllocateFailsWithoutSnapshotAndWritesNoEnv(t *testing.T) {
	dir := t.TempDir()
	writeEpic(t, dir, backendEpicEnv, "---\nid: s1\nrepo: api\n---\n")
	f := newFakeOps() // demo_tpl absent, and DBCreate against a missing template fails
	f.createErr["demo_s1"] = os.ErrInvalid
	a := &Allocator{EpicDir: dir, Ops: f}
	if _, err := a.Allocate("s1", ""); err == nil {
		t.Fatal("expected clone failure")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env.s1")); !os.IsNotExist(err) {
		t.Fatalf("no env file must exist after a failed clone, got err=%v", err)
	}
}

// A non-backend story with no db still publishes a complete minimal env, and validation passes.
func TestAllocateClientStoryMinimalEnv(t *testing.T) {
	dir := t.TempDir()
	writeEpic(t, dir, backendEpicEnv, "---\nid: s1\nrepo: web\n---\n")
	a := &Allocator{EpicDir: dir, Ops: newFakeOps()}
	if _, err := a.Allocate("s1", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env.s1"))
	if strings.Contains(string(b), "DB_NAME") {
		t.Fatalf("client story should have no DB block:\n%s", b)
	}
}

func TestReleaseKeepsOwnershipOnDropFailure(t *testing.T) {
	dir := t.TempDir()
	writeEpic(t, dir, backendEpicEnv, "---\nid: s1\nrepo: api\n---\n")
	f := newFakeOps()
	f.dbs["demo_tpl"] = true
	a := &Allocator{EpicDir: dir, Ops: f}
	if _, err := a.Allocate("s1", ""); err != nil {
		t.Fatal(err)
	}
	f.dropErr["demo_s1"] = os.ErrPermission
	if err := a.Release("s1"); err == nil {
		t.Fatal("release should fail when drop fails")
	}
	res, _ := loadResources(dir)
	sr := res.Stories["s1"]
	if sr.DB != "demo_s1" || sr.ConfirmedReleased {
		t.Fatalf("ownership must be kept on failed drop: %+v", sr)
	}
	// Now the drop succeeds: ownership clears and confirmed flips true.
	delete(f.dropErr, "demo_s1")
	if err := a.Release("s1"); err != nil {
		t.Fatal(err)
	}
	res, _ = loadResources(dir)
	sr = res.Stories["s1"]
	if sr.DB != "" || !sr.ConfirmedReleased {
		t.Fatalf("ownership must clear after confirmed drop: %+v", sr)
	}
}

func TestEnsurePortFreeForeignConflict(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeOps()
	f.portOwner[3333] = struct {
		pid int
		cmd string
	}{pid: 999, cmd: "node"}
	a := &Allocator{EpicDir: dir, Ops: f}
	err := a.EnsurePortFree(3333)
	var pc *ErrPortConflict
	if err == nil {
		t.Fatal("expected conflict")
	}
	if !asPortConflict(err, &pc) || pc.PID != 999 {
		t.Fatalf("want ErrPortConflict pid 999, got %v", err)
	}
}

// Stop must not kill a foreign pid that took over the port; it reports a conflict and leaves the process alive.
func TestStopBackendRefusesForeignPort(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeOps()
	a := &Allocator{EpicDir: dir, Ops: f}
	if err := a.RecordBackend(1234, 3333); err != nil {
		t.Fatal(err)
	}
	f.alive[1234] = true
	// A different pid now holds the port.
	f.portOwner[3333] = struct {
		pid int
		cmd string
	}{pid: 5678, cmd: "someone-else"}
	_, err := a.StopBackend()
	if err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("stop must refuse foreign port holder, got %v", err)
	}
	if _, killed := f.signaled[5678]; killed {
		t.Fatal("foreign pid must not be signaled")
	}
}

func TestPromoteSnapshotRestoresOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeOps()
	f.dbs["demo"] = true
	f.dbs["demo_tpl"] = true // an existing template
	a := &Allocator{EpicDir: dir, Ops: f}
	// The final rename (new -> tpl) fails.
	f.renameErr["demo_tpl_new->demo_tpl"] = os.ErrInvalid
	err := a.PromoteSnapshot("demo", "demo_tpl")
	if err == nil || !strings.Contains(err.Error(), "previous template restored") {
		t.Fatalf("expected restore-on-failure, got %v", err)
	}
	// The working template must still exist (restored from prev).
	if !f.dbs["demo_tpl"] {
		t.Fatalf("demo_tpl must be restored, dbs=%v", f.dbs)
	}
}

func TestPromoteSnapshotHappyPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeOps()
	f.dbs["demo"] = true
	f.dbs["demo_tpl"] = true
	a := &Allocator{EpicDir: dir, Ops: f}
	if err := a.PromoteSnapshot("demo", "demo_tpl"); err != nil {
		t.Fatal(err)
	}
	if !f.dbs["demo_tpl"] || f.dbs["demo_tpl_prev"] || f.dbs["demo_tpl_new"] {
		t.Fatalf("after promote only demo_tpl should remain: %v", f.dbs)
	}
}

// asPortConflict is a tiny errors.As shim to keep the test import list short.
func asPortConflict(err error, target **ErrPortConflict) bool {
	if pc, ok := err.(*ErrPortConflict); ok {
		*target = pc
		return true
	}
	return false
}
