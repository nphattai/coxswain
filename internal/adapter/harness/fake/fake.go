// Package fake is a configurable harness.Harness for tests. It records Package/LaunchArgs calls and returns a Card the
// test sets, so the interrupt scenarios can run against a push harness and a pull harness without Claude or Codex.
package fake

import "github.com/nphattai/coxswain/internal/adapter/harness"

// Harness is a fake harness. Cap is the card it returns; Ctx and CtxKnown control Telemetry.
type Harness struct {
	Cap          harness.Capability
	Ctx          harness.Context
	Packaged     []string         // dsts passed to Package
	LaunchedWith []harness.Launch // launches passed to LaunchArgs
	Prepared     []string         // worktrees passed to PrepareWorktree
}

// New returns a fake with a minimal push card by default.
func New(name string, wake harness.WakeMode) *Harness {
	cp := harness.CheckpointAuto
	if wake == harness.WakePull {
		cp = harness.CheckpointManual
	}
	return &Harness{Cap: harness.Capability{
		Name:             name,
		Roles:            []harness.Role{harness.RoleLeader, harness.RoleWorker},
		Wake:             wake,
		Checkpoint:       cp,
		Doorbell:         true,
		Interrupt:        true,
		BackendInterrupt: true, // default: the backend keystroke interrupt works (as for claude/codex); a test sets it false to exercise the harness inbox interrupt path
	}}
}

func (h *Harness) Card() harness.Capability { return h.Cap }

func (h *Harness) Package(role harness.Role, dst string) error {
	h.Packaged = append(h.Packaged, dst)
	return nil
}

func (h *Harness) LaunchArgs(l harness.Launch) []string {
	h.LaunchedWith = append(h.LaunchedWith, l)
	return []string{h.Cap.Name, string(l.Role), l.Worktree, l.Brief.StoryPath}
}

func (h *Harness) PrepareWorktree(wt string) error {
	h.Prepared = append(h.Prepared, wt)
	return nil
}

func (h *Harness) Telemetry(session string) (harness.Context, error) {
	return h.Ctx, nil
}
