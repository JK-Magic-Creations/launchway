package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// Pull request results in place: for the head commit of a pull request, what each pipeline said, in the words of the
// run page, with test totals and new failures against the target branch's last run, coverage and its change, findings
// by severity (the new ones apart), the policy verdict and where the change is deployed. It composes what the run
// page already asks the engine for; nothing is stored. Detail comes from a pipeline's project only for a caller who
// may read that project; for anyone else a pipeline shows its status and the sentence, not its findings or tests.

type pullChecksView struct {
	Head        string           `json:"head"`
	Target      string           `json:"target_branch"`
	Checks      []pullCheck      `json:"checks"`
	Verdict     string           `json:"policy_verdict,omitempty"`
	Deployments []pullDeployment `json:"deployments"`
	Notes       []pullNote       `json:"annotations"`
}

type pullCheck struct {
	Run      string `json:"run_id"`
	Number   int    `json:"number"`
	Project  string `json:"project"`
	Pipeline string `json:"pipeline"`
	Status   string `json:"status"`
	Tone     string `json:"tone"`
	// Sentence says where the change is, as the run page does.
	Sentence string        `json:"sentence"`
	Detail   bool          `json:"detail"`
	Tests    *pullTests    `json:"tests,omitempty"`
	Coverage *pullCoverage `json:"coverage,omitempty"`
	Findings *pullFindings `json:"findings,omitempty"`
	Policy   string        `json:"policy_verdict,omitempty"`
	BaseRun  string        `json:"base_run_id,omitempty"`
}

type pullTests struct {
	Totals      engine.TestTotals `json:"totals"`
	NewFailures []string          `json:"new_failures"`
	NewCount    int               `json:"new_failure_count"`
	Fixed       int               `json:"fixed"`
	// Compared says a run of the target branch with test results was there to compare with.
	Compared bool `json:"compared"`
}

type pullCoverage struct {
	Lines float64 `json:"lines_percent"`
	// Change is the difference in percentage points from the target branch's run; nil when there is none to compare.
	Change *float64 `json:"change,omitempty"`
}

type pullFindings struct {
	Counts engine.FindingCounts `json:"counts"`
	// New counts the findings that are new in this change, by severity, and in all.
	New         engine.FindingCounts `json:"new"`
	NewTotal    int                  `json:"new_total"`
	NotCompared bool                 `json:"not_compared,omitempty"`
}

type pullDeployment struct {
	Environment string `json:"environment"`
	Version     string `json:"version"`
	Phase       string `json:"phase"`
	Run         string `json:"run_id"`
	// Address is where the change can be seen, when the deploy step said (an http or https address).
	Address string `json:"address,omitempty"`
}

// pullNote is a finding or a failed test that has a place in the files.
type pullNote struct {
	Kind     string `json:"kind"` // finding or test
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Message  string `json:"message"`
	Run      string `json:"run_id"`
	New      bool   `json:"new,omitempty"`
}

const maxPullNotes = 200

