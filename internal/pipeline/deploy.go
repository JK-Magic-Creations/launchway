package pipeline

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/deploy/edit"
)

// Deploy steps. A deploy step changes what runs in an environment: it
// commits a version to a GitOps repository, asks Argo CD to sync if it must,
// and waits until the application (and its Argo Rollouts rollout, if it has
// one) is healthy on that version. A rollback step reverts what a deploy
// step committed when a step that guards the deployment did not succeed.
// Neither runs a script; a deploy runner that serves the environment's
// target does the work, with its own credentials.
const (
	KindDeploy   = "deploy"
	KindRollback = "rollback"
)

// Defaults and limits of deploy steps.
const (
	DefaultDeployTimeout   = 30 * time.Minute
	DefaultRollbackTimeout = 15 * time.Minute
	MaxDeployTimeout       = 24 * time.Hour
	MaxChanges             = 50
	MaxSyncResources       = 50
)

// Value is a value in a deploy step: written in place, or taken from an
// output of a step it needs, or from a fact about the run. There is no
// expression language.
type Value struct {
	Literal string     `json:"literal,omitempty"`
	Output  *OutputRef `json:"output,omitempty"` // { output: step.key }
	Run     string     `json:"run,omitempty"`    // { run: sha }: one of RunFacts
	// Input is a run input (schema 13): { input: version }. Bind puts the
	// run's value in Literal, so a run's graph never holds one.
	Input string `json:"input,omitempty"`
	// Fact, in a verify step's params only, is a fact about the deployment
	// it watches (schema 19): { deployment: canary_hash }. One of
	// DeploymentFacts.
	Fact string `json:"deployment,omitempty"`

	inputLine int
}

// RunFacts are the facts about a run a value may refer to.
//
// commit_time is the commit's committer time in UTC, as RFC 3339 with a Z and
// whole seconds (2026-11-20T06:12:56Z). A run whose source gives no commit
// has none, and a pipeline that uses it is refused when the run is created.
var RunFacts = []string{"sha", "short_sha", "branch", "tag", "number", "commit_time"}

// UsesRunFact returns the first step whose values refer to { run: fact }.
func (p *Pipeline) UsesRunFact(fact string) (string, bool) {
	for _, s := range p.Steps {
		var vals []*Value
		vals = append(vals, deployValues(s)...)
		vals = append(vals, imageValues(s)...)
		vals = append(vals, verifyValues(s)...)
		for _, v := range vals {
			if v != nil && v.Run == fact {
				return s.ID, true
			}
		}
	}
	return "", false
}

// String writes the value as it was written.
func (v *Value) String() string {
	switch {
	case v == nil:
		return ""
	case v.Output != nil:
		return "{ output: " + v.Output.Step + "." + v.Output.Key + " }"
	case v.Run != "":
		return "{ run: " + v.Run + " }"
	case v.Input != "":
		return "{ input: " + v.Input + " }"
	case v.Fact != "":
		return "{ deployment: " + v.Fact + " }"
	}
	return v.Literal
}

// ArgoCD says how a deploy step deploys with Argo CD.
type ArgoCD struct {
	Application string     `json:"application"`
	WriteBack   *WriteBack `json:"write_back,omitempty"`
	Sync        Sync       `json:"sync"`
	// Health is application (the application's sync and health decide) or
	// resources (only the resources listed under sync.resources do).
	Health string `json:"health"`
}

// WriteBack is the commit a deploy step makes.
type WriteBack struct {
	Repo    string   `json:"repo"` // a repository the deploy runner's configuration names
	Branch  string   `json:"branch"`
	Changes []Change `json:"changes"`
}

// Change is one edit of one file: either every reference to an image gets a
// tag, or a digest (schema 15: the reference becomes image@sha256:..., which
// no one can move), or the scalar at a YAML path gets a value.
type Change struct {
	File   string `json:"file"`
	Image  string `json:"image,omitempty"`
	Tag    *Value `json:"tag,omitempty"`
	Digest *Value `json:"digest,omitempty"`
	// Format says which form an image change writes: tag, digest, or
	// tag_and_digest (image:tag@sha256:..., readable and pinned). Without it
	// the values given decide: both make tag_and_digest.
	Format string `json:"format,omitempty"`
	Set    string `json:"set,omitempty"`
	Value  *Value `json:"value,omitempty"`
	Line   int    `json:"-"`
}

