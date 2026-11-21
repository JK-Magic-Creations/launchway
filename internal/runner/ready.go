package runner

import (
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// readySpec gives an executor a service's readiness check.
func readySpec(r *pipeline.Ready) *executor.Ready {
	if r == nil {
		return nil
	}
	out := &executor.Ready{Run: r.Run, TCP: r.TCP, Every: r.Every.D(), Within: r.Within.D(), Described: r.Describe()}
	if r.HTTP != nil {
		out.HTTPPort, out.HTTPPath = r.HTTP.Port, r.HTTP.Path
	}
	return out
}
