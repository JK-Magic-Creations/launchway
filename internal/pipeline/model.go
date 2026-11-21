// Package pipeline defines the pipeline graph and reads it from a file.
//
// The graph is the contract: the engine, the API and the UI work on it, and
// it can be produced from a declarative file today and from code later.
package pipeline

import (
	"encoding/json"
	"path"
	"slices"
	"strconv"
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
	// SupersedeOff is a pipeline that says supersede: false: a new commit on a
	// pull request does not cancel the older run, whatever the installation's
	// queues setting says.
	SupersedeOff bool `json:"supersede_off,omitempty"`
	// Lane controls which runs of the project may go at the same time
	// (schema 24); nil: runs overlap freely. See Lane.
	Lane *Lane `json:"lane,omitempty"`
	// Timeout bounds how long a run may stay open (schema 24); 0: no limit.
	// Time spent waiting for an approval does not count.
	Timeout Duration `json:"timeout,omitempty"`
	// TimeoutLine is where the timeout was written, for messages.
	TimeoutLine int `json:"-"`
	// Requires is the schema a server must know to run the pipeline, when
	// the pipeline uses something older servers would misread rather than
	// refuse (see control.go). Servers that do not know this field refuse
	// the pipeline, which is the point.
	Requires int `json:"requires,omitempty"`
	// Runner chooses the runners the pipeline's steps may run on: a pool,
	// labels or both (schema 21). Nil: any runner whose pool serves the
	// project.
	Runner *RunnerSelector `json:"runner,omitempty"`
	// Inputs are the values a run is started with (schema 13), in the order
	// they were written. A person starting a run gives them; runs started
	// any other way use the defaults, or what the schedule or webhook says.
	Inputs []*Input `json:"inputs,omitempty"`
	// Loops are the steps that were repeated over lists (for_each), once
	// expanded: each names the steps it became. Empty until Bind.
	Loops []*Loop `json:"loops,omitempty"`

	// maxLoop is the server's limit on a loop's combinations, for loops in
	// templates; set by Bind.
	maxLoop int
}

// Origin records that a pipeline came from the server's configuration of
// the project, because the repository has no pipeline file of its own.
type Origin struct {
	Kind string `json:"kind"` // OriginServer
	// Text is the pipeline file the configuration amounts to, kept with the
	// run because the configuration may change later.
	Text string `json:"text"`
	// Default is set when the pipeline is a default shared by many projects:
	// "organisation", or "unit <name>". Empty: configured for this project.
	Default string `json:"default,omitempty"`
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
	// Ready, for a service, says when it is ready; the first step waits for
	// it (schema 27; see ready.go).
	Ready *Ready `json:"ready,omitempty"`

	// What the server works out for a run's container from its steps'
	// resources and the installation's defaults, and hands the runner with
	// the step (never written in a pipeline file; see engine.runContainers).
	// CPU and Memory above then hold what the container is promised.
	CPULimit    string `json:"cpu_limit,omitempty"`    // the most CPU it may use; empty: no limit
	MemoryLimit string `json:"memory_limit,omitempty"` // the most memory it may use; empty: the same as Memory
	// Threads is how many parallel jobs the tools in it should start, so
	// that they fit the limits (0: no opinion); see resources.Threads.
	Threads int `json:"threads,omitempty"`
	// ExplicitCPU and ExplicitMemory say that something the container's
	// CPU, or memory, came from what a step (or the container's own cpu or
	// memory) asked for, and not from the installation's defaults. A runner
	// whose operator or namespace allows less holds the container to what
	// was asked for, and lowers what the server chose on its own.
	ExplicitCPU    bool `json:"explicit_cpu,omitempty"`
	ExplicitMemory bool `json:"explicit_memory,omitempty"`
}

// Triggers says which events start a run. A run can always be started by hand.
type Triggers struct {
	Push *PushTrigger `json:"push,omitempty"`
	// PullRequest, if set, starts a run when a pull request is opened or
	// its head moves. It has no settings: the server sees only each pull
	// request's head (refs/pull/<n>/head), not the branch it targets, so
	// it cannot filter by branch.
	PullRequest *PullRequestTrigger `json:"pull_request,omitempty"`
	// Schedule starts runs at times written as cron expressions (schema
	// 13). Only the default branch's pipeline is read for them.
	Schedule []*Schedule `json:"schedule,omitempty"`
	// Webhook lets other systems start runs by calling a URL, with inputs
	// taken from the request's JSON body (schema 13). Only the default
	// branch's pipeline is read for them.
	Webhook []*WebhookTrigger `json:"webhook,omitempty"`
}

