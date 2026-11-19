package policy

// Input is what an evaluation judges: facts the engine gathered from the
// run's events (and, for findings a waiver may name, from the rows derived
// from them), recorded with the verdict. Nothing in it is a secret: secrets
// are named by the variable a step asks for, never by value.
type Input struct {
	Point     string `json:"point"`
	Project   string `json:"project"`
	Run       string `json:"run"`
	RunNumber int    `json:"run_number"`
	Event     string `json:"event"`
	Branch    string `json:"branch,omitempty"`
	Tag       string `json:"tag,omitempty"`
	// Starter is who started the run: the person, or for a push the
	// commit's author as Git records it.
	Starter string `json:"starter,omitempty"`
	// Step is the step judged at a deploy or image point; Action what it is
	// about to do (deploy, promote, promote-full, retry; push for an image).
	Step   string `json:"step,omitempty"`
	Action string `json:"action,omitempty"`
	// Environment, at a deploy point, and whether it is protected; and
	// whether approvals.allow_self lets people approve their own runs.
	Environment  string `json:"environment,omitempty"`
	Protected    bool   `json:"protected,omitempty"`
	SelfApproval bool   `json:"self_approval,omitempty"`

	Pipeline *PipelineFacts `json:"pipeline,omitempty"` // run point
	Evidence *Evidence      `json:"evidence,omitempty"` // deploy point
	Image    *ImageStep     `json:"image,omitempty"`    // image point
}

// PipelineFacts are what the run-point rules judge: the expanded pipeline.
type PipelineFacts struct {
	Libraries []LibraryFact  `json:"libraries"`
	Templates []TemplateFact `json:"templates"`
	Images    []ImageRef     `json:"images"`
	Timeouts  []TimeoutFact  `json:"timeouts"`
	// Secrets lists the steps that ask for secrets (by variable name), and
	// SecretsAllowed whether the run's steps may read them at all (a
	// pull-request or draft run only if its project allows it).
	Secrets        []SecretFact `json:"secrets"`
	SecretsAllowed bool         `json:"secrets_allowed"`
}

// LibraryFact is a library the run uses, as it was resolved when the run
// was created.
type LibraryFact struct {
	Name     string `json:"name"` // what the pipeline calls it
	Repo     string `json:"repo"` // the server's name for it
	Version  string `json:"version"`
	Override string `json:"override,omitempty"`
	RefKind  string `json:"ref_kind"` // tag, branch or commit
	Commit   string `json:"commit"`
}

// TemplateFact is a template a step of the pipeline used.
type TemplateFact struct {
	Group    string `json:"group"`   // the uses step
	Library  string `json:"library"` // the server's name for the library
	Template string `json:"template"`
	Version  string `json:"version"` // as used: the override, if any
	RefKind  string `json:"ref_kind"`
}

// ImageRef is an image a step or container runs in.
type ImageRef struct {
	Where string `json:"where"` // step x, or container y
	Image string `json:"image"`
}

// TimeoutFact is a step's timeout, in milliseconds.
type TimeoutFact struct {
	Step    string `json:"step"`
	Kind    string `json:"kind,omitempty"`
	Timeout int64  `json:"timeout_ms"`
}

// SecretFact names the secrets one step asks for.
type SecretFact struct {
	Step  string   `json:"step"`
	Names []string `json:"names"`
}

// Evidence is what the deploy-point rules judge: what the steps before the
// deployment (every step it needs, directly or not) reported.
type Evidence struct {
	Steps []StepFact `json:"steps"`
	// Findings are findings worse than the most lenient threshold a
	// findings rule here uses, as far as the run kept them, so that a
	// waiver can name one; FindingsListed is false when there were more
	// than could be listed (or they were not all kept).
	Findings       []FindingFact `json:"findings,omitempty"`
	FindingsListed bool          `json:"findings_listed"`
	// Deployed are the image references the deployment writes, worked out
	// from its changes; Changes how many changes it has.
	Deployed []DeployedRef `json:"deployed"`
	Changes  int           `json:"changes"`
}

