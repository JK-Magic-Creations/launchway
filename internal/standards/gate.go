package standards

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// Policy is the effective profile as the gate needs it. It is made when the
// run is created and kept in the run's gate step, so the verdict can always
// be explained by what was in force then.
type Policy struct {
	ProfileVersion string            `json:"profile_version"`
	Checks         []PolicyCheck     `json:"checks"`
	FailOn         findings.Severity `json:"fail_on"`
	RequireFix     bool              `json:"require_fix"`
	// DefaultBranchBlocks: findings on the default branch fail the run.
	DefaultBranchBlocks bool `json:"default_branch_blocks"`
	// DefaultBranchRun: the run is for the default branch, so what it finds
	// is the backlog the other changes are measured against.
	DefaultBranchRun bool     `json:"default_branch_run"`
	IncompleteFails  bool     `json:"incomplete_fails"`
	Exemptions       []Ignore `json:"exemptions,omitempty"`
	// Notices repeat what the effective profile said, so the run page can
	// show them without looking the settings up again.
	Notices []string `json:"notices,omitempty"`
}

// PolicyCheck is a check that was added to the run.
type PolicyCheck struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State State  `json:"state"`
	Kind  Kind   `json:"kind"`
}

// PolicyOf makes the policy of a run from an effective profile.
func PolicyOf(e Effective, now time.Time) Policy {
	p := Policy{ProfileVersion: e.ProfileVersion, FailOn: e.FailOn.V, RequireFix: e.RequireFix.V,
		DefaultBranchBlocks: e.DefaultBranch.V == Block, IncompleteFails: e.Incomplete.V == IncompleteFail, Notices: e.Notices}
	for _, c := range e.Checks {
		if c.Active {
			p.Checks = append(p.Checks, PolicyCheck{ID: c.ID, Title: c.Title, State: c.State.V, Kind: c.Kind})
		}
	}
	for _, x := range e.Exemptions {
		if !x.Expired {
			p.Exemptions = append(p.Exemptions, x.Ignore)
		}
	}
	return p
}

func (p Policy) state(check string) (State, bool) {
	for _, c := range p.Checks {
		if c.ID == check {
			return c.State, true
		}
	}
	return Off, false
}

// Decision is a person's decision about one finding, kept by the server.
type Decision struct {
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"` // accepted-risk or false-positive
	Reason      string    `json:"reason"`
	By          string    `json:"by"`
	At          time.Time `json:"at"`
	Expires     time.Time `json:"expires"`
}

// Active reports whether the decision still counts.
func (d Decision) Active(now time.Time) bool { return now.Before(d.Expires) }

// Standing of a finding in a run.
const (
	StandingNew      = "new"      // introduced by this change
	StandingExisting = "existing" // already on the default branch
	StandingAccepted = "accepted" // a decision or exemption covers it
	StandingFalse    = "false-positive"
	StandingUnscored = "unscored" // no baseline exists yet
)

// Judged is a finding with what the gate made of it.
type Judged struct {
	Finding
	Standing string `json:"standing"`
	// Gating: it counts toward blocking.
	Gating bool `json:"gating,omitempty"`
	// Why says, for an accepted finding, who accepted it or which entry
	// covers it.
	Why string `json:"why,omitempty"`
}

// JudgeInput is what a verdict is made from.
type JudgeInput struct {
	Policy        Policy
	DefaultBranch bool
	Findings      []Finding
	// Ran are the checks whose step produced a report in this run.
	Ran map[string]bool
	// Baseline are the fingerprints open on the default branch, and
	// HasBaseline says the default branch has been inspected at all.
	Baseline    map[string]bool
	HasBaseline bool
	Decisions   map[string]Decision
	Now         time.Time
}

// Verdict is the gate's answer.
type Verdict struct {
	Blocked bool `json:"blocked"`
	// Summary is the verdict in one or two sentences.
	Summary string `json:"summary"`
	// Counts by standing.
	New, Existing, Accepted int            `json:"-"`
	Counts                  map[string]int `json:"counts"`
	// Gating are the findings that cause a block.
	Gating     []Judged `json:"gating,omitempty"`
	Incomplete []string `json:"incomplete,omitempty"`
	NoBaseline bool     `json:"no_baseline,omitempty"`
}

// Eligible reports whether a finding is of a kind and severity that may
// block under the policy, whatever its standing.
func (p Policy) Eligible(f Finding) bool {
	st, ok := p.state(f.Check)
	if !ok || st != Block {
		return false
	}
	if sevRank(f.Severity) < sevRank(p.FailOn) || f.Severity == findings.Unknown {
		return false
	}
	if p.RequireFix && f.Kind == KindVulnerability && f.FixedIn == "" && f.Reachable != "yes" {
		return false // nothing can be done yet: it is advice, not a block
	}
	if f.Kind == KindVulnerability && f.Reachable == "no" && f.Tool == "govulncheck" {
		return false // the module is listed but its vulnerable code is not called
	}
	return true
}

