package pipeline

import "fmt"

// What a use of a template gives the steps of its template: services and resources.

// checkServices checks, once the pipeline is expanded, that every service a step lists is a service container
// of the run: the pipeline's own, or one of a template's.
func (x *expander) checkServices(out *Pipeline) {
	for _, s := range out.Steps {
		for _, name := range s.Services {
			if msg := serviceProblem(out, name); msg != "" {
				x.problems = append(x.problems, Problem{Line: s.Line, Path: "steps." + s.ID + ".services", Message: fmt.Sprintf("step %s: %s", s.ID, msg)})
			}
		}
	}
}

// serviceNames writes the services a step lists as the run names them: a template's own service containers are
// renamed for the run; a name it does not have is the pipeline's.
func (sc *scope) serviceNames(names []string) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if to, ok := sc.containers[n]; ok {
			out[i] = to
		}
	}
	return out
}

// planResources gives a script step the resources its use gives the template, where it sets none of its own.
func (x *expander) planResources(sc *scope, c, from *Step) {
	if sc.resources == nil || c.ResourceKind() == "" {
		return
	}
	c.Resources = overlayResources(c.Resources, sc.resources)
	if c.resourcesLine == 0 {
		c.resourcesLine = from.Line
	}
	if _, err := c.Resources.Parse(); err != nil {
		x.problem(sc, c.resourcesLine, 0, "steps."+from.ID+".resources", "the resources of this stage, with those its use gives the template: %v", err)
	}
}
