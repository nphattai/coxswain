# CLI reference

The full `cox` command surface. Generated from `cox --help`; run it locally for the version you have.

```text
cox - coxswain CLI

usage:
  cox version
  cox workspace init [--from-repos-md <path>] [--root <dir>]
  cox epic new <project> <slug> --repo alias=ref ... [--no-push]
  cox epic stories --epic <dir>
  cox epic close --epic <dir> [--yes] [--force] [--stories-only]
  cox epic arena --epic <dir> [--lite] [--reason <text>] [--leader claude|codex]
  cox epic design --sign [--by <name>] | --amend --reason <why> --epic <dir>
  cox arena check|collect|synth --epic <dir> [--round <n>] [--html]
  cox epic design --html --epic <dir>
  cox plan --html <plan-dir> --epic <dir>
  cox plan compare --html <plan-dir> <architecture.html> --epic <dir>
  cox artifact list --epic <dir>
  cox review open <artifact> [--epic <dir>]
  cox review poll <artifact> --epic <dir> [--max 25m]
  cox review reply "<message>" <artifact> --epic <dir> [--max 25m]
  cox review share <artifact> [--share] --epic <dir>
  cox env up|down|refresh|smoke|status|snapshot|release <story> --epic <dir>
  cox audit pr <story> --epic <dir> [--pr <n>] [--json]
  cox ship facts --epic <dir> [--json] [--no-forge]
  cox reply <story> qNNN "<answer>" --epic <dir> [--again]   (terminal plane)
  cox reply <msg-id> "<text>" --epic <dir>                   (orchestration plane)
  cox question wait qNNN --epic <dir> --story <id> [--max 25m]
  cox state [<story>] --epic <dir> [--json] [--no-forge]
  cox route --story <id> --epic <dir> [--json]
  cox quota [--json] --epic <dir>
  cox quota set <harness> <percent> --until <RFC3339> [--model m] --epic <dir>
  cox quota unset <harness> --epic <dir>
  cox board --epic <dir> (--out <file> | --serve :port) [--no-forge]
  cox lab new|assign|report|retire <name> --epic <dir> [--rule <k> --metric <m> | --story <id> | --json]
  cox scorecard --epic <dir> [--story <id>] [--json] [--no-forge]
  cox baseline run --story <id> --epic <dir> --harness claude|codex --condition bare|v2 --before <sha> [--dry-run]
  cox doctor [--epic <dir>] [--json]
  cox migrate --epic <dir> [--apply]
  cox steer <story> "<text>" --epic <dir> [--fyi] [--override <why>]
  cox status <phase> "<note>" --epic <dir> --story <id>
  cox control <story> interrupt|park|relaunch [--note <progress>] --epic <dir>
  cox reconcile --epic <dir> [--apply] [--json]
  cox story dispatch|done|park|resume <id> --epic <dir> [--harness claude|codex --model <id>]
  cox story fail|cancel <id> --reason "<why>" --epic <dir> [--close-worktree] [--force]
  cox story report status|done|stuck --epic <dir> --story <id> --note "<summary>" [--evidence k=v ...]
  cox story report question --body "<question>" --epic <dir> --story <id>
  cox checkpoint facts|inject --epic <dir> --story <id>
  cox wake drain [--peek] | ack-through <gen> | wait [--max <dur>]  --epic <dir>
  cox watch --epic <dir> [--once]
  cox inbox ack <record-path>
  cox hook prompt-drain|stop-rewake|precompact|session-start
```
