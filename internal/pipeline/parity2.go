package pipeline

// Keys brought by schema 27 (fetching from loops, a runner for a single
// step, approvers, service readiness and the template secret references).
// A pipeline that uses any of them carries "requires": 27, so servers that
// do not know them leave its runs alone instead of ignoring the key.

// SchemaParity2 is the server schema that brought those keys.
const SchemaParity2 = 27

// UsesSchemaParity2 reports whether the pipeline uses a key servers older
// than schema 27 do not know.
func (p *Pipeline) UsesSchemaParity2() bool {
	for _, s := range p.Steps {
		for _, f := range s.Fetch {
			if f.Member != nil || p.Loop(f.From) != nil {
				return true
			}
		}
	}
	return false
}

// requireParity2 raises the schema the pipeline needs to 27 if it uses a
// key of that schema.
func (p *Pipeline) requireParity2() {
	if p.UsesSchemaParity2() {
		p.Requires = max(p.Requires, SchemaParity2)
	}
}
