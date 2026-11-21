package api

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// outputWarnings are the warnings validate gives about when: { output }
// tests that nothing the pipeline shows can satisfy, each with its line.
func outputWarnings(p *pipeline.Pipeline) []string {
	var out []string
	for _, w := range pipeline.OutputWarnings(p) {
		out = append(out, fmt.Sprintf("line %d, %s: %s", w.Line, w.Path, w.Message))
	}
	return out
}
