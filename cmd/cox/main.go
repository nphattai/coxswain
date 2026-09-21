// Command cox is the coxswain CLI. M1 ships three subcommands over stdlib flag (no cobra, decision-scope rule):
// `cox version`, `cox state`, and `cox doctor`. Exit codes are meaningful: 0 ok, 1 a data error or a detected
// divergence, 2 a usage error.
package main

import (
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/version"
)

const usage = `cox - coxswain CLI

usage:
  cox version
  cox workspace init --repo alias=path[:production] ... [--from-repos-md <path>] [--root <dir>]
  cox workspace hooks [--root <dir>] --harness claude|codex|pi
  cox workspace add-repo <alias>=<path>[:<production>] [--root <dir>]
  cox epic new <project> <slug> --repo alias=ref ... [--no-push]
  cox epic attach --epic <dir>
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
  cox baseline run --story <id> --epic <dir> --harness claude|codex|pi --condition bare|v2 --before <sha> [--dry-run]
  cox doctor [--epic <dir>] [--json]
  cox migrate --epic <dir> [--apply]
  cox steer <story> "<text>" --epic <dir> [--fyi] [--override <why>]
  cox status <phase> "<note>" --epic <dir> --story <id>
  cox control <story> interrupt|park|relaunch [--note <progress>] --epic <dir>
  cox reconcile --epic <dir> [--apply] [--json]
  cox story dispatch|done|park|resume <id> --epic <dir> [--harness claude|codex|pi --model <id>] [--allow-unsandboxed]
  cox story fail|cancel <id> --reason "<why>" --epic <dir> [--close-worktree] [--force]
  cox story report status|done|stuck --epic <dir> --story <id> --note "<summary>" [--evidence k=v ...]
  cox story report question --body "<question>" --epic <dir> --story <id>
  cox checkpoint facts|inject --epic <dir> --story <id>
  cox wake drain [--peek] | ack-through <gen> | wait [--max <dur>]  --epic <dir>
  cox watch --epic <dir> [--once]
  cox inbox ack <record-path>
  cox busy arm|apply|read <story> --epic <dir>
  cox hook prompt-drain|stop-rewake|precompact|session-start
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Printf("cox %s\n", version.Version)
		return 0
	case "workspace":
		return cmdWorkspace(args[1:])
	case "epic":
		return cmdEpic(args[1:])
	case "env":
		return cmdEnv(args[1:])
	case "audit":
		return cmdAudit(args[1:])
	case "ship":
		return cmdShip(args[1:])
	case "reply":
		return cmdReply(args[1:])
	case "question":
		return cmdQuestion(args[1:])
	case "state":
		return cmdState(args[1:])
	case "route":
		return cmdRoute(args[1:])
	case "quota":
		return cmdQuota(args[1:])
	case "board":
		return cmdBoard(args[1:])
	case "lab":
		return cmdLab(args[1:])
	case "scorecard":
		return cmdScorecard(args[1:])
	case "baseline":
		return cmdBaseline(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "migrate":
		return cmdMigrate(args[1:])
	case "steer":
		return cmdSteer(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "control":
		return cmdControl(args[1:])
	case "busy":
		return cmdBusy(args[1:])
	case "reconcile":
		return cmdReconcile(args[1:])
	case "story":
		return cmdStory(args[1:])
	case "arena":
		return cmdArena(args[1:])
	case "artifact":
		return cmdArtifact(args[1:])
	case "plan":
		return cmdPlan(args[1:])
	case "review":
		return cmdReview(args[1:])
	case "checkpoint":
		return cmdCheckpoint(args[1:])
	case "wake":
		return cmdWake(args[1:])
	case "watch":
		return cmdWatch(args[1:])
	case "inbox":
		return cmdInbox(args[1:])
	case "hook":
		return cmdHook(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "cox: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
