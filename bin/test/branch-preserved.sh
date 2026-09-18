#!/bin/bash
# F01: epic-new.sh must never force-delete a branch. A temp repo has an `epic-<slug>` branch with an unpushed commit;
# running the real epic-new.sh (copied into a temp kit so ws_root resolves there) against it must leave that branch intact.
# Real git; fake orca/jq/ssh on PATH. Run: /bin/bash bin/test/branch-preserved.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; src="$(cd "$here/.." && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
g() { git -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c init.defaultBranch=main "$@"; }

# temp kit: copy the scripts under test so ws_root (pwd -P) stays inside tmp, not the real checkout
kit="$tmp/kit"; mkdir -p "$kit/bin" "$kit/templates/epic" "$kit/proj/docs"
cp "$src/epic-new.sh" "$src/lib.sh" "$src/link.sh" "$kit/bin/"
echo '# {{slug}} {{project}}' > "$kit/templates/epic/DESIGN.md"; printf '{{repo_rows}}\n' >> "$kit/templates/epic/DESIGN.md"
printf '| Alias | Repo | Production |\n|---|---|---|\n| api | myrepo | main |\n' > "$kit/proj/docs/repos.md"
( cd "$kit" && g init -q && g add -A && g commit -qm init )

# bare origin + a story/epic worktree already on epic/t, plus an epic-t branch holding an unpushed commit
bare="$tmp/origin.git"; g init -q --bare "$bare"
ws="$tmp/ws"; wt="$ws/myrepo/epic-t"; mkdir -p "$wt"
( cd "$wt" && g init -q && g remote add origin "$bare" && echo base > f && g add f && g commit -qm base \
  && g switch -qc epic/t && g push -q -u origin epic/t \
  && g switch -qc epic-t && echo unpushed > f && g commit -qam unpushed \
  && g switch -q epic/t )

mkdir -p "$tmp/home/Work/repo/x/myrepo"
mkdir -p "$tmp/bin"
printf '#!/bin/bash\nexit 0\n' > "$tmp/bin/orca"
printf '#!/bin/bash\ncat\n' > "$tmp/bin/jq"
printf '#!/bin/bash\nexit 0\n' > "$tmp/bin/ssh"
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" HOME="$tmp/home" ORCA_WORKSPACES="$ws"

set +e; out="$("$kit/bin/epic-new.sh" proj t myrepo=myrepo 2>&1)"; set -e
fail=0
git -C "$wt" show-ref --quiet refs/heads/epic-t || { echo "FAIL: epic-t branch was deleted by epic-new.sh"; echo "$out" | tail -5; fail=1; }
[ "$(git -C "$wt" rev-parse epic-t 2>/dev/null)" ] && [ "$(git -C "$wt" log -1 --format=%s epic-t 2>/dev/null)" = unpushed ] \
  || { echo "FAIL: epic-t lost its unpushed commit"; fail=1; }
[ $fail = 0 ] && echo "ok: branch-preserved (epic-new.sh keeps an unpushed epic-<slug> branch)"
exit $fail
