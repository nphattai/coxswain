#!/bin/bash
# send.sh: 5 steers per story, the 6th is refused without --override, --fyi never counts, the SendMessage pointer is printed.
# Run: /bin/bash bin/test/send-budget.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
e="$tmp/proj/epics/t"; mkdir -p "$e/stories" "$tmp/bin"; echo "r orca-repo" > "$e/repos"
printf -- '---\nid: t-a\nrepo: r\n---\n' > "$e/stories/t-a.md"; printf 'dispatch.t-a=ctx_1\n' > "$e/.run"
printf '#!/bin/bash\necho "{}"\n' > "$tmp/bin/orca"; chmod +x "$tmp/bin/orca"; export PATH="$tmp/bin:$PATH"
fail=0
for i in 1 2 3 4 5; do out="$("$here/../send.sh" "$e" t-a "steer $i" 2>&1)" || { echo "FAIL: steer $i refused: $out"; fail=1; }; done
grep -q "SendMessage" <<<"$out" || { echo "FAIL: no SendMessage pointer: $out"; fail=1; }
if "$here/../send.sh" "$e" t-a "steer 6" >/dev/null 2>&1; then echo "FAIL: 6th steer accepted"; fail=1; fi
"$here/../send.sh" --fyi "$e" t-a "fyi" >/dev/null 2>&1 || { echo "FAIL: fyi refused"; fail=1; }
out="$("$here/../send.sh" --override "captain asked" "$e" t-a "steer 6" 2>&1)" || { echo "FAIL: override refused: $out"; fail=1; }
rec="$(ls "$e"/inbox/t-a/*.msg | tail -1)"; grep -q "override: captain asked" "$rec" || { echo "FAIL: override reason not in the record"; fail=1; }
[ "$(ls "$e"/inbox/t-a/*.msg | wc -l | tr -d ' ')" = 7 ] || { echo "FAIL: expected 7 records"; fail=1; }
[ $fail = 0 ] && echo "ok: steer budget 5, --override, --fyi uncounted, SendMessage pointer"
exit $fail
