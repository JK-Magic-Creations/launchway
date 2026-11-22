package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type proposals struct{ p *Provider }

// apiMR is a merge request as the host answers
// (https://docs.gitlab.com/api/merge_requests/ , "Get single MR").
type apiMR struct {
	IID             int    `json:"iid"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	State           string `json:"state"`
	Draft           bool   `json:"draft"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	SourceProjectID int    `json:"source_project_id"`
	TargetProjectID int    `json:"target_project_id"`
	SHA             string `json:"sha"`
	MergeCommitSHA  string `json:"merge_commit_sha"`
	WebURL          string `json:"web_url"`
	MergedAt        string `json:"merged_at"`
	Author          struct {
		Username string `json:"username"`
	} `json:"author"`
	DiffRefs *struct {
		BaseSHA string `json:"base_sha"`
	} `json:"diff_refs"`
}

func (a apiMR) proposal(repo forge.Repository) forge.Proposal {
	st := forge.ProposalOpen
	switch {
	case a.State == "merged" || a.MergedAt != "":
		st = forge.ProposalLanded
	case a.State == "closed":
		st = forge.ProposalClosed
	case a.Draft:
		st = forge.ProposalDraft
	}
	// A fork: the merge request's source project is not its target project.
	fork := a.SourceProjectID != a.TargetProjectID
	base := ""
	if a.DiffRefs != nil {
		base = a.DiffRefs.BaseSHA
	}
	return forge.Proposal{Repository: repo.ID, Number: a.IID, Title: a.Title, Body: a.Description,
		Author: forge.Principal{Login: a.Author.Username, Unlinked: true},
		Source: forge.Ref{Name: "refs/heads/" + a.SourceBranch, Commit: a.SHA}, Target: forge.Ref{Name: "refs/heads/" + a.TargetBranch},
		State: st, Head: a.SHA, Base: base, Authority: forge.AuthorityProvider,
		Ext: map[string]any{"fork": fork, "merge_commit": a.MergeCommitSHA, "url": a.WebURL}}
}

// List: GET /projects/:id/merge_requests?state=opened|closed|merged
// (https://docs.gitlab.com/api/merge_requests/ , "List project merge requests").
func (pr proposals) List(ctx context.Context, repo forge.Repository, state forge.ProposalState, cursor string) (forge.Page[forge.Proposal], error) {
	pp, err := pr.p.projectPath(repo)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	page := 1
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			page = n
		}
	}
	st := "opened"
	switch state {
	case forge.ProposalClosed:
		st = "closed"
	case forge.ProposalLanded:
		st = "merged"
	}
	var in []apiMR
	a, err := pr.p.c.Get(ctx, fmt.Sprintf("%s/merge_requests?state=%s&per_page=50&page=%d", pp, st, page), &in)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	out := forge.Page[forge.Proposal]{Items: []forge.Proposal{}}
	for _, m := range in {
		p := m.proposal(repo)
		if state == forge.ProposalOpen && p.State == forge.ProposalDraft || state == forge.ProposalDraft && p.State != forge.ProposalDraft {
			continue
		}
		out.Items = append(out.Items, p)
	}
	if a.Header.Get("X-Next-Page") != "" {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

// Get: GET /projects/:id/merge_requests/:merge_request_iid.
func (pr proposals) Get(ctx context.Context, repo forge.Repository, number int) (forge.Proposal, error) {
	pp, err := pr.p.projectPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiMR
	st, err := pr.p.c.Call(ctx, http.MethodGet, pp+"/merge_requests/"+strconv.Itoa(number), nil, &a, http.StatusNotFound)
	if err != nil {
		return forge.Proposal{}, err
	}
	if st == http.StatusNotFound {
		return forge.Proposal{}, fmt.Errorf("%w: merge request %d", forge.ErrNotFound, number)
	}
	return a.proposal(repo), nil
}

func (pr proposals) noDraft() error { return pr.p.Caps.Err(forge.CapDrafts) }

// Create: POST /projects/:id/merge_requests with source_branch, target_branch,
// title and description (required: source_branch, target_branch, title).
func (pr proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	if n.Draft {
		return forge.Proposal{}, pr.noDraft()
	}
	pp, err := pr.p.projectPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiMR
	if _, err := pr.p.c.Call(ctx, http.MethodPost, pp+"/merge_requests", map[string]any{
		"source_branch": restkit.TrimHeads(n.Source.Name), "target_branch": restkit.TrimHeads(n.Target.Name), "title": n.Title, "description": n.Body}, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// put: PUT /projects/:id/merge_requests/:iid with title, description or
// state_event (close, reopen).
func (pr proposals) put(ctx context.Context, repo forge.Repository, number int, body map[string]any) (forge.Proposal, error) {
	pp, err := pr.p.projectPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiMR
	if _, err := pr.p.c.Call(ctx, http.MethodPut, pp+"/merge_requests/"+strconv.Itoa(number), body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

func (pr proposals) Update(ctx context.Context, repo forge.Repository, number int, patch forge.ProposalPatch) (forge.Proposal, error) {
	if patch.Draft != nil {
		return forge.Proposal{}, pr.noDraft()
	}
	body := map[string]any{}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Body != nil {
		body["description"] = *patch.Body
	}
	if len(body) == 0 {
		return pr.Get(ctx, repo, number)
	}
	return pr.put(ctx, repo, number, body)
}

func (pr proposals) Close(ctx context.Context, repo forge.Repository, number int) error {
	_, err := pr.put(ctx, repo, number, map[string]any{"state_event": "close"})
	return err
}

func (pr proposals) Reopen(ctx context.Context, repo forge.Repository, number int) error {
	_, err := pr.put(ctx, repo, number, map[string]any{"state_event": "reopen"})
	return err
}

func (pr proposals) Revisions(ctx context.Context, repo forge.Repository, number int) ([]forge.Revision, error) {
	p, err := pr.Get(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return []forge.Revision{{Proposal: p.ID, Number: 1, Head: p.Head, Base: p.Base}}, nil
}
