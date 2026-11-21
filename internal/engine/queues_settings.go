package engine

import "time"

// QueueSettings are the installation's settings for queues and limits (schema
// 38); in the configuration they are the section queues. Zero values mean the
// defaults.
type QueueSettings struct {
	// MaxWait is the longest a step waits for room (the step namespace's
	// quota, nodes with space) before it fails with the reason; the default is
	// DefaultMaxCapacityWait. A negative value means no limit.
	MaxWait time.Duration
	// Projects are the projects' own settings, which replace the installation's
	// for each key they set.
	Projects map[string]QueueSettings
}

// queueSettings is the settings in force for a project: its own, over the
// installation's.
func (e *Engine) queueSettings(project string) QueueSettings {
	q := e.live().queues
	if p, ok := q.Projects[project]; ok {
		if p.MaxWait != 0 {
			q.MaxWait = p.MaxWait
		}
	}
	return q
}
