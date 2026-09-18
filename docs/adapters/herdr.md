# herdr backend adapter

herdr is an optional terminal backend. Coxswain creates plain git worktrees and runs each worker in a herdr pane. herdr
provides terminal lifecycle and native agent state; Coxswain owns report, question/reply, wake, checkpoint, and story
state.

## Support contract

| Capability | Contract |
|---|---|
| Worktree create/remove | Supported through git, with detach-before-remove and branch preservation |
| Spawn/send/interrupt/stop | Supported through herdr workspace and pane commands |
| Probe | Supported through native agent state; unreadable state is unknown |
| Composer | Reduced to unknown |
| Mailbox | Reduced to none; durable Coxswain handoff is used |
| Worker list | Reduced; callers probe a known session |
| Remote hosts | Out of scope |

An idle registered agent is alive. It must not be treated as a stopped worker. An unconfirmed pane close keeps
ownership. Spawn leaves presentation-level workspace cleanup to herdr rather than reproducing its UI projection layer.

The executable owners are `internal/adapter/backend/herdr/herdr.go` and `herdr_test.go`. Version-specific command
evidence and the open live-spawn check are in [herdr 0.8.2 compatibility evidence](../evidence/compatibility/herdr-0.8.2.md).

## Operator ceiling

Empty herdr workspaces may remain after a pane closes. Inspect and prune them with herdr's own workspace tooling only
after confirming no live pane depends on them.
