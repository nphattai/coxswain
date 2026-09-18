#!/bin/bash
# Self-check for the workspace host rule in bin/lib.sh: REMOTE is the first non-local line of <ws>/hosts; no file = local only.
# The kit under test is copied into a fake workspace so ws_root() resolves there. Run: /bin/bash bin/test/hosts.sh
set -u
here="$(cd "$(dirname "$0")" && pwd)"; t="$(cd "$(mktemp -d)" && pwd -P)"; trap 'rm -rf "$t"' EXIT
mkdir -p "$t/ws/bin"; cp "$here/../lib.sh" "$t/ws/bin/"; git -C "$t/ws" init -q
fail=0
probe() { (cd "$t/ws" && . bin/lib.sh && echo "root=$(ws_root) remote=${REMOTE:-none} $(hosts_ok "$1" && echo ok || echo bad)"); }
chk() { [ "$1" = "$2" ] || { echo "FAIL: want '$2' got '$1'"; fail=1; }; }
chk "$(probe local)"       "root=$t/ws remote=none ok"
chk "$(probe mini)"        "root=$t/ws remote=none bad"
chk "$(probe local,local)" "root=$t/ws remote=none ok"
printf 'local\nmini\n' > "$t/ws/hosts"
chk "$(probe mini)"        "root=$t/ws remote=mini ok"
chk "$(probe local,mini)"  "root=$t/ws remote=mini ok"
chk "$(probe box)"         "root=$t/ws remote=mini bad"
chk "$(probe 'mini,')"     "root=$t/ws remote=mini bad"
# ws_name: origin basename, else the directory name
chk "$(cd "$t/ws" && . bin/lib.sh && ws_name)" "ws"
git -C "$t/ws" remote add origin git@github.com:nphattai/solo.git
chk "$(cd "$t/ws" && . bin/lib.sh && ws_name)" "solo"
# the real callers source lib.sh under set -euo pipefail: a missing hosts file, or one that only says local, must not kill them
printf '#!/bin/bash\nset -euo pipefail; . "$(dirname "$0")/lib.sh"; echo "reached remote=${REMOTE:-none}"\n' > "$t/ws/bin/x.sh"; chmod +x "$t/ws/bin/x.sh"
chk "$("$t/ws/bin/x.sh")" "reached remote=mini"
echo local > "$t/ws/hosts"; chk "$("$t/ws/bin/x.sh" 2>&1)" "reached remote=none"
rm "$t/ws/hosts";           chk "$("$t/ws/bin/x.sh" 2>&1)" "reached remote=none"
printf 'Mini\r\n' > "$t/ws/hosts"; chk "$(probe mini)" "root=$t/ws remote=mini ok"   # normalised on read
[ $fail = 0 ] && echo "ok: hosts file -> REMOTE, hosts_ok, ws_root, ws_name, set -e callers"
exit $fail
