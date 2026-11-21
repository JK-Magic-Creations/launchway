package policy

import (
	"fmt"
	"strings"
)

// judgeAllowedFailures judges the allowed_failures rule: at most af.Max of
// the steps before the deployment failed and were allowed to.
func judgeAllowedFailures(r RuleResult, af *AllowedFailures, in Input) RuleResult {
	want := "no step before " + in.Step + " that failed and was allowed to"
	if af.Max > 0 {
		want = fmt.Sprintf("at most %d step(s) before %s that failed and were allowed to", af.Max, in.Step)
	}
	got := in.Evidence.AllowedFailures
	for _, s := range in.Evidence.Steps {
		if s.Allowed {
			r.Evidence = append(r.Evidence, EvidenceRef{Kind: "allowed_failure", Step: s.Step, Run: s.Run, RunNumber: s.RunNumber,
				Text: s.Step + " failed, and its pipeline allows that"})
		}
	}
	if len(got) > af.Max {
		r.Verdict, r.Found, r.Want = ResultFail, fmt.Sprintf("%d allowed failure(s): %s", len(got), strings.Join(got, ", ")), want
		return r
	}
	r.Found, r.Want = fmt.Sprintf("%d allowed failure(s)", len(got)), want
	return r
}

// judgeFacts judges the rules about the images a deployment writes and the
// steps that failed and were allowed to.
func judgeFacts(rule string, r RuleResult, rs Rules, in Input) RuleResult {
	if rule == RuleAllowedFailures {
		return judgeAllowedFailures(r, rs.AllowedFailures, in)
	}
	return judgeDeployed(r, rs.DeployedImages, in)
}
