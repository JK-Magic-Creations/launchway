// Package policy holds Launchway's delivery policies: typed, declarative
// rules that an organisation sets on the server, which pipelines cannot opt
// out of, and the pure function that judges a run against them.
//
// There is no policy language. A policy is a name, a scope, a mode (report
// or enforce) and rules taken from a fixed catalogue (Catalogue). Each rule
// is judged at one evaluation point:
//
//   - PointRun: when a run is created, re-run or resumed, on the pipeline's
//     shape (its libraries, templates, images, timeouts and secrets);
//   - PointDeploy: before a deploy step opens its deployment, and before a
//     rollout step promotes or retries it, on the run's evidence (tests,
//     findings, SBOMs, the images it built, approvals, the ref);
//   - PointImage: before an image step that would push is handed to a
//     runner, on what the step will do (scan, sign, SBOM, provenance,
//     registries).
//
// Evaluate is a pure function of the policies, the input (the facts the
// engine gathered from the run's events), the waivers in force and the
// time: the engine records all four in the run's policy.evaluated event,
// so every decision can be reproduced from the event log alone, and every
// server reaches the same verdict from the same inputs.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"time"
)

// Evaluation points.
const (
	PointRun    = "run"
	PointDeploy = "deploy"
	PointImage  = "image"
)

// Modes.
const (
	ModeReport  = "report"  // judged and shown; never changes an outcome
	ModeEnforce = "enforce" // a failure stops the step, or the run
)

// Verdicts of a whole evaluation.
const (
	VerdictPass  = "pass"  // nothing failed (or every failure was waived)
	VerdictWarn  = "warn"  // a policy in report mode failed
	VerdictBlock = "block" // a policy in enforce mode failed
)

// Verdicts of one policy or one rule.
const (
	ResultPass   = "pass"
	ResultFail   = "fail"
	ResultWaived = "waived"
)

// Rule names: the catalogue. See Catalogue for what each one judges.
const (
	RuleLibraryVersions   = "library_versions"
	RuleRequiredTemplates = "required_templates"
	RuleStepImages        = "step_images"
	RuleMaxTimeout        = "max_timeout"
	RuleForbidSecrets     = "forbid_secrets"
	RuleTests             = "tests"
	RuleFindings          = "findings"
	RuleSBOM              = "sbom"
	RuleDeployedImages    = "deployed_images"
	RuleApprovals         = "approvals"
	RuleRefs              = "refs"
	RuleImagePush         = "image_push"
	// RuleSource is not a rule anyone writes: it is how a policy source that
	// could not be loaded fails closed. It cannot be waived.
	RuleSource = "source"
)

// RuleInfo describes one rule of the catalogue.
type RuleInfo struct {
	Name    string `json:"name"`
	Point   string `json:"point"`
	Summary string `json:"summary"`
}

// Catalogue is every rule a policy may use, in the order documented.
var Catalogue = []RuleInfo{
	{RuleLibraryVersions, PointRun, "every shared library the run uses is pinned by a tag (or a commit), never a branch"},
	{RuleRequiredTemplates, PointRun, "the pipeline uses the named templates of the named libraries, at allowed versions"},
	{RuleStepImages, PointRun, "the images steps and containers run in are pinned by digest and/or come from allowed registries"},
	{RuleMaxTimeout, PointRun, "no step may run (or wait) longer than this"},
	{RuleForbidSecrets, PointRun, "runs of the listed events (pull_request, draft) may not read secrets"},
	{RuleTests, PointDeploy, "the steps before the deployment reported test results: at least min, none failed"},
	{RuleFindings, PointDeploy, "the steps before the deployment ran scanners, and nothing they found is worse than allowed"},
	{RuleSBOM, PointDeploy, "a step before the deployment made an SBOM"},
	{RuleDeployedImages, PointDeploy, "the images the deployment writes are pinned by digest, built by this run, signed and scanned"},
	{RuleApprovals, PointDeploy, "people other than the one who started the run approved it before the deployment"},
	{RuleRefs, PointDeploy, "the run is of an allowed branch or tag"},
	{RuleImagePush, PointImage, "an image step that pushes scans (enforced), signs, makes an SBOM and provenance, and pushes only to allowed registries"},
}

