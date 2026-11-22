package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Why a step failed, in a form people and programs can read. The reason of
// a failed step is a plain sentence; the cause is a small structured
// object behind it: what kind of failure it was, the evidence (for an
// analysis, its metrics with their counts and the failing measurement), and
// what Launchway did next. A deploy step's cause is written when the
// observation that failed it arrives (the analysis it saw is the evidence,
// and the cluster may have rolled it back since); every other cause is
// worked out from the step's state when it is read.

// Kinds of failure.
const (
	CauseAnalysisFailed = "analysis_failed" // a rollout's analysis run failed
	CauseRolloutFailed  = "rollout_failed"  // the rollout was aborted or degraded, without a failed analysis
	CauseSyncFailed     = "sync_failed"     // Argo CD could not sync, or the application is not healthy
	CauseTimeout        = "timeout"         // the step ran out of time
	CauseScriptExit     = "script_exit"     // a command exited with an error
	CauseRefused        = "refused"         // policy, a gate or a rule did not allow the step
	CauseRunnerLost     = "runner_lost"     // the runner stopped responding
	CauseNoRunner       = "no_runner"       // no deploy runner took the step within queues.deploy_pickup_wait
	CauseCommand        = "command_failed"  // a command given to the rollout was not applied
	CauseOther          = "other"
)

// StepCause is why a step failed.
type StepCause struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"` // one plain sentence
	// Environment and Phase are for a deploy: where it went and what it
	// had reached.
	Environment string `json:"environment,omitempty"`
	Phase       string `json:"phase,omitempty"`
	// Analysis is the analysis run that failed, with every metric, its
	// counts and its measurements, and for a job metric the job and the end
	// of its log (or why there is none).
	Analysis *deploy.AnalysisRun `json:"analysis,omitempty"`
	ExitCode *int                `json:"exit_code,omitempty"`
	// Next is what Launchway did about it, once it has.
	Next *CauseNext `json:"next,omitempty"`
}

// CauseNext is what happened after a deploy step failed.
type CauseNext struct {
	Kind    string `json:"kind"` // rolled_back, rolling_back, rollback_failed, no_rollback
	Step    string `json:"step,omitempty"`
	Version string `json:"version,omitempty"`
	Summary string `json:"summary"`
}

func stepNoun(def *pipeline.Step) string {
	switch {
	case def == nil:
		return "step"
	case def.IsRollback():
		return "rollback"
	case def.IsRolloutStep():
		return "rollout"
	case def.IsDeploy():
		return "deploy"
	}
	return "step"
}

// deployCause explains a deploy, rollout or rollback step that a
// verdict failed, from the snapshot it was judged on.
func deployCause(def *pipeline.Step, env string, v deploy.Verdict, snap deploy.Snapshot) *StepCause {
	c := &StepCause{Kind: CauseOther, Environment: env, Phase: v.Phase}
	what := strings.TrimSpace(env + " " + stepNoun(def))
	hash := ""
	if snap.Rollout != nil {
		hash = snap.Rollout.CurrentPodHash
	}
	switch v.Phase {
	case deploy.PhaseSyncFailed, deploy.PhaseSourceBad, deploy.PhaseBlocked:
		c.Kind = CauseSyncFailed
		c.Summary = fmt.Sprintf("The %s was stopped: %s.", what, strings.TrimSuffix(v.Detail, "."))
	case deploy.PhaseAborted, deploy.PhaseDegraded, deploy.PhasePaused, deploy.PhaseRollout:
		run := deploy.FailedAnalysisRun(snap.Analysis, hash)
		if run != nil {
			c.Kind, c.Analysis = CauseAnalysisFailed, run
			c.Summary = fmt.Sprintf("The %s was stopped by its %s: %s.", what, analysisWords(run), failingWords(run))
		} else {
			c.Kind = CauseRolloutFailed
			if v.Phase == deploy.PhaseDegraded && snap.Rollout == nil {
				c.Kind = CauseSyncFailed
			}
			c.Summary = fmt.Sprintf("The %s was stopped: %s.", what, strings.TrimSuffix(v.Detail, "."))
		}
	default:
		c.Summary = fmt.Sprintf("The %s failed: %s.", what, strings.TrimSuffix(v.Detail, "."))
	}
	return c
}

// analysisWords names a failed analysis run's failing metrics, such as
// `"smoke" analysis`.
func analysisWords(a *deploy.AnalysisRun) string {
	var names []string
	for _, m := range a.Metrics {
		if metricBad(m) {
			names = append(names, `"`+m.Name+`"`)
		}
	}
	if len(names) == 0 {
		return "analysis"
	}
	return strings.Join(names, " and ") + " analysis"
}

