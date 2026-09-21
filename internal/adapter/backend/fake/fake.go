// Package fake is an in-memory Backend for tests. It records every call and can be told to fail the next call to a
// named operation with FailNext, so the core's error paths (worktree create failure, probe error, unconfirmed stop)
// are exercised without Orca. It is not safe for concurrent use; tests drive it from one goroutine.
package fake

import (
	"errors"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// Backend is a fake in-memory backend.
type Backend struct {
	// Calls is the ordered log of operation names invoked, for assertions.
	Calls []string
	// failNext maps an op name to a pending error to return once.
	failNext map[string]error

	// Knobs the test sets to control return values.
	CreatedPath   string             // path WorktreeCreate returns (default derived from branch)
	CreatedBranch string             // branch WorktreeCreate returns (default = requested branch)
	Liveness      backend.Liveness   // what Probe returns when it does not fail
	StopConfirmed bool               // what Stop returns when it does not fail
	SendRang      bool               // what Send returns for rang when it does not fail (default true)
	ComposerState string             // what Composer returns (default "unknown")
	ScreenRows    []string           // what Screen returns when it does not fail
	Workers       []backend.Worker   // what WorkerList returns when it does not fail
	TerminalList  []backend.Terminal // what Terminals returns when it does not fail

	mailbox *Mailbox
}

// New returns a fake backend with an empty call log and its own mailbox. A doorbell rings by default (SendRang=true);
// tests that exercise the not-rung ladder path set SendRang=false.
func New() *Backend {
	return &Backend{
		failNext: map[string]error{},
		mailbox:  &Mailbox{},
		SendRang: true,
	}
}

// FailNext makes the next call to op return this error (or a default if err is nil). One-shot per op.
func (b *Backend) FailNext(op string, err error) {
	if err == nil {
		err = errors.New("fake: injected failure for " + op)
	}
	b.failNext[op] = err
}

func (b *Backend) record(op string) error {
	b.Calls = append(b.Calls, op)
	if err, ok := b.failNext[op]; ok {
		delete(b.failNext, op)
		return err
	}
	return nil
}

func (b *Backend) WorktreeCreate(repo, branch, base string) (backend.Worktree, error) {
	if err := b.record("WorktreeCreate"); err != nil {
		return backend.Worktree{}, err
	}
	path := b.CreatedPath
	if path == "" {
		path = "/fake/worktrees/" + branch
	}
	outBranch := b.CreatedBranch
	if outBranch == "" {
		outBranch = branch
	}
	return backend.Worktree{Path: path, Branch: outBranch}, nil
}

func (b *Backend) WorktreeRemove(wt backend.Worktree) error {
	return b.record("WorktreeRemove")
}

func (b *Backend) Spawn(wt backend.Worktree, h backend.HarnessSpec, brief backend.Brief) (backend.Session, error) {
	if err := b.record("Spawn"); err != nil {
		return backend.Session{}, err
	}
	return backend.Session{Kind: "fake", ID: "sess-" + wt.Branch}, nil
}

func (b *Backend) Send(s backend.Session, text string) (bool, error) {
	if err := b.record("Send"); err != nil {
		return false, err
	}
	return b.SendRang, nil
}
func (b *Backend) Interrupt(s backend.Session) error { return b.record("Interrupt") }

func (b *Backend) Stop(s backend.Session) (bool, error) {
	if err := b.record("Stop"); err != nil {
		return false, err
	}
	return b.StopConfirmed, nil
}

func (b *Backend) Probe(s backend.Session) (backend.Liveness, error) {
	if err := b.record("Probe"); err != nil {
		return backend.Unknown, err
	}
	return b.Liveness, nil
}

func (b *Backend) Composer(s backend.Session) (string, error) {
	if err := b.record("Composer"); err != nil {
		return backend.ComposerUnknown, err
	}
	if b.ComposerState == "" {
		return backend.ComposerUnknown, nil
	}
	return b.ComposerState, nil
}

func (b *Backend) Screen(s backend.Session) ([]string, error) {
	if err := b.record("Screen"); err != nil {
		return nil, err
	}
	return b.ScreenRows, nil
}

func (b *Backend) WorkerList() ([]backend.Worker, error) {
	if err := b.record("WorkerList"); err != nil {
		return nil, err
	}
	return b.Workers, nil
}

func (b *Backend) Terminals() ([]backend.Terminal, error) {
	if err := b.record("Terminals"); err != nil {
		return nil, err
	}
	return b.TerminalList, nil
}

func (b *Backend) Mail() backend.Mailbox { return b.mailbox }

// Mailbox is an in-memory mailbox. Check returns queued messages without consuming them; Ack records the delivery id
// so a test can assert Check did not silently acknowledge.
type Mailbox struct {
	Queue    []backend.Message
	Sent     []backend.Message
	Replies  map[string]string
	Acked    []string
	Delivery string
}

func (m *Mailbox) Send(to, subject, body string) error {
	m.Sent = append(m.Sent, backend.Message{From: "self", Subject: subject, Body: body, Type: "status"})
	return nil
}

func (m *Mailbox) Check() ([]backend.Message, string, error) {
	// Return a copy so callers cannot mutate the queue; do NOT clear it (Check must not consume).
	out := make([]backend.Message, len(m.Queue))
	copy(out, m.Queue)
	return out, m.Delivery, nil
}

func (m *Mailbox) Reply(msgID, body string) error {
	if m.Replies == nil {
		m.Replies = map[string]string{}
	}
	m.Replies[msgID] = body
	return nil
}

func (m *Mailbox) Ack(deliveryID string) error {
	m.Acked = append(m.Acked, deliveryID)
	return nil
}
