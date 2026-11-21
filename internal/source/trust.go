package source

import (
	"path"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// classifyRun says what the queue needs to know about a run being created: it
// is for a protected branch (the project's default branch), and whether an
// automation account started it.
func (m *Manager) classifyRun(nr *engine.NewRun) {
	p := m.c().projects[nr.Project]
	if p == nil {
		return
	}
	nr.Protected = nr.Trigger.Branch != "" && nr.Trigger.Branch == p.DefaultBranch
	nr.Automation = isAutomation(p.AutomationAccounts, nr.Trigger.Author, nr.Trigger.Actor)
}

// isAutomation reports whether any of the names matches a pattern (`*` and
// `?`, any case).
func isAutomation(patterns []string, names ...string) bool {
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		for _, p := range patterns {
			if ok, _ := path.Match(strings.ToLower(p), n); ok {
				return true
			}
		}
	}
	return false
}

// trustedPipelineSHA is the commit whose pipeline a pull request's run uses,
// and whether the run is restricted. A pull request from an automation account
// (and one from a fork, where the forge says so) runs the pipeline of the
// default branch's tip, so it cannot rewrite its own checks; any other pull
// request runs the pipeline at its own head, as before.
func (p *project) trustedPipelineSHA(refs []Ref, r Ref) (string, *engine.RestrictedEvent) {
	if p.UntrustedAllowed || !isAutomation(p.AutomationAccounts, r.Author) {
		return r.SHA, nil
	}
	restr := &engine.RestrictedEvent{Code: engine.RestrictAutomation, Reason: "pull request from an automation account (" + r.Author + ")"}
	for _, ref := range refs {
		if ref.Kind == "branch" && ref.Name == p.DefaultBranch {
			return ref.SHA, restr
		}
	}
	return r.SHA, restr
}