func metricBad(m deploy.Metric) bool {
	return m.Phase == "Failed" || m.Phase == "Error" || m.Phase == "Inconclusive"
}

// failingWords says in a sentence how the failing metrics failed.
func failingWords(a *deploy.AnalysisRun) string {
	var parts []string
	for _, m := range a.Metrics {
		if !metricBad(m) {
			continue
		}
		p := fmt.Sprintf("%s failed %d of %d measurements, and it allows %d", m.Name, m.Failed+m.Error, m.Count, m.FailureLimit)
		if m.Phase == "Inconclusive" {
			p = fmt.Sprintf("%s was inconclusive in %d of %d measurements", m.Name, m.Inconclusive, m.Count)
		}
		if msg := deploy.FailingMessage(m); msg != "" {
			p += " (" + strings.TrimSuffix(msg, ".") + ")"
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return nonEmpty(a.Message, "it did not pass")
	}
	return strings.Join(parts, "; ")
}

// causeOf is why a failed step failed: the cause its event recorded, or
// one worked out from the step's state. It is nil for a step that did not
// fail.
func (st *RunState) causeOf(id string) *StepCause {
	s := st.Steps[id]
	if s == nil || s.Status != StepFailed {
		return nil
	}
	def := st.Pipeline.Step(id)
	var c *StepCause
	if s.Why != nil {
		w := *s.Why
		c = &w
	} else {
		c = st.deriveCause(s, def)
	}
	if def != nil && def.IsDeploy() {
		c.Next = st.nextAfter(id, s)
		if c.Next != nil && c.Next.Kind == "rolled_back" && !strings.Contains(c.Summary, "rolled back") {
			// The summary stays about the failure; Next says the rest.
			_ = c
		}
	}
	return c
}

func (st *RunState) deriveCause(s *StepState, def *pipeline.Step) *StepCause {
	c := &StepCause{Kind: CauseOther, ExitCode: s.ExitCode}
	name := s.ID
	if def != nil && def.IsDeployKind() && s.Dep != nil {
		c.Environment = s.Dep.Environment
		name = strings.TrimSpace(s.Dep.Environment + " " + stepNoun(def))
	}
	e := strings.TrimSpace(s.Error)
	switch {
	case strings.HasPrefix(e, policyBlockedPrefix) || strings.HasPrefix(e, runRefusedPrefix) || strings.Contains(e, "do not run in") || strings.HasPrefix(e, "deploy steps"):
		c.Kind, c.Summary = CauseRefused, sentence(e)
	case def != nil && def.IsDeployKind() && strings.HasPrefix(e, noDeployRunnerPrefix):
		c.Kind, c.Summary = CauseNoRunner, fmt.Sprintf("The %s was never started: %s", name, sentence(e))
	case s.FailCause == pipeline.CauseInfrastructure || strings.Contains(e, "runner stopped") || strings.Contains(e, "stopped responding"):
		c.Kind, c.Summary = CauseRunnerLost, fmt.Sprintf("The runner of %s stopped responding%s", s.ID, tail(e))
	case s.FailCause == pipeline.CauseTimeout || strings.Contains(e, "timed out") || (def != nil && def.IsDeployKind() && strings.HasPrefix(e, "not ") && strings.Contains(e, " within ")):
		c.Kind, c.Summary = CauseTimeout, fmt.Sprintf("The %s ran out of time: %s", name, sentence(nonEmpty(e, "it did not finish in time")))
	case strings.HasPrefix(e, "the ") && strings.Contains(e, " was not applied"):
		c.Kind, c.Summary = CauseCommand, sentence(e)
	case def != nil && def.IsDeployKind():
		c.Summary = fmt.Sprintf("The %s failed: %s", name, sentence(nonEmpty(e, "no reason was recorded")))
		if strings.Contains(e, "sync") || strings.Contains(e, "Argo CD") {
			c.Kind = CauseSyncFailed
		} else if strings.Contains(e, "rollout") {
			c.Kind = CauseRolloutFailed
		}
	case s.ExitCode != nil && *s.ExitCode != 0:
		c.Kind, c.Summary = CauseScriptExit, fmt.Sprintf("Step %s ended with exit code %d%s", s.ID, *s.ExitCode, tail(e))
	default:
		c.Summary = fmt.Sprintf("Step %s failed: %s", s.ID, sentence(nonEmpty(e, "no reason was recorded")))
	}
	return c
}

func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "?") {
		s += "."
	}
	return s
}

