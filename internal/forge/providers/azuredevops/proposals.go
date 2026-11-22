package azuredevops

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type proposals struct{ p *Provider }

type apiIdentity struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
}

func (a apiIdentity) login() string {
	if a.UniqueName != "" {
		return a.UniqueName
	}
	return a.DisplayName
}

type apiCommitRef struct {
	CommitID string `json:"commitId"`
}

// apiPull is a GitPullRequest
// (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-requests).
type apiPull struct {
	ID                    int          `json:"pullRequestId"`
	Title                 string       `json:"title"`
	Description           string       `json:"description"`
	Status                string       `json:"status"`
	IsDraft               bool         `json:"isDraft"`
	CreatedBy             apiIdentity  `json:"createdBy"`
	SourceRef             string       `json:"sourceRefName"`
	TargetRef             string       `json:"targetRefName"`
	LastMergeSourceCommit apiCommitRef `json:"lastMergeSourceCommit"`
	LastMergeCommit       apiCommitRef `json:"lastMergeCommit"`
	ForkSource            *struct {
		Repository struct {
			Name    string `json:"name"`
			Project struct {
				Name string `json:"name"`
			} `json:"project"`
		} `json:"repository"`
	} `json:"forkSource"`
	AutoCompleteSetBy *apiIdentity `json:"autoCompleteSetBy"`
	CompletionOptions *struct {
		MergeStrategy string `json:"mergeStrategy"`
	} `json:"completionOptions"`
	Repository struct {
		Name      string `json:"name"`
		WebURL    string `json:"webUrl"`
		RemoteURL string `json:"remoteUrl"`
		Project   struct {
			Name string `json:"name"`
		} `json:"project"`
	} `json:"repository"`
}

// fork: "If this is a PR from a fork this will contain information about its source" (forkSource).
func (a apiPull) fork() bool { return a.ForkSource != nil }

// webURL is where a person opens the pull request.
func (a apiPull) webURL() string {
	base := a.Repository.WebURL
	if base == "" {
		base = noUser(a.Repository.RemoteURL)
	}
	if base == "" {
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/pullrequest/" + strconv.Itoa(a.ID)
}

func (a apiPull) proposal(repo forge.Repository) forge.Proposal {
	st := forge.ProposalOpen
	switch {
	case a.Status == "completed":
		st = forge.ProposalLanded
	case a.Status == "abandoned":
		st = forge.ProposalClosed
	case a.IsDraft:
		st = forge.ProposalDraft
	}
	src := forge.Ref{Name: a.SourceRef, Commit: a.LastMergeSourceCommit.CommitID}
	if a.fork() {
		src.Repository = a.ForkSource.Repository.Project.Name + "/" + a.ForkSource.Repository.Name
	}
	return forge.Proposal{Repository: repo.ID, Number: a.ID, Title: a.Title, Body: a.Description, Author: forge.Principal{Login: a.CreatedBy.login(), Unlinked: true},
		Source: src, Target: forge.Ref{Name: a.TargetRef}, State: st, Head: a.LastMergeSourceCommit.CommitID, Authority: forge.AuthorityProvider,
		Ext: map[string]any{"fork": a.fork(), "merge_commit": a.LastMergeCommit.CommitID, "url": a.webURL(), "auto_complete": a.AutoCompleteSetBy != nil}}
}

const pageSize = 50

// List: GET .../pullrequests?searchCriteria.status={active|abandoned|completed}&$top=50&$skip=n
// (scope vso.code; the list truncates a description to 400 characters). A
// draft is an active pull request with isDraft. The cursor is the number
// skipped.
func (pr proposals) List(ctx context.Context, repo forge.Repository, state forge.ProposalState, cursor string) (forge.Page[forge.Proposal], error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	skip := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return forge.Page[forge.Proposal]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
		skip = n
	}
	st := map[forge.ProposalState]string{forge.ProposalClosed: "abandoned", forge.ProposalLanded: "completed"}[state]
	if st == "" {
		st = "active"
	}
	var in page[apiPull]
	if _, err := pr.p.c.Get(ctx, rp+"/pullrequests"+qs("searchCriteria.status", st, "$top", strconv.Itoa(pageSize), "$skip", strconv.Itoa(skip)), &in); err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	out := forge.Page[forge.Proposal]{Items: []forge.Proposal{}}
	for _, m := range in.Value {
		p := m.proposal(repo)
		if state == forge.ProposalOpen && p.State == forge.ProposalDraft || state == forge.ProposalDraft && p.State != forge.ProposalDraft {
			continue
		}
		out.Items = append(out.Items, p)
	}
	if len(in.Value) == pageSize {
		out.Next = strconv.Itoa(skip + pageSize)
	}
	return out, nil
}

