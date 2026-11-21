package engine

import "sort"

// EnvironmentsFor returns the environments a project may deploy to, in the
// order the configuration lists them: the order a delivery goes through.
func (e *Engine) EnvironmentsFor(project string) []Environment {
	var out []Environment
	for _, env := range e.live().envs {
		if env.Allows(project) {
			out = append(out, env)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].Name < out[j].Name
	})
	return out
}
