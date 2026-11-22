package gitea

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type proposals struct{ p *Provider }

type apiSide struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// apiPull is a pull request as the host answers (PullRequest, PRBranchInfo).
type apiPull struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	Body           string  `json:"body"`
	State          string  `json:"state"`
	Draft          bool    `json:"draft"`
	Merged         bool    `json:"merged"`
	MergeCommitSHA string  `json:"merge_commit_sha"`
	HTMLURL        string  `json:"html_url"`
	User           apiUser `json:"user"`
	Head           apiSide `json:"head"`
	Base           apiSide `json:"base"`
}

// fork: the head repository is not the repository the pull request is for.
func (a apiPull) fork(repoPath string) bool {
	return a.Head.Repo == nil || !strings.EqualFold(a.Head.Repo.FullName, repoPath)
}

func (a apiPull) proposal(repo forge.Repository) forge.Proposal {
	st := forge.ProposalOpen
	switch {
	case a.Merged:
		st = forge.ProposalLanded
	case a.State == "closed":
		st = forge.ProposalClosed
	case a.Draft:
		st = forge.ProposalDraft
	}
	src := forge.Ref{Name: "refs/heads/" + a.Head.Ref, Commit: a.Head.SHA}
	fork := a.fork(repo.Path)
	if fork && a.Head.Repo != nil {
		src.Repository = a.Head.Repo.FullName
	}
	return forge.Proposal{Repository: repo.ID, Number: a.Number, Title: a.Title, Body: a.Body,
		Author: forge.Principal{Login: a.User.Login, Unlinked: true}, Source: src,
		Target: forge.Ref{Name: "refs/heads/" + a.Base.Ref, Commit: a.Base.SHA}, State: st,
		Head: a.Head.SHA, Base: a.Base.SHA, Authority: forge.AuthorityProvider,
		Ext: map[string]any{"fork": fork, "merge_commit": a.MergeCommitSHA, "url": a.HTMLURL}}
}

// List: GET /repos/{o}/{r}/pulls?state=open|closed&page&limit (repoListPullRequests).
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
	a, err := pr.p.c.Get(ctx, fmt.Sprintf("%s/pulls?state=%s&limit=%d&page=%d", rp, st, pageLimit, page), &in)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	out := forge.Page[forge.Proposal]{Items: []forge.Proposal{}}
	for _, m := range in {
		p := m.proposal(repo)
		if state == forge.ProposalOpen && p.State == forge.ProposalDraft || state == forge.ProposalDraft && p.State != forge.ProposalDraft ||
			state == forge.ProposalLanded && p.State != forge.ProposalLanded {
			continue
		}
		out.Items = append(out.Items, p)
	}
	if more(a) {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

// Get: GET /repos/{o}/{r}/pulls/{index} (repoGetPullRequest).
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

// Create: POST /repos/{o}/{r}/pulls with title, body, head and base
// (repoCreatePullRequest; CreatePullRequestOption has no draft field).
func (pr proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	if n.Draft {
		return forge.Proposal{}, pr.p.Caps.Err(forge.CapDrafts)
	}
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPost, rp+"/pulls", map[string]any{
		"title": n.Title, "body": n.Body, "head": restkit.TrimHeads(n.Source.Name), "base": restkit.TrimHeads(n.Target.Name)}, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// patch: PATCH /repos/{o}/{r}/pulls/{index} (repoEditPullRequest: title, body, state).
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
		return forge.Proposal{}, pr.p.Caps.Err(forge.CapDrafts)
	}
	body := map[string]any{}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Body != nil {
		body["body"] = *patch.Body
	}
	if len(body) == 0 {
		return pr.Get(ctx, repo, number)
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
