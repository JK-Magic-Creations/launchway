package bitbucketdc

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type proposals struct{ p *Provider }

// apiRepoRef names a repository inside a pull request's side.
type apiRepoRef struct {
	Slug    string `json:"slug"`
	Project struct {
		Key string `json:"key"`
	} `json:"project"`
}

func (r apiRepoRef) full() string {
	if r.Slug == "" {
		return ""
	}
	return r.Project.Key + "/" + r.Slug
}

// apiSide is a pull request's fromRef or toRef (RestPullRequestRef).
type apiSide struct {
	ID           string     `json:"id"`
	DisplayID    string     `json:"displayId"`
	LatestCommit string     `json:"latestCommit"`
	Repository   apiRepoRef `json:"repository"`
}

type apiPerson struct {
	User struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"user"`
}

// apiPull is a pull request as the host answers (RestPullRequest).
type apiPull struct {
	ID          int         `json:"id"`
	Version     int         `json:"version"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	State       string      `json:"state"`
	Draft       bool        `json:"draft"`
	Author      apiPerson   `json:"author"`
	Reviewers   []apiPerson `json:"reviewers"`
	FromRef     apiSide     `json:"fromRef"`
	ToRef       apiSide     `json:"toRef"`
	Links       struct {
		Self []*struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
}

// fork: the source repository is not the target repository.
func (a apiPull) fork() bool {
	f, t := a.FromRef.Repository.full(), a.ToRef.Repository.full()
	return f != "" && !strings.EqualFold(f, t)
}

func (a apiPull) url() string {
	for _, l := range a.Links.Self {
		if l != nil && l.Href != "" {
			return l.Href
		}
	}
	return ""
}

func (a apiPull) proposal(repo forge.Repository) forge.Proposal {
	st := forge.ProposalOpen
	switch {
	case a.State == "MERGED":
		st = forge.ProposalLanded
	case a.State == "DECLINED":
		st = forge.ProposalClosed
	case a.Draft:
		st = forge.ProposalDraft
	}
	src := forge.Ref{Name: a.FromRef.ID, Commit: a.FromRef.LatestCommit}
	if a.fork() {
		src.Repository = a.FromRef.Repository.full()
	}
	return forge.Proposal{Repository: repo.ID, Number: a.ID, Title: a.Title, Body: a.Description,
		Author: forge.Principal{Login: a.Author.User.Name, Name: a.Author.User.DisplayName, Unlinked: true},
		Source: src, Target: forge.Ref{Name: a.ToRef.ID}, State: st, Head: a.FromRef.LatestCommit, Authority: forge.AuthorityProvider,
		Ext: map[string]any{"fork": a.fork(), "url": a.url(), "version": a.Version}}
}

// List: GET .../pull-requests?state=OPEN|MERGED|DECLINED (REPO_READ); a draft
// pull request is an open one with draft true, told apart here.
func (pr proposals) List(ctx context.Context, repo forge.Repository, state forge.ProposalState, cursor string) (forge.Page[forge.Proposal], error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return forge.Page[forge.Proposal]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
		start = n
	}
	st := map[forge.ProposalState]string{forge.ProposalClosed: "DECLINED", forge.ProposalLanded: "MERGED"}[state]
	if st == "" {
		st = "OPEN"
	}
	var in page[apiPull]
	q := url.Values{"state": {st}, "limit": {"50"}, "start": {strconv.Itoa(start)}, "order": {"NEWEST"}}
	if _, err := pr.p.c.Get(ctx, rp+"/pull-requests?"+q.Encode(), &in); err != nil {
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
	if !in.IsLastPage {
		out.Next = strconv.Itoa(in.NextPageStart)
	}
	return out, nil
}

// get: GET .../pull-requests/{pullRequestId} (REPO_READ).
func (pr proposals) get(ctx context.Context, repo forge.Repository, number int) (apiPull, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return apiPull{}, err
	}
	var a apiPull
	st, err := pr.p.c.Call(ctx, http.MethodGet, rp+"/pull-requests/"+strconv.Itoa(number), nil, &a, http.StatusNotFound)
	if err != nil {
		return apiPull{}, err
	}
	if st == http.StatusNotFound {
		return apiPull{}, fmt.Errorf("%w: pull request %d", forge.ErrNotFound, number)
	}
	return a, nil
}

