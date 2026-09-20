#!/usr/bin/env bash
# Deterministic lifecycle suite for the Coxswain Pi extension (hooks/pi/). It proves the invariants the pi card's
# wake=push / checkpoint=auto claims are gated on, without a real Pi session (DESIGN section 4). Real-tool E2E is the
# separate wave-2 dogfood story.
#
# Steps:
#   1. node --test on the runtime-independent core (cox-supervisor.test.ts)
#   2. a load/strip check that the wiring extension (cox-pi.ts) parses and exports a Pi extension factory
#   3. optional tsc type-check against the installed Pi package when tsc is available (skipped with a note otherwise)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
cd "$root"

echo "== 1. node --test: extension lifecycle core =="
node --test hooks/pi/*.test.ts

echo "== 2. load check: cox-pi.ts wiring parses and exports a factory =="
node --input-type=module -e "
  const m = await import('./hooks/pi/cox-pi.ts');
  if (typeof m.default !== 'function') { console.error('cox-pi.ts must export a default extension factory'); process.exit(1); }
  console.log('ok: cox-pi.ts default export is a function');
"

echo "== 3. type-check against the installed Pi package =="
if command -v tsc >/dev/null 2>&1; then
  tsc --noEmit --strict --module nodenext --moduleResolution nodenext hooks/pi/cox-pi.ts hooks/pi/cox-supervisor.ts
  echo "ok: tsc type-check passed"
else
  echo "skip: tsc not installed; type-check runs in dogfood/CI where the Pi toolchain is present"
fi

echo "PASS: pi-extension lifecycle suite"
