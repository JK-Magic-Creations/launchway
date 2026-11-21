package standards

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// Levels, from the widest to the nearest, then the repository's own file.
// The names are the stable ones of the API; people read Base, Wing,
// Squadron and Project.
const (
	LevelInstallation = "installation"
	LevelOrganization = "organization"
	LevelUnit         = "unit"
	LevelProject      = "project"
	LevelFile         = "file"
)

// LevelOrder lists the levels from the widest to the nearest.
var LevelOrder = []string{LevelInstallation, LevelOrganization, LevelUnit, LevelProject, LevelFile}

// DefaultWords are the words people read for the levels.
var DefaultWords = map[string]string{
	LevelInstallation: "Base", LevelOrganization: "Wing", LevelUnit: "Squadron", LevelProject: "Project", LevelFile: "repository file",
}

// What the gate does with a finding it cannot compare.
const (
	IncompletePass = "pass"
	IncompleteFail = "fail"
)

// Settings is what one level (or the repository file) says. Everything is
// optional: what a level does not say, it inherits.
type Settings struct {
	// Off switches Standing orders off for everything below this level that
	// has not locked them on. Setting it needs a reason, and every change
	// is audited.
	Off       *bool  `json:"off,omitempty"`
	OffReason string `json:"off_reason,omitempty"`
	// Checks sets the state of checks by ID.
	Checks   map[string]State `json:"checks,omitempty"`
	Gate     GateSettings     `json:"gate,omitempty"`
	Licenses LicenseSettings  `json:"licenses,omitempty"`
	// Ignore are exemptions: findings the level accepts, with a reason and
	// an end date.
	Ignore []Ignore `json:"ignore,omitempty"`
	// Locks say, by key, what nobody below this level may weaken, and why.
	Locks map[string]string `json:"locks,omitempty"`
}

// GateSettings are the thresholds that decide what blocks.
type GateSettings struct {
	// FailOn is the lowest severity that blocks: critical, high, medium or
	// low.
	FailOn findings.Severity `json:"fail_on,omitempty"`
	// RequireFix: a vulnerability blocks only when a fixed version exists.
	RequireFix *bool `json:"require_fix,omitempty"`
	// DefaultBranch: advise (the backlog is shown, never fails) or block
	// (a finding on the default branch fails its run).
	DefaultBranch State `json:"default_branch,omitempty"`
	// Incomplete: pass or fail when a check that should have run did not.
	Incomplete string `json:"incomplete,omitempty"`
}

// LicenseSettings are the license lists. nil: not set at this level.
type LicenseSettings struct {
	Allow *[]string `json:"allow,omitempty"`
	Deny  *[]string `json:"deny,omitempty"`
}

// Ignore is an exemption: a finding a person has decided to live with.
type Ignore struct {
	Check       string `json:"check,omitempty"`
	Rule        string `json:"rule,omitempty"`
	Package     string `json:"package,omitempty"`
	Path        string `json:"path,omitempty"` // a glob: ** matches directories
	Fingerprint string `json:"fingerprint,omitempty"`
	Status      string `json:"status,omitempty"` // accepted-risk (the default) or false-positive
	Reason      string `json:"reason"`
	Owner       string `json:"owner,omitempty"`
	Expires     string `json:"expires"` // YYYY-MM-DD
}

// Setting keys, for locks and messages.
const (
	KeyOff           = "off"
	KeyFailOn        = "gate.fail_on"
	KeyRequireFix    = "gate.require_fix"
	KeyDefaultBranch = "gate.default_branch"
	KeyIncomplete    = "gate.incomplete"
	KeyAllow         = "licenses.allow"
	KeyDeny          = "licenses.deny"
	KeyIgnore        = "ignore"
)

// CheckKey is the key of one check's state.
func CheckKey(id string) string { return "check." + id }

