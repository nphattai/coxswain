package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/artifact"
)

// cmdArtifact implements `cox artifact list --epic <dir>`: the review pages under <epic>/reports/visual and their
// sidecars (kind, generator, source count, synthesis sha for an arena artifact). It is read-only.
func cmdArtifact(args []string) int {
	if len(args) == 0 || args[0] != "list" {
		return usageErr("cox artifact list --epic <dir>")
	}
	fs := flag.NewFlagSet("artifact list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox artifact list --epic <dir>")
	}
	cards, err := artifact.List(*epicDir)
	if err != nil {
		return fail("%v", err)
	}
	if len(cards) == 0 {
		fmt.Println("no artifacts under reports/visual (run cox epic design --html, cox plan --html, or cox arena synth --html)")
		return 0
	}
	for _, c := range cards {
		line := fmt.Sprintf("%-24s kind=%s generator=%s sources=%d", c.HTML, c.Kind, c.Generator, len(c.Sources))
		if c.SynthesisSHA != "" {
			line += " synthesis_sha=" + c.SynthesisSHA
		}
		line += " " + c.GeneratedAt
		fmt.Println(line)
	}
	return 0
}
