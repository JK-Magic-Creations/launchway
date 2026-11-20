package pipeline

import (
	"regexp"

	"gopkg.in/yaml.v3"
)

// SchemaPools is the server schema that brought runner pools for build
// steps and the pipeline's runner key (see RunnerSelector).
const SchemaPools = 22

// RunnerSelector says which runners may run a pipeline's steps:
//
//	runner: { pool: gpu, labels: [arm64, cuda] }
//
// It is written once, for the whole pipeline, because a run's steps share
// one runner's workspace: every step of the run goes to the runner that took
// its first step, so they must all be able to go to the same runners. Pool
// names one of the server's runner pools (it must serve the project);
// without it, any pool that serves the project may run it. Labels are what
// the runner must have, every one of them.
type RunnerSelector struct {
	Pool   string   `json:"pool,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// labelRe is what a runner label or a pool name looks like.
var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62})$`)

// ValidLabel reports whether s can be a runner label or a pool name: lower
// case letters, digits, '.', '_' and '-', up to 63 characters, starting with
// a letter or digit.
func ValidLabel(s string) bool { return labelRe.MatchString(s) }

// MaxRunnerLabels bounds the labels a pipeline asks for.
const MaxRunnerLabels = 16

func (ps *parser) parseRunner(n *yaml.Node, p *Pipeline) {
	sel := &RunnerSelector{}
	ps.eachKey(n, "runner", []string{"pool", "labels"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "pool":
			sel.Pool = ps.str(v, "runner.pool")
			if sel.Pool != "" && !ValidLabel(sel.Pool) {
				ps.add(v, "runner.pool", "%q is not a pool name: use lower-case letters, digits, '.', '_' and '-'", sel.Pool)
			}
		case "labels":
			sel.Labels = ps.strList(v, "runner.labels")
			seen := map[string]bool{}
			for _, l := range sel.Labels {
				switch {
				case !ValidLabel(l):
					ps.add(v, "runner.labels", "%q is not a label: use lower-case letters, digits, '.', '_' and '-'", l)
				case seen[l]:
					ps.add(v, "runner.labels", "%q is listed twice", l)
				}
				seen[l] = true
			}
			if len(sel.Labels) > MaxRunnerLabels {
				ps.add(v, "runner.labels", "at most %d labels", MaxRunnerLabels)
			}
		}
	})
	if sel.Pool == "" && len(sel.Labels) == 0 {
		ps.add(n, "runner", "name a pool, labels or both, for example: runner: { pool: gpu, labels: [arm64] }")
		return
	}
	p.Runner = sel
}

// UsesSchemaPools reports whether the pipeline chooses its runners, which
// servers older than schema 22 would ignore and so hand its steps to any
// runner.
func (p *Pipeline) UsesSchemaPools() bool { return p.Runner != nil }
