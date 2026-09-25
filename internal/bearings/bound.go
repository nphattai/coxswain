package bearings

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	"github.com/nphattai/coxswain/internal/boundexec"
)

// DefaultTimeout bounds the whole digest (FM_SESSION_START_TIMEOUT default, bin/fm-session-start.sh:278).
const DefaultTimeout = 120 * time.Second

// RunBounded runs argv in its own process group under a hard deadline: on expiry the whole group gets TERM, then KILL
// after a short grace, and the result is exit 124; a natural exit status passes through (fm_run_timed). It is the
// shared boundexec.Run with no captured output.
func RunBounded(timeout time.Duration, argv ...string) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("bearings: RunBounded needs a command")
	}
	return boundexec.Run(context.Background(), timeout, exec.Command(argv[0], argv[1:]...))
}

// stageProcs runs a digest's stage subprocesses, so the runtime bound can reap them: reap ends every live one (group
// TERM, then KILL) and waits for them before it returns, and no stage starts after it.
type stageProcs struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// init makes the reap context on first use. Caller holds mu.
func (s *stageProcs) init() {
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
}

// run runs argv bounded by bound and waits; after the reap it starts nothing.
func (s *stageProcs) run(argv []string, bound time.Duration) {
	if len(argv) == 0 {
		return
	}
	s.mu.Lock()
	s.init()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	_, _ = boundexec.Run(s.ctx, bound, exec.Command(argv[0], argv[1:]...))
}

// reap kills every live stage group, refuses new ones, and returns once they are gone.
func (s *stageProcs) reap() {
	s.mu.Lock()
	s.init()
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}