func (s *Server) pullChecks(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	out := pullChecksView{Head: p.HeadSHA, Target: p.TargetBranch, Checks: []pullCheck{}, Deployments: []pullDeployment{}, Notes: []pullNote{}}
	if s.Engine == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	runs, err := pullRuns{s}.RunsOf(r.Context(), "", repo.Path, p.Number)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The newest run of each pipeline for the head commit.
	latest := map[string]int{}
	var mine []int
	for i, ri := range runs {
		if ri.ID == "" || ri.SHA != p.HeadSHA {
			continue
		}
		key := ri.Project + "/" + ri.Pipeline
		if j, seen := latest[key]; seen {
			if runs[j].Created >= ri.Created {
				continue
			}
			latest[key] = i
			continue
		}
		latest[key] = i
		mine = append(mine, i)
	}
	sort.Slice(mine, func(a, b int) bool { return runs[mine[a]].Pipeline < runs[mine[b]].Pipeline })
	id := identityFrom(r)
	for _, i := range mine {
		ri := runs[latest[runs[i].Project+"/"+runs[i].Pipeline]]
		c, notes, deps, err := s.pullCheckOf(r.Context(), ri.ID, p.TargetBranch, s.authorize(id, auth.Read, ri.Project))
		if err != nil {
			s.fail(w, err)
			return
		}
		out.Checks = append(out.Checks, c)
		out.Notes = append(out.Notes, notes...)
		out.Deployments = append(out.Deployments, deps...)
		out.Verdict = worseVerdict(out.Verdict, c.Policy)
	}
	if len(out.Notes) > maxPullNotes {
		out.Notes = out.Notes[:maxPullNotes]
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) pullCheckOf(ctx context.Context, runID, target string, detail bool) (pullCheck, []pullNote, []pullDeployment, error) {
	d, err := s.Engine.GetRun(runID)
	if err != nil {
		return pullCheck{}, nil, nil, err
	}
	ll := launchLineOf(d)
	c := pullCheck{Run: d.ID, Number: d.Number, Project: d.Project, Pipeline: d.Pipeline, Status: string(d.Status), Tone: ll.Tone, Sentence: ll.Now, Detail: detail}
	if !detail {
		return c, nil, nil, nil
	}
	var deps []pullDeployment
	for _, dp := range d.Deployments {
		deps = append(deps, pullDeployment{Environment: dp.Environment, Version: dp.Version, Phase: dp.Phase, Run: d.ID, Address: dp.Address})
	}
	base := s.baseRunOf(d, target)
	if base != "" {
		c.BaseRun = base
	}
	var notes []pullNote
	// Tests.
	if rt, err := s.Engine.RunTests(runID); err != nil {
		return c, nil, nil, err
	} else if rt.Totals != nil {
		t := &pullTests{Totals: *rt.Totals, NewFailures: []string{}}
		if base != "" {
			cmp, err := s.Engine.CompareTestsWith(runID, base)
			if err != nil {
				return c, nil, nil, err
			}
			t.Compared = cmp.Previous != nil
			t.NewCount, t.Fixed = cmp.Counts["new_failures"], cmp.Counts["fixed"]
			for _, nf := range cmp.NewFailures {
				t.NewFailures = append(t.NewFailures, strings.TrimSpace(nf.Suite+" "+nf.Name))
			}
		}
		c.Tests = t
		if rt.Totals.Failed+rt.Totals.Errored > 0 {
			page, err := s.Engine.RunTestCases(runID, engine.CaseQuery{Status: "failed", Limit: 50})
			if err != nil {
				return c, nil, nil, err
			}
			for _, tc := range page.Cases {
				if tc.File != "" {
					notes = append(notes, pullNote{Kind: "test", File: tc.File, Message: clip(strings.TrimSpace(tc.Name+": "+tc.Message), 300), Run: runID})
				}
			}
		}
	}
	// Coverage, against the target branch.
	if cv, err := s.Engine.RunCoverage(runID, "refs/heads/"+target); err != nil {
		return c, nil, nil, err
	} else if cv.Totals != nil && cv.Totals.Lines.Total > 0 {
		pc := &pullCoverage{Lines: percent(cv.Totals.Lines)}
		if cv.Base != nil && cv.Base.Totals.Lines.Total > 0 {
			ch := pc.Lines - percent(cv.Base.Totals.Lines)
			pc.Change = &ch
		}
		c.Coverage = pc
	}
	// Findings, with those that are new in this change apart.
	if fs, err := s.Engine.RunFindings(runID, engine.FindingQuery{Limit: maxPullNotes}); err != nil {
		return c, nil, nil, err
	} else if fs.Counts != nil {
		pf := &pullFindings{Counts: *fs.Counts, NotCompared: fs.Previous == nil}
		for _, f := range fs.Findings {
			isNew := f.Status == engine.FindingNew
			if isNew {
				pf.NewTotal++
				addSeverity(&pf.New, f.Severity)
			}
			if f.File != "" {
				notes = append(notes, pullNote{Kind: "finding", File: f.File, Line: f.StartLine, Severity: f.Severity, Tool: f.Tool, Message: clip(f.Message, 300), Run: runID, New: isNew})
			}
		}
		pf.New.Total = pf.NewTotal
		c.Findings = pf
	}
	// Policy: the worst verdict the run has.
	ds, err := s.Engine.RunPolicy(runID)
	if err != nil {
		return c, nil, nil, err
	}
	for _, dc := range ds {
		c.Policy = worseVerdict(c.Policy, dc.Verdict)
	}
	return c, notes, deps, nil
}

// baseRunOf is the latest finished run of the same pipeline on the target branch.
func (s *Server) baseRunOf(d *engine.RunDetail, target string) string {
	runs, err := s.Engine.ListRuns(engine.RunFilter{Project: d.Project, Branch: target, Limit: 20})
	if err != nil {
		return ""
	}
	for _, r := range runs {
		if r.ID != d.ID && r.Pipeline == d.Pipeline && (r.Status == engine.RunSucceeded || r.Status == engine.RunFailed) {
			return r.ID
		}
	}
	return ""
}

func worseVerdict(a, b string) string {
	rank := map[string]int{"": 0, policy.VerdictPass: 1, policy.VerdictWarn: 2, policy.VerdictBlock: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func addSeverity(c *engine.FindingCounts, sev string) {
	switch sev {
	case "critical":
		c.Critical++
	case "high":
		c.High++
	case "medium":
		c.Medium++
	case "low":
		c.Low++
	default:
		c.Unknown++
	}
}

func percent(c engine.CoverageCounter) float64 {
	if c.Total == 0 {
		return 0
	}
	return float64(int(float64(c.Covered)/float64(c.Total)*1000+0.5)) / 10
}
