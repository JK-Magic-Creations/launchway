package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// fleetStep adds the problem a fleet deploy step has on this server and
// reports whether the step is one: a Station runs fleet steps with
// launchway fleet run; the server does not schedule them yet, and says so
// instead of accepting a run it could not do.
func fleetStep(out *pipeline.Problems, s *pipeline.Step, line int) bool {
	if s.Fleet == nil {
		return false
	}
	*out = append(*out, pipeline.Problem{Line: line, Path: "steps." + s.ID + ".fleet",
		Message: fmt.Sprintf("%s: this server does not schedule fleet deploy steps yet; a Station runs them with launchway fleet run", s.ID)})
	return true
}
