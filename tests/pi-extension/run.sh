#!/usr/bin/env bash
# Deterministic lifecycle suite for the Coxswain Pi extension (internal/adapter/harness/pi/extension/). It proves the invariants the pi card's
# wake=push / checkpoint=auto claims are gated on, without a real Pi session (DESIGN section 4). Real-tool E2E is the
# separate wave-2 dogfood story.
#
# Steps:
#   1. build the real `cox` (go build) for the real-binary test (cox-real.test.ts), when go is available
#   2. node --test on every extension suite: the core, the wiring against a faithful fake of the Pi 0.86.1 session, and
#      the extension as `cox workspace init` installs it against the real binary. A per-test timeout turns a blocked
#      cox child (dogfood F-4) into a failure instead of a hang.
#   3. a load/strip check that the wiring extension (cox-pi.ts) parses and exports a Pi extension factory
#   4. tsc type-check against the installed Pi package (npm -g @earendil-works/pi-coding-agent) when tsc and the
#      package are available (skipped with a note otherwise; CI installs both)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
cd "$root"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

echo "== 1. build the real cox for the real-binary test =="
if command -v go >/dev/null 2>&1; then
  go build -o "$scratch/cox" ./cmd/cox
  export COX_REAL_BIN="$scratch/cox"
  echo "ok: built $COX_REAL_BIN"
else
  echo "skip: go not installed; cox-real.test.ts is skipped"
fi

echo "== 2. node --test: extension lifecycle suites =="
node --test --test-timeout=120000 internal/adapter/harness/pi/extension/*.test.ts

echo "== 3. load check: cox-pi.ts wiring parses and exports a factory =="
node --input-type=module -e "
  const m = await import('./internal/adapter/harness/pi/extension/cox-pi.ts');
  if (typeof m.default !== 'function') { console.error('cox-pi.ts must export a default extension factory'); process.exit(1); }
  console.log('ok: cox-pi.ts default export is a function');
"

echo "== 4. type-check against the installed Pi package =="
pipkg="$(npm root -g 2>/dev/null)/@earendil-works/pi-coding-agent"
if command -v tsc >/dev/null 2>&1 && [ -f "$pipkg/dist/index.d.ts" ]; then
  ext="$root/internal/adapter/harness/pi/extension"
  cat >"$scratch/tsconfig.json" <<JSON
{ "compilerOptions": { "noEmit": true, "strict": true, "module": "esnext", "moduleResolution": "bundler", "target": "es2022",
    "allowImportingTsExtensions": true, "skipLibCheck": true,
    "paths": { "@earendil-works/pi-coding-agent": ["$pipkg/dist/index.d.ts"] },
    "typeRoots": ["$pipkg/node_modules/@types"], "types": ["node"] },
  "files": ["$ext/cox-pi.ts", "$ext/cox-supervisor.ts", "$ext/cox-commands.ts"] }
JSON
  tsc -p "$scratch/tsconfig.json"
  echo "ok: tsc type-check passed ($(node -p "require('$pipkg/package.json').version"))"
else
  echo "skip: tsc or the Pi package not installed (npm i -g typescript @earendil-works/pi-coding-agent@0.86.1)"
fi

echo "PASS: pi-extension lifecycle suite"
