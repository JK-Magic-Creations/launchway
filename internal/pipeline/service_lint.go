package pipeline

import (
	"fmt"
	"slices"
	"strings"
)

// ServiceWarnings says what changes for a pipeline whose services (containers with service: true) are
// started for more than one step. On Kubernetes a Station that gives each step a pod of its own (the default)
// starts the services again with each step's pod, so a service's state, a migrated database, say, does not
// carry from one step to the next, and ten steps start a database ten times. A step that lists the services it
// needs (services: [db]) starts only those; one that says nothing starts all of them, as it always did, and
// those are the steps the warning names. The warning is about the pipeline, not a mistake in it: it names what
// to check and what to do.
func ServiceWarnings(p *Pipeline) Problems {
	var out Problems
	for _, c := range p.Containers {
		if !c.Service {
			continue
		}
		var steps []string
		for _, s := range p.Steps {
			if p.ContainerFor(s) != "" && (!s.ServicesSet || slices.Contains(s.Services, c.Name)) {
				steps = append(steps, s.ID)
			}
		}
		if len(steps) < 2 {
			continue
		}
		out = append(out, Problem{Path: "containers." + c.Name, Code: "service-per-step",
			Message: fmt.Sprintf("the service %s is started for %d steps (%s). On Kubernetes, where each step runs in a pod of its own, it is started again with each step, so what one step leaves in it (a migrated or seeded database, say) is not there for the next. "+
				"Name the steps that need it with services: [%s], and the others do not start it (services: [] for none); "+
				"a step that needs the state should make it itself, or the work that shares it belongs in one step; other executors, and a Station set to kubernetes.pod: per-run, keep one service for the whole Sortie",
				c.Name, len(steps), strings.Join(steps, ", "), c.Name)})
	}
	return out
}
