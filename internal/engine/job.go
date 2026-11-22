package engine

import "github.com/jk-magic-creations/launchway/internal/pipeline"

// job builds what the runner needs, including the outputs of needed steps.
func (c *cmd) job(stepID, leaseID string) *Job {
	st := c.st
	def := st.Pipeline.Step(stepID)
	s := st.Steps[stepID]
	env := map[string]string{}
	for _, v := range c.stepEnv(stepID) {
		env[v.Name] = v.Value
	}
	var exclude []string
	if w := st.Pipeline.Workspace; w != nil {
		exclude = w.Exclude
	}
	stepContainer, sizing := c.stepContainer(def)
	c.keepSizing(stepID, s.Attempt, sizing)
	return &Job{
		Sizing:         sizing,
		WorkspaceExclude: exclude,
		Container:        st.Pipeline.ContainerFor(def), Containers: c.runContainers(), StepContainer: stepContainer, ResourcesAsked: def.Resources != nil,
		LeaseID: leaseID, LeaseTTL: pipeline.Duration(c.e.ttl), Beat: pipeline.Duration(c.e.beat()),
		RunID: st.ID, Project: st.Project, RunNumber: st.Number, Pipeline: st.Pipeline.Name,
		// advance fails a draft's step that asks for secrets it may not
		// read before it is queued; stepSecrets is a second guard, so that
		// a runner is never asked to look a secret up for one.
		StepID: stepID, Attempt: s.Attempt, Image: def.Image, Script: def.Run, Env: env, Secrets: c.stepSecrets(def),
		Timeout: def.Timeout, Source: st.Source, SHA: st.Trigger.SHA, Event: st.Trigger.Event, Workspace: st.Workspace, Library: def.Library,
		Reports: c.jobReports(def), ImageBuild: c.imageJobFor(def), Cache: c.jobCache(def),
	}
}
