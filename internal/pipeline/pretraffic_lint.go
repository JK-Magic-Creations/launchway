package pipeline

import "fmt"

// Codes of the warnings about real traffic reaching a version nobody proved.
const (
	CodeTrafficUnverified   = "live-traffic-unverified"
	CodeTrafficBeforeVerify = "live-traffic-before-verify"
)

// DeliveryWarnings returns what would send real traffic to a new version
// before anything proved it, each as a plain sentence with the fix. They
// are warnings, never problems (the author may have a reason), and a
// deploy step says it chose that with rollout: { unverified: true }.
//
// The safe pattern is to test a new version where real users are not:
// the rollout starts at 0% (a first step setWeight: 0 and a pause), the
// deploy step stops there (rollout.until: paused), a verify step that
// watches the deployment calls the new pods through the canary or
// preview service or a header or host route that real users do not use,
// and a rollout step with action: promote, which needs the verify step,
// carries the rollout on only after the verification passed.
func DeliveryWarnings(p *Pipeline) Problems {
	var out Problems
	for _, s := range p.Steps {
		if !s.IsDeploy() || s.Rollout == nil || s.Rollout.Unverified {
			continue
		}
		where := fmt.Sprintf("steps.%s.rollout", s.ID)
		line := s.Rollout.untilLine
		if line == 0 {
			line = s.Line
		}
		var watchers []string
		for _, o := range p.Steps {
			if o.IsVerify() && o.Deployment == s.ID {
				watchers = append(watchers, o.ID)
			}
		}
		switch {
		case len(watchers) == 0:
			out = append(out, Problem{Line: line, Path: where, Code: CodeTrafficUnverified, Message: fmt.Sprintf(
				"%s sends real traffic to the new version as its rollout takes weight, and no verify step watches it. Add a verify step with deployment: %s, set rollout: { until: paused }, start the rollout at 0%% (setWeight: 0, then a pause) and carry it on with a kind: rollout step (action: promote) that needs the verify step, so the new pods are tested before real users reach them; or write unverified: true under rollout to say real traffic may reach it unproved",
				s.ID, s.ID)})
		case s.Rollout.Until != "paused":
			out = append(out, Problem{Line: line, Path: where, Code: CodeTrafficBeforeVerify, Message: fmt.Sprintf(
				"%s waits until its rollout is healthy, which is after real traffic has reached the new version; %s can only prove it afterwards. Set until: paused and start the rollout at 0%% (setWeight: 0, then a pause) so %s tests the new pods before any weight goes to real users; or write unverified: true under rollout to say it is meant",
				s.ID, watchers[0], watchers[0])})
		}
	}
	return out
}
