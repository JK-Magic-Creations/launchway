package pipeline

// whenIn is a condition written in the scope that has the given prefix, as
// the run sees it: an output test names a step of the run by its full ID.
// The condition is copied, because a template's steps are shared by every
// use of the template.
func whenIn(c *Condition, prefix string) *Condition {
	if c == nil || c.Output == "" || prefix == "" {
		return c
	}
	cc := *c
	cc.Output = prefix + c.Output
	return &cc
}

// whenFor is the condition of a step written in this scope: its own, as the
// run sees it, or else the one the scope's uses step gave.
func (sc *scope) whenFor(own *Condition) *Condition {
	if own == nil {
		return sc.when
	}
	return whenIn(own, sc.prefix)
}

// requireFlow raises the schema the expanded pipeline needs to the one flow
// control keys need (on_failure, a condition on an output, a retry that
// waits), if its steps use them.
func (p *Pipeline) requireFlow() {
	if p.UsesSchemaFlow() {
		p.Requires = max(p.Requires, SchemaFlow)
	}
}
