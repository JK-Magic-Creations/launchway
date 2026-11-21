package engine

// Fetching from loops: one written fetch of a loop becomes a fetch of each
// member, in a directory of its own.

import (
	"maps"
	"path"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// stepFetch is one fetch of a step as the run does it: a written fetch of a
// loop becomes one per member.
type stepFetch struct {
	pipeline.Fetch
	// member says the fetch came from a loop (the all-members form or one
	// member): a member that failed and was allowed to is fetched from as
	// well, and, in the all-members form, one that kept nothing is left out.
	member bool
	all    bool
}

// stepFetches resolves the fetches of a step, in the order written, members
// in the order of the loop. Each member of the all-members form goes in
// <into>/<member>, so two members never share a path.
func stepFetches(st *RunState, def *pipeline.Step) []stepFetch {
	var out []stepFetch
	for _, f := range def.Fetch {
		l := st.Pipeline.Loop(f.From)
		if l == nil || st.Pipeline.Step(f.From) != nil {
			out = append(out, stepFetch{Fetch: *f})
			continue
		}
		for _, m := range l.Members {
			if f.Member != nil && !maps.Equal(m.Each, f.Member) {
				continue
			}
			c := pipeline.Fetch{From: m.ID, Name: f.Name, Into: f.Into}
			if f.Member == nil {
				c.Into = path.Join(f.Into, l.MemberDir(m))
			}
			out = append(out, stepFetch{Fetch: c, member: true, all: f.Member == nil})
		}
	}
	return out
}

// resolveStepFetch finds a fetch's artifact. In the all-members form it
// reports skip for a member that was skipped, and for one that failed, was
// allowed to, and kept nothing; a member that succeeded without the
// artifact is a problem, as for any step.
func (c *cmd) resolveStepFetch(def *pipeline.Step, f stepFetch) (jf JobFetch, skip bool, err error) {
	if f.all {
		if s := c.st.Steps[f.From]; s != nil && s.Status == StepSkippedS {
			return jf, true, nil
		}
	}
	jf, err = c.resolveFetch(def, &f.Fetch, f.member)
	if err == nil && f.all && jf.Problem != "" {
		if s := c.st.Steps[f.From]; s != nil && s.Status == StepFailed && s.Allowed {
			return jf, true, nil
		}
	}
	return jf, false, err
}
