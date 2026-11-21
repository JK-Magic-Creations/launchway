package config

import (
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// QueuesConfig sets how long steps wait for room, how many runs go at once
// and in what order the others wait (see the guide "Queues and limits"). It
// is a section of the installation settings; each project may override it. A
// key left out takes the next level up, then the default. For the numbers, 0
// means no limit at all, and leaving the key out means the default.
type QueuesConfig struct {
	// MaxWait is the longest a step waits for room before it fails with the
	// reason, such as "30m". Default 60m; "off" waits for ever.
	MaxWait string `yaml:"max_wait"`
	// Runs is how many runs of a project go at once (default 10); PipelineRuns
	// how many of one pipeline (no limit); PullRequestRuns, PushRuns,
	// ScheduleRuns and ManualRuns how many of each kind (pull requests 4, the
	// rest no limit); AutomationRuns how many started by automation accounts
	// (default 2). Pushes to protected branches are never held.
	Runs            *int `yaml:"runs"`
	PipelineRuns    *int `yaml:"pipeline_runs"`
	PullRequestRuns *int `yaml:"pull_request_runs"`
	PushRuns        *int `yaml:"push_runs"`
	ScheduleRuns    *int `yaml:"schedule_runs"`
	ManualRuns      *int `yaml:"manual_runs"`
	AutomationRuns  *int `yaml:"automation_runs"`
	// SupersedePullRequests: a new commit on a pull request cancels the older
	// run of the same pull request (default true).
	SupersedePullRequests *bool `yaml:"supersede_pull_requests"`
}

// queuesWait reads max_wait.
func queuesWait(q QueuesConfig, where string) (time.Duration, error) {
	switch q.MaxWait {
	case "":
		return 0, nil
	case "off":
		return -1, nil
	}
	d, err := time.ParseDuration(q.MaxWait)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: max_wait must be a duration such as 30m, or off", where)
	}
	return d, nil
}

// engineQueues converts one level's queues settings.
func engineQueues(q QueuesConfig, where string) (engine.QueueSettings, error) {
	var out engine.QueueSettings
	var err error
	if out.MaxWait, err = queuesWait(q, where); err != nil {
		return out, err
	}
	num := func(key string, p *int) (int, error) {
		switch {
		case p == nil:
			return 0, nil
		case *p < 0:
			return 0, fmt.Errorf("%s: %s cannot be negative (0 means no limit)", where, key)
		case *p == 0:
			return -1, nil
		}
		return *p, nil
	}
	for _, f := range []struct {
		key string
		in  *int
		out *int
	}{
		{"runs", q.Runs, &out.ProjectRuns}, {"pipeline_runs", q.PipelineRuns, &out.PipelineRuns},
		{"pull_request_runs", q.PullRequestRuns, &out.PullRequestRuns}, {"push_runs", q.PushRuns, &out.PushRuns},
		{"schedule_runs", q.ScheduleRuns, &out.ScheduleRuns}, {"manual_runs", q.ManualRuns, &out.ManualRuns},
		{"automation_runs", q.AutomationRuns, &out.AutomationRuns},
	} {
		if *f.out, err = num(f.key, f.in); err != nil {
			return out, err
		}
	}
	out.SupersedePullRequests = q.SupersedePullRequests
	return out, nil
}

// EngineQueues converts the queues settings for the engine.
func (c *Config) EngineQueues() (engine.QueueSettings, error) {
	out, err := engineQueues(c.Queues, "queues")
	if err != nil {
		return out, err
	}
	for _, p := range c.Projects {
		if p.Queues == nil {
			continue
		}
		q, err := engineQueues(*p.Queues, "project "+p.Name+": queues")
		if err != nil {
			return out, err
		}
		if out.Projects == nil {
			out.Projects = map[string]engine.QueueSettings{}
		}
		out.Projects[p.Name] = q
	}
	return out, nil
}
