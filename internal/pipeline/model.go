// Package pipeline defines the pipeline graph and reads it from a file.
//
// The graph is the contract: the engine, the API and the UI work on it, and
// it can be produced from a declarative file today and from code later.
package pipeline

import (
	"encoding/json"
	"path"
	"strings"
	"time"
)

// Pipeline is a validated, typed pipeline graph.
type Pipeline struct {
	Name       string       `json:"name"`
	On         Triggers     `json:"on"`
	Libraries  []*Library   `json:"libraries,omitempty"`  // shared libraries its steps use templates from
	Containers []*Container `json:"containers,omitempty"` // named toolboxes and services
	Steps      []*Step      `json:"steps"`                // in the order they were written
	// Groups are the steps that used a template, once expanded: each names
	// the steps the template became. Empty until Expand.
	Groups []*Group `json:"groups,omitempty"`
	// Origin says where the pipeline came from when it was not the
	// repository's own file or a draft; nil otherwise.
	Origin *Origin `json:"origin,omitempty"`
	// Supersede says that a new run started by a push or a pull request
	// cancels the older runs of the same ref that are still going, if they
	// too were started by a push or a pull request. Runs started by hand,
	// drafts and re-runs never supersede and are never superseded.
	Supersede bool `json:"supersede,omitempty"`
}

// Origin records that a pipeline came from the server's configuration of
// the project, because the repository has no pipeline file of its own.
type Origin struct {
	Kind string `json:"kind"` // OriginServer
	// Text is the pipeline file the configuration amounts to, kept with the
	// run because the configuration may change later.
	Text string `json:"text"`
}

// OriginServer is the Origin.Kind of a pipeline configured on the server.
const OriginServer = "server"

// Container is a named image that steps run in, or a service that runs
// alongside them for the whole run (a database for tests, say). All of a
// run's containers share the workspace; on Kubernetes they share a pod, so
// services are reachable at localhost.
type Container struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Env     map[string]string `json:"env,omitempty"`
	CPU     string            `json:"cpu,omitempty"`     // e.g. 500m or 2
	Memory  string            `json:"memory,omitempty"`  // e.g. 512Mi or 2Gi
	Service bool              `json:"service,omitempty"` // runs its own command for the whole run
	Command []string          `json:"command,omitempty"` // services only; default: the image's own
}

// Triggers says which events start a run. A run can always be started by hand.
type Triggers struct {
	Push *PushTrigger `json:"push,omitempty"`
	// PullRequest, if set, starts a run when a pull request is opened or
	// its head moves. It has no settings: the server sees only each pull
	// request's head (refs/pull/<n>/head), not the branch it targets, so
	// it cannot filter by branch.
	PullRequest *PullRequestTrigger `json:"pull_request,omitempty"`
}

// PullRequestTrigger starts a run for pull requests. It is empty for now.
type PullRequestTrigger struct{}

