package source

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// InspectionPipelineName is the name of the pipeline of an Inspection started by hand: it has no steps of its own,
// only the ones Standing orders add.
const InspectionPipelineName = "inspection"

// InspectNow starts a run that holds only the Inspection (the checks Standing orders call for, and the verdict) at a
// branch or tag of a project, whatever the repository's own pipeline says or whether it has one.
func (m *Manager) InspectNow(ctx context.Context, rq StartRequest) (*engine.RunSummary, error) {
	p := m.c().projects[rq.Project]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, rq.Project)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.resolve(ctx, rq.Ref, false)
	if err != nil {
		return nil, err
	}
	nr := engine.NewRun{Project: rq.Project, Pipeline: &pipeline.Pipeline{Name: InspectionPipelineName}, Trigger: trigger(r, pipeline.EventManual, rq.Actor),
		Source: engine.Source{Kind: "git", URL: p.URL}}
	m.classifyRun(&nr)
	m.inspect(ctx, &nr)
	if len(nr.Pipeline.Steps) == 0 {
		why := strings.TrimSpace(nr.Note)
		if why == "" {
			why = "No check applies to what this repository holds, or Standing orders are switched off for it."
		}
		return nil, fmt.Errorf("%w: no Inspection was made: %s", engine.ErrConflict, why)
	}
	return m.eng.CreateRun(nr)
}
