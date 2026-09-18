#!/bin/bash
# Smoke for the two scripts nothing else covers: bin/infra.sh status and bin/hygiene.sh must run to the end with a stubbed
# docker/xcrun (they died on an unbound $root once). Run: /bin/bash bin/test/infra-smoke.sh
set -u
here="$(cd "$(dirname "$0")" && pwd)"; t="$(cd "$(mktemp -d)" && pwd -P)"; trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin"; printf '#!/bin/bash\necho "docker $*" >> "%s/calls"; exit 0\n' "$t" > "$t/bin/docker"; printf '#!/bin/bash\nexit 1\n' > "$t/bin/xcrun"
chmod +x "$t/bin/docker" "$t/bin/xcrun"; export PATH="$t/bin:$PATH"
fail=0
out="$(/bin/bash "$here/../infra.sh" status 2>&1)"; rc=$?; [ $rc = 0 ] && grep -q "project=crewkit" "$t/calls" || { echo "FAIL: infra.sh status rc=$rc: $out"; fail=1; }
out="$(ORCA_WORKSPACES="$t/none" /bin/bash "$here/../hygiene.sh" 2>&1)"; rc=$?; [ $rc = 0 ] && ! grep -q "unbound" <<<"$out" || { echo "FAIL: hygiene.sh rc=$rc: $out"; fail=1; }
[ $fail = 0 ] && echo "ok: infra.sh status + hygiene.sh run end to end (stubbed docker)"
exit $fail
