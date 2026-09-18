# Board

`cox board` renders a read-only captain view of an epic. It is state only (decision 0011): there is no form, no button,
no POST anywhere in the page, and the server answers `GET` only. Every action stays in the leader chat.

## Commands

```
cox board --epic <dir> --out board.html      # write a self-contained snapshot (opens with no network)
cox board --epic <dir> --serve :8787         # serve it and refresh /data.json every 10s
cox board --epic <dir> --serve :8787 --no-forge   # skip the gh probe (pr/checks stay unknown)
```

- `--out` embeds the current snapshot in the HTML, so the file opens offline and still renders fully. It loads no
  external resource (CSS and JS are inlined).
- `--serve` serves the same page at `/` plus a same-origin `/data.json` the page polls every 10s. `GET /data.json`
  returns the snapshot as JSON; any non-GET is `405`, and an unknown path is `404`.

## What it shows

From `coxswain.fleet.v1` and the other on-disk sources, per story: state, attempt, liveness, composer, forge
(PR/checks/merged), per-attempt scorecard, steer budget used, pending questions, and unacked wakes. Plus, per epic: the
arena round, whether a second round is required, whether the design is signed, the latest unacked wake, and the list of
decisions waiting on the captain (an unsigned-but-ready design, a required arena round 2, and open questions from the
story handoffs).

Every number that cannot be read prints `unknown`, never `0`.

## Limits (decision 0011)

- Read-only. The board never mutates state; it is not a place to act in the leader's stead. A review rejects any
  endpoint that writes.
- The board was opened by a direct captain request, not as a remedy for a core bug (fix the core instead).
