package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type proposals struct{ p *Provider }

type apiSide struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// apiPull is a pull request as the host answers (PullRequest, Pull Request Endpoint).
type apiPull struct {
	ID     int     `json:"id"`
	Title  string  `json:"title"`
	Desc   string  `json:"description"`
	State  string  `json:"state"`
	Draft  bool    `json:"draft"`
	Author apiUser `json:"author"`
	Source apiSide `json:"source"`
	Dest   apiSide `json:"destination"`
	Merge  *struct {
		Hash string `json:"hash"`
	} `json:"merge_commit"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// fork: the source repository is not the destination repository.
func (a apiPull) fork() bool {
	return a.Source.Repository.FullName != "" && a.Source.Repository.FullName != a.Dest.Repository.FullName
}

func (a apiPull) proposal(repo forge.Repository) forge.Proposal {
	st := forge.ProposalOpen
	switch {
	case a.State == "MERGED":
		st = forge.ProposalLanded
	case a.State == "DECLINED" || a.State == "SUPERSEDED":
		st = forge.ProposalClosed
	case a.Draft:
		st = forge.ProposalDraft
	}
	src := forge.Ref{Name: "refs/heads/" + a.Source.Branch.Name, Commit: a.Source.Commit.Hash}
	if a.fork() {
		src.Repository = a.Source.Repository.FullName
	}
	merge := ""
	if a.Merge != nil {
		merge = a.Merge.Hash
	}
	return forge.Proposal{Repository: repo.ID, Number: a.ID, Title: a.Title, Body: a.Desc, Author: forge.Principal{Login: a.Author.login(), Unlinked: true},
		Source: src, Target: forge.Ref{Name: "refs/heads/" + a.Dest.Branch.Name}, State: st, Head: a.Source.Commit.Hash, Authority: forge.AuthorityProvider,
		Ext: map[string]any{"fork": a.fork(), "merge_commit": merge, "url": a.Links.HTML.Href}}
}

// List: GET /repositories/{w}/{r}/pullrequests?state=OPEN|MERGED|DECLINED (scope pullrequest).
func (pr proposals) List(ctx context.Context, repo forge.Repository, state forge.ProposalState, cursor string) (forge.Page[forge.Proposal], error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	pg := 1
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			pg = n
		}
	}
	st := map[forge.ProposalState]string{forge.ProposalClosed: "DECLINED", forge.ProposalLanded: "MERGED"}[state]
	if st == "" {
		st = "OPEN"
	}
	var in page[apiPull]
	if _, err := pr.p.c.Get(ctx, fmt.Sprintf("%s/pullrequests?state=%s&pagelen=50&page=%d", rp, st, pg), &in); err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	out := forge.Page[forge.Proposal]{Items: []forge.Proposal{}}
	for _, m := range in.Values {
		p := m.proposal(repo)
		if state == forge.ProposalOpen && p.State == forge.ProposalDraft || state == forge.ProposalDraft && p.State != forge.ProposalDraft {
			continue
		}
		out.Items = append(out.Items, p)
	}
	if in.Next != "" {
		out.Next = strconv.Itoa(pg + 1)
	}
	return out, nil
}

// Get: GET /repositories/{w}/{r}/pullrequests/{id}.
func (pr proposals) Get(ctx context.Context, repo forge.Repository, number int) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiPull
	st, err := pr.p.c.Call(ctx, http.MethodGet, rp+"/pullrequests/"+strconv.Itoa(number), nil, &a, http.StatusNotFound)
	if err != nil {
		return forge.Proposal{}, err
	}
	if st == http.StatusNotFound {
		return forge.Proposal{}, fmt.Errorf("%w: pull request %d", forge.ErrNotFound, number)
	}
	return a.proposal(repo), nil
}

// Create: POST /repositories/{w}/{r}/pullrequests (scope pullrequest:write) with
// title, description, draft, source.branch.name and destination.branch.name.
func (pr proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	body := map[string]any{"title": n.Title, "description": n.Body, "draft": n.Draft,
		"source":      map[string]any{"branch": map[string]any{"name": restkit.TrimHeads(n.Source.Name)}},
		"destination": map[string]any{"branch": map[string]any{"name": restkit.TrimHeads(n.Target.Name)}}}
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPost, rp+"/pullrequests", body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// Update: PUT /repositories/{w}/{r}/pullrequests/{id} with title, description or draft.
func (pr proposals) Update(ctx context.Context, repo forge.Repository, number int, patch forge.ProposalPatch) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	body := map[string]any{}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Body != nil {
		body["description"] = *patch.Body
	}
	if patch.Draft != nil {
		body["draft"] = *patch.Draft
	}
	if len(body) == 0 {
		return pr.Get(ctx, repo, number)
	}
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPut, rp+"/pullrequests/"+strconv.Itoa(number), body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// Close declines the pull request: POST .../pullrequests/{id}/decline (scope pullrequest:write).
func (pr proposals) Close(ctx context.Context, repo forge.Repository, number int) error {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return err
	}
	_, err = pr.p.c.Call(ctx, http.MethodPost, rp+"/pullrequests/"+strconv.Itoa(number)+"/decline", nil, nil)
	return err
}

// Reopen: the host has no call to reopen a declined pull request.
func (pr proposals) Reopen(context.Context, forge.Repository, int) error {
	return fmt.Errorf("%w: Bitbucket cannot reopen a declined pull request; open a new one", forge.ErrInvalid)
}

func (pr proposals) Revisions(ctx context.Context, repo forge.Repository, number int) ([]forge.Revision, error) {
	p, err := pr.Get(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return []forge.Revision{{Proposal: p.ID, Number: 1, Head: p.Head}}, nil
}