// Sync says whether and how a deploy step asks Argo CD to sync.
type Sync struct {
	// Request is auto (only if the application does not sync by itself),
	// always or never.
	Request   string     `json:"request"`
	Resources []Resource `json:"resources,omitempty"` // sync (and judge) only these; default: the whole application
	Prune     bool       `json:"prune,omitempty"`
	// Windows is wait or fail: what to do while Argo CD's sync windows do
	// not allow the sync. Windows are never overridden.
	Windows string `json:"windows"`
}

// Resource names one resource of an application.
type Resource struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// RolloutRef is the Argo Rollouts rollout a deploy step watches.
type RolloutRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Until     string `json:"until"` // healthy or paused (stop at the first indefinite pause)
	// Pauses says what to do at an indefinite pause on the way to healthy:
	// wait for a person (the default; empty means wait), promote, or fail.
	Pauses string `json:"pauses,omitempty"`

	untilLine, pausesLine int // where until: paused and pauses were written
}

// IsDeploy reports whether the step deploys.
func (s *Step) IsDeploy() bool { return s.Kind == KindDeploy }

// EnvironmentLine is where a deploy step's environment was written, or the
// step's line; 0 once the pipeline has been stored and read back.
func (s *Step) EnvironmentLine() int {
	if s.environmentLine > 0 {
		return s.environmentLine
	}
	return s.Line
}

// IsRollback reports whether the step rolls a deployment back.
func (s *Step) IsRollback() bool { return s.Kind == KindRollback }

// IsDeployKind reports whether the step is about a deployment and done by
// deploy runners (an action, or what they observe): a deploy, rollback or
// rollout step.
func (s *Step) IsDeployKind() bool { return s.IsDeploy() || s.IsRollback() || s.IsRolloutStep() }

// UsesSchema11 reports whether the pipeline has deploy or rollback steps,
// which servers older than schema 11 do not know.
func (p *Pipeline) UsesSchema11() bool {
	for _, s := range p.Steps {
		if s.IsDeployKind() {
			return true
		}
	}
	return false
}

// DeploymentScope is a deploy step's deployment: the deploy step, the
// rollback, rollout and approval steps naming it, and every step on a needs
// path between the deploy step and them, in pipeline order. The
// environment's lock is held until all of them are done.
func (p *Pipeline) DeploymentScope(deployID string) []string {
	in := map[string]bool{deployID: true}
	var down map[string]bool
	for _, member := range p.Steps {
		if member.Deployment != deployID || member.ID == deployID || !member.namesDeployment() {
			continue
		}
		if down == nil {
			down = p.Downstream(deployID)
		}
		up := p.Upstream(member.ID)
		for id := range down {
			if up[id] {
				in[id] = true
			}
		}
		in[member.ID] = true
	}
	var out []string
	for _, s := range p.Steps {
		if in[s.ID] {
			out = append(out, s.ID)
		}
	}
	return out
}

// Downstream returns every step that needs the step, directly or not.
func (p *Pipeline) Downstream(id string) map[string]bool {
	out := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, s := range p.Steps {
			if out[s.ID] {
				continue
			}
			for _, n := range s.Needs {
				if n == id || out[n] {
					out[s.ID], changed = true, true
					break
				}
			}
		}
	}
	return out
}

// Upstream returns every step the step needs, directly or not.
func (p *Pipeline) Upstream(id string) map[string]bool {
	out := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		s := p.Step(id)
		if s == nil {
			return
		}
		for _, n := range s.Needs {
			if !out[n] {
				out[n] = true
				walk(n)
			}
		}
	}
	walk(id)
	return out
}

// RollbackOf returns the rollback step of a deploy step, or nil.
func (p *Pipeline) RollbackOf(deployID string) *Step {
	for _, s := range p.Steps {
		if s.IsRollback() && s.Deployment == deployID {
			return s
		}
	}
	return nil
}

// ---- parsing ----

