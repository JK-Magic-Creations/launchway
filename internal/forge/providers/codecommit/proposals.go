package codecommit

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type proposals struct{ p *Provider }

// apiPull is a PullRequest
// (https://docs.aws.amazon.com/codecommit/latest/APIReference/API_PullRequest.html);
// a pull request has one target, with the source and destination of the one repository
// (API_PullRequestTarget.html).
type apiPull struct {
	ID           string  `json:"pullRequestId"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	Status       string  `json:"pullRequestStatus"` // OPEN or CLOSED
	AuthorARN    string  `json:"authorArn"`
	Revision     string  `json:"revisionId"`
	LastActivity float64 `json:"lastActivityDate"`
	Targets      []struct {
		DestCommit string `json:"destinationCommit"`
		DestRef    string `json:"destinationReference"`
		MergeBase  string `json:"mergeBase"`
		Merge      struct {
			IsMerged bool   `json:"isMerged"`
			Commit   string `json:"mergeCommitId"`
		} `json:"mergeMetadata"`
		Repository string `json:"repositoryName"`
		SrcCommit  string `json:"sourceCommit"`
		SrcRef     string `json:"sourceReference"`
	} `json:"pullRequestTargets"`
	ApprovalRules []struct {
		Name string `json:"approvalRuleName"`
	} `json:"approvalRules"`
}

func (a apiPull) number() int {
	n, _ := strconv.Atoi(a.ID)
	return n
}

func (a apiPull) proposal(repo forge.Repository) forge.Proposal {
	st := forge.ProposalOpen
	var t = struct{ src, dst, srcRef, dstRef, base, merge string }{}
	merged := false
	if len(a.Targets) > 0 {
		x := a.Targets[0]
		t.src, t.dst, t.srcRef, t.dstRef, t.base, t.merge = x.SrcCommit, x.DestCommit, x.SrcRef, x.DestRef, x.MergeBase, x.Merge.Commit
		merged = x.Merge.IsMerged
	}
	switch {
	case a.Status == "CLOSED" && merged:
		st = forge.ProposalLanded
	case a.Status == "CLOSED":
		st = forge.ProposalClosed
	}
	return forge.Proposal{Repository: repo.ID, Number: a.number(), Title: a.Title, Body: a.Description,
		Author: forge.Principal{Login: login(a.AuthorARN), Unlinked: true},
		Source: forge.Ref{Name: t.srcRef, Commit: t.src}, Target: forge.Ref{Name: t.dstRef, Commit: t.dst}, State: st,
		Head: t.src, Base: t.base, Authority: forge.AuthorityProvider,
		Ext: map[string]any{"fork": false, "revision_id": a.Revision, "merge_commit": t.merge, "approval_rules": len(a.ApprovalRules), "author_arn": a.AuthorARN}}
}

// pageSize is the number of pull requests asked for in one listing.
const pageSize = 50

// List: ListPullRequests with pullRequestStatus OPEN or CLOSED, maxResults and
// nextToken (permission codecommit:ListPullRequests;
// https://docs.aws.amazon.com/codecommit/latest/APIReference/API_ListPullRequests.html),
// which returns only ids, so each is read with GetPullRequest. A merged pull
// request is a closed one with mergeMetadata.isMerged. There are no drafts.
func (pr proposals) List(ctx context.Context, repo forge.Repository, state forge.ProposalState, cursor string) (forge.Page[forge.Proposal], error) {
	name, err := pr.p.repoName(repo)
	if err != nil {
		return forge.Page[forge.Proposal]{}, err
	}
	out := forge.Page[forge.Proposal]{Items: []forge.Proposal{}}
	if state == forge.ProposalDraft {
		return out, nil
	}
	status := "OPEN"
	if state == forge.ProposalClosed || state == forge.ProposalLanded {
		status = "CLOSED"
	}
	in := map[string]any{"repositoryName": name, "pullRequestStatus": status, "maxResults": pageSize}
	if cursor != "" {
		in["nextToken"] = cursor
	}
	var ids struct {
		IDs  []string `json:"pullRequestIds"`
		Next string   `json:"nextToken"`
	}
	if err := pr.p.invoke(ctx, "ListPullRequests", in, &ids); err != nil {
		return forge.Page[forge.Proposal]{Items: []forge.Proposal{}}, err
	}
	for _, id := range ids.IDs {
		a, err := pr.get(ctx, id)
		if err != nil {
			return forge.Page[forge.Proposal]{Items: []forge.Proposal{}}, err
		}
		p := a.proposal(repo)
		if status == "CLOSED" && p.State != state {
			continue
		}
		out.Items = append(out.Items, p)
	}
	out.Next = ids.Next
	return out, nil
}

func (pr proposals) get(ctx context.Context, id string) (apiPull, error) {
	var out struct {
		PullRequest apiPull `json:"pullRequest"`
	}
	if err := pr.p.invoke(ctx, "GetPullRequest", map[string]any{"pullRequestId": id}, &out); err != nil {
		return apiPull{}, err
	}
	return out.PullRequest, nil
}

// owns says a pull request is of this repository: the id is account-wide in a
// region, and a pull request of another repository must not be shown as this one's.
func (pr proposals) owns(a apiPull, name string) bool {
	return len(a.Targets) > 0 && a.Targets[0].Repository == name
}

// Get: GetPullRequest (permission codecommit:GetPullRequest).
func (pr proposals) Get(ctx context.Context, repo forge.Repository, number int) (forge.Proposal, error) {
	name, err := pr.p.repoName(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	a, err := pr.get(ctx, fmt.Sprint(number))
	if err != nil {
		return forge.Proposal{}, err
	}
	if !pr.owns(a, name) {
		return forge.Proposal{}, fmt.Errorf("%w: pull request %d is not in %s", forge.ErrNotFound, number, repo.Path)
	}
	return a.proposal(repo), nil
}

// Create: CreatePullRequest with title (at most 150 characters), description
// (at most 10,240), a clientRequestToken and one target with the repository's
// name and full reference names (permission codecommit:CreatePullRequest;
// https://docs.aws.amazon.com/codecommit/latest/APIReference/API_CreatePullRequest.html).
func (pr proposals) Create(ctx context.Context, repo forge.Repository, n forge.NewProposal) (forge.Proposal, error) {
	name, err := pr.p.repoName(repo)
	if err != nil {
		return forge.Proposal{}, err
	}
	if n.Draft {
		return forge.Proposal{}, pr.p.Caps.Err(forge.CapDrafts)
	}
	if len([]rune(n.Title)) == 0 || len([]rune(n.Title)) > 150 || len(n.Body) > 10240 {
		return forge.Proposal{}, fmt.Errorf("%w: a pull request title is 1 to 150 characters and its description at most 10,240", forge.ErrInvalid)
	}
	body := map[string]any{"clientRequestToken": token(), "title": n.Title, "description": n.Body,
		"targets": []map[string]any{{"repositoryName": name, "sourceReference": headRef(n.Source.Name), "destinationReference": headRef(n.Target.Name)}}}
	var out struct {
		PullRequest apiPull `json:"pullRequest"`
	}
	if err := pr.p.invoke(ctx, "CreatePullRequest", body, &out); err != nil {
		return forge.Proposal{}, err
	}
	return out.PullRequest.proposal(repo), nil
}

func headRef(name string) string {
	if strings.HasPrefix(name, "refs/") {
		return name
	}
	return "refs/heads/" + name
}

// Update: UpdatePullRequestTitle (title at most 150 characters) and
// UpdatePullRequestDescription (at most 10,240), each with pullRequestId, and
// each permission codecommit:UpdatePullRequest... of its name. There is no
// draft flag.
func (pr proposals) Update(ctx context.Context, repo forge.Repository, number int, patch forge.ProposalPatch) (forge.Proposal, error) {
	if patch.Draft != nil {
		return forge.Proposal{}, pr.p.Caps.Err(forge.CapDrafts)
	}
	if _, err := pr.Get(ctx, repo, number); err != nil {
		return forge.Proposal{}, err
	}
	id := fmt.Sprint(number)
	if patch.Title != nil {
		if l := len([]rune(*patch.Title)); l == 0 || l > 150 {
			return forge.Proposal{}, fmt.Errorf("%w: a pull request title is 1 to 150 characters", forge.ErrInvalid)
		}
		if err := pr.p.invoke(ctx, "UpdatePullRequestTitle", map[string]any{"pullRequestId": id, "title": *patch.Title}, nil); err != nil {
			return forge.Proposal{}, err
		}
	}
	if patch.Body != nil {
		if len(*patch.Body) > 10240 {
			return forge.Proposal{}, fmt.Errorf("%w: a description is at most 10,240 characters on this host", forge.ErrInvalid)
		}
		if err := pr.p.invoke(ctx, "UpdatePullRequestDescription", map[string]any{"pullRequestId": id, "description": *patch.Body}, nil); err != nil {
			return forge.Proposal{}, err
		}
	}
	return pr.Get(ctx, repo, number)
}

// Close: UpdatePullRequestStatus to CLOSED (permission codecommit:UpdatePullRequestStatus).
func (pr proposals) Close(ctx context.Context, repo forge.Repository, number int) error {
	if _, err := pr.Get(ctx, repo, number); err != nil {
		return err
	}
	return pr.p.invoke(ctx, "UpdatePullRequestStatus", map[string]any{"pullRequestId": fmt.Sprint(number), "pullRequestStatus": "CLOSED"}, nil)
}

// Reopen is refused: the reference allows only OPEN to OPEN, OPEN to CLOSED and
// CLOSED to CLOSED (API_UpdatePullRequestStatus.html).
func (pr proposals) Reopen(context.Context, forge.Repository, int) error {
	return fmt.Errorf("%w: CodeCommit cannot reopen a closed pull request (a status can only go from OPEN to CLOSED); open a new pull request from the same branch", forge.ErrInvalid)
}

func (pr proposals) Revisions(ctx context.Context, repo forge.Repository, number int) ([]forge.Revision, error) {
	p, err := pr.Get(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return []forge.Revision{{Proposal: p.ID, Number: 1, Head: p.Head, Base: p.Base}}, nil
}
