package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Controlling a deployment's Argo Rollouts rollout.
//
// A rollout step (kind rollout) acts on the rollout of a deployment made by
// a deploy step it needs: it promotes the rollout past an indefinite pause,
// promotes it fully (skipping the remaining steps and analyses), aborts it,
// or retries it after an abort, then watches it until it is healthy (or, for
// until: paused, until its next indefinite pause; for an abort, until it is
// aborted). A deploy step may also stop at the rollout's first indefinite
// pause (rollout.until: paused), and says what to do at the pauses on its
// way to healthy (rollout.pauses). An approval may name a deployment: it
// then shows the live rollout and is rejected by itself if the rollout is
// aborted. A rollback step may abort the rollout before reverting
// (abort_first), and a deploy step may abort it when the run is canceled by
// hand (on_cancel: abort).

// KindRolloutStep is the kind of a step that acts on a deployment's rollout.
const KindRolloutStep = "rollout"

// Actions of rollout steps and of commands on a deployment.
const (
	ActionPromote     = "promote"
	ActionPromoteFull = "promote-full"
	ActionAbort       = "abort"
	ActionRetry       = "retry"
)

// RolloutActions are the actions a rollout step may take.
var RolloutActions = []string{ActionPromote, ActionPromoteFull, ActionAbort, ActionRetry}

// What a deployment does at an indefinite pause of its rollout.
const (
	PausesWait    = "wait"
	PausesPromote = "promote"
	PausesFail    = "fail"
)

// SchemaControl is the server schema that brought rollout steps and the
// other keys in this file (see UsesSchemaControl).
const SchemaControl = 14

var rolloutStepKeys = []string{"kind", "description", "needs", "timeout", "deployment", "action", "until", "pauses"}

// controlLines are where the keys of this file were written, for messages.
type controlLines struct {
	action, until, pauses, abortFirst, onCancel int
}

// IsRolloutStep reports whether the step acts on a deployment's rollout.
func (s *Step) IsRolloutStep() bool { return s.Kind == KindRolloutStep }

// namesDeployment reports whether the step's Deployment makes it a member of
// that deployment: a rollback, rollout or approval step.
func (s *Step) namesDeployment() bool {
	return s.Deployment != "" && (s.IsRollback() || s.IsRolloutStep() || s.IsApproval() || s.IsVerify())
}

// LinksDeployment reports whether the step is an approval that names a
// deployment.
func (s *Step) LinksDeployment() bool { return s.IsApproval() && s.Deployment != "" }

// StopsAtPause reports whether a deploy step ends at its rollout's first
// indefinite pause.
func (s *Step) StopsAtPause() bool { return s.Rollout != nil && s.Rollout.Until == "paused" }

// UsesSchemaControl reports whether the pipeline uses what this file adds.
// Some of it older servers would not refuse but misread (until: paused,
// on_cancel: abort, an approval's deployment), so a pipeline that uses any
// of it carries Requires, which they refuse.
func (p *Pipeline) UsesSchemaControl() bool {
	for _, s := range p.Steps {
		switch {
		case s.IsRolloutStep(), s.LinksDeployment(), s.AbortFirst, s.OnCancel == "abort":
			return true
		case s.Rollout != nil && (s.Rollout.Until == "paused" || s.Rollout.Pauses != ""):
			return true
		}
	}
	return false
}

// RolloutStepsOf returns the rollout steps acting on a deploy step's
// deployment, in pipeline order.
func (p *Pipeline) RolloutStepsOf(deployID string) []*Step {
	var out []*Step
	for _, s := range p.Steps {
		if s.IsRolloutStep() && s.Deployment == deployID {
			out = append(out, s)
		}
	}
	return out
}

// pausesValue reads wait, promote or fail.
func (ps *parser) pausesValue(n *yaml.Node, at string) string {
	switch v := ps.scalar(n, at); v {
	case PausesWait, PausesPromote, PausesFail:
		return v
	default:
		ps.add(n, at, "write wait (a person promotes from the run page), promote or fail")
		return ""
	}
}