// StepFact is one step before the deployment and what it reported. Run is
// where it ran: this run, or for a step a re-run reused, the earlier run.
type StepFact struct {
	Step      string        `json:"step"`
	Kind      string        `json:"kind,omitempty"`
	Status    string        `json:"status"`
	Run       string        `json:"run"`
	RunNumber int           `json:"run_number"`
	Attempt   int           `json:"attempt,omitempty"`
	Tests     *TestFact     `json:"tests,omitempty"`
	Findings  *FindingsFact `json:"findings,omitempty"`
	SBOM      *SBOMFact     `json:"sbom,omitempty"`
	Image     *BuiltImage   `json:"image,omitempty"`
	Approval  *ApprovalFact `json:"approval,omitempty"`
	// Declared says which reports the step declares (tests, findings,
	// sbom), so that a step that declared them and reported nothing is told
	// apart from one that never would.
	Declared []string `json:"declared,omitempty"`
}

// TestFact is a step's test totals.
type TestFact struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
	Skipped int `json:"skipped"`
}

// Counts are findings by severity.
type Counts struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

// Above counts findings strictly worse than a threshold (none counts every
// finding, unknown ones included).
func (c Counts) Above(threshold string) int {
	switch threshold {
	case "none":
		return c.Total
	case "low":
		return c.Medium + c.High + c.Critical
	case "medium":
		return c.High + c.Critical
	case "high":
		return c.Critical
	}
	return 0
}

func (c *Counts) add(o Counts) {
	c.Total += o.Total
	c.Critical += o.Critical
	c.High += o.High
	c.Medium += o.Medium
	c.Low += o.Low
	c.Unknown += o.Unknown
}

// FindingsFact is what a step's scanners reported: counts overall and by
// tool, and the tools that reported. ByTool is nil for reports kept by a
// server that did not count by tool.
type FindingsFact struct {
	Counts
	Tools  []string          `json:"tools"`
	ByTool map[string]Counts `json:"by_tool,omitempty"`
}

// SBOMFact is the size of a step's SBOM.
type SBOMFact struct {
	Components int `json:"components"`
}

// BuiltImage is what an image step made.
type BuiltImage struct {
	Digest     string   `json:"digest"`
	Platforms  []string `json:"platforms,omitempty"` // the platforms' own digests
	Pushed     bool     `json:"pushed"`
	Signed     bool     `json:"signed"`
	KeySource  string   `json:"key_source,omitempty"`
	Scanned    bool     `json:"scanned"`
	ScanFailed bool     `json:"scan_failed,omitempty"`
}

// ApprovalFact is an approval's decision.
type ApprovalFact struct {
	By string `json:"by"`
	At int64  `json:"at"`
}

// FindingFact is one finding, for waivers that name one.
type FindingFact struct {
	Fingerprint string `json:"fingerprint"`
	Tool        string `json:"tool"`
	Rule        string `json:"rule"`
	Severity    string `json:"severity"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Step        string `json:"step"`
}

// DeployedRef is one image reference a deployment writes.
type DeployedRef struct {
	File  string `json:"file"`
	Image string `json:"image"` // the repository, as the change names it
	// Digest is the digest written (a change with digest:), Tag the tag
	// (a change with tag:); one of them.
	Digest string `json:"digest,omitempty"`
	Tag    string `json:"tag,omitempty"`
}

// ImageStep is what the image-point rule judges: what the step will do.
type ImageStep struct {
	Push       bool     `json:"push"`
	Registries []string `json:"registries"` // registries it pushes to (tags), as the runner names them
	Cache      string   `json:"cache,omitempty"`
	ScanMode   string   `json:"scan_mode,omitempty"` // report or enforce; empty: no scan
	ScanFailOn string   `json:"scan_fail_on,omitempty"`
	Sign       string   `json:"sign,omitempty"` // runner, key or empty
	SBOM       bool     `json:"sbom"`
	Provenance bool     `json:"provenance"`
}
