package standards

import (
	"sort"
	"time"
)

// ProfileVersion is the version of the profile: which checks exist, what
// triggers them and how they run. A run records the version it was made
// under, so that a verdict can be explained later. It changes with a
// release, never at run time.
const ProfileVersion = "2026-11-21.1"

// State is what a check does with what it finds.
type State string

// The three states of a check, from the weakest to the strongest. Only a
// check that can gate may be block.
const (
	Off    State = "off"
	Advise State = "advise"
	Block  State = "block"
)

// Rank orders states: a higher rank is stronger.
func (s State) Rank() int {
	switch s {
	case Block:
		return 2
	case Advise:
		return 1
	}
	return 0
}

// Valid reports whether s is one of the three states.
func (s State) Valid() bool { return s == Off || s == Advise || s == Block }

// Kind says what a check looks for. It decides how a finding is shown
// and whether it can ever block.
type Kind string

// The kinds. The first four are security kinds: by default they may
// block. License, inventory and style never block unless a team says so.
const (
	KindSecret        Kind = "secret"
	KindVulnerability Kind = "vulnerability"
	KindCode          Kind = "code"
	KindConfig        Kind = "config"
	KindLicense       Kind = "license"
	KindInventory     Kind = "inventory"
	KindStyle         Kind = "style"
)

// Security reports whether the kind is one a gate is set up for by
// default.
func (k Kind) Security() bool {
	return k == KindSecret || k == KindVulnerability || k == KindCode || k == KindConfig
}

// Ecosystem names. A repository can have several; "any" applies to every
// repository.
const (
	Any        = "any"
	Go         = "go"
	JavaScript = "javascript"
	Python     = "python"
	Java       = "java"
	Container  = "container"
	Kubernetes = "kubernetes"
	Infra      = "infrastructure"
	Shell      = "shell"
)

// Ecosystems lists the ecosystems the profile knows, in the order they are
// shown.
var Ecosystems = []string{Any, Go, JavaScript, Python, Java, Container, Kubernetes, Infra, Shell}

// EcosystemTitle is how an ecosystem is shown.
var EcosystemTitle = map[string]string{
	Any: "Any repository", Go: "Go", JavaScript: "JavaScript and TypeScript", Python: "Python", Java: "Java (Maven and Gradle)",
	Container: "Container images", Kubernetes: "Helm and Kubernetes manifests", Infra: "Infrastructure code", Shell: "Shell",
}

// Report says which kind of file a check's step writes for Launchway to
// read.
type Report string

// The two kinds of report.
const (
	ReportFindings Report = "findings" // SARIF, or a format an adapter reads (see Adapt)
	ReportSBOM     Report = "sbom"     // CycloneDX
)

// CacheSpec is what a check keeps between runs.
type CacheSpec struct {
	Name  string
	Paths []string
	Files []string // key: the files whose content decides the entry
}

// Check is one entry of the profile.
type Check struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Why is one or two sentences of what the check protects against, in
	// plain words, shown beside the check.
	Why  string `json:"why"`
	Kind Kind   `json:"kind"`
	Tool string `json:"tool"`
	// Ecosystem is the ecosystem the check belongs to (Any for every
	// repository); Detect says which files make it apply.
	Ecosystem string `json:"ecosystem"`
	// Detect: the check applies when a file matches. Matchers are tried in
	// order and the first that matches gives the reason.
	Detect []Matcher `json:"-"`
	Report Report    `json:"report"`
	// Outputs are the globs, relative to the workspace, of the files the
	// step's script writes.
	Outputs []string `json:"outputs"`
	// Default is the state when nothing sets it. Gates says a check can be
	// block at all.
	Default State `json:"default"`
	Gates   bool  `json:"gates"`
	// Budget is the time the step may take; it is the step's timeout.
	Budget time.Duration `json:"budget"`
	// Network says the tool needs to reach the internet (its advisory
	// database, a module proxy). The profile says so plainly.
	Network string `json:"network,omitempty"`
	// Cache is what is kept between runs; nil: nothing.
	Cache *CacheSpec `json:"-"`
	// Restricted: the check may run in a restricted run (a pull request
	// from a fork or an automation account). Every check here needs no
	// secret, so every check may; the field is for the day one does.
	Restricted bool `json:"restricted"`
	// Fix says what Launchway can suggest: dependency-bump (the updater
	// opens a pull request), suggested (the tool gives a change), advice.
	Fix string `json:"fix"`
	// Script runs in the tool's image, in the workspace. It never fails
	// because it found something; it fails when the tool did not do its
	// work, so that "no report" can never look like "no findings".
	Script string `json:"-"`
	// Unavailable, when set, says why the check cannot run yet; it is shown
	// and never injected.
	Unavailable string `json:"unavailable,omitempty"`
}

