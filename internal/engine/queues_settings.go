package engine

import "time"

// QueueSettings are the installation's settings for queues and limits (schema
// 38); in the configuration they are the section queues. For every number, 0
// means "not set here", so the level above, then the default, decides, and a
// negative number means no limit.
type QueueSettings struct {
	// MaxWait is the longest a step waits for room (the step namespace's
	// quota, nodes with space) before it fails with the reason; the default is
	// DefaultMaxCapacityWait. A negative value means no limit.
	MaxWait time.Duration
	// DeployPickupWait is the longest a deploy or rollback step waits for a
	// deploy runner to take it before it fails saying so; the step's own
	// time limit starts when a runner takes it, not before. The default is
	// DefaultDeployPickupWait. A negative value means no limit.
	DeployPickupWait time.Duration
	// ProjectRuns, PipelineRuns and the per-kind numbers are how many runs may
	// go at the same time in a project, of one pipeline, and of each kind;
	// AutomationRuns is how many runs started by automation accounts. A run
	// over a limit is held, with the reason, and starts when room is free.
	// Pushes to protected branches are never held.
	ProjectRuns, PipelineRuns                int
	PullRequestRuns, PushRuns                int
	ScheduleRuns, ManualRuns, AutomationRuns int
	// SupersedePullRequests says that a new commit on a pull request cancels
	// the older run of the same pull request (default true).
	SupersedePullRequests *bool
	// Projects are the projects' own settings, which replace the installation's
	// for each key they set.
	Projects map[string]QueueSettings
}

// The defaults, chosen so that a burst of pull requests cannot starve the main
// build or fill the step namespace's quota without anyone configuring anything.
const (
	DefaultProjectRuns     = 10
	DefaultPullRequestRuns = 4
	DefaultAutomationRuns  = 2
)

// DefaultDeployPickupWait is how long a deploy step waits for a deploy runner to
// take it when queues.deploy_pickup_wait is not set.
const DefaultDeployPickupWait = 15 * time.Minute

// deployPickupDeadline is when a deploy action queued now fails because no
// deploy runner took it (Unix ms). The step's own time limit does not run until
// a runner takes the step. No limit ("off") is a deadline ten years away rather
// than none, so a deadline an earlier action left behind never applies.
func (c *cmd) deployPickupDeadline() int64 {
	w := c.e.queueSettings(c.st.Project).DeployPickupWait
	switch {
	case w < 0:
		w = 10 * 365 * 24 * time.Hour
	case w == 0:
		w = DefaultDeployPickupWait
	}
	return c.now + w.Milliseconds()
}

// queueSettings is the settings in force for a project: its own, over the
// installation's, with the defaults filled in.
func (e *Engine) queueSettings(project string) QueueSettings {
	q := e.live().queues
	p := q.Projects[project]
	pick := func(own, inst int) int {
		if own != 0 {
			return own
		}
		return inst
	}
	out := QueueSettings{
		ProjectRuns: pick(p.ProjectRuns, q.ProjectRuns), PipelineRuns: pick(p.PipelineRuns, q.PipelineRuns),
		PullRequestRuns: pick(p.PullRequestRuns, q.PullRequestRuns), PushRuns: pick(p.PushRuns, q.PushRuns),
		ScheduleRuns: pick(p.ScheduleRuns, q.ScheduleRuns), ManualRuns: pick(p.ManualRuns, q.ManualRuns),
		AutomationRuns: pick(p.AutomationRuns, q.AutomationRuns), SupersedePullRequests: q.SupersedePullRequests}
	out.MaxWait = time.Duration(pick(int(p.MaxWait), int(q.MaxWait)))
	out.DeployPickupWait = time.Duration(pick(int(p.DeployPickupWait), int(q.DeployPickupWait)))
	if p.SupersedePullRequests != nil {
		out.SupersedePullRequests = p.SupersedePullRequests
	}
	return out
}

// limit is the limit in force for a number: the default when unset, none (0)
// when negative.
func limit(set, def int) int {
	switch {
	case set < 0:
		return 0
	case set == 0:
		return def
	}
	return set
}

// supersedesPullRequests says whether a new commit on a pull request cancels
// the older run (the default).
func (q QueueSettings) supersedesPullRequests() bool {
	return q.SupersedePullRequests == nil || *q.SupersedePullRequests
}
