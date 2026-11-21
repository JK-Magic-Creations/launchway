package launchway

import (
	"context"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// Proposals are pull requests of the built-in hosting (internal/forge/pulls).
// The provider is the same interface every forge has: the updater and any
// other client opens, updates and closes proposals through it.
func (p *Provider) Proposals() forge.Proposals {
	if p.o.Pulls == nil {
		return p.Base.Proposals()
	}
	return proposals{p}
}

type proposals struct{ p *Provider }

func (x proposals) svc() *pulls.Service { return x.p.o.Pulls }

func branchOf(ref string) string { return strings.TrimPrefix(ref, "refs/heads/") }

func refOf(branch string) forge.Ref { return forge.Ref{Name: "refs/heads/" + branchOf(branch)} }

func toProposal(repo forge.Repository, pr pulls.PullRequest) forge.Proposal {
	st := forge.ProposalOpen
	switch {
	case pr.State == pulls.Merged:
		st = forge.ProposalLanded
	case pr.State == pulls.Closed:
		st = forge.ProposalClosed
	case pr.Draft:
		st = forge.ProposalDraft
	}
	m := forge.Mergeability{Ready: pr.Mergeability.State == pulls.MergeClean}
	if pr.Mergeability.State == pulls.MergeConflicts {
		m.Reasons = append(m.Reasons, "the branches conflict")
	}
	return forge.Proposal{
		ID: pr.ID, Repository: repo.ID, Number: pr.Number, Title: pr.Title, Body: pr.Body,
		Author: forge.Principal{Login: pr.Author, User: pr.Author},
		Source: forge.Ref{Name: "refs/heads/" + pr.SourceBranch, Commit: pr.HeadSHA},
		Target: forge.Ref{Name: "refs/heads/" + pr.TargetBranch, Commit: pr.BaseSHA},
		State:  st, Head: pr.HeadSHA, Base: pr.BaseSHA, Mergeable: m, Authority: forge.AuthorityLaunchway,
		SyncedAt: pr.UpdatedAt, Ext: map[string]any{"labels": pr.Labels, "closed_by": pr.ClosedBy},
	}
}

func (x proposals) List(ctx context.Context, repo forge.Repository, state forge.ProposalState, cursor string) (forge.Page[forge.Proposal], error) {
	f := pulls.Filter{Limit: 200}
	switch state {
	case forge.ProposalOpen, forge.ProposalDraft:
		f.State = "open"
	case forge.ProposalLanded:
		f.State = "merged"
	case forge.ProposalClosed:
		f.State = "closed"
	}
	items, _, err := x.svc().Store.List(ctx, repo.ID, f)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	var out []forge.Proposal
	for _, it := range items {
		pp := toProposal(repo, it)
		if state == forge.ProposalDraft && pp.State != forge.ProposalDraft {
			continue
		}
		out = append(out, pp)
	}
	return forge.Page[forge.Proposal]{Items: out}, nil
}

func (x proposals) Get(ctx context.Context, repo forge.Repository, number int) (forge.Proposal, error) {
	pr, err := x.svc().Store.Get(ctx, repo.ID, number)
	if err != nil {
		return forge.Proposal{}, err
	}
	return toProposal(repo, pr), nil
}

func (x proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	author := x.p.o.ProposalAuthor
	if author == "" {
		author = "launchway"
	}
	pr, err := x.svc().Open(ctx, repo.ID, author, pulls.New{Title: n.Title, Body: n.Body,
		SourceBranch: branchOf(n.Source.Name), TargetBranch: branchOf(n.Target.Name), Draft: n.Draft})
	if err != nil {
		return forge.Proposal{}, err
	}
	return toProposal(repo, pr), nil
}

func (x proposals) Update(ctx context.Context, repo forge.Repository, number int, patch forge.ProposalPatch) (forge.Proposal, error) {
	pr, err := x.svc().Store.Get(ctx, repo.ID, number)
	if err != nil {
		return forge.Proposal{}, err
	}
	if err := x.svc().Store.Apply(ctx, pr.ID, pulls.Edit{Title: patch.Title, Body: patch.Body, Draft: patch.Draft}); err != nil {
		return forge.Proposal{}, err
	}
	pr, err = x.svc().Store.Get(ctx, repo.ID, number)
	return toProposal(repo, pr), err
}

func (x proposals) Close(ctx context.Context, repo forge.Repository, number int) error {
	pr, err := x.svc().Store.Get(ctx, repo.ID, number)
	if err != nil {
		return err
	}
	actor := x.p.o.ProposalAuthor
	if actor == "" {
		actor = "launchway"
	}
	return x.svc().Close(ctx, repo.ID, pr, actor)
}

func (x proposals) Reopen(ctx context.Context, repo forge.Repository, number int) error {
	pr, err := x.svc().Store.Get(ctx, repo.ID, number)
	if err != nil {
		return err
	}
	return x.svc().Reopen(ctx, repo.ID, pr, x.p.o.ProposalAuthor)
}

func (x proposals) Revisions(ctx context.Context, repo forge.Repository, number int) ([]forge.Revision, error) {
	pr, err := x.svc().Store.Get(ctx, repo.ID, number)
	if err != nil {
		return nil, err
	}
	revs, err := x.svc().Store.Revisions(ctx, pr.ID)
	if err != nil {
		return nil, err
	}
	var out []forge.Revision
	for _, r := range revs {
		out = append(out, forge.Revision{Proposal: pr.ID, Number: r.Seq, Head: r.HeadSHA, Base: r.BaseSHA, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// SetLabels replaces the labels of a proposal. It is not part of the common
// Proposals interface (forges differ), so clients ask for it by type.
func (x proposals) SetLabels(ctx context.Context, repo forge.Repository, number int, labels []string) error {
	pr, err := x.svc().Store.Get(ctx, repo.ID, number)
	if err != nil {
		return err
	}
	return x.svc().Store.Apply(ctx, pr.ID, pulls.Edit{Labels: &labels})
}

// ClosingNote is the last comment of a closed proposal: what the person who
// closed it said, which the updater reads as "ignore this version/major/dependency".
func (x proposals) ClosingNote(ctx context.Context, repo forge.Repository, number int) (string, error) {
	pr, err := x.svc().Store.Get(ctx, repo.ID, number)
	if err != nil {
		return "", err
	}
	cs, err := x.svc().Store.Comments(ctx, pr.ID)
	if err != nil {
		return "", err
	}
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Author != x.p.o.ProposalAuthor && cs[i].Body != "" {
			return cs[i].Body, nil
		}
	}
	return "", nil
}