// Available reports whether the check can run.
func (c Check) Available() bool { return c.Unavailable == "" }

// StepID is the ID of the step that runs the check.
func (c Check) StepID() string { return "inspect-" + c.ID }

// Matcher matches repository files by base name, suffix or path part.
type Matcher struct {
	// Names are exact base names; Prefix and Suffix match the base name;
	// Dirs match a directory name anywhere in the path.
	Names  []string
	Prefix string
	Suffix []string
	Dirs   []string
	// Label is what the reason says was found, when it is not the file.
	Label string
}

var dockerfiles = []Matcher{{Names: []string{"Dockerfile", "Containerfile"}}, {Prefix: "Dockerfile."}, {Suffix: []string{".Dockerfile", ".dockerfile"}}}

var checks = []Check{
	{
		ID: "secrets", Title: "Secrets in the repository's history", Kind: KindSecret, Tool: "gitleaks", Ecosystem: Any,
		Why:    "A password, key or token that is committed stays in the history for good and anyone who can read the repository can use it.",
		Detect: []Matcher{{Names: []string{"*"}, Label: "every repository"}}, Report: ReportFindings, Outputs: []string{".standing/secrets.sarif"},
		Default: Block, Gates: true, Budget: 10 * time.Minute, Restricted: true, Fix: "advice", Script: scriptSecrets,
	},
	{
		ID: "vuln-deps", Title: "Known vulnerabilities in dependencies", Kind: KindVulnerability, Tool: "osv-scanner", Ecosystem: Any,
		Why:    "A dependency with a published vulnerability puts the application at the same risk, whether or not anyone looked at it.",
		Detect: []Matcher{{Names: []string{"go.mod", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "poetry.lock", "uv.lock", "Pipfile.lock", "pom.xml", "gradle.lockfile", "buildscript-gradle.lockfile", "Cargo.lock", "Gemfile.lock", "composer.lock"}}, {Prefix: "requirements", Suffix: []string{".txt"}}},
		Report: ReportFindings, Outputs: []string{".standing/vuln-deps.sarif"}, Default: Block, Gates: true, Budget: 10 * time.Minute, Restricted: true,
		Network: "reads the OSV advisory database (api.osv.dev)", Fix: "dependency-bump", Script: scriptVulnDeps,
	},
	{
		ID: "vuln-reach-go", Title: "Reachable vulnerabilities in Go code", Kind: KindVulnerability, Tool: "govulncheck", Ecosystem: Go,
		Why:    "Separates a vulnerable module that is only listed from one whose vulnerable function the code can actually call, so the urgent ones come first.",
		Detect: []Matcher{{Names: []string{"go.mod"}}}, Report: ReportFindings, Outputs: []string{".standing/vuln-reach-go-*.sarif"}, Default: Advise, Gates: true,
		Budget: 15 * time.Minute, Restricted: true, Network: "downloads Go modules (proxy.golang.org) and reads the Go vulnerability database (vuln.go.dev)", Fix: "dependency-bump",
		Cache: &CacheSpec{Name: "standing-go", Paths: []string{".standing/cache/gomod", ".standing/cache/gobuild"}, Files: []string{"go.sum"}}, Script: scriptVulnReachGo,
	},
	{
		ID: "sast-go", Title: "Security rules for Go code", Kind: KindCode, Tool: "gosec", Ecosystem: Go,
		Why:    "Finds insecure patterns in the code itself: command injection, weak cryptography, unsafe file access, hard-coded credentials.",
		Detect: []Matcher{{Names: []string{"go.mod"}}}, Report: ReportFindings, Outputs: []string{".standing/sast-go-*.sarif"}, Default: Advise, Gates: true,
		Budget: 15 * time.Minute, Restricted: true, Network: "downloads Go modules (proxy.golang.org)", Fix: "advice",
		Cache: &CacheSpec{Name: "standing-go", Paths: []string{".standing/cache/gomod", ".standing/cache/gobuild"}, Files: []string{"go.sum"}}, Script: scriptSastGo,
	},
	{
		ID: "sast-python", Title: "Security rules for Python code", Kind: KindCode, Tool: "ruff", Ecosystem: Python,
		Why:    "Finds insecure patterns in the code itself: shell injection, unsafe deserialization, weak hashes, hard-coded passwords (the bandit rule set, run by ruff).",
		Detect: []Matcher{{Suffix: []string{".py"}}}, Report: ReportFindings, Outputs: []string{".standing/sast-python.sarif"}, Default: Advise, Gates: true,
		Budget: 5 * time.Minute, Restricted: true, Fix: "advice", Script: scriptSastPython,
	},
	{
		ID: "lint-python", Title: "Python lint", Kind: KindStyle, Tool: "ruff", Ecosystem: Python,
		Why:    "Catches mistakes the language lets through (unused names, syntax problems) before they reach a review. Advice only.",
		Detect: []Matcher{{Suffix: []string{".py"}}}, Report: ReportFindings, Outputs: []string{".standing/lint-python.sarif"}, Default: Advise, Gates: false,
		Budget: 5 * time.Minute, Restricted: true, Fix: "suggested", Script: scriptLintPython,
	},
	{
		ID: "license", Title: "Licenses of dependencies", Kind: KindLicense, Tool: "osv-scanner", Ecosystem: Any,
		Why:    "A dependency under a license you cannot ship with, or with no clear license, is a legal risk that is cheap to avoid early.",
		Detect: []Matcher{{Names: []string{"go.mod", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "poetry.lock", "uv.lock", "Pipfile.lock", "pom.xml", "gradle.lockfile", "Cargo.lock", "Gemfile.lock", "composer.lock"}}, {Prefix: "requirements", Suffix: []string{".txt"}}},
		Report: ReportFindings, Outputs: []string{".standing/licenses.json"}, Default: Advise, Gates: true, Budget: 10 * time.Minute, Restricted: true,
		Network: "looks up licenses on deps.dev", Fix: "advice", Script: scriptLicense,
	},
	{
		ID: "sbom", Title: "Software bill of materials", Kind: KindInventory, Tool: "trivy", Ecosystem: Any,
		Why:    "A list of every part the software is made of, kept with the run, so a new advisory can be matched to what you ship without a new scan.",
		Detect: []Matcher{{Names: []string{"go.mod", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "poetry.lock", "uv.lock", "Pipfile.lock", "pom.xml", "gradle.lockfile", "Cargo.lock", "Gemfile.lock", "composer.lock"}}, {Prefix: "requirements", Suffix: []string{".txt"}}},
		Report: ReportSBOM, Outputs: []string{".standing/sbom.cdx.json"}, Default: Advise, Gates: false, Budget: 10 * time.Minute, Restricted: true, Fix: "advice",
		Cache: &CacheSpec{Name: "standing-trivy", Paths: []string{".standing/cache/trivy"}, Files: []string{}}, Script: scriptSBOM,
	},
	{
		ID: "dockerfile", Title: "Dockerfile best practice", Kind: KindConfig, Tool: "hadolint", Ecosystem: Container,
		Why:    "Unpinned bases, running as root, piping downloads into a shell: the usual ways an image build becomes unsafe or unrepeatable.",
		Detect: dockerfiles, Report: ReportFindings,
		Outputs: []string{".standing/dockerfile-*.sarif"}, Default: Advise, Gates: true, Budget: 5 * time.Minute, Restricted: true, Fix: "suggested", Script: scriptDockerfile,
	},
	{
		ID: "misconfig", Title: "Misconfiguration in manifests and infrastructure code", Kind: KindConfig, Tool: "trivy", Ecosystem: Infra,
		Why:    "Privileged containers, open security groups, missing encryption: settings that are legal and wrong, found before they are deployed.",
		Detect: []Matcher{{Suffix: []string{".tf", ".tfvars"}, Label: "Terraform files"}, {Names: []string{"Chart.yaml", "kustomization.yaml", "Dockerfile", "Containerfile", "docker-compose.yml", "docker-compose.yaml", "cloudformation.yaml", "template.yaml"}}, {Dirs: []string{"k8s", "kubernetes", "manifests", "charts", "helm", "terraform", "ansible", "playbooks"}}},
		Report: ReportFindings, Outputs: []string{".standing/misconfig.sarif"}, Default: Advise, Gates: true, Budget: 10 * time.Minute, Restricted: true,
		Network: "fetches the rule bundle (ghcr.io); built-in rules are used when it cannot", Fix: "suggested",
		Cache: &CacheSpec{Name: "standing-trivy", Paths: []string{".standing/cache/trivy"}, Files: []string{}}, Script: scriptMisconfig,
	},
	{
		ID: "k8s-practice", Title: "Kubernetes best practice", Kind: KindConfig, Tool: "kube-linter", Ecosystem: Kubernetes,
		Why:    "Containers without limits, probes or a read-only root filesystem: what makes a workload fragile or easy to break out of.",
		Detect: []Matcher{{Names: []string{"Chart.yaml", "kustomization.yaml"}}, {Dirs: []string{"k8s", "kubernetes", "manifests", "charts", "helm"}}}, Report: ReportFindings,
		Outputs: []string{".standing/k8s-practice.sarif"}, Default: Advise, Gates: true, Budget: 5 * time.Minute, Restricted: true, Fix: "suggested", Script: scriptK8s,
	},
	{
		ID: "shell", Title: "Shell script checks", Kind: KindStyle, Tool: "shellcheck", Ecosystem: Shell,
		Why:    "Unquoted variables and unsafe globbing in a script are how an innocent rm turns into a deleted directory. Advice only.",
		Detect: []Matcher{{Suffix: []string{".sh", ".bash"}}}, Report: ReportFindings, Outputs: []string{".standing/shell.json"}, Default: Advise, Gates: false,
		Budget: 5 * time.Minute, Restricted: true, Fix: "suggested", Script: scriptShell,
	},
	{
		ID: "pipeline-lint", Title: "Unsafe patterns in pipeline and build files", Kind: KindConfig, Tool: "launchway-pipeline-lint", Ecosystem: Any,
		Why:    "Images that are not pinned by digest, secrets written in plain text, and downloads piped into a shell are how a build is taken over.",
		Detect: []Matcher{{Dirs: []string{".launchway"}, Suffix: []string{".yaml", ".yml"}}, {Names: []string{"Dockerfile", "Containerfile"}}, {Prefix: "Dockerfile."}}, Report: ReportFindings,
		Outputs: []string{".standing/pipeline-lint.sarif"}, Default: Advise, Gates: true, Budget: 2 * time.Minute, Restricted: true, Fix: "suggested", Script: scriptPipelineLint,
	},
	{
		ID: "image-scan", Title: "Vulnerabilities in the built image", Kind: KindVulnerability, Tool: "trivy", Ecosystem: Container,
		Why:    "The operating system packages in an image are as much a part of what you ship as your own code.",
		Detect: dockerfiles, Report: ReportFindings, Outputs: []string{".standing/image-scan.sarif"},
		Default: Advise, Gates: true, Budget: 10 * time.Minute, Restricted: true, Fix: "dependency-bump",
		Unavailable: "it needs the image a pipeline step built; the image step already scans what it builds, and Standing orders will take that over when the Flight plans call the Inspection",
	},
}

// Checks returns the profile's checks, in the order they are shown.
func Checks() []Check { return append([]Check(nil), checks...) }

// CheckByID returns one check.
func CheckByID(id string) (Check, bool) {
	for _, c := range checks {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}

// CheckIDs returns the IDs of every check, sorted.
func CheckIDs() []string {
	ids := make([]string, 0, len(checks))
	for _, c := range checks {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids
}