func tail(e string) string {
	if e == "" {
		return "."
	}
	return ": " + e
}

// nextAfter says what the run did after a deploy step failed: a rollback
// step that reverted it, is reverting it, or failed.
func (st *RunState) nextAfter(deployID string, s *StepState) *CauseNext {
	for _, id := range st.Order {
		def := st.Pipeline.Step(id)
		if !def.IsRollback() || def.Deployment != deployID {
			continue
		}
		rs := st.Steps[id]
		version := ""
		if s.Dep != nil && s.Dep.Previous != nil {
			version = s.Dep.Previous.Version
		}
		to := "the previous version"
		if version != "" {
			to = version
		}
		switch rs.Status {
		case StepSucceeded:
			return &CauseNext{Kind: "rolled_back", Step: id, Version: version, Summary: fmt.Sprintf("Rolled back to %s by %s.", to, id)}
		case StepFailed:
			return &CauseNext{Kind: "rollback_failed", Step: id, Version: version, Summary: fmt.Sprintf("Rolling back to %s by %s failed: %s", to, id, sentence(rs.Error))}
		case StepRunning, StepQueuedS, StepWaitingS:
			return &CauseNext{Kind: "rolling_back", Step: id, Version: version, Summary: fmt.Sprintf("Rolling back to %s by %s.", to, id)}
		}
	}
	return &CauseNext{Kind: "no_rollback", Summary: "No rollback step ran for this deploy."}
}

// Evidence lists, one line each, what backs a cause up: for an analysis,
// each metric with its counts, the failing measurement's message, the job
// it ran, and the end of its log (or why there is none). For the command
// line and agents.
func (c *StepCause) Evidence() []string {
	if c == nil || c.Analysis == nil {
		return nil
	}
	a := c.Analysis
	out := []string{fmt.Sprintf("analysis run %s: %s", a.Name, strings.ToLower(a.Phase))}
	for _, m := range a.Metrics {
		line := fmt.Sprintf("metric %s: %s, %d of %d measurements failed (limit %d)", m.Name, strings.ToLower(m.Phase), m.Failed+m.Error, m.Count, m.FailureLimit)
		if msg := deploy.FailingMessage(m); msg != "" {
			line += "; " + msg
		}
		out = append(out, line)
		if m.Job != nil {
			out = append(out, fmt.Sprintf("  job %s/%s", m.Job.Namespace, m.Job.Name))
		}
		if m.JobLog != nil {
			if m.JobLog.Note != "" {
				out = append(out, "  "+m.JobLog.Note)
			}
			for _, l := range m.JobLog.Lines {
				out = append(out, "  | "+l)
			}
		}
	}
	return out
}

// firstFailedStep is the first step, in pipeline order, that failed (and
// was not allowed to) or was canceled; "" if none.
func (st *RunState) firstFailedStep() string {
	for _, id := range st.Order {
		if s := st.Steps[id]; (s.Status == StepFailed && !s.Allowed) || s.Status == StepCanceledS {
			return id
		}
	}
	return ""
}

// ResumeAdvice says whether a failed or canceled run can be resumed. A run
// that made a deployment cannot: a half-done deployment is never continued.
// Re-running from the first step that failed is what works then (a new
// deployment attempt of the same version, with every earlier step reused).
type ResumeAdvice struct {
	Allowed bool `json:"allowed"`
	// Reason and RerunFrom say, when it is not allowed, why, and the step to
	// re-run from instead.
	Reason    string `json:"reason,omitempty"`
	RerunFrom string `json:"rerun_from,omitempty"`
	// FirstFailed is the first step that failed or was canceled.
	FirstFailed string `json:"first_failed,omitempty"`
}

func (st *RunState) resumeAdvice() *ResumeAdvice {
	if st.Status != RunFailed && st.Status != RunCanceled {
		return nil
	}
	a := &ResumeAdvice{Allowed: true, FirstFailed: st.firstFailedStep()}
	if st.isRollbackRun() {
		a.Allowed, a.Reason = false, st.rollbackRunRefusal()
		return a
	}
	if id := st.openedDeployment(); id != "" {
		a.Allowed, a.RerunFrom = false, st.rerunTarget(id)
		a.Reason = fmt.Sprintf("%s made a deployment in this run, and a half-done deployment is never continued", id)
	}
	return a
}

