package source

// Pull requests from forks (docs/guide/forks.md). The server hosting a
// repository knows which pull requests come from a fork; the watcher asks it
// through a ForkGate before it starts a run. A fork's pull request is code
// nobody on the team reviewed, so its run is restricted exactly as an
// automation account's is (no secrets, no deploys, no saved caches, no registry
// pushes) and takes its pipeline from the default branch's tip. Its author's
// first contribution waits until a person with write access approves running it.

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// ForkVerdict is what the repository's host says about a pull request.
type ForkVerdict struct {
	// Fork says the pull request comes from another repository.
	Fork bool
	// Reason is the sentence a restricted run is explained with.
	Reason string
	// Wait, when set, says why the run does not start yet (it waits for an
	// approval); the head is looked at again at every poll.
	Wait string
}

// ForkGate answers for one pull request of a project's repository: its number
// and the head commit the run would be for.
type ForkGate func(ctx context.Context, project, url string, number int, sha string) ForkVerdict

type forkGateHolder struct{ g ForkGate }

// SetForkGate tells the watcher how to find out whether a pull request is from a
// fork (nil: none is).
func (m *Manager) SetForkGate(g ForkGate) { m.forkGate.Store(&forkGateHolder{g}) }

func (m *Manager) forkVerdict(ctx context.Context, p *project, number int, sha string) ForkVerdict {
	h := m.forkGate.Load()
	if h == nil || h.g == nil {
		return ForkVerdict{}
	}
	return h.g(ctx, p.Name, p.URL, number, sha)
}

// forkPipelineSHA is the commit whose pipeline a fork's pull request runs: the
// default branch's tip, so the pull request cannot rewrite its own checks, and
// the restriction that goes with it.
func (p *project) forkPipelineSHA(refs []Ref, r Ref, reason string) (string, *engine.RestrictedEvent) {
	restr := &engine.RestrictedEvent{Code: engine.RestrictFork, Reason: reason}
	// The pull_requests.untrusted setting is for automation accounts; a fork's pull
	// request is restricted and takes the default branch's pipeline whatever it says.
	for _, ref := range refs {
		if ref.Kind == "branch" && ref.Name == p.DefaultBranch {
			return ref.SHA, restr
		}
	}
	return r.SHA, restr
}
