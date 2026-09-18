package env

import (
	"errors"
	"path/filepath"
	"testing"
)

func allocatorAt(dir string, ops Ops) *Allocator { return &Allocator{EpicDir: dir, Ops: ops} }

func TestReconcileReleasesFreedAllocation(t *testing.T) {
	epic := t.TempDir()
	if err := WriteMigratedAlloc(epic, MigratedAlloc{Story: "s1", Port: 3700, EnvFile: filepath.Join(epic, ".env.s1"), State: StateMigratedUnverified}); err != nil {
		t.Fatal(err)
	}
	// env file does not exist and no listener => releasable.
	a := allocatorAt(epic, newFakeOps())

	// Dry-run writes nothing.
	res, err := a.ReconcileMigrated(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].State != StateMigratedUnverified {
		t.Fatalf("dry-run should not release: %+v", res)
	}
	if allocs, _ := LoadMigratedAllocs(epic); allocs[0].State != StateMigratedUnverified {
		t.Fatal("dry-run changed the file")
	}

	// Apply releases it.
	res, err = a.ReconcileMigrated(true)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].State != StateReleased {
		t.Fatalf("apply should release, got %s (%s)", res[0].State, res[0].Note)
	}
	if allocs, _ := LoadMigratedAllocs(epic); allocs[0].State != StateReleased {
		t.Fatalf("file not released on disk: %+v", allocs)
	}
}

func TestReconcileKeepsListeningPort(t *testing.T) {
	epic := t.TempDir()
	if err := WriteMigratedAlloc(epic, MigratedAlloc{Story: "s1", Port: 3800, State: StateMigratedUnverified}); err != nil {
		t.Fatal(err)
	}
	ops := newFakeOps()
	ops.portOwner[3800] = struct {
		pid int
		cmd string
	}{pid: 4242, cmd: "node"}
	a := allocatorAt(epic, ops)
	res, err := a.ReconcileMigrated(true)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].State == StateReleased {
		t.Error("a listening port must not be released")
	}
	if !res[0].PortLive || res[0].PortPID != 4242 {
		t.Errorf("expected the listener pid reported, got %+v", res[0])
	}
}

func TestReconcileUnknownProbeKeeps(t *testing.T) {
	epic := t.TempDir()
	if err := WriteMigratedAlloc(epic, MigratedAlloc{Story: "s1", Port: 3900, State: StateMigratedUnverified}); err != nil {
		t.Fatal(err)
	}
	ops := newFakeOps()
	ops.portErr = errors.New("lsof: not found") // no lsof => unknown, never released
	a := allocatorAt(epic, ops)
	res, err := a.ReconcileMigrated(true)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].State == StateReleased {
		t.Errorf("unknown probe must not release: %+v", res[0])
	}
}

func TestPortHeldByMigratedGuardsEnvUp(t *testing.T) {
	epic := t.TempDir()
	if err := WriteMigratedAlloc(epic, MigratedAlloc{Story: "s1", Port: 3400, State: StateMigratedUnverified}); err != nil {
		t.Fatal(err)
	}
	if s, held := PortHeldByMigrated(epic, 3400); !held || s != "s1" {
		t.Errorf("port 3400 should be held by s1, got (%q,%v)", s, held)
	}
	if _, held := PortHeldByMigrated(epic, 9999); held {
		t.Error("an unallocated port must not be reported held")
	}
	// Once released, the port is free to grant.
	_ = WriteMigratedAlloc(epic, MigratedAlloc{Story: "s1", Port: 3400, State: StateReleased})
	if _, held := PortHeldByMigrated(epic, 3400); held {
		t.Error("a released allocation must not hold its port")
	}
}

func TestLoadMigratedAllocsMissingDirIsEmpty(t *testing.T) {
	allocs, err := LoadMigratedAllocs(t.TempDir())
	if err != nil || allocs != nil {
		t.Fatalf("missing .cox/env should be empty, got %v err=%v", allocs, err)
	}
}
