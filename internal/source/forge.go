package source

// The project's forge connection (the Git host that holds its pull requests),
// as far as the pipeline's pull-request filter needs it. A pull request is
// only a ref to the server (refs/pull/<n>/head): the branch it targets is
// the host's to say, so a pipeline that filters pull requests by it
// (on.pull_request.into) asks the project's forge. The connection and its
// credentials are the forge provider's business; the server only asks this
// interface, and never runs a pull request it could not ask about.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// PullRequestInfo is what a forge says about a pull request.
type PullRequestInfo struct {
	Number int
	// Head is the commit at the pull request's head, when the forge says;
	// empty if it does not.
	Head string
	// Base is the branch the pull request would be merged into.
	Base string
}

// Forge is a project's connection to its Git host.
type Forge interface {
	// PullRequest reads one pull request of the repository the project
	// mirrors. An error means the host could not be asked, or did not
	// answer: the caller must not guess.
	PullRequest(ctx context.Context, repo string, number int) (PullRequestInfo, error)
}

// forgeTimeout bounds one question to a forge.
const forgeTimeout = 15 * time.Second

// SetForge gives a project its forge connection, or takes it away with nil.
func (m *Manager) SetForge(project string, f Forge) error {
	p := m.projects[project]
	if p == nil {
		return fmt.Errorf("%w: project %s", engine.ErrNotFound, project)
	}
	m.forgeMu.Lock()
	defer m.forgeMu.Unlock()
	if m.forges == nil {
		m.forges = map[string]Forge{}
	}
	if f == nil {
		delete(m.forges, project)
	} else {
		m.forges[project] = f
	}
	return nil
}

// forge is the project's forge connection, or nil.
func (m *Manager) forge(project string) Forge {
	m.forgeMu.Lock()
	defer m.forgeMu.Unlock()
	return m.forges[project]
}

// HasForge reports whether the project has a forge connection.
func (m *Manager) HasForge(project string) bool { return m.forge(project) != nil }

type targetKey struct {
	project string
	pr      int
	sha     string
}

// intoResult is what the pull-request filter decided for one head.
type intoResult struct {
	// Target is the branch the pull request targets, when it was read.
	Target string
	// Note adds to the decision of a run that starts, such as a retarget.
	Note string
	// Reject, if set, is why the pull request is not run (the decision).
	Reject string
	// Wait, if set, means the target could not be read: nothing runs and
	// the head is looked at again at the next poll.
	Wait string
}

// pullRequestInto applies the default branch's on.pull_request.into to one
// pull-request head. It fails closed: without a forge, or when the forge
// cannot say, the pull request does not run, whatever the filter would have
// said, and the head is not marked seen, so that it runs once there is a
// forge that answers.
func (m *Manager) pullRequestInto(ctx context.Context, p *project, n int, sha string, filter *pipeline.PullRequestTrigger) intoResult {
	if filter == nil || len(filter.Into) == 0 {
		return intoResult{}
	}
	f := m.forge(p.Name)
	if f == nil {
		// Not a verdict on this head: adding a forge later must run it, so it
		// is not claimed, and the decision is recorded once.
		return intoResult{Wait: fmt.Sprintf("waiting: pull request #%d not run: the pipeline filters by target branch and the project has no forge connection; "+
			"it is looked at again at every poll, and runs once the project has one", n)}
	}
	key := targetKey{p.Name, n, sha}
	m.forgeMu.Lock()
	target, cached := m.targets[key]
	m.forgeMu.Unlock()
	if !cached {
		cctx, cancel := context.WithTimeout(ctx, forgeTimeout)
		info, err := f.PullRequest(cctx, p.URL, n)
		cancel()
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return intoResult{Wait: "waiting: forge unreachable (the server is stopping)"}
			}
			return intoResult{Wait: fmt.Sprintf("waiting: forge unreachable: pull request #%d is not run until the host answers (%s)", n, clipLine(redact.URLs(err.Error())))}
		case info.Base == "":
			return intoResult{Wait: fmt.Sprintf("waiting: forge unreachable: the host did not say which branch pull request #%d targets", n)}
		case info.Head != "" && info.Head != sha:
			return intoResult{Wait: fmt.Sprintf("waiting: the host reports another head for pull request #%d (%.10s, not %.10s); trying again at the next poll", n, info.Head, sha)}
		}
		target = info.Base
		m.forgeMu.Lock()
		if m.targets == nil || len(m.targets) > 2000 {
			m.targets = map[targetKey]string{}
		}
		m.targets[key] = target
		prev, had := m.lastTarget[targetPR{p.Name, n}]
		if m.lastTarget == nil || len(m.lastTarget) > 2000 {
			m.lastTarget = map[targetPR]string{}
		}
		m.lastTarget[targetPR{p.Name, n}] = target
		m.forgeMu.Unlock()
		res := m.matchInto(n, target, filter)
		if had && prev != target {
			res.Note = fmt.Sprintf("the target changed from %s to %s", prev, target)
			if res.Reject != "" {
				res.Reject += " (the target changed from " + prev + ")"
				res.Note = ""
			}
		}
		return res
	}
	return m.matchInto(n, target, filter)
}

// matchInto compares a target with the filter.
func (m *Manager) matchInto(n int, target string, filter *pipeline.PullRequestTrigger) intoResult {
	if !(pipeline.Triggers{PullRequest: filter}).MatchPullRequestInto(target) {
		return intoResult{Target: target, Reject: fmt.Sprintf("pull request #%d not run: it targets %s, and the pipeline runs for pull requests into %s",
			n, target, strings.Join(filter.Into, ", "))}
	}
	return intoResult{Target: target}
}

type targetPR struct {
	project string
	pr      int
}

func clipLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