// Judge decides what a run's findings mean. The rule is the one in the
// guide: a change is blocked only by what it introduces, measured against
// the default branch; the default branch's own backlog is shown and does
// not fail the run unless the Standing orders say so; a decision about a
// finding (accepted risk, false positive) counts until it ends; a check
// that did not run is never taken for a clean result.
func Judge(in JudgeInput) Verdict {
	v := Verdict{Counts: map[string]int{}, NoBaseline: !in.HasBaseline && !in.DefaultBranch}
	ignored := func(f Finding) (string, string, bool) {
		if d, ok := in.Decisions[f.Fingerprint]; ok && d.Active(in.Now) {
			return d.Status, fmt.Sprintf("%s: %s", d.By, d.Reason), true
		}
		for _, ig := range in.Policy.Exemptions {
			if !ig.Expired(in.Now) && ig.Matches(f) {
				st := ig.Status
				if st == "" {
					st = StatusAccepted
				}
				return st, "the repository's own list: " + ig.Reason, true
			}
		}
		return "", "", false
	}
	for _, f := range in.Findings {
		j := Judged{Finding: f}
		switch status, why, ok := ignored(f); {
		case ok:
			j.Standing, j.Why = StandingAccepted, why
			if status == StatusFalsePositive {
				j.Standing = StandingFalse
			}
			v.Accepted++
		case in.DefaultBranch:
			j.Standing = StandingExisting
			v.Existing++
			if in.Policy.DefaultBranchBlocks && in.Policy.Eligible(f) {
				j.Gating = true
			}
		case !in.HasBaseline:
			j.Standing = StandingUnscored
			v.Existing++
		case in.Baseline[f.Fingerprint]:
			j.Standing = StandingExisting
			v.Existing++
		default:
			j.Standing = StandingNew
			v.New++
			if in.Policy.Eligible(f) {
				j.Gating = true
			}
		}
		v.Counts[j.Standing]++
		if j.Gating {
			v.Gating = append(v.Gating, j)
		}
	}
	sort.SliceStable(v.Gating, func(i, k int) bool { return sevRank(v.Gating[i].Severity) > sevRank(v.Gating[k].Severity) })
	for _, c := range in.Policy.Checks {
		if c.State != Off && !in.Ran[c.ID] {
			v.Incomplete = append(v.Incomplete, c.ID)
			if c.State == Block && in.Policy.IncompleteFails {
				v.Blocked = true
			}
		}
	}
	if len(v.Gating) > 0 {
		v.Blocked = true
	}
	v.Summary = v.words(in)
	return v
}

func (v Verdict) words(in JudgeInput) string {
	var parts []string
	switch {
	case len(v.Gating) > 0 && in.DefaultBranch:
		parts = append(parts, fmt.Sprintf("Blocked: %d finding(s) on the default branch at %s or above, and the Standing orders say the default branch must be clean.", len(v.Gating), in.Policy.FailOn))
	case len(v.Gating) > 0:
		parts = append(parts, fmt.Sprintf("Blocked: this change introduces %d finding(s) at %s or above.", len(v.Gating), in.Policy.FailOn))
	case v.Blocked:
		parts = append(parts, "Blocked: "+strings.Join(v.Incomplete, ", ")+" did not run, and the Standing orders say an Inspection that did not finish fails.")
	default:
		parts = append(parts, fmt.Sprintf("Passed: this change introduces nothing at %s or above.", in.Policy.FailOn))
		if in.DefaultBranch {
			parts[0] = "Passed: the default branch's backlog is recorded and does not fail the run."
		}
	}
	if v.NoBaseline {
		parts = append(parts, "The default branch has not been inspected yet, so nothing can be compared and nothing blocks; the next run on the default branch sets the baseline.")
	} else if v.Existing > 0 && !in.DefaultBranch {
		parts = append(parts, fmt.Sprintf("%d existing finding(s) were already on the default branch and are not counted.", v.Existing))
	}
	if v.Accepted > 0 {
		parts = append(parts, fmt.Sprintf("%d finding(s) are accepted or marked as false positives.", v.Accepted))
	}
	if len(v.Incomplete) > 0 && !v.Blocked {
		parts = append(parts, "Did not finish: "+strings.Join(v.Incomplete, ", ")+".")
	}
	return strings.Join(parts, " ")
}
