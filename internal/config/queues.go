package config

import (
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// QueuesConfig sets how long steps wait for room, and (see the guide "Queues
// and limits") how many runs go at once and in what order the others wait. It
// is a section of the installation settings; each project may override it.
// A key left out takes the next level up, then the default.
type QueuesConfig struct {
	// MaxWait is the longest a step waits for room (the step namespace's
	// quota, nodes with space) before it fails with the reason, such as "30m".
	// Default 60m; "off" waits for ever.
	MaxWait string `yaml:"max_wait"`
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

// EngineQueues converts the queues settings for the engine.
func (c *Config) EngineQueues() (engine.QueueSettings, error) {
	var out engine.QueueSettings
	var err error
	if out.MaxWait, err = queuesWait(c.Queues, "queues"); err != nil {
		return out, err
	}
	for _, p := range c.Projects {
		if p.Queues == nil {
			continue
		}
		var q engine.QueueSettings
		if q.MaxWait, err = queuesWait(*p.Queues, "project "+p.Name+": queues"); err != nil {
			return out, err
		}
		if out.Projects == nil {
			out.Projects = map[string]engine.QueueSettings{}
		}
		out.Projects[p.Name] = q
	}
	return out, nil
}
