package boundexec

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// KillLive ends a bounded command still running when its owner is about to os.Exit: the run returns at once with the
// group's signal status, not at its 30s bound (#47 follow-up: `cox watch` exiting on TERM orphaned a bounded probe).
func TestKillLiveEndsARunningBoundedCommand(t *testing.T) {
	done := make(chan int, 1)
	go func() {
		code, _ := Run(context.Background(), 30*time.Second, exec.Command("sleep", "30"))
		done <- code
	}()
	for i := 0; len(groups()) == 0; i++ {
		if i > 200 {
			t.Fatal("the bounded command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	began := time.Now()
	KillLive()
	select {
	case code := <-done:
		if code != 128+15 && code != 128+9 {
			t.Errorf("killed run exit = %d, want 143 (TERM) or 137 (KILL)", code)
		}
		if took := time.Since(began); took > 3*time.Second {
			t.Errorf("KillLive took %s to end the run", took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("KillLive did not end the bounded command")
	}
	KillLive() // nothing live: a no-op
}
