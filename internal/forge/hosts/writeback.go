package hosts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// SplitRepo splits "connection:owner/name", the way an Airfield names a
// repository kept on an outside host in write_back.repo.
func SplitRepo(ref string) (connection, path string, ok bool) {
	connection, path, ok = strings.Cut(ref, ":")
	return connection, path, ok && connection != "" && strings.Count(path, "/") == 1
}

// PushAuth is what a deploy step needs to push to the repository.
type PushAuth struct {
	URL      string
	Username string
	Secret   string
}

// WriteBackAuth checks that a deploy step may write to the repository and
// gives it the credential. A direct write to a branch the host protects
// (reviews or checks required) is refused here, before anything is pushed,
// with a message that names the pull_request mode. Merge "auto" is refused:
// Launchway does not merge on this host; its pull requests are merged there.
func (s *Service) WriteBackAuth(ctx context.Context, connection, path, branch, mode, merge string) (PushAuth, error) {
	p, _, err := s.Provider(ctx, connection)
	if err != nil {
		return PushAuth{}, err
	}
	repo, err := p.Repositories().Get(ctx, path)
	if err != nil {
		return PushAuth{}, err
	}
	switch mode {
	case "direct":
		rules, err := p.Rules().List(ctx, repo)
		if err == nil && protectedBranch(rules, branch) {
			return PushAuth{}, fmt.Errorf("%w: %s on %s needs a pull request (required reviews or checks); set the Airfield's write_back mode to pull_request, which opens one", ErrProtected, branch, repo.Path)
		}
	case "pull_request":
		if merge == "auto" && !canAutoMerge(p) {
			return PushAuth{}, errors.New("this host has no auto-merge Launchway can switch on: set write_back.merge to person (the step waits until the host merges it)")
		}
	}
	cred, err := p.Access().Credential(ctx, repo, true)
	if err != nil {
		return PushAuth{}, err
	}
	return PushAuth{URL: repo.Clone.HTTPS, Username: cred.Username, Secret: cred.Secret}, nil
}

// canAutoMerge says the host adds an auto-merge that Launchway can drive.
func canAutoMerge(p forge.Provider) bool {
	for _, a := range p.Additions() {
		if a.Key == forge.AdditionAutoMerge && a.Drivable {
			return true
		}
	}
	return false
}

// EnableAutoMerge switches on the host's own auto-merge of a pull request the
// step opened: the host merges it when its reviews and checks pass.
func (s *Service) EnableAutoMerge(ctx context.Context, connection, path string, number int) error {
	p, _, err := s.Provider(ctx, connection)
	if err != nil {
		return err
	}
	if !canAutoMerge(p) {
		return fmt.Errorf("%w: this host has no auto-merge Launchway can switch on", forge.ErrInvalid)
	}
	repo, err := p.Repositories().Get(ctx, path)
	if err != nil {
		return err
	}
	return p.Land().AutoLand(ctx, repo, number, forge.StrategySquash, true)
}

// DeployPull is the state of a deploy step's pull request.
type DeployPull struct {
	Number int
	URL    string
	State  string // open, merged or closed
	Commit string
	// WaitingFor says in a sentence what an open pull request waits for.
	WaitingFor string
}

func pullState(pr forge.Proposal, url string) DeployPull {
	d := DeployPull{Number: pr.Number, URL: url, State: "open"}
	switch pr.State {
	case forge.ProposalLanded:
		d.State = "merged"
		d.Commit, _ = pr.Ext["merge_commit"].(string)
	case forge.ProposalClosed:
		d.State = "closed"
	default:
		d.WaitingFor = "the host to merge it: its reviews and checks decide, and a person or the host's auto-merge does it"
	}
	return d
}

// OpenDeployPull opens the pull request from head into base, or finds the open one the same step opened before.
func (s *Service) OpenDeployPull(ctx context.Context, connection, path, head, base, title, body string) (DeployPull, error) {
	p, c, err := s.Provider(ctx, connection)
	if err != nil {
		return DeployPull{}, err
	}
	repo, err := p.Repositories().Get(ctx, path)
	if err != nil {
		return DeployPull{}, err
	}
	link := func(n int) string { return PullRequestURL(c.BaseURL, repo.Path, n) }
	page, err := p.Proposals().List(ctx, repo, forge.ProposalOpen, "")
	if err == nil {
		for _, pr := range page.Items {
			if pr.Source.Name == "refs/heads/"+head && pr.Source.Repository == "" && pr.Target.Name == "refs/heads/"+base {
				return pullState(pr, link(pr.Number)), nil
			}
		}
	}
	pr, err := p.Proposals().Create(ctx, repo, forge.NewProposal{Title: title, Body: body,
		Source: forge.Ref{Name: "refs/heads/" + head}, Target: forge.Ref{Name: "refs/heads/" + base}})
	if err != nil {
		return DeployPull{}, err
	}
	return pullState(pr, link(pr.Number)), nil
}

// DeployPullStatus reads a pull request the step opened.
func (s *Service) DeployPullStatus(ctx context.Context, connection, path string, number int) (DeployPull, error) {
	p, c, err := s.Provider(ctx, connection)
	if err != nil {
		return DeployPull{}, err
	}
	repo, err := p.Repositories().Get(ctx, path)
	if err != nil {
		return DeployPull{}, err
	}
	pr, err := p.Proposals().Get(ctx, repo, number)
	if err != nil {
		return DeployPull{}, err
	}
	return pullState(pr, PullRequestURL(c.BaseURL, repo.Path, number)), nil
}
