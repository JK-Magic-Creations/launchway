// Package importer turns the pipeline definitions a team already has into a
// Launchway pipeline, and says what it could and could not carry over.
//
// There is one intermediate model (Pipeline) and one reader per source format.
// A reader is a pure function from files to the model; it never runs, fetches
// or evaluates anything in the files. Every construct a reader meets is
// classified (see Class) in the reader's own table and reported as a Finding:
// nothing is dropped silently and no behaviour is invented. A writer then turns
// the model into .launchway/pipeline.yaml, using the built-in templates where a
// job plainly is the standard job for a language.
package importer

import "sort"

// Class says how well a construct came across.
type Class string

const (
	// Exact: Launchway does the same thing.
	Exact Class = "exact"
	// Different: converted, with a difference that the finding states.
	Different Class = "different"
	// Manual: not converted; the finding says why and what a person does.
	Manual Class = "manual"
)

// Finding is one thing the importer did or could not do.
type Finding struct {
	Code    string `json:"code"`           // the table entry: "hosted.matrix.exclude"
	Class   Class  `json:"class"`          // exact, different or manual
	Message string `json:"message"`        // plain words: what happened
	Next    string `json:"next"`           // what to do now ("" when nothing)
	File    string `json:"file"`           // the source file
	Line    int    `json:"line"`           // the line it concerns; 0 for the whole file
	Step    string `json:"step,omitempty"` // the Launchway step it ended up in
	Fix     string `json:"fix,omitempty"`  // a fix-up the wizard offers: "template", "secret", "image", "condition"
	Name    string `json:"name,omitempty"` // what a fix-up is about: a secret's name, a step's name
}

// Pipeline is the format-neutral description of one source pipeline.
type Pipeline struct {
	Format   string
	File     string
	Name     string
	On       Triggers
	Inputs   []Input
	Env      []KV // variables for every job
	Secrets  []string
	Jobs     []*Job
	Findings []Finding
	// Lanes is true when the source asks that runs of the same ref do not overlap.
	Supersede bool
}

// KV is an ordered name and value; values never hold secrets.
type KV struct{ K, V string }

// Triggers say which events start a run.
type Triggers struct {
	Push      *Push
	PR        *PullRequest
	Schedules []Schedule
	Manual    bool
	Present   bool // the source said something about triggers
}

// Push triggers.
type Push struct{ Branches, Tags, Paths, PathsIgnore []string }

// PullRequest triggers: into is the branch the pull request targets.
type PullRequest struct{ Into, Paths, PathsIgnore []string }

// Schedule is a cron time.
type Schedule struct{ Cron, Zone string }

// Input is a value a run is started with.
type Input struct {
	Name, Type, Description, Default string
	Options                          []string
	Required                         bool
}

// Job is one unit of work: a Launchway step is made of it.
type Job struct {
	ID, Name   string
	Line       int
	Needs      []string
	Image      string
	Services   []Service
	Env        []KV
	SecretEnv  []KV // variable name -> secret name
	SecretLine map[string]int
	Steps      []Step
	Matrix     *Matrix
	Caches     []Cache
	Artifacts  []Artifact
	Tests      []string
	Coverage   []string
	Findings   []string // security scanner reports
	Fetch      []Fetch
	When       Cond
	Approval   *Approval
	Timeout    string // a Go duration
	Retry      int
	AllowFail  bool
	Delivers   bool   // pushes, releases or deploys
	DeliverWhy string // what made it a delivering step
	Template   *TemplateUse
	Image2     *ImageBuild
	Dir        string
	NoteLines  []string // comments to write above the step
}

// Service runs beside a job.
type Service struct {
	Name, Image string
	Env         []KV
	Command     []string
	Port        string
}

// Step is one command of a job. Several steps become one script.
type Step struct {
	Name, Run, Dir string
	Env            []KV
	Line           int
	Cond           string // the unconverted condition, as written
	Shell          string
}

// Matrix repeats a job.
type Matrix struct {
	Keys        []string
	Values      map[string][]string
	Exclude     []map[string]string
	Add         []map[string]string
	MaxParallel int
	FailFast    *bool
}

// Cache keeps directories between runs.
type Cache struct {
	Name     string
	Paths    []string
	KeyFiles []string
	KeyText  string
}

// Artifact keeps files.
type Artifact struct {
	Name   string
	Paths  []string
	Retain string
	When   string // success, failure, always
}

// Fetch takes an earlier job's artifact.
type Fetch struct{ From, Name, Into string }

// Cond is the closed set of conditions Launchway has.
type Cond struct {
	Branch, Tag, Event, Changed []string
	Upstream                    string // "", failed or always
}

// Empty says there is no condition.
func (c Cond) Empty() bool {
	return len(c.Branch)+len(c.Tag)+len(c.Event)+len(c.Changed) == 0 && c.Upstream == ""
}

// Approval is a person's decision before a job.
type Approval struct {
	Message string
	Users   []string
}

// TemplateUse says a job is the standard job of a built-in template.
type TemplateUse struct {
	Name string // go-test
	With []KV
}

// ImageBuild is a container image build from a Dockerfile.
type ImageBuild struct {
	Context, File, Target string
	Repo                  string
	Tags                  []string // literal tags or "{sha}" for the commit
	Push                  bool
	Platforms             []string
	BuildArgs             []KV
}

// Format describes one reader.
type Format struct {
	ID       string // "hosted-workflows"
	Title    string // for people: "Hosted workflow files"
	Where    string // where the files live, for the guide
	Detect   func(path string, head []byte) bool
	Read     func(root File, others map[string]File, lim Limits) *Pipeline
	Table    Table
	Sections []string // guide: the order of groups in the table
}

// File is one source file.
type File struct {
	Path    string
	Content []byte
}

var formats []*Format

func register(f *Format) { formats = append(formats, f) }

// Formats lists the readers in the order they were added.
func Formats() []*Format { return formats }

// FormatByID finds a reader.
func FormatByID(id string) *Format {
	for _, f := range formats {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// Count totals findings by class.
func Count(fs []Finding) map[Class]int {
	m := map[Class]int{}
	for _, f := range fs {
		m[f.Class]++
	}
	return m
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].Line < fs[j].Line
	})
}
