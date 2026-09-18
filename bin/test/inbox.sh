#!/bin/bash
# inbox: sequence never reused, handled/ ack, doorbell text
set -e; root="$(cd "$(dirname "$0")/../.." && pwd)"; . "$root/bin/inbox-lib.sh"
t="$(mktemp -d)"; r1="$(inbox_write "$t" s1 "first")"; r2="$(inbox_write "$t" s1 "second line
two")"
[ "$(basename "$r1")" = 001.msg ] && [ "$(basename "$r2")" = 002.msg ] || { echo "seq wrong: $r1 $r2"; exit 1; }
mv "$r1" "$t/inbox/s1/handled/"; r3="$(inbox_write "$t" s1 "third")"; [ "$(basename "$r3")" = 003.msg ] || { echo "seq reused after ack"; exit 1; }
grep -q "^--$" "$r2" && sed -n '/^--$/,$p' "$r2" | grep -q "two" || { echo "body lost"; exit 1; }
inbox_doorbell "$t/inbox/s1" | grep -q "handled/" || { echo "doorbell text"; exit 1; }
echo "ok: inbox seq, ack, body, doorbell"