// PointOf is the evaluation point a rule is judged at, or "".
func PointOf(rule string) string {
	for _, r := range Catalogue {
		if r.Name == rule {
			return r.Point
		}
	}
	return ""
}

// RuleNames lists the catalogue's rule names, for messages.
func RuleNames() string {
	names := make([]string, len(Catalogue))
	for i, r := range Catalogue {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

// Policy is one policy. Its JSON form is recorded in every evaluation that
// judges it, so a decision can be checked later against exactly the rules
// that made it.
type Policy struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Mode        string `json:"mode"`
	Scope       Scope  `json:"scope"`
	Rules       Rules  `json:"rules"`
	// Where it was written: the source ("config", or a source's name), the
	// file and line, and the source's version (a commit for a Git source;
	// a hash of the policies for the configuration).
	Source  string `json:"source"`
	Version string `json:"version,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// Scope says which runs a policy applies to. Every non-empty list must
// match; an empty list matches everything.
type Scope struct {
	Projects     []string `json:"projects,omitempty"`     // patterns: shop, pay-*
	Environments []string `json:"environments,omitempty"` // patterns; only deploy rules may be scoped to environments
	Branches     []string `json:"branches,omitempty"`     // patterns; a run without a branch (a tag, a pull request) does not match
	Events       []string `json:"events,omitempty"`       // push, manual, draft, pull_request, schedule, webhook
}

// Rules are a policy's rules; each is nil (or empty) when not used.
type Rules struct {
	LibraryVersions   *LibraryVersions   `json:"library_versions,omitempty"`
	RequiredTemplates []RequiredTemplate `json:"required_templates,omitempty"`
	StepImages        *StepImages        `json:"step_images,omitempty"`
	MaxTimeout        string             `json:"max_timeout,omitempty"` // a duration, as written
	ForbidSecrets     *ForbidSecrets     `json:"forbid_secrets,omitempty"`
	Tests             *Tests             `json:"tests,omitempty"`
	Findings          *Findings          `json:"findings,omitempty"`
	SBOM              *SBOM              `json:"sbom,omitempty"`
	DeployedImages    *DeployedImages    `json:"deployed_images,omitempty"`
	Approvals         *Approvals         `json:"approvals,omitempty"`
	Refs              *Refs              `json:"refs,omitempty"`
	ImagePush         *ImagePush         `json:"image_push,omitempty"`
}

// LibraryVersions: Pinned is tag (a tag or a full commit id) or commit.
type LibraryVersions struct {
	Pinned string `json:"pinned"`
}

// RequiredTemplate is a template the pipeline must use: Library is the
// server's name for the library repository (as under libraries: in the
// configuration), not what the pipeline calls it. Versions, if given, are
// patterns the version used must match; it must then also be a tag or a
// commit, never a branch.
type RequiredTemplate struct {
	Library  string   `json:"library"`
	Template string   `json:"template"`
	Versions []string `json:"versions,omitempty"`
}

// StepImages: Digest requires every image to be pinned by digest;
// Registries, if given, are patterns of image repositories (host/path; a
// final /** matches any depth below) every image must match.
type StepImages struct {
	Digest     bool     `json:"digest,omitempty"`
	Registries []string `json:"registries,omitempty"`
}

// ForbidSecrets lists the events whose runs may not read secrets.
type ForbidSecrets struct {
	Events []string `json:"events"`
}

// Tests: at least Min results (default 1), none failed or errored.
type Tests struct {
	Min int `json:"min"`
}

// Findings: MaxSeverity is the worst severity allowed overall (none, low,
// medium, high, critical; none allows no finding at all); PerTool the
// worst allowed per scanner, by the tool's name as its SARIF says it;
// Scanners the tools that must have reported.
type Findings struct {
	MaxSeverity string            `json:"max_severity,omitempty"`
	PerTool     map[string]string `json:"per_tool,omitempty"`
	Scanners    []string          `json:"scanners,omitempty"`
}

// SBOM: a step made one with at least MinComponents components (default 1).
type SBOM struct {
	MinComponents int `json:"min_components"`
}

// DeployedImages says what must hold of every image a deployment writes.
type DeployedImages struct {
	Digest     bool `json:"digest,omitempty"`
	BuiltByRun bool `json:"built_by_run,omitempty"`
	Signed     bool `json:"signed,omitempty"`
	Scanned    bool `json:"scanned,omitempty"`
}

// Approvals: at least Min people (default 1), other than the one who
// started the run, approved an approval step before the deployment.
type Approvals struct {
	Min int `json:"min"`
}

// Refs: the run is of a branch matching Branches or a tag matching Tags.
type Refs struct {
	Branches []string `json:"branches,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// ImagePush says what an image step that pushes must do. Scan is the
// loosest fail_on allowed with scan mode enforce (critical, high, medium or
// low); Sign is runner (the runner's own key) or any.
type ImagePush struct {
	Scan       string   `json:"scan,omitempty"`
	Sign       string   `json:"sign,omitempty"`
	SBOM       bool     `json:"sbom,omitempty"`
	Provenance bool     `json:"provenance,omitempty"`
	Registries []string `json:"registries,omitempty"`
}

// used lists the rules a policy uses, in catalogue order.
func (r Rules) used() []string {
	var out []string
	add := func(ok bool, name string) {
		if ok {
			out = append(out, name)
		}
	}
	add(r.LibraryVersions != nil, RuleLibraryVersions)
	add(len(r.RequiredTemplates) > 0, RuleRequiredTemplates)
	add(r.StepImages != nil, RuleStepImages)
	add(r.MaxTimeout != "", RuleMaxTimeout)
	add(r.ForbidSecrets != nil, RuleForbidSecrets)
	add(r.Tests != nil, RuleTests)
	add(r.Findings != nil, RuleFindings)
	add(r.SBOM != nil, RuleSBOM)
	add(r.DeployedImages != nil, RuleDeployedImages)
	add(r.Approvals != nil, RuleApprovals)
	add(r.Refs != nil, RuleRefs)
	add(r.ImagePush != nil, RuleImagePush)
	return out
}

// RuleList lists the rules the policy uses, in catalogue order.
func (p *Policy) RuleList() []string { return p.Rules.used() }

// HasRule reports whether the policy uses a rule.
func (p *Policy) HasRule(rule string) bool { return slices.Contains(p.Rules.used(), rule) }

// Points lists the evaluation points the policy has rules at.
func (p *Policy) Points() []string {
	var out []string
	for _, r := range p.Rules.used() {
		if pt := PointOf(r); !slices.Contains(out, pt) {
			out = append(out, pt)
		}
	}
	return out
}

// Governs reports whether the policy applies to runs of a project with this
// branch and event, whatever the environment: the run is then governed by
// it from the start.
func (p *Policy) Governs(project, branch, event string) bool {
	s := p.Scope
	return matchList(s.Projects, project, true) && matchList(s.Branches, branch, false) &&
		(len(s.Events) == 0 || slices.Contains(s.Events, event))
}

// CoversProject reports whether the policy's scope names the project (or
// names no projects).
func (p *Policy) CoversProject(project string) bool {
	return matchList(p.Scope.Projects, project, true)
}

// AppliesTo reports whether the policy is judged at a point for a run: it
// governs the run, has rules at the point, and, at a deploy point, covers
// the environment.
func (p *Policy) AppliesTo(point, project, branch, event, environment string) bool {
	if !p.Governs(project, branch, event) || !slices.Contains(p.Points(), point) {
		return false
	}
	if point == PointDeploy {
		return matchList(p.Scope.Environments, environment, true)
	}
	return true
}

// matchList matches value against patterns; an empty list matches
// everything, unless value is empty and empty values must not match
// (a run without a branch does not match a policy scoped to branches).
func matchList(patterns []string, value string, emptyOK bool) bool {
	if len(patterns) == 0 {
		return true
	}
	if value == "" && !emptyOK {
		return false
	}
	return matchAny(patterns, value)
}

func matchAny(patterns []string, value string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, value); ok {
			return true
		}
	}
	return false
}