var (
	deployKeys   = []string{"kind", "description", "needs", "when", "timeout", "environment", "service", "version", "argocd", "rollout", "on_cancel"}
	rollbackKeys = []string{"kind", "description", "needs", "timeout", "deployment", "abort_first"}
	argocdKeys   = []string{"application", "write_back", "sync", "health"}
	writeKeys    = []string{"repo", "branch", "changes"}
	changeKeys   = []string{"file", "image", "tag", "digest", "format", "set", "value"}
	syncKeys     = []string{"request", "resources", "prune", "windows"}
	resourceKeys = []string{"group", "kind", "name", "namespace"}
	rolloutKeys  = []string{"name", "namespace", "until", "pauses"}
	// Keys of deploy steps that the design has and a later release brings.
	deployNotYet = map[string]string{
		"revision": "syncing an application to a revision without a commit arrives in a later release; use write_back",
		"source":   "syncing an application to a revision without a commit arrives in a later release; use write_back",
		"target":   "watching a rollout on another target arrives in a later release",
	}
	// k8sNameRe is a Kubernetes object name (DNS subdomain).
	k8sNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	// k8sNamespaceRe is a namespace name (DNS label).
	k8sNamespaceRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	kindRe         = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,62}$`)
	groupRe        = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	branchRe       = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	repoNameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// knownGroups are the API groups of kinds commonly synced alone, so that a
// resource can be named without its group.
var knownGroups = map[string]string{
	"Rollout": "argoproj.io", "AnalysisTemplate": "argoproj.io",
	"Deployment": "apps", "StatefulSet": "apps", "DaemonSet": "apps", "ReplicaSet": "apps",
	"Service": "", "ConfigMap": "", "ServiceAccount": "", "PersistentVolumeClaim": "",
	"Job": "batch", "CronJob": "batch", "Ingress": "networking.k8s.io",
}

// parseDeploy reads the keys of a deploy or rollback step. It is called from
// parseSteps with the step's mapping; the keys every step has (kind,
// description, needs, when, timeout) are read there.
func (ps *parser) parseDeployKeys(v *yaml.Node, at string, s *Step) {
	if v.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, n := v.Content[i], v.Content[i+1]
		where := at + "." + k.Value
		switch k.Value {
		case "environment":
			if n.Kind == yaml.MappingNode && hasKey(n, "input") && ps.tmpl != nil {
				ps.add(n, where, "a template cannot read run inputs; take the environment as a parameter of this template and write { param: name }")
			} else if n.Kind == yaml.MappingNode && hasKey(n, "input") {
				ps.eachKey(n, where, []string{"input"}, func(_, x *yaml.Node) { s.environmentInput = ps.scalar(x, where+".input") })
				s.Environment = "{" + s.environmentInput + "}"
			} else {
				s.Environment = ps.scalar(n, where)
			}
			s.environmentLine = n.Line
		case "service":
			s.Service = ps.scalar(n, where)
		case "version":
			s.Version = ps.value(n, where)
			s.versionLine = n.Line
		case "argocd":
			s.ArgoCD = ps.parseArgoCD(n, where)
		case "rollout":
			s.Rollout = ps.parseRollout(n, where)
		case "on_cancel":
			switch c := ps.scalar(n, where); c {
			case "leave":
				s.OnCancel = c
			case "abort":
				s.OnCancel = c
				s.ctl.onCancel = n.Line
			case "":
			default:
				ps.add(n, where, "write leave or abort")
			}
		case "deployment":
			s.Deployment = ps.scalar(n, where)
			s.deploymentLine = n.Line
		default:
			ps.parseControlKey(k, n, where, s)
		}
	}
}

// checkDeployKeys reports keys a deploy or rollback step does not take, and
// then the keys it needs.
func (ps *parser) checkDeployStep(k *yaml.Node, v *yaml.Node, at string, s *Step) {
	known := deployKeys
	switch {
	case s.IsRollback():
		known = rollbackKeys
	case s.IsRolloutStep():
		known = rolloutStepKeys
	}
	if v.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(v.Content); i += 2 {
			key := v.Content[i]
			switch {
			case contains(known, key.Value):
			case deployNotYet[key.Value] != "":
				ps.add(key, at+"."+key.Value, "%q is not supported yet (%s)", key.Value, deployNotYet[key.Value])
			case contains(loopKeys, key.Value):
				ps.add(key, at+"."+key.Value, "a %s step cannot be repeated by for_each: each deployment holds its environment's lock "+
					"and is recorded on its own; write one %s step for each environment", s.Kind, s.Kind)
			case contains(runOnly, key.Value) || key.Value == "uses" || key.Value == "with":
				ps.add(key, at+"."+key.Value, "a %s step runs no script, so it takes no %s", s.Kind, key.Value)
			case key.Value == "when" && s.IsRolloutStep():
				ps.add(key, at+".when", "a rollout step has no when: it runs when the steps it needs have succeeded")
			case key.Value == "when" && s.IsRollback():
				ps.add(key, at+".when", "a rollback step has no when: it runs exactly when its deployment was made and a step it needs did not succeed")
			default:
				ps.add(key, at+"."+key.Value, "%q is not a key of %s steps; expected one of: %s", key.Value, s.Kind, strings.Join(known, ", "))
			}
		}
	}
	if s.IsRolloutStep() {
		ps.checkRolloutStep(k, at, s)
		return
	}
	if s.IsRollback() {
		if s.Deployment == "" {
			ps.add(k, at+".deployment", "name the deploy step this rolls back, for example: deployment: deploy-prod")
		}
		if len(s.Needs) == 0 {
			ps.add(k, at+".needs", "list the steps that guard the deployment under needs; the rollback runs when one of them does not succeed")
		}
		return
	}
	switch {
	case s.Environment == "":
		ps.add(k, at+".environment", "name the environment to deploy to, for example: environment: prod")
	case s.environmentInput != "":
		// A choice input, whose options are checked to be environment
		// names (checkDeployInputs).
	case !nameRe.MatchString(s.Environment):
		ps.add(k, at+".environment", "%q is not a valid environment name: use lower-case letters, digits and hyphens", s.Environment)
	}
	if s.Service != "" && !nameRe.MatchString(s.Service) {
		ps.add(k, at+".service", "%q is not a valid service name: use lower-case letters, digits and hyphens", s.Service)
	}
	if s.Version == nil {
		s.Version = &Value{Run: "short_sha"}
	}
	if s.ArgoCD == nil {
		ps.add(k, at+".argocd", "say how to deploy: argocd: { application: ..., write_back: ... }")
	}
	if s.OnCancel == "" {
		s.OnCancel = "leave"
	}
}

// value reads a Value: a scalar, { output: step.key } or { run: fact }.
func (ps *parser) value(n *yaml.Node, at string) *Value {
	if n.Kind == yaml.ScalarNode {
		if strings.TrimSpace(n.Value) == "" {
			ps.add(n, at, "a value is required")
			return &Value{}
		}
		return &Value{Literal: n.Value}
	}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "write a value, { output: step.key }, { run: sha } or { input: name }")
		return &Value{}
	}
	if hasKey(n, "param") {
		// A template's parameter: its value, written in place (resolved when
		// the run is created).
		lit := ps.scalar(n, at)
		if strings.TrimSpace(lit) == "" {
			ps.add(n, at, "a value is required")
		}
		return &Value{Literal: lit}
	}
	v := &Value{}
	set := 0
	ps.eachKey(n, at, []string{"output", "run", "input"}, func(k, x *yaml.Node) {
		set++
		switch k.Value {
		case "input":
			if ps.tmpl != nil {
				ps.add(x, at+".input", "a template cannot read run inputs; take the value as a parameter of this template and write { param: name }")
			}
			v.Input, v.inputLine = ps.scalar(x, at+".input"), x.Line
		case "output":
			ref := ps.scalar(x, at+".output")
			step, key, ok := strings.Cut(ref, ".")
			if !ok || step == "" || strings.TrimSpace(key) == "" || strings.ContainsAny(key, " \t") {
				ps.add(x, at+".output", "write the step and the output's key, for example: { output: build.tag }")
				return
			}
			v.Output = &OutputRef{Step: step, Key: key}
		case "run":
			fact := ps.scalar(x, at+".run")
			if !contains(RunFacts, fact) {
				ps.add(x, at+".run", "%q is not a fact about the run; use %s", fact, strings.Join(RunFacts, ", "))
				return
			}
			v.Run = fact
		}
	})
	if set != 1 {
		ps.add(n, at, "write exactly one of output, run or input")
	}
	return v
}

func (ps *parser) parseArgoCD(n *yaml.Node, at string) *ArgoCD {
	a := &ArgoCD{Sync: Sync{Request: "auto", Windows: "wait"}}
	var healthNode *yaml.Node
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; deployNotYet[k.Value] != "" {
				ps.add(k, at+"."+k.Value, "%q is not supported yet (%s)", k.Value, deployNotYet[k.Value])
				n = withoutKey(n, k.Value)
				break
			}
		}
	}
	ps.eachKey(n, at, argocdKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "application":
			a.Application = ps.scalar(v, where)
			if a.Application != "" && !k8sNameRe.MatchString(a.Application) {
				ps.add(v, where, "%q is not a valid application name", a.Application)
			}
		case "write_back":
			a.WriteBack = ps.parseWriteBack(v, where)
		case "sync":
			ps.parseSync(v, where, &a.Sync)
		case "health":
			healthNode = v
			a.Health = ps.scalar(v, where)
		}
	})
	if n.Kind != yaml.MappingNode {
		return a
	}
	if a.Application == "" {
		ps.add(n, at+".application", "name the Argo CD application, for example: application: beacon")
	}
	if a.WriteBack == nil {
		ps.add(n, at+".write_back", "say what to commit: write_back: { repo: ..., branch: ..., changes: [...] }")
	}
	switch a.Health {
	case "":
		a.Health = "application"
		if len(a.Sync.Resources) > 0 {
			a.Health = "resources"
		}
	case "application":
	case "resources":
		if len(a.Sync.Resources) == 0 {
			ps.add(healthNode, at+".health", "health: resources judges the resources listed under sync.resources; list them there")
		}
	default:
		ps.add(healthNode, at+".health", "write application or resources")
	}
	return a
}

// withoutKey returns a copy of a mapping without one key, so that a key
// already reported is not reported again as unknown.
func withoutKey(m *yaml.Node, key string) *yaml.Node {
	c := *m
	c.Content = nil
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			c.Content = append(c.Content, m.Content[i], m.Content[i+1])
		}
	}
	return &c
}

func (ps *parser) parseWriteBack(n *yaml.Node, at string) *WriteBack {
	w := &WriteBack{}
	var changesNode *yaml.Node
	ps.eachKey(n, at, writeKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "repo":
			w.Repo = ps.scalar(v, where)
			if w.Repo != "" && !repoNameRe.MatchString(w.Repo) {
				ps.add(v, where, "%q is not a repository name; name one the deploy runner's configuration lists, in lower case", w.Repo)
			}
		case "branch":
			w.Branch = ps.scalar(v, where)
			if w.Branch != "" && (!branchRe.MatchString(w.Branch) || strings.Contains(w.Branch, "..") ||
				strings.HasPrefix(w.Branch, "/") || strings.HasSuffix(w.Branch, "/") || strings.HasPrefix(w.Branch, "-")) {
				ps.add(v, where, "%q is not a valid branch name", w.Branch)
			}
		case "changes":
			changesNode = v
			if v.Kind != yaml.SequenceNode {
				ps.add(v, where, "changes is a list, for example: - { file: apps/x.yaml, image: team/x, tag: { output: build.tag } }")
				return
			}
			if len(v.Content) > MaxChanges {
				ps.add(v, where, "at most %d changes", MaxChanges)
			}
			for i, c := range v.Content {
				w.Changes = append(w.Changes, ps.parseChange(c, fmt.Sprintf("%s[%d]", where, i)))
			}
		}
	})
	if n.Kind != yaml.MappingNode {
		return w
	}
	if w.Repo == "" {
		ps.add(n, at+".repo", "name the repository to commit to, as the deploy runner's configuration names it")
	}
	if w.Branch == "" {
		ps.add(n, at+".branch", "name the branch to commit to, for example: branch: main")
	}
	if changesNode == nil || len(w.Changes) == 0 {
		where := n
		if changesNode != nil {
			where = changesNode
		}
		ps.add(where, at+".changes", "list at least one change")
	}
	return w
}

func (ps *parser) parseChange(n *yaml.Node, at string) Change {
	c := Change{Line: n.Line}
	keys := map[string]*yaml.Node{}
	ps.eachKey(n, at, changeKeys, func(k, v *yaml.Node) {
		keys[k.Value] = v
		where := at + "." + k.Value
		switch k.Value {
		case "file":
			c.File = ps.scalar(v, where)
		case "image":
			c.Image = ps.scalar(v, where)
			if c.Image != "" {
				if err := edit.CheckImage(c.Image); err != nil {
					ps.add(v, where, "%s", err)
				}
			}
		case "tag":
			c.Tag = ps.value(v, where)
			if c.Tag.Literal != "" {
				if err := edit.CheckTag(c.Tag.Literal); err != nil {
					ps.add(v, where, "%s", err)
				}
			}
		case "digest":
			c.Digest = ps.value(v, where)
			if c.Digest.Literal != "" {
				if err := edit.CheckDigest(c.Digest.Literal); err != nil {
					ps.add(v, where, "%s", err)
				}
			}
		case "format":
			c.Format = ps.scalar(v, where)
		case "set":
			c.Set = ps.scalar(v, where)
			if c.Set != "" {
				if err := edit.CheckPath(c.Set); err != nil {
					ps.add(v, where, "%s", err)
				}
			}
		case "value":
			c.Value = ps.value(v, where)
		}
	})
	if n.Kind != yaml.MappingNode {
		return c
	}
	switch {
	case c.File == "":
		ps.add(n, at+".file", "name the file to change, relative to the repository's root")
	case !cleanRelPath(c.File):
		ps.add(keys["file"], at+".file", "%q must be a path relative to the repository's root, without .. or a leading /", c.File)
	}
	image, set := keys["image"] != nil || keys["tag"] != nil || keys["digest"] != nil, keys["set"] != nil || keys["value"] != nil
	switch {
	case image && set:
		ps.add(n, at, "a change either sets an image's tag or digest (image and tag, or image and digest) or a value at a YAML path (set and value), not both")
	case image && !validChangeFormat(c):
		ps.add(n, at, "format is %s, and tag_and_digest needs both a tag and a digest (tag needs a tag, digest a digest)", strings.Join(ChangeFormats, ", "))
	case image && (c.Image == "" || (c.Tag == nil && c.Digest == nil)):
		ps.add(n, at, "an image change needs image, and tag or digest")
	case set && (c.Set == "" || c.Value == nil):
		ps.add(n, at, "a set change needs both set and value")
	case !image && !set:
		ps.add(n, at, "say what to change: image and tag, or set and value")
	}
	return c
}

// ChangeFormats are the forms an image change may write.
var ChangeFormats = []string{"tag", "digest", "tag_and_digest"}

// ChangeForm is the form an image change writes.
func (c Change) ChangeForm() string {
	switch {
	case c.Format != "":
		return c.Format
	case c.Tag != nil && c.Digest != nil:
		return "tag_and_digest"
	case c.Digest != nil:
		return "digest"
	}
	return "tag"
}

func validChangeFormat(c Change) bool {
	switch c.Format {
	case "":
		return true
	case "tag":
		return c.Tag != nil
	case "digest":
		return c.Digest != nil
	case "tag_and_digest":
		return c.Tag != nil && c.Digest != nil
	}
	return false
}

func cleanRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.ContainsAny(p, "\x00\r\n") {
		return false
	}
	return path.Clean(p) == p && p != "." && !strings.HasPrefix(p, "../") && p != ".." && !strings.Contains(p, "/../")
}

func (ps *parser) parseSync(n *yaml.Node, at string, s *Sync) {
	ps.eachKey(n, at, syncKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "request":
			switch r := ps.scalar(v, where); r {
			case "auto", "always", "never":
				s.Request = r
			default:
				ps.add(v, where, "write auto (only if the application does not sync by itself), always or never")
			}
		case "prune":
			s.Prune = ps.bool(v, where)
		case "windows":
			switch w := ps.scalar(v, where); w {
			case "wait", "fail":
				s.Windows = w
			default:
				ps.add(v, where, "write wait or fail: what to do while a sync window does not allow syncing (windows are never overridden)")
			}
		case "resources":
			if v.Kind != yaml.SequenceNode {
				ps.add(v, where, "resources is a list, for example: - { kind: Rollout, name: shop, namespace: shop }")
				return
			}
			if len(v.Content) > MaxSyncResources {
				ps.add(v, where, "at most %d resources", MaxSyncResources)
			}
			for i, r := range v.Content {
				s.Resources = append(s.Resources, ps.parseResource(r, fmt.Sprintf("%s[%d]", where, i)))
			}
		}
	})
}

func (ps *parser) parseResource(n *yaml.Node, at string) Resource {
	r := Resource{}
	groupSet := false
	ps.eachKey(n, at, resourceKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		val := ps.scalar(v, where)
		switch k.Value {
		case "group":
			r.Group, groupSet = val, true
			if val != "" && !groupRe.MatchString(val) {
				ps.add(v, where, "%q is not an API group", val)
			}
		case "kind":
			r.Kind = val
			if !kindRe.MatchString(val) {
				ps.add(v, where, "%q is not a kind; write it as Kubernetes does, for example Rollout or Deployment", val)
			}
		case "name":
			r.Name = val
			if !k8sNameRe.MatchString(val) {
				ps.add(v, where, "%q is not a valid name", val)
			}
		case "namespace":
			r.Namespace = val
			if val != "" && !k8sNamespaceRe.MatchString(val) {
				ps.add(v, where, "%q is not a valid namespace", val)
			}
		}
	})
	if n.Kind != yaml.MappingNode {
		return r
	}
	if r.Kind == "" || r.Name == "" {
		ps.add(n, at, "a resource needs kind and name")
	}
	if !groupSet && r.Kind != "" {
		g, ok := knownGroups[r.Kind]
		if !ok {
			ps.add(n, at+".group", "give the API group of %s (empty for core kinds)", r.Kind)
		}
		r.Group = g
	}
	return r
}

func (ps *parser) parseRollout(n *yaml.Node, at string) *RolloutRef {
	r := &RolloutRef{Until: "healthy"}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; deployNotYet[k.Value] != "" {
				ps.add(k, at+"."+k.Value, "%q is not supported yet (%s)", k.Value, deployNotYet[k.Value])
				n = withoutKey(n, k.Value)
				break
			}
		}
	}
	ps.eachKey(n, at, rolloutKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "name":
			r.Name = ps.scalar(v, where)
			if r.Name != "" && !k8sNameRe.MatchString(r.Name) {
				ps.add(v, where, "%q is not a valid name", r.Name)
			}
		case "namespace":
			r.Namespace = ps.scalar(v, where)
			if r.Namespace != "" && !k8sNamespaceRe.MatchString(r.Namespace) {
				ps.add(v, where, "%q is not a valid namespace", r.Namespace)
			}
		case "until":
			switch u := ps.scalar(v, where); u {
			case "healthy", "paused":
				r.Until, r.untilLine = u, v.Line
			default:
				ps.add(v, where, "write healthy or paused (stop at the rollout's first indefinite pause)")
			}
		case "pauses":
			r.Pauses, r.pausesLine = ps.pausesValue(v, where), v.Line
		}
	})
	if n.Kind == yaml.MappingNode && (r.Name == "" || r.Namespace == "") {
		ps.add(n, at, "name the rollout and its namespace, for example: rollout: { name: shop, namespace: shop }")
	}
	return r
}

// checkDeploys validates what deploy and rollback steps say about other
// steps: the outputs they use, the deployments they roll back, and two
// deploy steps that could deploy the same service to the same environment at
// once. It runs after the needs have been checked and found free of loops.
func (ps *parser) checkDeploys(p *Pipeline) {
	ids := map[string]*Step{}
	for _, s := range p.Steps {
		ids[s.ID] = s
	}
	refs := func(s *Step) []struct {
		v    *Value
		line int
		at   string
	} {
		var out []struct {
			v    *Value
			line int
			at   string
		}
		add := func(v *Value, line int, at string) {
			if v != nil {
				out = append(out, struct {
					v    *Value
					line int
					at   string
				}{v, line, at})
			}
		}
		add(s.Version, max(s.versionLine, s.Line), "version")
		if s.ArgoCD != nil && s.ArgoCD.WriteBack != nil {
			for _, c := range s.ArgoCD.WriteBack.Changes {
				add(c.Tag, c.Line, "tag")
				add(c.Digest, c.Line, "digest")
				add(c.Value, c.Line, "value")
			}
		}
		return out
	}
	rollbacks := map[string]string{}
	for _, s := range p.Steps {
		switch {
		case s.IsDeploy():
			for _, r := range refs(s) {
				if r.v.Output == nil {
					continue
				}
				if ids[r.v.Output.Step] == nil {
					ps.problems = append(ps.problems, Problem{Line: r.line, Path: "steps." + s.ID,
						Message: fmt.Sprintf("%s: %s refers to %s.%s, but there is no step %s", s.ID, r.at, r.v.Output.Step, r.v.Output.Key, r.v.Output.Step)})
				} else if !contains(s.Needs, r.v.Output.Step) {
					ps.problems = append(ps.problems, Problem{Line: r.line, Path: "steps." + s.ID,
						Message: fmt.Sprintf("%s: %s refers to %s.%s, but %s is not in this step's needs", s.ID, r.at, r.v.Output.Step, r.v.Output.Key, r.v.Output.Step)})
				}
			}
		case s.IsRollback():
			line := s.deploymentLine
			if line == 0 {
				line = s.Line
			}
			d := ids[s.Deployment]
			switch {
			case s.Deployment == "":
			case d == nil:
				ps.problems = append(ps.problems, Problem{Line: line, Path: "steps." + s.ID + ".deployment",
					Message: fmt.Sprintf("%s: there is no step %q", s.ID, s.Deployment)})
			case !d.IsDeploy():
				ps.problems = append(ps.problems, Problem{Line: line, Path: "steps." + s.ID + ".deployment",
					Message: fmt.Sprintf("%s: deployment %q is not a deploy step", s.ID, s.Deployment)})
			case !p.Upstream(s.ID)[d.ID]:
				ps.problems = append(ps.problems, Problem{Line: max(s.NeedsLine, line), Path: "steps." + s.ID + ".needs",
					Message: fmt.Sprintf("%s: must need %s, directly or through other steps", s.ID, d.ID)})
			case rollbacks[d.ID] != "":
				ps.problems = append(ps.problems, Problem{Line: line, Path: "steps." + s.ID + ".deployment",
					Message: fmt.Sprintf("%s: %s already has a rollback step, %s", s.ID, d.ID, rollbacks[d.ID])})
			default:
				rollbacks[d.ID] = s.ID
			}
			for _, n := range s.Needs {
				if dep := ids[n]; dep != nil && dep.IsRollback() {
					ps.problems = append(ps.problems, Problem{Line: s.Line, Path: "steps." + s.ID + ".needs",
						Message: fmt.Sprintf("%s: a rollback cannot guard another (it needs %s)", s.ID, n)})
				}
			}
		}
	}
	ps.checkControl(p, ids)
	// Two deploy steps of the same service and environment must be ordered.
	var deploys []*Step
	for _, s := range p.Steps {
		if s.IsDeploy() {
			deploys = append(deploys, s)
		}
	}
	for i, a := range deploys {
		for _, b := range deploys[i+1:] {
			if a.Environment != b.Environment || a.Service != b.Service || a.Environment == "" {
				continue
			}
			if p.Upstream(a.ID)[b.ID] || p.Upstream(b.ID)[a.ID] {
				// Ordered; but one inside the other's deployment would wait
				// for a lock the other holds until it ends.
				for _, x := range [][2]*Step{{a, b}, {b, a}} {
					if contains(p.DeploymentScope(x[0].ID), x[1].ID) {
						ps.problems = append(ps.problems, Problem{Line: x[1].Line, Path: "steps." + x[1].ID,
							Message: fmt.Sprintf("%s deploys to %s inside deployment %s, which holds that environment's lock until its steps end; make it need %s's last step instead",
								x[1].ID, a.Environment, x[0].ID, x[0].ID)})
					}
				}
				continue
			}
			what := a.Service
			if what == "" {
				what = "the project"
			}
			ps.problems = append(ps.problems, Problem{Line: b.Line, Path: "steps." + b.ID,
				Message: fmt.Sprintf("%s and %s both deploy %s to %s and neither needs the other", a.ID, b.ID, what, a.Environment)})
		}
	}
}