// PullRequestTrigger starts a run for pull requests. Without settings it is
// every pull request. The server sees each pull request's head
// (refs/pull/<n>/head) and learns the branch it targets from the project's
// forge connection, so it can filter by that (schema 24).
type PullRequestTrigger struct {
	// Into are patterns of the branches a pull request may target, as the
	// branches of on.push; empty means any.
	Into []string `json:"into,omitempty"`
	// Paths and PathsIgnore filter pull requests by the files they changed
	// since they left the target branch (see paths.go).
	Paths       []string `json:"paths,omitempty"`
	PathsIgnore []string `json:"paths_ignore,omitempty"`
}

// PushTrigger starts a run when matching branches or tags are pushed.
type PushTrigger struct {
	Branches []string `json:"branches,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// Paths and PathsIgnore filter pushes to branches by the files they
	// changed (see paths.go); tags are never filtered.
	Paths       []string `json:"paths,omitempty"`
	PathsIgnore []string `json:"paths_ignore,omitempty"`
}

// Step kinds. A plain step runs a script; an approval pauses the run until a
// person decides. Deploy and rollback steps are in deploy.go.
const (
	KindStep     = "step"
	KindApproval = "approval"
)

// Step runs a script in a container image, or, if it is an approval, waits
// for a person to approve or reject the run going further.
type Step struct {
	ID          string `json:"id"`                    // after expansion, <uses-step>/<template-step> for a template's steps
	Description string `json:"description,omitempty"` // one line saying what the step is for
	Kind        string `json:"kind,omitempty"`        // empty for a plain step, or KindApproval
	Message     string `json:"message,omitempty"`     // approvals: the question to decide
	// Runner, when set, gives the step a runner and a workspace of its own
	// (schema 27); see step_runner.go.
	Runner    *RunnerSelector      `json:"runner,omitempty"`
	Approvers *Approvers           `json:"approvers,omitempty"` // approvals: who may decide and how many must approve (schema 27)
	Container string               `json:"container,omitempty"` // a named container; Image is then filled from it
	Image     string               `json:"image"`
	Run       string               `json:"run"`
	Needs     []string             `json:"needs,omitempty"`
	When      *Condition           `json:"when,omitempty"`
	Env       map[string]string    `json:"env,omitempty"`
	Secrets   map[string]SecretRef `json:"secrets,omitempty"` // variable name → where its value is kept
	Retry     int                  `json:"retry"`             // extra attempts after a failure
	// RetryWait, RetryBackoff and RetryOn are the rest of the map form of
	// retry (schema 24): the pause before the first retry, what multiplies
	// it each time (0 or 1: nothing), and the causes that are retried
	// (empty: every cause, as for a whole number).
	RetryWait    Duration `json:"retry_wait,omitempty"`
	RetryBackoff float64  `json:"retry_backoff,omitempty"`
	RetryOn      []string `json:"retry_on,omitempty"`
	// OnFailure is continue when a step that fails for good (after its
	// retries) must not fail the run (schema 24); empty means stop.
	OnFailure string   `json:"on_failure,omitempty"`
	Timeout   Duration `json:"timeout"` // per attempt; for an approval, how long to wait for a decision
	// Reports are files the step writes that Launchway reads once it ends,
	// whether it passed or failed: test results, for now.
	Reports *Reports `json:"reports,omitempty"`
	// Resources are the CPU and memory the step asks for (schema 22); nil
	// when it asks for none, and the installation's default for its kind
	// applies. See resources.go.
	Resources *Resources `json:"resources,omitempty"`

	// ImageBuild, for an image step (kind image), is what it builds; see
	// image.go.
	ImageBuild *ImageBuild `json:"image_build,omitempty"`

	// Verify, for a verify step (kind verify), is what it watches and how
	// it decides; AI, for an ai step (kind ai), what it asks. See verify.go
	// and ai.go (schema 19).
	Verify *Verify `json:"verify,omitempty"`
	AI     *AIStep `json:"ai,omitempty"`

	// Deploy steps (kind deploy) only; see deploy.go.
	Environment string      `json:"environment,omitempty"` // a server's environment, which gives the lock and the record
	Service     string      `json:"service,omitempty"`     // what is deployed there; empty means the project's name
	Version     *Value      `json:"version,omitempty"`     // what "current version" shows
	ArgoCD      *ArgoCD     `json:"argocd,omitempty"`
	Rollout     *RolloutRef `json:"rollout,omitempty"`
	OnCancel    string      `json:"on_cancel,omitempty"` // leave
	// Deployment, for a rollback step, is the deploy step it rolls back;
	// for a rollout step, the deploy step whose rollout it acts on; for an
	// approval, the deploy step whose deployment it shows and guards.
	Deployment string `json:"deployment,omitempty"`
	// Rollout steps (kind rollout) and rollback steps only; see control.go.
	Action     string `json:"action,omitempty"`      // rollout: promote, promote-full, abort or retry
	Until      string `json:"until,omitempty"`       // rollout: healthy or paused
	Pauses     string `json:"pauses,omitempty"`      // rollout: wait, promote or fail
	AbortFirst bool   `json:"abort_first,omitempty"` // rollback: abort the rollout before reverting
	// Restore, for the rollback step of a rollback run, is what to restore;
	// see restore.go. Never written in a pipeline file.
	Restore *Restore `json:"restore,omitempty"`
	ctl     controlLines

	// continueFail says a loop's continue_for selected this member, or the
	// loop member it is part of (template steps): see tolerate.
	continueFail bool

	environmentLine, deploymentLine, versionLine, resourcesLine int
	// environmentInput is the run input a deploy step's environment comes
	// from (environment: { input: name }); Bind puts its value in place.
	environmentInput string
	Line             int `json:"-"` // where it was written, for messages
	NeedsLine        int `json:"-"` // where its needs were written, for messages

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
	// Artifacts are sets of files the step keeps once it has run, for
	// people to download and later steps to fetch (see Artifact).
	Artifacts []*Artifact `json:"artifacts,omitempty"`
	// Fetch puts artifacts of earlier steps of the run into the workspace
	// before the step runs (see Fetch).
	Fetch []*Fetch `json:"fetch,omitempty"`
	// Cache keeps directories between runs: restored before the step's
	// script, saved after it (see Cache; schema 25).
	Cache []*Cache `json:"cache,omitempty"`

	// Added is set on a step the server put in the pipeline (Standing
	// orders); Gate on the step that gives the Inspection's verdict
	// (Kind gate). Neither is ever written in a pipeline file. See standing.go.
	Added *Added `json:"added,omitempty"`
	Gate  *Gate  `json:"gate,omitempty"`

	// ForEach repeats the step over lists (schema 13). Only a pipeline that
	// has not been bound has such steps; Bind replaces each by one step per
	// combination of values.
	ForEach *ForEach `json:"for_each,omitempty"`
	// Each, for a step a loop made, is the combination of values it is for:
	// key → value. Its script receives them as LAUNCHWAY_EACH_<KEY>.
	Each map[string]string `json:"each,omitempty"`

	withLines map[string]*yamlPos // where each with value was written
	usesLine  int
	ifTest    *paramTest // templates only: removes the step when false
	// What was written, so that a uses step's settings apply only where the
	// template step does not set its own.
	retrySet, timeoutSet bool

	// Run inputs and loop values a with, env or image refers to, resolved
	// by Bind; see bind.go.
	withInputs map[string]*inputRef
	eachRefs   []eachRef
}

// Reports names the files a step writes for Launchway to read after it.
type Reports struct {
	// Tests are globs, relative to the workspace, of test result files:
	// JUnit XML, or the output of go test -json. A path part ** matches any
	// number of directories.
	Tests []string `json:"tests"`
	// Findings are globs of security scanner results in SARIF 2.1.0.
	Findings []string `json:"findings,omitempty"`
	// SBOM are globs of software bills of materials in CycloneDX JSON. They
	// are kept as the step's artifact "sbom" and summarised.
	SBOM []string `json:"sbom,omitempty"`

	// What schema 18 brought. A server older than that does not know these
	// fields, so it reads a pipeline that uses them as not understood.

	// TestAttachments is a directory, relative to the workspace, whose
	// files the test reports may attach to their results (screenshots,
	// videos, traces). It is kept as the artifact test-attachments, and
	// only files inside it are ever shown as attachments.
	TestAttachments string `json:"test_attachments,omitempty"`
	// Coverage are globs of coverage reports: Cobertura, JaCoCo or Clover
	// XML, LCOV, Go coverprofiles or Istanbul json-summary.
	Coverage []string `json:"coverage,omitempty"`
	// CoverageFailUnder fails the step when its coverage is below it.
	CoverageFailUnder *CoverageThreshold `json:"coverage_fail_under,omitempty"`
	// HTML are directories of HTML that a tool generated (Playwright's or
	// Karate's report, an Allure site, coverage HTML), published with the
	// run and shown sandboxed.
	HTML []HTMLReport `json:"html,omitempty"`
}

// CoverageThreshold is the least coverage a step must have, in percent; a
// zero field is not checked.
type CoverageThreshold struct {
	Lines     float64 `json:"lines,omitempty"`
	Branches  float64 `json:"branches,omitempty"`
	Functions float64 `json:"functions,omitempty"`
}

// HTMLReport is a directory of HTML a step publishes.
type HTMLReport struct {
	Name  string `json:"name"`  // its name in the run, and of its artifact report-<name>
	Path  string `json:"path"`  // the directory, relative to the workspace
	Index string `json:"index"` // the page it opens on, relative to the directory
	Line  int    `json:"-"`
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
	// Input tests a run input (schema 13): the step runs only if the input's
	// value is one of Equals. Values are written in their canonical form
	// (see Input.Canon), so they compare as text.
	Input  string   `json:"input,omitempty"`
	Equals []string `json:"equals,omitempty"`
	// Output tests an output of a step this one needs (schema 24), written
	// step.key: the step runs only if the output's value is one of Equals
	// (the same list Input uses; a condition has one or the other). An
	// output that was never written never matches.
	Output string `json:"output,omitempty"`
	// Upstream says how the steps this one needs must have ended for it
	// to run (schema 13): succeeded (the default), failed (at least one
	// failed, was cancelled or was not run because of a failure) or
	// always (whatever happened). See UpstreamAllows.
	Upstream string `json:"upstream,omitempty"`
	// Changed makes the step run only if a changed file matches one of the
	// globs (see paths.go). A run whose changes are not known runs it.
	Changed []string `json:"changed,omitempty"`

	inputLine int // where input or output was written, for messages
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
	// EventSchedule is a run started by one of the pipeline's schedules.
	EventSchedule = "schedule"
	// EventWebhook is a run started by a call to one of the pipeline's
	// webhook triggers.
	EventWebhook = "webhook"
)

// Events lists every event, in the order they are documented.
var Events = []string{EventPush, EventManual, EventDraft, EventPullRequest, EventSchedule, EventWebhook}

// Context is what a condition is tested against.
type Context struct {
	Event  string // push, manual, draft or pull_request
	Branch string // empty when the run is for a tag
	Tag    string
	// Inputs are the run's input values, canonical, by name.
	Inputs map[string]string
	// Output looks up an output a step wrote (schema 24); nil, or ok false,
	// means it was never written.
	Output func(step, key string) (value string, ok bool)
	// OutputKeys lists the outputs a step did write, so that a missing one
	// can say what was there instead; nil if not known.
	OutputKeys func(step string) []string
	// Changed is what the run's change touched; nil means not known.
	Changed *ChangeSet
}

// OutputRef splits the step and key of an output test, which are written
// step.key; ok is false if c tests none.
func (c *Condition) OutputRef() (step, key string, ok bool) {
	if c == nil || c.Output == "" {
		return "", "", false
	}
	step, key, ok = strings.Cut(c.Output, ".")
	return step, key, ok
}

// OutputWhy explains why the condition's output test fails, or returns ""
// if it holds or the condition has none. An output that was never written
// is reported as such, not as a mismatch.
func (c *Condition) OutputWhy(ctx Context) string {
	step, key, ok := c.OutputRef()
	if !ok {
		return ""
	}
	if ctx.Output != nil {
		if v, set := ctx.Output(step, key); set {
			if slices.Contains(c.Equals, v) {
				return ""
			}
			want := make([]string, len(c.Equals))
			for i, e := range c.Equals {
				want[i] = strconv.Quote(e)
			}
			if len(want) == 1 {
				return c.Output + " was " + strconv.Quote(v) + ", not " + want[0]
			}
			return c.Output + " was " + strconv.Quote(v) + ", not one of " + strings.Join(want, ", ")
		}
	}
	return c.missingOutput(ctx, step, key)
}

// missingOutput says which output was not written, and by which step, and
// what the step wrote instead: a mistyped key is the usual cause.
func (c *Condition) missingOutput(ctx Context, step, key string) string {
	msg := "the output " + strconv.Quote(key) + " of step " + step + " was not set: " + step + " did not write it"
	if ctx.OutputKeys == nil {
		return msg
	}
	keys := ctx.OutputKeys(step)
	if len(keys) == 0 {
		return msg + " (it wrote no outputs)"
	}
	slices.Sort(keys)
	return msg + " (it wrote: " + strings.Join(keys, ", ") + ")"
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
	if c.Input != "" {
		v, ok := ctx.Inputs[c.Input]
		if !ok || !slices.Contains(c.Equals, v) {
			return false
		}
	}
	if c.OutputWhy(ctx) != "" || c.ChangedWhy(ctx) != "" {
		return false
	}
	if len(c.Event) > 0 {
		return matchAny(c.Event, ctx.Event)
	}
	if (c.Upstream != "" || len(c.Changed) > 0) && len(c.Branch) == 0 && len(c.Tag) == 0 && c.Input == "" && c.Output == "" {
		return true // only about upstream: not a guard that keeps steps out of drafts
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
// PushPaths and PullRequestPaths are the path filters of the triggers.
func (t Triggers) PushPaths() PathFilter {
	if t.Push == nil {
		return PathFilter{}
	}
	return PathFilter{Paths: t.Push.Paths, Ignore: t.Push.PathsIgnore}
}

func (t Triggers) PullRequestPaths() PathFilter {
	if t.PullRequest == nil {
		return PathFilter{}
	}
	return PathFilter{Paths: t.PullRequest.Paths, Ignore: t.PullRequest.PathsIgnore}
}

func (t Triggers) MatchPullRequest() bool { return t.PullRequest != nil }

// FiltersPullRequests reports whether the pipeline runs only for pull
// requests that target some branches (on.pull_request.into), which the
// server can only learn from the project's forge connection.
func (t Triggers) FiltersPullRequests() bool {
	return t.PullRequest != nil && len(t.PullRequest.Into) > 0
}

// MatchPullRequestInto reports whether a pull request that targets the
// branch starts a run: any does unless the trigger says into.
func (t Triggers) MatchPullRequestInto(branch string) bool {
	if t.PullRequest == nil {
		return false
	}
	return len(t.PullRequest.Into) == 0 || matchAny(t.PullRequest.Into, branch)
}

// UsesSchema9 reports whether the pipeline uses keys that servers older
// than schema 9 do not know: on.pull_request and supersede.
func (p *Pipeline) UsesSchema9() bool { return p.On.PullRequest != nil || p.Supersede }

// UsesSchema10 reports whether the pipeline uses keys that servers older
// than schema 10 do not know: a step's reports.
func (p *Pipeline) UsesSchema10() bool {
	for _, s := range p.Steps {
		if s.Reports != nil {
			return true
		}
	}
	return false
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
		if !s.IsApproval() && !s.IsDeployKind() && !s.IsUse() && !s.IsImage() && !s.IsVerify() && !s.IsAI() && s.Container == "" && p.containerForImage(out, s.Image) == "" {
			out = append(out, Container{Name: "image-" + itoa(len(out)-len(p.Containers)+1), Image: s.Image})
		}
	}
	return out
}

// ContainerFor names the container a step runs in, matching RunContainers.
// It is empty for an approval, an image step (the runner builds it, in no
// container of the run), and a step not yet replaced by a template.
func (p *Pipeline) ContainerFor(s *Step) string {
	if s.IsApproval() || s.IsDeployKind() || s.IsUse() || s.IsImage() || s.IsVerify() || s.IsAI() {
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