// Severities, from least to most severe. "none" is only a threshold.
var severities = []string{"none", "low", "medium", "high", "critical"}

// rank orders severities; unknown is below low (it is never worse than a
// threshold other than none).
func rank(sev string) int {
	if sev == "unknown" {
		return 0
	}
	return slices.Index(severities, sev)
}

// Source is a set of policies from one place, as it was last loaded.
type Source struct {
	Name     string   `json:"name"`     // config, or the source's name
	Version  string   `json:"version"`  // a commit (Git) or a hash of the policies (config)
	Ref      string   `json:"ref"`      // the tag (or commit) the source is pinned to; "" for config
	Projects []string `json:"projects"` // the projects it may govern; empty means every project
	Policies []Policy `json:"policies"`
	// Error, when set, says why the source could not be loaded (or used):
	// it then fails closed for every run of the projects it covers.
	Error string `json:"error,omitempty"`
}

// Covers reports whether a source may govern a project.
func (s *Source) Covers(project string) bool {
	return len(s.Projects) == 0 || matchAny(s.Projects, project)
}

// Set is the policies in force: every source, loaded or not.
type Set struct {
	Sources []Source `json:"sources"`
}

// Policies lists the policies of every source that loaded.
func (s Set) Policies() []Policy {
	var out []Policy
	for _, src := range s.Sources {
		if src.Error == "" {
			out = append(out, src.Policies...)
		}
	}
	return out
}

