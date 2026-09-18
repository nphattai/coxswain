#!/bin/bash
# F05: park.sh must keep ownership when it cannot stop the worker. When worker-stop AND worker-abandon both fail it must
# leave dispatch.<id>/term.<id> intact, write no parked.<id>, and exit non-zero. A worker-release failure after a
# successful stop reports but still parks. Fake orca. Run: /bin/bash bin/test/park-keeps-ownership.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
e="$tmp/proj/epics/t"; mkdir -p "$e/stories" "$e/handoffs" "$tmp/bin"
echo "r orca-repo" > "$e/repos"; printf -- '---\nid: t-a\nrepo: r\n---\n' > "$e/stories/t-a.md"
mk_run() { printf 'run=run_p\ndispatch.t-a=ctx_1\nterm.t-a=term_1\n' > "$e/.run"; }
fail=0

# --- case 1: stop + abandon + release all fail -> ownership kept, no park, exit != 0 ---
cat > "$tmp/bin/orca" <<'SH'
#!/bin/bash
echo "$*" >> "$FAKE_LOG"; exit 1
SH
chmod +x "$tmp/bin/orca"
export PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/calls" HOME="$tmp/home"
mk_run; : > "$FAKE_LOG"
set +e; out="$("$here/../park.sh" "$e" t-a --now 2>&1)"; rc=$?; set -e
[ "$rc" != 0 ] || { echo "FAIL: park exited 0 though stop and abandon both failed"; fail=1; }
[ "$(sed -n 's/^dispatch\.t-a=//p' "$e/.run" | tail -1)" = ctx_1 ] || { echo "FAIL: dispatch.t-a ownership lost: $(cat "$e/.run")"; fail=1; }
[ "$(sed -n 's/^term\.t-a=//p' "$e/.run" | tail -1)" = term_1 ] || { echo "FAIL: term.t-a ownership lost"; fail=1; }
grep -q "^parked.t-a=" "$e/.run" && { echo "FAIL: parked.t-a written despite failed stop"; fail=1; }
grep -q "worker-abandon" "$FAKE_LOG" || { echo "FAIL: abandon not attempted after stop failed"; fail=1; }

# --- case 2: stop succeeds, release fails -> parks, clears ownership, warns about release ---
cat > "$tmp/bin/orca" <<'SH'
#!/bin/bash
echo "$*" >> "$FAKE_LOG"
case "$1 $2" in
  "orchestration worker-release") exit 1;;   # only release fails; stop/abandon succeed
  *) echo '{"result":{}}';;
esac
SH
chmod +x "$tmp/bin/orca"
mk_run; : > "$FAKE_LOG"
set +e; out2="$("$here/../park.sh" "$e" t-a --now 2>&1)"; rc2=$?; set -e
[ "$rc2" = 0 ] || { echo "FAIL: park should succeed when only release fails: $out2"; fail=1; }
[ -z "$(sed -n 's/^dispatch\.t-a=//p' "$e/.run" | tail -1)" ] || { echo "FAIL: dispatch.t-a not cleared after a good stop"; fail=1; }
grep -q "^parked.t-a=" "$e/.run" || { echo "FAIL: parked.t-a not written after a good stop"; fail=1; }
grep -qi "release" <<<"$out2" || { echo "FAIL: release failure not reported"; fail=1; }

# --- case 3: stop fails, abandon succeeds -> parks (dispatch fenced), warns worker NOT confirmed stopped ---
cat > "$tmp/bin/orca" <<'SH'
#!/bin/bash
echo "$*" >> "$FAKE_LOG"
case "$1 $2" in
  "orchestration worker-stop") exit 1;;         # stop fails
  *) echo '{"result":{}}';;                       # abandon + release succeed
esac
SH
chmod +x "$tmp/bin/orca"
mk_run; : > "$FAKE_LOG"
set +e; out3="$("$here/../park.sh" "$e" t-a --now 2>&1)"; rc3=$?; set -e
[ "$rc3" = 0 ] || { echo "FAIL: park should succeed when stop fails but abandon succeeds: $out3"; fail=1; }
grep -q "^parked.t-a=" "$e/.run" || { echo "FAIL: parked.t-a not written after a good abandon"; fail=1; }
grep -qi "NOT confirmed stopped" <<<"$out3" || { echo "FAIL: abandon-only park did not warn worker NOT confirmed stopped: $out3"; fail=1; }

[ $fail = 0 ] && echo "ok: park-keeps-ownership (all-fail keeps ownership + exits non-zero; release-fail still parks but reports; abandon-only parks but warns)"
exit $fail
