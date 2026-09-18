package main

import (
	"fmt"
	"strings"

	"github.com/nphattai/coxswain/internal/artifact"
)

// cmdPlan implements the plan review generators (M13):
//
//	cox plan --html <plan-dir> --epic <dir>
//	cox plan compare --html <plan-dir> <architecture.html> --epic <dir>
//
// Both write a self-contained artifact plus a sidecar under <epic>/reports/visual (ADR 0013, DESIGN Option C).
func cmdPlan(args []string) int {
	if len(args) > 0 && args[0] == "compare" {
		return planCompare(args[1:])
	}
	epicDir, htmlOut, pos := parsePlanArgs(args)
	if epicDir == "" || !htmlOut || len(pos) < 1 {
		return usageErr("cox plan --html <plan-dir> --epic <dir>")
	}
	out, err := artifact.GeneratePlan(epicDir, pos[0])
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println("wrote", out)
	return 0
}

// planCompare implements `cox plan compare --html <plan-dir> <architecture.html> --epic <dir>`.
func planCompare(args []string) int {
	epicDir, htmlOut, pos := parsePlanArgs(args)
	if epicDir == "" || !htmlOut || len(pos) < 2 {
		return usageErr("cox plan compare --html <plan-dir> <architecture.html> --epic <dir>")
	}
	out, err := artifact.GenerateCompare(epicDir, pos[0], pos[1])
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println("wrote", out)
	return 0
}

// parsePlanArgs scans --epic/--html anywhere in args (flags may follow the positionals, which the stdlib flag package
// cannot do) and returns the epic dir, whether --html was given, and the remaining positionals in order.
func parsePlanArgs(args []string) (epicDir string, htmlOut bool, pos []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--epic" && i+1 < len(args):
			i++
			epicDir = args[i]
		case strings.HasPrefix(a, "--epic="):
			epicDir = strings.TrimPrefix(a, "--epic=")
		case a == "--html":
			htmlOut = true
		default:
			pos = append(pos, a)
		}
	}
	return epicDir, htmlOut, pos
}
