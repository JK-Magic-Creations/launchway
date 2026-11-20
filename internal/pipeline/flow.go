package pipeline

// Flow control keys of schema 24: conditions on step outputs, tolerated
// failures, retry waits and causes, a run's timeout, lanes and the target
// branch of a pull request. A pipeline that uses any of them carries
// "requires": 24, so servers that do not know them leave its runs alone.

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SchemaFlow is the server schema that brought flow control: when: { output },
// on_failure, retry with a wait and causes, a run timeout, lanes and
// on.pull_request.into.
const SchemaFlow = 24

// OnFailure values.
const (
	OnFailureStop     = "stop"
	OnFailureContinue = "continue"
)

// UsesSchemaFlow reports whether the pipeline uses a key servers older
// than schema 24 do not know.
func (p *Pipeline) UsesSchemaFlow() bool {
	if p.Timeout > 0 {
		return true
	}
	for _, s := range p.Steps {
		if s.OnFailure != "" || s.RetryFlow() || (s.When != nil && s.When.Output != "") {
			return true
		}
	}
	return false
}

// parseOnFailure reads on_failure of a step that runs something: stop (the
// default) or continue.
func (ps *parser) parseOnFailure(s *Step, v *yaml.Node, at string) {
	switch val := ps.str(v, at); val {
	case OnFailureStop:
		s.OnFailure = ""
	case OnFailureContinue:
		s.OnFailure = OnFailureContinue
	default:
		ps.add(v, at, "on_failure is stop (the default) or continue")
	}
}

// checkOutputTests checks the when conditions that test step outputs, once
// the graph is known: the step must be one this step needs, directly or
// through others, and not one that repeats.
func (ps *parser) checkOutputTests(p *Pipeline) {
	ids := map[string]*Step{}
	for _, s := range p.Steps {
		ids[s.ID] = s
	}
	for _, s := range p.Steps {
		c := s.When
		if c == nil || c.Output == "" {
			continue
		}
		at := "steps." + s.ID + ".when.output"
		line := c.inputLine
		if line == 0 {
			line = s.Line
		}
		add := func(format string, args ...any) {
			ps.problems = append(ps.problems, Problem{Line: line, Path: at, Message: fmt.Sprintf(format, args...)})
		}
		step, key, ok := strings.Cut(c.Output, ".")
		switch {
		case !ok || step == "" || strings.TrimSpace(key) == "" || strings.ContainsAny(key, " \t"):
			add("write the step and the output's key, for example: when: { output: scan.changed, equals: true }")
			continue
		case c.Input != "":
			add("a condition tests an input or an output, not both: put them in two steps, or test one of them")
			continue
		case len(c.Equals) == 0:
			add("say which values run the step, with equals: when: { output: %s, equals: true }", c.Output)
			continue
		}
		t := ids[step]
		switch {
		case step == s.ID:
			add("a step cannot test its own output")
		case t == nil:
			add("there is no step %q to test an output of", clipText(step, 60))
		case t.IsUse():
			add("%s uses a template: test an output of one of the steps the template made, which the template does not expose by name", step)
		case t.ForEach != nil:
			add("%s repeats over a list (for_each); an output of one of its members cannot be tested, because which member is not known when the pipeline is written. "+
				"Add a step that needs %s and writes the output you want to test", step, step)
		case !p.Upstream(s.ID)[step]:
			add("%s is not a step this step needs, directly or through other steps, so its output is not known when this step is decided: add it to needs", step)
		}
	}
}

// The causes a failed attempt can have, as retry's on lists them.
const (
	CauseExit           = "exit"           // the script ended non-zero
	CauseTimeout        = "timeout"        // the step ran longer than its timeout
	CauseInfrastructure = "infrastructure" // the machinery failed: image pull, workspace, secret store, a runner's refusal
)

// RetryCauses lists the causes, in the order they are documented.
var RetryCauses = []string{CauseExit, CauseTimeout, CauseInfrastructure}

// Bounds of the map form of retry, and of a run's timeout.
const (
	MaxRetryWait    = time.Hour
	MaxRetryBackoff = 10
	MaxRunTimeout   = 30 * 24 * time.Hour // the most a pipeline may ask for, and the most an installation may allow
	MinRunTimeout   = time.Minute
)

// retryKeys are the keys of retry's map form.
var retryKeys = []string{"times", "wait", "backoff", "on"}

// retryPolicy is what retry says: how often, how long between, and for
// which causes.
type retryPolicy struct {
	times   int
	wait    Duration
	backoff float64
	on      []string
}

func (s *Step) retryPolicy() retryPolicy {
	return retryPolicy{times: s.Retry, wait: s.RetryWait, backoff: s.RetryBackoff, on: s.RetryOn}
}