func (pr proposals) Get(ctx context.Context, repo forge.Repository, number int) (forge.Proposal, error) {
	a, err := pr.get(ctx, repo, number)
	if err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

func (pr proposals) refOf(r forge.Ref, own forge.Repository) (map[string]any, error) {
	path := own.Path
	if r.Repository != "" {
		path = r.Repository
	}
	k, s, err := pr.p.split(path)
	if err != nil {
		return nil, err
	}
	name := r.Name
	if !strings.HasPrefix(name, "refs/") {
		name = "refs/heads/" + name
	}
	return map[string]any{"id": name, "repository": map[string]any{"slug": s, "project": map[string]any{"key": k}}}, nil
}

// Create: POST .../pull-requests (REPO_READ on both repositories) with title,
// description, draft, fromRef and toRef (each a full ref name and its repository
// by slug and project key).
func (pr proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	from, err := pr.refOf(n.Source, repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	to, err := pr.refOf(forge.Ref{Name: n.Target.Name}, repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	body := map[string]any{"title": n.Title, "description": n.Body, "draft": n.Draft, "fromRef": from, "toRef": to}
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPost, rp+"/pull-requests", body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// Update: PUT .../pull-requests/{id} (the author, or REPO_WRITE) with the
// pull request's version (the host refuses a stale one), title, description,
// draft and the reviewers it has now (the host replaces the list it is given).
func (pr proposals) Update(ctx context.Context, repo forge.Repository, number int, patch forge.ProposalPatch) (forge.Proposal, error) {
	cur, err := pr.get(ctx, repo, number)
	if err != nil {
		return forge.Proposal{}, err
	}
	if patch.Title == nil && patch.Body == nil && patch.Draft == nil {
		return cur.proposal(repo), nil
	}
	rp, _ := pr.p.repoPath(repo)
	body := map[string]any{"version": cur.Version, "title": cur.Title, "description": cur.Description, "draft": cur.Draft}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Body != nil {
		body["description"] = *patch.Body
	}
	if patch.Draft != nil {
		body["draft"] = *patch.Draft
	}
	reviewers := []any{}
	for _, r := range cur.Reviewers {
		reviewers = append(reviewers, map[string]any{"user": map[string]any{"name": r.User.Name}})
	}
	body["reviewers"] = reviewers
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPut, rp+"/pull-requests/"+strconv.Itoa(number), body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// act posts to .../pull-requests/{id}/decline or /reopen with the version.
func (pr proposals) act(ctx context.Context, repo forge.Repository, number int, verb string) error {
	cur, err := pr.get(ctx, repo, number)
	if err != nil {
		return err
	}
	rp, _ := pr.p.repoPath(repo)
	_, err = pr.p.c.Call(ctx, http.MethodPost, rp+"/pull-requests/"+strconv.Itoa(number)+"/"+verb, map[string]any{"version": cur.Version}, nil)
	return err
}

// Close declines the pull request: POST .../decline (REPO_READ).
func (pr proposals) Close(ctx context.Context, repo forge.Repository, number int) error {
	return pr.act(ctx, repo, number, "decline")
}

// Reopen: POST .../reopen re-opens a declined pull request (REPO_READ).
func (pr proposals) Reopen(ctx context.Context, repo forge.Repository, number int) error {
	return pr.act(ctx, repo, number, "reopen")
}

func (pr proposals) Revisions(ctx context.Context, repo forge.Repository, number int) ([]forge.Revision, error) {
	p, err := pr.Get(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return []forge.Revision{{Proposal: p.ID, Number: 1, Head: p.Head}}, nil
}