func (pr proposals) get(ctx context.Context, repo forge.Repository, number int) (apiPull, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return apiPull{}, err
	}
	var a apiPull
	st, err := pr.p.c.Call(ctx, http.MethodGet, rp+"/pullrequests/"+strconv.Itoa(number)+qs(), nil, &a, http.StatusNotFound)
	if err != nil {
		return apiPull{}, err
	}
	if st == http.StatusNotFound {
		return apiPull{}, fmt.Errorf("%w: pull request %d", forge.ErrNotFound, number)
	}
	return a, nil
}

// Get: GET .../pullrequests/{pullRequestId} (scope vso.code).
func (pr proposals) Get(ctx context.Context, repo forge.Repository, number int) (forge.Proposal, error) {
	a, err := pr.get(ctx, repo, number)
	if err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// Create: POST .../pullrequests with sourceRefName, targetRefName, title,
// description and isDraft (scope vso.code_write;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/create).
func (pr proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	body := map[string]any{"sourceRefName": headRef(n.Source.Name), "targetRefName": headRef(n.Target.Name), "title": n.Title, "description": n.Body, "isDraft": n.Draft}
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPost, rp+"/pullrequests"+qs(), body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

func headRef(name string) string {
	if strings.HasPrefix(name, "refs/") {
		return name
	}
	return "refs/heads/" + name
}

// Update: PATCH .../pullrequests/{id} with title or description (scope
// vso.code_write). The reference lists the properties an update may change:
// status, title, description (up to 4000 characters), completion options,
// merge options, the auto-complete identity and the target; a draft flag is
// not among them, so a change of it is refused here instead of being sent to
// be ignored.
func (pr proposals) Update(ctx context.Context, repo forge.Repository, number int, patch forge.ProposalPatch) (forge.Proposal, error) {
	if patch.Draft != nil {
		return forge.Proposal{}, fmt.Errorf("%w: the host's reference does not list the draft flag among what a pull request update may change; make the pull request ready on Azure DevOps", forge.ErrInvalid)
	}
	body := map[string]any{}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Body != nil {
		if len(*patch.Body) > 4000 {
			return forge.Proposal{}, fmt.Errorf("%w: a description is at most 4000 characters on this host", forge.ErrInvalid)
		}
		body["description"] = *patch.Body
	}
	if len(body) == 0 {
		return pr.Get(ctx, repo, number)
	}
	return pr.patch(ctx, repo, number, body)
}

func (pr proposals) patch(ctx context.Context, repo forge.Repository, number int, body map[string]any) (forge.Proposal, error) {
	rp, err := pr.p.repoPath(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	var a apiPull
	if _, err := pr.p.c.Call(ctx, http.MethodPatch, rp+"/pullrequests/"+strconv.Itoa(number)+qs(), body, &a); err != nil {
		return forge.Proposal{}, err
	}
	return a.proposal(repo), nil
}

// Close abandons the pull request: PATCH with status abandoned.
func (pr proposals) Close(ctx context.Context, repo forge.Repository, number int) error {
	_, err := pr.patch(ctx, repo, number, map[string]any{"status": "abandoned"})
	return err
}

// Reopen makes an abandoned pull request active again: PATCH with status active.
func (pr proposals) Reopen(ctx context.Context, repo forge.Repository, number int) error {
	_, err := pr.patch(ctx, repo, number, map[string]any{"status": "active"})
	return err
}

func (pr proposals) Revisions(ctx context.Context, repo forge.Repository, number int) ([]forge.Revision, error) {
	p, err := pr.Get(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return []forge.Revision{{Proposal: p.ID, Number: 1, Head: p.Head}}, nil
}
