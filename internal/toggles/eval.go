package toggles

// maxDepth is how many prerequisites deep an evaluation goes before it gives
// up: a loop of prerequisites is refused when a toggle is saved, so this only
// guards a ruleset that was written by hand.
const maxDepth = 10

// Evaluate returns the value of the toggle key for a context. A toggle that
// is not in the ruleset gives ReasonNotFound and a nil value: the caller's own
// default applies.
func Evaluate(rs *Ruleset, key string, ctx Context) Result {
	if ctx == nil {
		ctx = Context{}
	}
	return evaluate(rs, key, ctx, 0)
}

func evaluate(rs *Ruleset, key string, ctx Context, depth int) Result {
	f, ok := rs.Toggles[key]
	if !ok {
		return Result{Reason: ReasonNotFound}
	}
	if depth > maxDepth {
		return Result{Value: f.OffValue, Reason: ReasonError}
	}
	if !f.On {
		return Result{Value: f.OffValue, Reason: ReasonOff}
	}
	for _, p := range f.Prerequisites {
		got := evaluate(rs, p.Toggle, ctx, depth+1)
		if got.Reason == ReasonNotFound || got.Reason == ReasonError || !same(got.Value, p.Value) {
			return Result{Value: f.OffValue, Reason: ReasonPrerequisite}
		}
	}
	for _, r := range f.Rules {
		if allMatch(r.Clauses, ctx, rs.Segments, 0) {
			v, reason := serve(f, r.Serve, ctx)
			if reason == "" {
				reason = ReasonRule
			}
			return Result{Value: v, Reason: reason, RuleID: r.ID}
		}
	}
	v, reason := serve(f, f.Fallthrough, ctx)
	if reason == "" {
		reason = ReasonFallthrough
	}
	return Result{Value: v, Reason: reason}
}

// serve resolves what a rule or the fallthrough hands out. A reason comes
// back only when it is not the ordinary one.
func serve(f Flag, s Serve, ctx Context) (any, string) {
	if s.Rollout == nil {
		return s.Value, ""
	}
	by := s.Rollout.By
	if by == "" {
		by = "key"
	}
	text, ok := bucketText(ctx[by])
	if !ok || text == "" {
		return f.OffValue, ReasonNoBucketKey
	}
	return s.Rollout.pick(Bucket(f.Salt, text)), ""
}