// parseControlKey reads the keys this file adds to deploy-kind steps.
func (ps *parser) parseControlKey(k, n *yaml.Node, where string, s *Step) {
	switch k.Value {
	case "action":
		s.ctl.action = n.Line
		switch a := ps.scalar(n, where); {
		case contains(RolloutActions, a):
			s.Action = a
		default:
			ps.add(n, where, "write one of %s", strings.Join(RolloutActions, ", "))
		}
	case "until":
		s.ctl.until = n.Line
		switch u := ps.scalar(n, where); u {
		case "healthy", "paused":
			s.Until = u
		default:
			ps.add(n, where, "write healthy or paused (the rollout's next indefinite pause)")
		}
	case "pauses":
		s.ctl.pauses = n.Line
		s.Pauses = ps.pausesValue(n, where)
	case "abort_first":
		s.ctl.abortFirst = n.Line
		s.AbortFirst = ps.bool(n, where)
	}
}

// checkRolloutStep checks a rollout step's own keys; checkControl checks
// what it says about other steps.
func (ps *parser) checkRolloutStep(k *yaml.Node, at string, s *Step) {
	if s.Deployment == "" {
		ps.add(k, at+".deployment", "name the deploy step whose rollout this acts on, for example: deployment: deploy-prod")
	}
	if len(s.Needs) == 0 {
		ps.add(k, at+".needs", "list the steps that must succeed first; a rollout step must need its deploy step, directly or through other steps")
	}
	switch {
	case s.Action == "" && s.ctl.action == 0:
		ps.add(k, at+".action", "say what to do: action: promote, promote-full, abort or retry")
	case s.Action == ActionAbort && s.Until != "":
		ps.problems = append(ps.problems, Problem{Line: s.ctl.until, Path: "steps." + s.ID + ".until",
			Message: fmt.Sprintf("%s: an abort step ends once the rollout is aborted; it takes no until", s.ID)})
	case s.Action == ActionAbort && s.Pauses != "":
		ps.problems = append(ps.problems, Problem{Line: s.ctl.pauses, Path: "steps." + s.ID + ".pauses",
			Message: fmt.Sprintf("%s: an abort step meets no pauses; it takes no pauses", s.ID)})
	case s.Until == "paused" && s.Pauses != "":
		ps.problems = append(ps.problems, Problem{Line: s.ctl.pauses, Path: "steps." + s.ID + ".pauses",
			Message: fmt.Sprintf("%s: with until: paused the step ends at the next indefinite pause, so pauses has nothing to say; take one out", s.ID)})
	}
	if s.Action != "" && s.Action != ActionAbort && s.Until == "" {
		s.Until = "healthy"
	}
}

