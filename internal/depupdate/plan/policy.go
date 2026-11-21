// Package plan decides what to propose: it holds the update policy and the
// way policy layers combine (instance, organization, project, repository
// settings, and the repository's own .launchway/updates.yaml), picks the
// version each dependency should move to, groups the updates, and applies
// the limits so that a burst of pull requests can never flood the pipeline.
package plan

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Defaults of a new repository, written down in one place and used by the
// docs. Security updates on, routine updates on and grouped, weekly, three
// open pull requests at most, a three-day wait for non-security versions,
// automatic merging off.
const (
	DefaultCooldownDays   = 3
	DefaultMaxOpen        = 3
	DefaultHour           = 6
	DefaultDay            = "monday"
	DefaultPerRun         = 3
	DefaultBranchPrefix   = "launchway/update"
	DefaultCommitPrefix   = "Update"
	DefaultTimezone       = "UTC"
	DefaultIntervalWeekly = "weekly"
)

// Ecosystem switches.
type Ecosystem struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Directories limits discovery to these repository-relative directories
	// (empty: everywhere).
	Directories []string `yaml:"directories,omitempty" json:"directories,omitempty"`
}

// Schedule says when routine updates are looked for. Security updates are
// never held to it.
type Schedule struct {
	Interval string `yaml:"interval" json:"interval"` // daily, weekly, monthly
	Day      string `yaml:"day,omitempty" json:"day,omitempty"`
	Hour     int    `yaml:"hour" json:"hour"`
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
}

// Group collects updates into one pull request.
type Group struct {
	Name      string   `yaml:"name" json:"name"`
	Ecosystem string   `yaml:"ecosystem,omitempty" json:"ecosystem,omitempty"`
	Patterns  []string `yaml:"patterns,omitempty" json:"patterns,omitempty"` // name globs; empty: every dependency
	Kinds     []string `yaml:"kinds,omitempty" json:"kinds,omitempty"`       // patch, minor, major; empty: patch and minor
	Directory string   `yaml:"directory,omitempty" json:"directory,omitempty"`
}

// Limits keep a burst from flooding the pipeline.
type Limits struct {
	MaxOpen             int `yaml:"max_open" json:"max_open"`                                                 // open update pull requests per repository
	MaxPerRun           int `yaml:"max_per_run" json:"max_per_run"`                                           // opened by one run of the scheduler
	MaxOpenPerEcosystem int `yaml:"max_open_per_ecosystem,omitempty" json:"max_open_per_ecosystem,omitempty"` // 0: no per-ecosystem limit
}

