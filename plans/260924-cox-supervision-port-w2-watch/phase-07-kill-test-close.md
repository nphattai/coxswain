---
phase: 7
title: "Kill test, untag, Makefile, supersession notes"
status: pending
priority: P1
effort: ""
dependencies: [2,3,4,5,6]
---

# Phase 7: Kill test, untag, Makefile, supersession notes

## Overview
Epic AC 3: a script builds cox, makes a temp workspace + epic, starts the REAL `cox watch`, dispatches a story whose
worker PATH puts `/usr/bin/false` first as `cox` (fake backend terminal / a shell worker that ends its turn), lets
the turn end, and asserts an urgent wake for the leader within the grace window. Transcript to
`henrylab/.../reports/evidence/kill-test/` (left for the leader to commit).

## Steps
1. `scripts/kill-test.sh` is NOT a new repo file (Files touched); the script lives with the evidence under
   `reports/evidence/kill-test/kill-test.sh` and the transcript beside it.
2. Remove `//go:build port` from `port_lifecycle_test.go` if zero red (R16 x2 belong to w2-hooks, so likely stays
   tagged - reported). `port_triage_test.go` stays tagged (decision fold).
3. Makefile `test:` runs `test-port` with the red ceiling (Q4).
4. One-line supersession notes: `docs/decisions/0016`, `0014` as applicable, `docs/ARCHITECTURE.md`.
5. Before/after red counts per package from `go test -tags port -json` for the PR body.

## Success
AC 1-5 of the story; kill test transcript shows the urgent wake and its latency.
