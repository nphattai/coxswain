package main

import (
	"testing"

	"github.com/nphattai/coxswain/internal/protocol/busy"
)

func TestBusyProgressMarksWithoutChangingState(t *testing.T) {
	epic := t.TempDir()
	gen, err := busy.Arm(epic, "s1", "pi", []string{"dispatch", "pi-ext"})
	if err != nil {
		t.Fatal(err)
	}
	if code := cmdBusy([]string{"progress", "s1", "--gen", gen, "--epic", epic}); code != 0 {
		t.Fatalf("busy progress exited %d", code)
	}
	if _, ok := busy.ProgressAt(epic, "s1"); !ok {
		t.Fatal("busy progress wrote no progress marker")
	}
	if st := busy.Read(epic, "s1"); st != busy.Busy {
		t.Fatalf("busy progress changed the semantic state to %q", st)
	}
	if code := cmdBusy([]string{"progress", "s1", "--gen", "stale", "--epic", epic}); code == 0 {
		t.Fatal("busy progress accepted a stale gen")
	}
}
