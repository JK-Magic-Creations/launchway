package policy

import "gopkg.in/yaml.v3"

// countRule reads the rules that are a whole number in a mapping:
// approvals: { min: N } (1 to 20, one by default) and allowed_failures:
// { max: N } (0 to 100, none by default).
func (ps *parser) countRule(r *Rules, key string, v *yaml.Node, where string) {
	if key == RuleApprovals {
		a := &Approvals{Min: 1}
		ps.mapping(v, where, []string{"min"}, func(_ string, _, x *yaml.Node) {
			a.Min = ps.integer(x, where+".min", 1, 20)
		})
		r.Approvals = a
		return
	}
	af := &AllowedFailures{}
	ps.mapping(v, where, []string{"max"}, func(_ string, _, x *yaml.Node) {
		af.Max = ps.integer(x, where+".max", 0, 100)
	})
	r.AllowedFailures = af
}
