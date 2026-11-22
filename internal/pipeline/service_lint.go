package pipeline

import (
	"fmt"
	"strings"
)

// ServiceWarnings says what changes for a pipeline whose services (containers with service: true) are
// there for more than one step. On Kubernetes a Station that gives each step a pod of its own (the default)
// starts the services again with each step's pod, so a service's state, a migrated database, say, does not
// carry from one step to the next. Other executors keep one service for the whole run. The warning is
// about the pipeline, not a mistake in it: it names what to check and what to do.
func ServiceWarnings(p *Pipeline) Problems {
	var services []*Container
	for _, c := range p.Containers {
		if c.Service {
			services = append(services, c)
		}
	}
	if len(services) == 0 {
		return nil
	}
	var steps []string
	for _, s := range p.Steps {
		if p.ContainerFor(s) != "" {
			steps = append(steps, s.ID)
		}
	}
	if len(steps) < 2 {
		return nil
	}
	var out Problems
	for _, c := range services {
		out = append(out, Problem{Path: "containers." + c.Name, Code: "service-per-step",
			Message: fmt.Sprintf("the service %s is there for %d steps (%s). On Kubernetes, where each step runs in a pod of its own, it is started again with each step, so what one step leaves in it (a migrated or seeded database, say) is not there for the next. "+
				"A step that needs the state should make it itself, or the work that shares it belongs in one step; other executors, and a Station set to kubernetes.pod: per-run, keep one service for the whole Sortie",
				c.Name, len(steps), strings.Join(steps, ", "))})
	}
	return out
}
