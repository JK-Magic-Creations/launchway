package engine

import "github.com/jk-magic-creations/launchway/internal/pipeline"

// deployAddress is the address a deploy step says its deployment can be seen at: the step's address value, written in
// place or taken from an output of a step it needs. An address that is not a web address (a mistake in what a step
// wrote, never in the pipeline text, which is checked when it is read) is left out rather than failing the deployment:
// the deployment goes ahead without a link.
func (c *cmd) deployAddress(def *pipeline.Step) string {
	if def.Address == nil {
		return ""
	}
	a, err := c.resolve(def, def.Address)
	if err != nil || pipeline.ValidAddress(a) != "" {
		return ""
	}
	return a
}