func (s *Step) setRetryPolicy(r retryPolicy) {
	s.Retry, s.RetryWait, s.RetryBackoff, s.RetryOn = r.times, r.wait, r.backoff, slices.Clone(r.on)
}

// RetryFlow reports whether the step's retry uses more than a count: a
// wait, a backoff or causes (schema 24).
func (s *Step) RetryFlow() bool { return s.RetryWait > 0 || s.RetryBackoff > 1 || len(s.RetryOn) > 0 }

// RetryDelay is the pause before the retry that follows the failure-th
// failure of a step (1 for the first): the wait, multiplied by the backoff
// for each failure before this one, and never more than an hour.
func (s *Step) RetryDelay(failure int) time.Duration {
	if s.RetryWait <= 0 || failure < 1 {
		return 0
	}
	d := float64(s.RetryWait)
	if s.RetryBackoff > 1 {
		d *= math.Pow(s.RetryBackoff, float64(failure-1))
	}
	return time.Duration(min(d, float64(MaxRetryWait)))
}

// RetriesCause reports whether a failure with the given cause may be
// retried: with no causes listed, every one may; explicit says that the
// cause was listed by name, which a refusal needs (see the engine).
func (s *Step) RetriesCause(cause string) bool {
	return len(s.RetryOn) == 0 || slices.Contains(s.RetryOn, cause)
}

// parseRetry reads retry: a whole number of extra attempts, or a mapping
// { times, wait, backoff, on }.
func (ps *parser) parseRetry(s *Step, n *yaml.Node, at string) {
	s.retrySet = true
	if n.Kind != yaml.MappingNode || hasKey(n, "param") || hasKey(n, "each") {
		s.Retry = ps.int(n, at)
		if s.Retry < 0 || s.Retry > MaxRetry {
			ps.add(n, at, "retry must be between 0 and %d", MaxRetry)
		}
		return
	}
	hasTimes := false
	var waitNode, backoffNode *yaml.Node
	ps.eachKey(n, at, retryKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "times":
			hasTimes = true
			s.Retry = ps.int(v, where)
			if s.Retry < 0 || s.Retry > MaxRetry {
				ps.add(v, where, "times must be between 0 and %d", MaxRetry)
			}
		case "wait":
			waitNode = v
			d, err := time.ParseDuration(ps.scalar(v, where))
			switch {
			case err != nil:
				ps.add(v, where, "write a duration such as 20s or 5m")
			case d < time.Second || d > MaxRetryWait:
				ps.add(v, where, "wait must be between 1s and %s", Duration(MaxRetryWait).Short())
			default:
				s.RetryWait = Duration(d)
			}
		case "backoff":
			backoffNode = v
			f, err := strconv.ParseFloat(ps.scalar(v, where), 64)
			switch {
			case err != nil:
				ps.add(v, where, "write a number from 1 to %d: what each wait is multiplied by", MaxRetryBackoff)
			case f < 1 || f > MaxRetryBackoff:
				ps.add(v, where, "backoff must be between 1 and %d", MaxRetryBackoff)
			case f > 1:
				s.RetryBackoff = f
			}
		case "on":
			list := ps.valueList(v, where)
			if len(list) == 0 {
				ps.add(v, where, "list at least one cause (%s), or leave on out to retry every cause", strings.Join(RetryCauses, ", "))
			}
			for _, c := range list {
				switch {
				case !slices.Contains(RetryCauses, c):
					ps.add(v, where, "%q is not a cause; use %s", clipText(c, 40), strings.Join(RetryCauses, ", "))
				case slices.Contains(s.RetryOn, c):
					ps.add(v, where, "%s is listed twice", c)
				default:
					s.RetryOn = append(s.RetryOn, c)
				}
			}
		}
	})
	if !hasTimes {
		ps.add(n, at, "say how many extra attempts: retry: { times: 3, wait: 30s }")
	}
	if backoffNode != nil && waitNode == nil {
		ps.add(backoffNode, at+".backoff", "backoff multiplies the wait between attempts, so it goes with wait")
	}
}

// parseRunTimeout reads the pipeline's own timeout: how long a run may stay
// open, from its creation, not counting time spent waiting for approvals.
func (ps *parser) parseRunTimeout(p *Pipeline, n *yaml.Node) {
	d, err := time.ParseDuration(ps.scalar(n, "timeout"))
	switch {
	case err != nil:
		ps.add(n, "timeout", "write a duration such as 2h or 90m")
	case d < MinRunTimeout || d > MaxRunTimeout:
		ps.add(n, "timeout", "the run timeout must be between %s and %s", Duration(MinRunTimeout).Short(), Duration(MaxRunTimeout).Short())
	default:
		p.Timeout, p.TimeoutLine = Duration(d), n.Line
	}
}