// Policy finds a policy by name among the sources that loaded.
func (s Set) Policy(name string) *Policy {
	for _, src := range s.Sources {
		if src.Error != "" {
			continue
		}
		for i := range src.Policies {
			if src.Policies[i].Name == name {
				return &src.Policies[i]
			}
		}
	}
	return nil
}

// Governed reports whether any policy (or a source that could not be
// loaded) governs runs of a project with this branch and event.
func (s Set) Governed(project, branch, event string) bool {
	for _, src := range s.Sources {
		if !src.Covers(project) {
			continue
		}
		if src.Error != "" {
			return true
		}
		for i := range src.Policies {
			if src.Policies[i].Governs(project, branch, event) {
				return true
			}
		}
	}
	return false
}

// Hash names a list of policies by their content: sha256: and 12 hex
// digits of the SHA-256 of their JSON. Inline policies are versioned by it.
func Hash(ps []Policy) string {
	b, _ := json.Marshal(ps)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

// Waiver lets a failing rule pass: for one project, one policy and one
// rule, optionally only in one run, one environment or for one finding,
// until it expires. A waiver is never a way around a source that could not
// be loaded.
type Waiver struct {
	ID          string `json:"id"`
	Project     string `json:"project"`
	Policy      string `json:"policy"`
	Rule        string `json:"rule"`
	Run         string `json:"run,omitempty"`
	Environment string `json:"environment,omitempty"`
	Finding     string `json:"finding,omitempty"` // a finding's fingerprint; only for the findings rule
	Reason      string `json:"reason"`
	GrantedBy   string `json:"granted_by"`
	GrantedAt   int64  `json:"granted_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

// Active reports whether the waiver is in force at a time (Unix ms).
func (w *Waiver) Active(at int64) bool { return at >= w.GrantedAt && at < w.ExpiresAt }

// Limits on what policies and waivers may hold.
const (
	MaxPolicies     = 200
	MaxReason       = 500
	MaxDescription  = 500
	MaxPatterns     = 100
	DefaultMaxDays  = 30
	MaxWaiverDays   = 365
	MaxListedFacts  = 200 // facts of one kind an evaluation records
	MaxTimeoutLimit = 30 * 24 * time.Hour
)
