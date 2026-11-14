// Package pipeline defines the pipeline graph and reads it from a file.
//
// The graph is the contract: the engine, the API and the UI work on it, and
// it can be produced from a declarative file today and from code later.
package pipeline

import (
	"encoding/json"
	"path"
	"time"
)

// Pipeline is a validated, typed pipeline graph.
type Pipeline struct {
	Name  string   `json:"name"`
	On    Triggers `json:"on"`
	Steps []*Step  `json:"steps"` // in the order they were written
}

// Triggers says which events start a run. A run can always be started by hand.
type Triggers struct {
	Push *PushTrigger `json:"push,omitempty"`
}

// PushTrigger starts a run when matching branches or tags are pushed.
type PushTrigger struct {
	Branches []string `json:"branches,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Step runs a script in a container image.
type Step struct {
	ID      string            `json:"id"`
	Image   string            `json:"image"`
	Run     string            `json:"run"`
	Needs   []string          `json:"needs,omitempty"`
	When    *Condition        `json:"when,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Retry   int               `json:"retry"`   // extra attempts after a failure
	Timeout Duration          `json:"timeout"` // per attempt
	Line    int               `json:"-"`       // where it was written, for messages
}

// Condition is a closed set of tests, deliberately not an expression language.
// Every listed field must match; within a field any value may match.
type Condition struct {
	Branch []string `json:"branch,omitempty"`
	Tag    []string `json:"tag,omitempty"`
	Event  []string `json:"event,omitempty"`
}

// Context is what a condition is tested against.
type Context struct {
	Event  string // "push" or "manual"
	Branch string // empty when the run is for a tag
	Tag    string
}

// Match reports whether the condition holds. A nil condition always holds.
func (c *Condition) Match(ctx Context) bool {
	if c == nil {
		return true
	}
	if len(c.Branch) > 0 && !matchAny(c.Branch, ctx.Branch) {
		return false
	}
	if len(c.Tag) > 0 && !matchAny(c.Tag, ctx.Tag) {
		return false
	}
	if len(c.Event) > 0 && !matchAny(c.Event, ctx.Event) {
		return false
	}
	return true
}

// MatchPush reports whether a push to the given branch or tag starts a run.
func (t Triggers) MatchPush(branch, tag string) bool {
	if t.Push == nil {
		return false
	}
	if branch != "" {
		return matchAny(t.Push.Branches, branch)
	}
	return matchAny(t.Push.Tags, tag)
}

func matchAny(patterns []string, value string) bool {
	if value == "" {
		return false
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, value); ok {
			return true
		}
	}
	return false
}

// Step returns the step with the given id, or nil.
func (p *Pipeline) Step(id string) *Step {
	for _, s := range p.Steps {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Levels groups steps so that every step comes after everything it needs.
// Within a level, steps keep the order they were written in.
func (p *Pipeline) Levels() [][]string {
	level := map[string]int{}
	var depth func(s *Step) int
	depth = func(s *Step) int {
		if d, ok := level[s.ID]; ok {
			return d
		}
		d := 0
		for _, n := range s.Needs {
			if dep := p.Step(n); dep != nil {
				if x := depth(dep) + 1; x > d {
					d = x
				}
			}
		}
		level[s.ID] = d
		return d
	}
	max := 0
	for _, s := range p.Steps {
		if d := depth(s); d > max {
			max = d
		}
	}
	out := make([][]string, max+1)
	for _, s := range p.Steps {
		out[level[s.ID]] = append(out[level[s.ID]], s.ID)
	}
	return out
}

// Duration is a time.Duration written as "10m" in files and JSON.
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}