// checkControl validates what rollout steps, linked approvals and the
// control keys of deploy and rollback steps say about other steps. It runs
// once the needs are known to be sound.
func (ps *parser) checkControl(p *Pipeline, ids map[string]*Step) {
	at := func(line, fallback int) int {
		if line > 0 {
			return line
		}
		return fallback
	}
	for _, s := range p.Steps {
		switch {
		case s.IsDeploy() && s.Rollout != nil:
			r := s.Rollout
			if r.Until == "paused" && r.Pauses != "" {
				ps.problems = append(ps.problems, Problem{Line: at(r.pausesLine, s.Line), Path: "steps." + s.ID + ".rollout.pauses",
					Message: fmt.Sprintf("%s: pauses says what to do at the pauses on the way to healthy; with until: paused the step ends at the first one. Take one out", s.ID)})
			}
			if r.Until == "paused" && len(p.RolloutStepsOf(s.ID)) == 0 {
				ps.problems = append(ps.problems, Problem{Line: at(r.untilLine, s.Line), Path: "steps." + s.ID + ".rollout.until",
					Message: fmt.Sprintf("%s: until: paused leaves the rollout at its first pause; add a step with kind: rollout and deployment: %s "+
						"(for example action: promote) to carry it on, or watch until healthy with pauses: wait to promote from the run page", s.ID, s.ID)})
			}
		case s.IsDeploy() && s.OnCancel == "abort":
			ps.problems = append(ps.problems, Problem{Line: at(s.ctl.onCancel, s.Line), Path: "steps." + s.ID + ".on_cancel",
				Message: fmt.Sprintf("%s: on_cancel: abort aborts the deployment's rollout, but the step names no rollout", s.ID)})
		case s.IsRollback() && s.AbortFirst:
			if d := ids[s.Deployment]; d != nil && d.IsDeploy() && d.Rollout == nil {
				ps.problems = append(ps.problems, Problem{Line: at(s.ctl.abortFirst, s.Line), Path: "steps." + s.ID + ".abort_first",
					Message: fmt.Sprintf("%s: abort_first aborts the rollout of %s, but %s names no rollout", s.ID, d.ID, d.ID)})
			}
		case s.IsRolloutStep(), s.LinksDeployment():
			line := at(s.deploymentLine, s.Line)
			d := ids[s.Deployment]
			switch {
			case s.Deployment == "":
			case d == nil:
				ps.problems = append(ps.problems, Problem{Line: line, Path: "steps." + s.ID + ".deployment",
					Message: fmt.Sprintf("%s: there is no step %q", s.ID, s.Deployment)})
			case !d.IsDeploy():
				ps.problems = append(ps.problems, Problem{Line: line, Path: "steps." + s.ID + ".deployment",
					Message: fmt.Sprintf("%s: deployment %q is not a deploy step", s.ID, s.Deployment)})
			case s.IsRolloutStep() && d.Rollout == nil:
				ps.problems = append(ps.problems, Problem{Line: line, Path: "steps." + s.ID + ".deployment",
					Message: fmt.Sprintf("%s: %s names no rollout, so there is nothing to %s; add rollout: { name: ..., namespace: ... } to it", s.ID, d.ID, s.Action)})
			case !p.Upstream(s.ID)[d.ID]:
				ps.problems = append(ps.problems, Problem{Line: max(s.NeedsLine, line), Path: "steps." + s.ID + ".needs",
					Message: fmt.Sprintf("%s: must need %s, directly or through other steps", s.ID, d.ID)})
			}
		}
		// A rollout step or an approval that names a deployment acts only
		// when the steps it needs succeeded: none of them, directly or not,
		// may run after failures (when: { upstream: failed | always }), whose
		// success would let it promote or approve a deployment whose checks
		// failed. An approval that names a deployment takes no upstream rule.
		if s.IsRolloutStep() || s.LinksDeployment() {
			if s.LinksDeployment() && s.When.UpstreamRule() != UpstreamSucceeded {
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: "steps." + s.ID + ".when.upstream", Message: fmt.Sprintf(
					"%s: an approval that names a deployment is asked only after the steps it needs succeeded; it takes no upstream", s.ID)})
			}
			what := "promote, abort or retry the rollout"
			if s.IsApproval() {
				what = "be approved"
			}
			for _, id := range sortedKeys(p.Upstream(s.ID)) {
				if g := p.Step(id); g != nil && g.When.UpstreamRule() != UpstreamSucceeded {
					ps.problems = append(ps.problems, Problem{Line: g.Line, Path: "steps." + g.ID + ".when.upstream", Message: fmt.Sprintf(
						"%s runs after failures (upstream: %s), so %s, which needs it, could %s after a failure; make %s need %s instead",
						g.ID, g.When.Upstream, s.ID, what, g.ID, s.ID)})
				}
			}
		}
		// A rollback step cannot guard a rollout step that comes after it.
		if s.IsRolloutStep() {
			for _, n := range s.Needs {
				if dep := ids[n]; dep != nil && dep.IsRollback() {
					ps.problems = append(ps.problems, Problem{Line: max(s.NeedsLine, s.Line), Path: "steps." + s.ID + ".needs",
						Message: fmt.Sprintf("%s: a rollout step cannot come after a rollback (it needs %s)", s.ID, n)})
				}
			}
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