// rerunTarget is the step to re-run from instead of resuming: the first
// step that failed, or, when that step belongs to a deployment (it checks
// or rolls one back), the deploy step that made it, since a deployment is
// made again from its deploy step. fallback is used when nothing failed.
func (st *RunState) rerunTarget(fallback string) string {
	f := st.firstFailedStep()
	if f == "" {
		return fallback
	}
	for _, d := range st.Pipeline.Steps {
		if d.IsDeploy() && d.ID != f && slices.Contains(st.Pipeline.DeploymentScope(d.ID), f) {
			return d.ID
		}
	}
	return f
}

// resumeRefusal is why a run that opened a deployment cannot be resumed,
// naming the action that works. The Resume call and the run's actions list
// both say exactly this.
func (st *RunState) resumeRefusal(opened string) string {
	failed := st.rerunTarget(opened)
	return fmt.Sprintf("%s made a deployment in this run, and a half-done deployment is never continued, so this run cannot be resumed. "+
		"Use \"Re-run from %s\" (the button, launchway runs rerun %s --from %s, or retry_run with from_step): it makes a new deployment of the same version and reuses "+
		"everything before it, so nothing is rebuilt", opened, failed, st.ID, failed)
}

// Action is something a person may do with a resource, as the server
// computes it in one place: the web interface, MCP and the command line
// all read this list, so they cannot disagree. An action that is not
// available says why and what to do instead; one that is available but
// risky says what it will do (Risk) and still goes ahead once confirmed.
type Action struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	// Reason, when unavailable, is the plain reason; Instead names the
	// action that works, and WouldHelp what would make this one available.
	Reason    string `json:"reason,omitempty"`
	Instead   string `json:"instead,omitempty"`
	WouldHelp string `json:"would_help,omitempty"`
	// Who is the role it needs.
	Who string `json:"who"`
	// Risk is the consequence of a risky action, and Suggestion what to
	// consider first; the action is not blocked by them.
	Risk       string `json:"risk,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	// Targets, for a rollback, are the earlier deployments it can restore,
	// the previous successful one first. Queueable says the action is not
	// available only because another deployment holds the environment's
	// lock, and asking to queue behind it will work. Emergency says it is
	// an emergency action on a protected environment (reviewed afterwards),
	// RequiresApproval that an installation or environment setting asks for
	// an approval first even then.
	Targets          []RollbackTarget `json:"targets,omitempty"`
	Queueable        bool             `json:"queueable,omitempty"`
	Emergency        bool             `json:"emergency,omitempty"`
	RequiresApproval bool             `json:"requires_approval,omitempty"`
}

// runActions computes the actions of a run from its state alone (the
// caller adds the person's role).
func (st *RunState) runActions() []Action {
	over := st.Status.Terminal()
	acts := []Action{}
	add := func(a Action) { a.Who = "operator"; acts = append(acts, a) }
	cancel := Action{Name: "cancel", Available: !over && !st.CancelRequested,
		Risk: "Running steps are stopped; a deployment in flight is aborted if the step says on_cancel: abort."}
	switch {
	case over:
		cancel.Reason = "the run has already finished"
	case st.CancelRequested:
		cancel.Reason = "the run is already being canceled"
	}
	add(cancel)
	failedOrCanceled := st.Status == RunFailed || st.Status == RunCanceled
	resume := Action{Name: "resume", Available: failedOrCanceled}
	rerun := Action{Name: "rerun", Available: over}
	if st.isRollbackRun() {
		why := st.rollbackRunRefusal()
		resume.Available, resume.Reason = false, why
		rerun.Available, rerun.Reason = false, why
		add(resume)
		add(rerun)
		return acts
	}
	if !failedOrCanceled {
		resume.Reason = "only a failed or canceled run can be resumed"
	} else if id := st.openedDeployment(); id != "" {
		resume.Available, resume.Reason, resume.Instead = false, st.resumeRefusal(id), "rerun from "+st.rerunTarget(id)
	}
	if !over {
		rerun.Reason = "only a finished run can be re-run"
	}
	add(resume)
	add(rerun)
	if st.Queue != nil && st.Queue.Held {
		acts = append(acts, st.queueActions()...)
	}
	acts = append(acts, st.trustAction()...)
	return acts
}

// failCause is the cause a failed observation gives its step.
func (c *cmd) failCause(stepID string, s *StepState, v deploy.Verdict, snap deploy.Snapshot) *StepCause {
	env := ""
	if s.Dep != nil {
		env = s.Dep.Environment
	}
	return deployCause(c.st.Pipeline.Step(stepID), env, v, snap)
}

// attachCause gives a failed step view its reason and cause.
func (st *RunState) attachCause(v *StepView) {
	if v.Status != StepFailed {
		return
	}
	if c := st.causeOf(v.ID); c != nil {
		v.Cause, v.Reason = c, c.Summary
	}
}