// KeyName says in words what a key sets.
func KeyName(key string) string {
	switch key {
	case KeyOff:
		return "the switch that turns Standing orders off"
	case KeyFailOn:
		return "the severity that blocks"
	case KeyRequireFix:
		return "whether a fix must exist for a vulnerability to block"
	case KeyDefaultBranch:
		return "what findings on the default branch do"
	case KeyIncomplete:
		return "what an Inspection that did not finish does"
	case KeyAllow:
		return "the list of allowed licenses"
	case KeyDeny:
		return "the list of denied licenses"
	case KeyIgnore:
		return "exemptions"
	}
	if id, ok := strings.CutPrefix(key, "check."); ok {
		if c, ok := CheckByID(id); ok {
			return "the state of the check \"" + c.Title + "\""
		}
		return "the state of the check " + id
	}
	return key
}

// Defaults are what applies when nothing sets a value: everything on, the
// defaults of each check, the default gates.
var Defaults = struct {
	FailOn        findings.Severity
	RequireFix    bool
	DefaultBranch State
	Incomplete    string
	Deny          []string
}{
	FailOn: findings.High, RequireFix: true, DefaultBranch: Advise, Incomplete: IncompletePass,
	Deny: []string{"AGPL-3.0", "AGPL-3.0-only", "AGPL-3.0-or-later", "SSPL-1.0"},
}

