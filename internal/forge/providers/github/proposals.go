package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type proposals struct{ p *Provider }

type apiPull struct {
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	State    string     `json:"state"`
	Draft    bool       `json:"draft"`
	MergedAt *time.Time `json:"merged_at"`
	MergeSHA string     `json:"merge_commit_sha"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	Head apiPullSide `json:"head"`
	Base apiPullSide `json:"base"`
}

type apiPullSide struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

func (a apiPull) proposal(repo forge.Repository) forge.Proposal {
	st := forge.ProposalOpen
	switch {
	case a.MergedAt != nil:
		st = forge.ProposalLanded
	case a.State == "closed":
		st = forge.ProposalClosed
	case a.Draft:
		st = forge.ProposalDraft
	}
	src := forge.Ref{Name: "refs/heads/" + a.Head.Ref, Commit: a.Head.SHA}
	fork := a.Head.Repo == nil || a.Head.Repo.FullName != repo.Path
	if fork && a.Head.Repo != nil {
		src.Repository = a.Head.Repo.FullName
	}
	return forge.Proposal{Repository: repo.ID, Number: a.Number, Title: a.Title, Body: a.Body,
		Author: forge.Principal{Login: a.User.Login, Unlinked: true}, Source: src,
		Target: forge.Ref{Name: "refs/heads/" + a.Base.Ref, Commit: a.Base.SHA}, State: st,
		Head: a.Head.SHA, Base: a.Base.SHA, Authority: forge.AuthorityProvider, Ext: map[string]any{"fork": fork, "merge_commit": a.MergeSHA}}
}

// List: GET /repos/{o}/{r}/pulls (https://docs.github.com/en/rest/pulls/pulls#list-pull-requests).
func (pr proposals) List(ctx context.Context, repo forge.Repository, state forge.ProposalState, cursor string) (forge.Page[forge.Proposal], error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	page := 1
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			page = n
		}
	}
	st := "open"
	if state == forge.ProposalClosed || state == forge.ProposalLanded {
		st = "closed"
	}
	var in []apiPull
	more, err := pr.p.c.CallPage(ctx, fmt.Sprintf("%s/pulls?state=%s&per_page=50&page=%d", rp, st, page), &in)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	out := forge.Page[forge.Proposal]{Items: []forge.Proposal{}}
	for _, a := range in {
		p := a.proposal(repo)
		if state == forge.ProposalOpen && p.State == forge.ProposalDraft || state == forge.ProposalDraft && p.State != forge.ProposalDraft ||
			state == forge.ProposalLanded && p.State != forge.ProposalLanded {
			continue
		}
		out.Items = append(out.Items, p)
	}
	if more {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

// Get: GET /repos/{o}/{r}/pulls/{n} (https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request).
func (pr proposals) Get(ctx context.Context, repo forge.Repository, number int) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiPull
	st, err := pr.p.c.Call(ctx, http.MethodGet, rp+"/pulls/"+strconv.Itoa(number), nil, &a, http.StatusNotFound)
	if err != nil {
		return forge.Proposal{}, err
	}
	if st == http.StatusNotFound {
		return forge.Proposal{}, fmt.Errorf("%w: pull request %d", forge.ErrNotFound, number)
	}
	return a.proposal(repo), nil
}

// Create: POST /repos/{o}/{r}/pulls with title, head, base, body, draft
// (https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request).
func (pr proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiPull
	_, err = pr.p.c.Call(ctx, http.MethodPost, rp+"/pulls", map[string]any{
		"title": n.Title, "body": n.Body, "head": trimHeads(n.Source.Name), "base": trimHeads(n.Target.Name), "draft": n.Draft}, &a)
	if err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

func trimHeads(s string) string {
	const p = "refs/heads/"
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

// patch: PATCH /repos/{o}/{r}/pulls/{n} (https://docs.github.com/en/rest/pulls/pulls#update-a-pull-request).
func (pr proposals) patch(ctx context.Context, repo forge.Repository, number int, body map[string]any) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPatch, rp+"/pulls/"+strconv.Itoa(number), body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

func (pr proposals) Update(ctx context.Context, repo forge.Repository, number int, patch forge.ProposalPatch) (forge.Proposal, error) {
	if patch.Draft != nil {
		return forge.Proposal{}, &forge.ErrUnsupported{Capability: forge.CapDrafts, Reason: "the host changes draft state only through its GraphQL API",
			Workaround: "Mark the pull request ready or draft on the Git host"}
	}
	body := map[string]any{}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Body != nil {
		body["body"] = *patch.Body
	}
	return pr.patch(ctx, repo, number, body)
}

func (pr proposals) Close(ctx context.Context, repo forge.Repository, number int) error {
	_, err := pr.patch(ctx, repo, number, map[string]any{"state": "closed"})
	return err
}

func (pr proposals) Reopen(ctx context.Context, repo forge.Repository, number int) error {
	_, err := pr.patch(ctx, repo, number, map[string]any{"state": "open"})
	return err
}

func (pr proposals) Revisions(ctx context.Context, repo forge.Repository, number int) ([]forge.Revision, error) {
	p, err := pr.Get(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return []forge.Revision{{Proposal: p.ID, Number: 1, Head: p.Head, Base: p.Base}}, nil
}