// PushTrigger starts a run when matching branches or tags are pushed.
type PushTrigger struct {
	Branches []string `json:"branches,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Step kinds. A plain step runs a script; an approval pauses the run until a
// person decides.
const (
	KindStep     = "step"
	KindApproval = "approval"
)

// Step runs a script in a container image, or, if it is an approval, waits
// for a person to approve or reject the run going further.
type Step struct {
	ID          string               `json:"id"`                    // after expansion, <uses-step>/<template-step> for a template's steps
	Description string               `json:"description,omitempty"` // one line saying what the step is for
	Kind        string               `json:"kind,omitempty"`        // empty for a plain step, or KindApproval
	Message     string               `json:"message,omitempty"`     // approvals: the question to decide
	Container   string               `json:"container,omitempty"`   // a named container; Image is then filled from it
	Image       string               `json:"image"`
	Run         string               `json:"run"`
	Needs       []string             `json:"needs,omitempty"`
	When        *Condition           `json:"when,omitempty"`
	Env         map[string]string    `json:"env,omitempty"`
	Secrets     map[string]SecretRef `json:"secrets,omitempty"` // variable name → where its value is kept
	Retry       int                  `json:"retry"`             // extra attempts after a failure
	Timeout     Duration             `json:"timeout"`           // per attempt; for an approval, how long to wait for a decision
	Line        int                  `json:"-"`                 // where it was written, for messages
	NeedsLine   int                  `json:"-"`                 // where its needs were written, for messages

	// Uses names the template a step is replaced by, as library/template
	// (or, in a template, a template of the same library). Only a pipeline
	// that has not been expanded has such steps; With is what it passes to
	// the template's parameters, as written.
	Uses string            `json:"uses,omitempty"`
	With map[string]string `json:"with,omitempty"`
	// Group is the uses step a template's step came from (the innermost,
	// when templates use templates); empty for a step written in the
	// pipeline itself.
	Group string `json:"group,omitempty"`
	// Library is the library whose files the step works with
	// (LAUNCHWAY_LIB): the library of the template it came from.
	Library string `json:"library,omitempty"`
	// NeedsGroups lists the uses steps this step needs as a whole. Needs
	// already lists their steps; these are named so the step receives the
	// outputs each template exposes, as LAUNCHWAY_NEEDS_<GROUP>_<KEY>.
	NeedsGroups []string `json:"needs_groups,omitempty"`

	withLines map[string]*yamlPos // where each with value was written
	usesLine  int
	ifTest    *paramTest // templates only: removes the step when false
	// What was written, so that a uses step's settings apply only where the
	// template step does not set its own.
	retrySet, timeoutSet bool
}

// IsUse reports whether the step is replaced by a template.
func (s *Step) IsUse() bool { return s.Uses != "" }

// Library is a shared library a pipeline uses templates from: a Git
// repository the server's configuration allows, at a version.
type Library struct {
	Name    string `json:"name"`    // what the pipeline calls it, as in uses: name/template
	Repo    string `json:"repo"`    // the server's name for the repository
	Version string `json:"version"` // a tag, branch or full commit id, as written
	// Override is the version the person who started the run asked for
	// instead of Version; empty if none.
	Override string `json:"override,omitempty"`
	// Commit is what the version (or override) pointed at when the run was
	// created, and RefKind says what it was: tag, branch or commit. Every
	// step of the run, and every resume, uses this commit.
	Commit  string `json:"commit,omitempty"`
	RefKind string `json:"ref_kind,omitempty"`
	Line    int    `json:"-"`
}

// Wanted is the version to resolve: the override if there is one.
func (l *Library) Wanted() string {
	if l.Override != "" {
		return l.Override
	}
	return l.Version
}

// Library returns the library the pipeline calls name, or nil.
func (p *Pipeline) Library(name string) *Library {
	for _, l := range p.Libraries {
		if l.Name == name {
			return l
		}
	}
	return nil
}

// Group is a step that used a template, after expansion: the template's
// steps carry its ID as their Group, and steps that needed it need them all.
type Group struct {
	ID          string `json:"id"`                    // the uses step's ID
	Parent      string `json:"parent,omitempty"`      // the group it is in, when a template used a template
	Description string `json:"description,omitempty"` // the uses step's, else the template's
	Uses        string `json:"uses"`                  // as written
	Library     string `json:"library"`
	Template    string `json:"template"`
	// Params are the values the template was expanded with, defaults
	// included.
	Params map[string]string `json:"params,omitempty"`
	// Steps are the steps it became, in order, including those of
	// templates it used. Empty if every step's if was false.
	Steps []string `json:"steps"`
	// Outputs are the step outputs the template exposes: key → step.key.
	Outputs map[string]OutputRef `json:"outputs,omitempty"`
}

// OutputRef names one output of one step.
type OutputRef struct {
	Step string `json:"step"`
	Key  string `json:"key"`
}

// Group returns the group with the given ID, or nil.
func (p *Pipeline) Group(id string) *Group {
	for _, g := range p.Groups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// Expanded reports whether the pipeline can run as it is: no step still
// waits to be replaced by its template.
func (p *Pipeline) Expanded() bool {
	for _, s := range p.Steps {
		if s.IsUse() {
			return false
		}
	}
	return true
}

// IsApproval reports whether the step waits for a person instead of running.
func (s *Step) IsApproval() bool { return s.Kind == KindApproval }

// SecretRef says where a secret is kept. It never holds the value: the
// runner looks the value up just before the step runs.
type SecretRef struct {
	Store string `json:"store"`           // a store configured on the runner, such as env or vault
	Key   string `json:"key"`             // the secret's name or path in that store
	Field string `json:"field,omitempty"` // a field within the secret, for stores that have them
}

// String names the reference as store:key#field.
func (r SecretRef) String() string {
	s := r.Store + ":" + r.Key
	if r.Field != "" {
		s += "#" + r.Field
	}
	return s
}

// Condition is a closed set of tests, deliberately not an expression language.
// Every listed field must match; within a field any value may match.
type Condition struct {
	Branch []string `json:"branch,omitempty"`
	Tag    []string `json:"tag,omitempty"`
	Event  []string `json:"event,omitempty"`
}

// The events a run can be started by.
const (
	EventPush   = "push"   // a push matched the pipeline's trigger
	EventManual = "manual" // someone started it by hand, from a branch or tag
	// EventDraft is a run of pipeline text that is not in Git: someone is
	// trying out an edit. A step with a when condition runs in a draft only
	// if its event list names draft, so deployments guarded by when do not
	// run from unsaved, unreviewed text.
	EventDraft = "draft"
	// EventPullRequest is a run of a pull request's head commit, which may
	// come from someone who cannot push to the repository. It has no branch
	// or tag, so when: { branch: ... } never matches it.
	EventPullRequest = "pull_request"
)

// Events lists every event, in the order they are documented.
var Events = []string{EventPush, EventManual, EventDraft, EventPullRequest}

// Context is what a condition is tested against.
type Context struct {
	Event  string // push, manual, draft or pull_request
	Branch string // empty when the run is for a tag
	Tag    string
}

// Match reports whether the condition holds. A nil condition always holds.
//
// In a draft run, a condition holds only if it lists the draft event
// explicitly: when: { branch: main } alone does not run in a draft of main.
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
	if len(c.Event) > 0 {
		return matchAny(c.Event, ctx.Event)
	}
	return ctx.Event != EventDraft
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

// MatchPullRequest reports whether pull requests start a run.
func (t Triggers) MatchPullRequest() bool { return t.PullRequest != nil }

// UsesSchema9 reports whether the pipeline uses keys that servers older
// than schema 9 do not know: on.pull_request and supersede.
func (p *Pipeline) UsesSchema9() bool { return p.On.PullRequest != nil || p.Supersede }

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

// Container returns the named container, or nil.
func (p *Pipeline) Container(name string) *Container {
	for _, c := range p.Containers {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// RunContainers is every container a run needs: the named ones, then one
// for each image that steps use directly, named image-1, image-2 and so on.
// Approvals run nothing, so they need no container.
func (p *Pipeline) RunContainers() []Container {
	var out []Container
	for _, c := range p.Containers {
		out = append(out, *c)
	}
	for _, s := range p.Steps {
		if !s.IsApproval() && !s.IsUse() && s.Container == "" && p.containerForImage(out, s.Image) == "" {
			out = append(out, Container{Name: "image-" + itoa(len(out)-len(p.Containers)+1), Image: s.Image})
		}
	}
	return out
}

// ContainerFor names the container a step runs in, matching RunContainers.
// It is empty for an approval, and for a step not yet replaced by a template.
func (p *Pipeline) ContainerFor(s *Step) string {
	if s.IsApproval() || s.IsUse() {
		return ""
	}
	if s.Container != "" {
		return s.Container
	}
	return p.containerForImage(p.RunContainers(), s.Image)
}

func (p *Pipeline) containerForImage(cs []Container, image string) string {
	for _, c := range cs {
		if p.Container(c.Name) == nil && c.Image == image {
			return c.Name
		}
	}
	return ""
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
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

// Short writes the duration without trailing zero parts: 24h, 1h30m, 45s.
func (d Duration) Short() string {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

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