// licenseRe is what a license name may be: letters, digits, dots, plus and
// minus (an SPDX identifier). The names reach a shell script through the
// environment, so nothing else is accepted.
var licenseRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+-]{0,63}$`)

// ValidLicense reports whether a license name is acceptable.
func ValidLicense(s string) bool { return licenseRe.MatchString(s) }

const (
	maxIgnores      = 500
	maxReasonLen    = 500
	maxLicenseNames = 100
	// MaxExemptionDays is the longest an exemption may last from today.
	MaxExemptionDays = 365
	minReasonLen     = 10
)

// DateFormat is how expiry dates are written.
const DateFormat = "2006-01-02"

// Problems are what is wrong with a Settings document, in plain words, each
// naming the place. An empty list means it is acceptable. now is today, for
// the limit on how far away an end date may be.
func (s Settings) Problems(now time.Time, allowLocks bool) []string {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	for id, st := range s.Checks {
		c, ok := CheckByID(id)
		switch {
		case !ok:
			add("checks: %q is not a check. The checks are: %s.", id, strings.Join(CheckIDs(), ", "))
		case !st.Valid():
			add("checks.%s: %q is not a state; use off, advise or block.", id, st)
		case st == Block && !c.Gates:
			add("checks.%s: \"%s\" can only advise: it has no gate, so it cannot block.", id, c.Title)
		}
	}
	if s.Off != nil && *s.Off && len(strings.TrimSpace(s.OffReason)) < minReasonLen {
		add("off: switching Standing orders off needs a reason of at least %d characters, so the next person knows why.", minReasonLen)
	}
	if v := s.Gate.FailOn; v != "" {
		switch v {
		case findings.Critical, findings.High, findings.Medium, findings.Low:
		default:
			add("gate.fail_on: %q is not a severity; use critical, high, medium or low.", v)
		}
	}
	if v := s.Gate.DefaultBranch; v != "" && v != Advise && v != Block {
		add("gate.default_branch: %q is not allowed; use advise or block.", v)
	}
	if v := s.Gate.Incomplete; v != "" && v != IncompletePass && v != IncompleteFail {
		add("gate.incomplete: %q is not allowed; use pass or fail.", v)
	}
	for _, l := range []struct {
		name string
		list *[]string
	}{{"licenses.allow", s.Licenses.Allow}, {"licenses.deny", s.Licenses.Deny}} {
		if l.list == nil {
			continue
		}
		if len(*l.list) > maxLicenseNames {
			add("%s: at most %d names.", l.name, maxLicenseNames)
		}
		for _, n := range *l.list {
			if !ValidLicense(n) {
				add("%s: %q is not a license name; write the SPDX identifier, for example MIT or Apache-2.0.", l.name, n)
			}
		}
	}
	if len(s.Ignore) > maxIgnores {
		add("ignore: at most %d entries.", maxIgnores)
	}
	for i, ig := range s.Ignore {
		out = append(out, ig.Problems(now, fmt.Sprintf("ignore[%d]", i+1))...)
	}
	for key, reason := range s.Locks {
		switch {
		case !allowLocks:
			add("locks.%s: a lock can only be set from the settings page or the API by someone who administers this level, not from the repository.", key)
		case !validKey(key):
			add("locks: %q is not something that can be locked.", key)
		case len(strings.TrimSpace(reason)) < minReasonLen:
			add("locks.%s: a lock needs a reason of at least %d characters.", key, minReasonLen)
		}
	}
	sort.Strings(out)
	return out
}

func validKey(k string) bool {
	switch k {
	case KeyOff, KeyFailOn, KeyRequireFix, KeyDefaultBranch, KeyIncomplete, KeyAllow, KeyDeny, KeyIgnore:
		return true
	}
	if id, ok := strings.CutPrefix(k, "check."); ok {
		_, ok := CheckByID(id)
		return ok
	}
	return false
}

// Problems are what is wrong with an exemption.
func (ig Ignore) Problems(now time.Time, where string) []string {
	var out []string
	add := func(format string, a ...any) { out = append(out, where+": "+fmt.Sprintf(format, a...)) }
	if ig.Check == "" && ig.Rule == "" && ig.Package == "" && ig.Path == "" && ig.Fingerprint == "" {
		add("say what to ignore: a check, a rule, a package, a path or a fingerprint. An entry that matches everything is not allowed.")
	}
	if ig.Check != "" {
		if _, ok := CheckByID(ig.Check); !ok {
			add("%q is not a check. The checks are: %s.", ig.Check, strings.Join(CheckIDs(), ", "))
		}
	}
	switch ig.Status {
	case "", StatusAccepted, StatusFalsePositive:
	default:
		add("status %q is not allowed; use accepted-risk or false-positive.", ig.Status)
	}
	if r := strings.TrimSpace(ig.Reason); len(r) < minReasonLen {
		add("a reason is needed, at least %d characters: why is this safe to live with?", minReasonLen)
	} else if len(r) > maxReasonLen {
		add("the reason is longer than %d characters.", maxReasonLen)
	}
	if ig.Expires == "" {
		add("an end date is needed (expires: YYYY-MM-DD): an exemption that never ends is forgotten. At most %d days from today.", MaxExemptionDays)
	} else if t, err := time.Parse(DateFormat, ig.Expires); err != nil {
		add("expires %q is not a date; write it as YYYY-MM-DD.", ig.Expires)
	} else if t.After(now.AddDate(0, 0, MaxExemptionDays)) {
		add("expires %s is more than %d days away; choose a date within a year, and renew it when it comes.", ig.Expires, MaxExemptionDays)
	}
	return out
}

// Exemption statuses.
const (
	StatusAccepted      = "accepted-risk"
	StatusFalsePositive = "false-positive"
)

// ExpiresAt returns the end of the day the exemption names, in UTC.
func (ig Ignore) ExpiresAt() (time.Time, bool) {
	t, err := time.Parse(DateFormat, ig.Expires)
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(24*time.Hour - time.Nanosecond), true
}

// Expired reports whether the exemption has ended.
func (ig Ignore) Expired(now time.Time) bool {
	t, ok := ig.ExpiresAt()
	return !ok || now.After(t)
}

// Matches reports whether the exemption covers a finding.
func (ig Ignore) Matches(f Finding) bool {
	if ig.Fingerprint != "" && ig.Fingerprint != f.Fingerprint {
		return false
	}
	if ig.Check != "" && ig.Check != f.Check {
		return false
	}
	if ig.Rule != "" && !strings.EqualFold(ig.Rule, f.Rule) && !f.hasAlias(ig.Rule) {
		return false
	}
	if ig.Package != "" && ig.Package != f.Package {
		return false
	}
	if ig.Path != "" && !GlobMatch(ig.Path, f.File) {
		return false
	}
	return ig.Check != "" || ig.Rule != "" || ig.Package != "" || ig.Path != "" || ig.Fingerprint != ""
}