// Ignore leaves a dependency, a version range or a kind of update alone.
type Ignore struct {
	Name     string   `yaml:"name" json:"name"`                             // glob
	Versions string   `yaml:"versions,omitempty" json:"versions,omitempty"` // npm-style range of target versions to skip
	Kinds    []string `yaml:"kinds,omitempty" json:"kinds,omitempty"`
	Reason   string   `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// Constraint keeps a dependency on a major version ("stay on 4").
type Constraint struct {
	Name  string `yaml:"name" json:"name"` // glob
	Major *int   `yaml:"major,omitempty" json:"major,omitempty"`
	Range string `yaml:"range,omitempty" json:"range,omitempty"` // only versions inside this range are proposed
}

// AutoMerge is off unless a level turns it on, and then only for the kinds
// and groups listed, and only once the pull request's required checks pass.
type AutoMerge struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Kinds   []string `yaml:"kinds,omitempty" json:"kinds,omitempty"`   // e.g. [patch]
	Groups  []string `yaml:"groups,omitempty" json:"groups,omitempty"` // dependency groups: dev, prod, tool
}

// Commit conventions.
type Commit struct {
	MessagePrefix string `yaml:"message_prefix,omitempty" json:"message_prefix,omitempty"`
	BranchPrefix  string `yaml:"branch_prefix,omitempty" json:"branch_prefix,omitempty"`
}

// Partial is one layer of settings: a field that is nil is "not set at this
// level". The field names are the keys a level can lock.
type Partial struct {
	Enabled      *bool                 `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Security     *bool                 `yaml:"security,omitempty" json:"security,omitempty"`
	Routine      *bool                 `yaml:"routine,omitempty" json:"routine,omitempty"`
	Ecosystems   *map[string]Ecosystem `yaml:"ecosystems,omitempty" json:"ecosystems,omitempty"`
	Schedule     *Schedule             `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	CooldownDays *int                  `yaml:"cooldown_days,omitempty" json:"cooldown_days,omitempty"`
	Groups       *[]Group              `yaml:"groups,omitempty" json:"groups,omitempty"`
	Limits       *Limits               `yaml:"limits,omitempty" json:"limits,omitempty"`
	Allow        *[]string             `yaml:"allow,omitempty" json:"allow,omitempty"`
	Ignore       *[]Ignore             `yaml:"ignore,omitempty" json:"ignore,omitempty"`
	Constraints  *[]Constraint         `yaml:"constraints,omitempty" json:"constraints,omitempty"`
	AutoMerge    *AutoMerge            `yaml:"auto_merge,omitempty" json:"auto_merge,omitempty"`
	Commit       *Commit               `yaml:"commit,omitempty" json:"commit,omitempty"`
	Labels       *[]string             `yaml:"labels,omitempty" json:"labels,omitempty"`
	Reviewers    *[]string             `yaml:"reviewers,omitempty" json:"reviewers,omitempty"`
	Assignees    *[]string             `yaml:"assignees,omitempty" json:"assignees,omitempty"`
	Prereleases  *bool                 `yaml:"prereleases,omitempty" json:"prereleases,omitempty"`
	ImageFiles   *[]string             `yaml:"image_files,omitempty" json:"image_files,omitempty"` // files with NAME_IMAGE='ref' lines
	// Exclude lists path globs (* matches any run of characters, "/"
	// included) whose files are not looked at: examples, test fixtures.
	Exclude *[]string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	// Toolchains names the images the lock files are regenerated in
	// (keys go, node, uv). Pin them by digest and mirror them.
	Toolchains *map[string]string `yaml:"toolchains,omitempty" json:"toolchains,omitempty"`

	// Lock lists keys this level fixes: no narrower level and not the
	// repository's file may change them. The file's own Lock is ignored.
	Lock []string `yaml:"lock,omitempty" json:"lock,omitempty"`
}

// Policy is the result of combining layers: nothing is nil.
type Policy struct {
	Enabled      bool                 `json:"enabled"`
	Security     bool                 `json:"security"`
	Routine      bool                 `json:"routine"`
	Ecosystems   map[string]Ecosystem `json:"ecosystems"`
	Schedule     Schedule             `json:"schedule"`
	CooldownDays int                  `json:"cooldown_days"`
	Groups       []Group              `json:"groups"`
	Limits       Limits               `json:"limits"`
	Allow        []string             `json:"allow,omitempty"`
	Ignore       []Ignore             `json:"ignore,omitempty"`
	Constraints  []Constraint         `json:"constraints,omitempty"`
	AutoMerge    AutoMerge            `json:"auto_merge"`
	Commit       Commit               `json:"commit"`
	Labels       []string             `json:"labels,omitempty"`
	Reviewers    []string             `json:"reviewers,omitempty"`
	Assignees    []string             `json:"assignees,omitempty"`
	Prereleases  bool                 `json:"prereleases"`
	ImageFiles   []string             `json:"image_files,omitempty"`
	Toolchains   map[string]string    `json:"toolchains,omitempty"`
	Exclude      []string             `json:"exclude,omitempty"`
	// Locked says which level locked each key, for the settings page.
	Locked map[string]string `json:"locked,omitempty"`
	// Ignored lists file settings that were not applied, with the reason.
	Ignored []string `json:"ignored,omitempty"`
}

func ptr[T any](v T) *T { return &v }

// Defaults is the bottom layer: what a new repository gets.
func Defaults() Partial {
	return Partial{
		Enabled:  ptr(true),
		Security: ptr(true),
		Routine:  ptr(true),
		Ecosystems: ptr(map[string]Ecosystem{
			"go": {Enabled: true}, "npm": {Enabled: true}, "python": {Enabled: true}, "container": {Enabled: true}, "workflow": {Enabled: true},
		}),
		Schedule:     &Schedule{Interval: DefaultIntervalWeekly, Day: DefaultDay, Hour: DefaultHour, Timezone: DefaultTimezone},
		CooldownDays: ptr(DefaultCooldownDays),
		Groups:       ptr([]Group{{Name: "minor-and-patch", Kinds: []string{"patch", "minor"}}}),
		Limits:       &Limits{MaxOpen: DefaultMaxOpen, MaxPerRun: DefaultPerRun},
		AutoMerge:    &AutoMerge{},
		Commit:       &Commit{MessagePrefix: DefaultCommitPrefix, BranchPrefix: DefaultBranchPrefix},
		Labels:       ptr([]string{"dependencies"}),
		Prereleases:  ptr(false),
	}
}

// Layer is one named level of settings.
type Layer struct {
	Level string // "instance", "organization", "project", "repository", "file"
	Partial
}

// Merge combines layers, broadest first. A key set in a layer replaces the
// value; a key locked by a broader level cannot be changed by a narrower one
// (the attempt is recorded in Ignored). The first layer is normally
// Defaults.
func Merge(layers ...Layer) (Policy, error) {
	acc := Partial{}
	locked := map[string]string{}
	var ignored []string
	for _, l := range layers {
		pv := reflect.ValueOf(l.Partial)
		av := reflect.ValueOf(&acc).Elem()
		t := pv.Type()
		for i := 0; i < t.NumField(); i++ {
			key := yamlKey(t.Field(i))
			if key == "lock" || pv.Field(i).IsNil() {
				continue
			}
			if by, ok := locked[key]; ok && by != l.Level {
				ignored = append(ignored, fmt.Sprintf("%s: set by the %s level, which is locked by the %s level", key, l.Level, by))
				continue
			}
			av.Field(i).Set(pv.Field(i))
		}
		if l.Level != "file" {
			for _, k := range l.Lock {
				if !knownKey(k) {
					return Policy{}, fmt.Errorf("the %s level locks %q, which is not a setting", l.Level, k)
				}
				if _, ok := locked[k]; !ok {
					locked[k] = l.Level
				}
			}
		}
	}
	p := Policy{Locked: locked, Ignored: ignored}
	if acc.Enabled != nil {
		p.Enabled = *acc.Enabled
	}
	if acc.Security != nil {
		p.Security = *acc.Security
	}
	if acc.Routine != nil {
		p.Routine = *acc.Routine
	}
	if acc.Ecosystems != nil {
		p.Ecosystems = *acc.Ecosystems
	}
	if acc.Schedule != nil {
		p.Schedule = *acc.Schedule
	}
	if acc.CooldownDays != nil {
		p.CooldownDays = *acc.CooldownDays
	}
	if acc.Groups != nil {
		p.Groups = *acc.Groups
	}
	if acc.Limits != nil {
		p.Limits = *acc.Limits
	}
	if acc.Allow != nil {
		p.Allow = *acc.Allow
	}
	if acc.Ignore != nil {
		p.Ignore = *acc.Ignore
	}
	if acc.Constraints != nil {
		p.Constraints = *acc.Constraints
	}
	if acc.AutoMerge != nil {
		p.AutoMerge = *acc.AutoMerge
	}
	if acc.Commit != nil {
		p.Commit = *acc.Commit
	}
	if acc.Labels != nil {
		p.Labels = *acc.Labels
	}
	if acc.Reviewers != nil {
		p.Reviewers = *acc.Reviewers
	}
	if acc.Assignees != nil {
		p.Assignees = *acc.Assignees
	}
	if acc.Prereleases != nil {
		p.Prereleases = *acc.Prereleases
	}
	if acc.ImageFiles != nil {
		p.ImageFiles = *acc.ImageFiles
	}
	if acc.Exclude != nil {
		p.Exclude = *acc.Exclude
	}
	if acc.Toolchains != nil {
		p.Toolchains = *acc.Toolchains
	}
	return p, p.Validate()
}

func yamlKey(f reflect.StructField) string {
	return strings.Split(f.Tag.Get("yaml"), ",")[0]
}

func knownKey(k string) bool {
	t := reflect.TypeOf(Partial{})
	for i := 0; i < t.NumField(); i++ {
		if yamlKey(t.Field(i)) == k && k != "lock" {
			return true
		}
	}
	return false
}

// Validate checks values. Every problem is named with the key it is in.
func (p Policy) Validate() error {
	var bad []string
	if p.CooldownDays < 0 || p.CooldownDays > 90 {
		bad = append(bad, "cooldown_days must be between 0 and 90")
	}
	if p.Limits.MaxOpen < 0 || p.Limits.MaxOpen > 50 || p.Limits.MaxPerRun < 0 || p.Limits.MaxPerRun > 50 || p.Limits.MaxOpenPerEcosystem < 0 {
		bad = append(bad, "limits: max_open and max_per_run must be between 0 and 50 (0 opens none)")
	}
	switch p.Schedule.Interval {
	case "daily", "weekly", "monthly":
	default:
		bad = append(bad, fmt.Sprintf(`schedule.interval must be "daily", "weekly" or "monthly", not %q`, p.Schedule.Interval))
	}
	if p.Schedule.Hour < 0 || p.Schedule.Hour > 23 {
		bad = append(bad, "schedule.hour must be 0 to 23")
	}
	if p.Schedule.Interval == "weekly" && p.Schedule.Day != "" && !validDay(p.Schedule.Day) {
		bad = append(bad, "schedule.day must be a weekday name such as monday")
	}
	for e := range p.Ecosystems {
		if !knownEcosystem(e) {
			bad = append(bad, fmt.Sprintf("ecosystems.%s is not an ecosystem that works today", e))
		}
	}
	for _, k := range append(append([]string{}, p.AutoMerge.Kinds...), groupKinds(p.Groups)...) {
		if k != "patch" && k != "minor" && k != "major" {
			bad = append(bad, fmt.Sprintf("%q is not an update kind (patch, minor, major)", k))
		}
	}
	for _, g := range p.Groups {
		if g.Name == "" {
			bad = append(bad, "every group needs a name")
		}
	}
	for _, i := range p.Ignore {
		if i.Name == "" {
			bad = append(bad, "every ignore rule needs a name pattern")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("the update settings are not valid: %s", strings.Join(bad, "; "))
	}
	return nil
}

func groupKinds(g []Group) []string {
	var out []string
	for _, x := range g {
		out = append(out, x.Kinds...)
	}
	return out
}

func knownEcosystem(e string) bool {
	switch e {
	case "go", "npm", "python", "container", "workflow":
		return true
	}
	return false
}

func validDay(d string) bool {
	switch strings.ToLower(d) {
	case "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday":
		return true
	}
	return false
}
