package azuredevops

import (
	"context"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type rules struct{ p *Provider }

// The policy types the sample responses of the policy configuration pages name
// by id and display name
// (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/policy-configurations/get).
const (
	policyRequiredReviewers = "fd2167ab-b0be-447a-8ec8-39368250530e" // "Required reviewers"
	policyMinimumApprovers  = "fa4e907d-c16b-4a4c-9dfa-4906e5d171dd" // "Minimum approval count"
	policyBuild             = "0609b952-1397-4640-95ec-e00a01b2c241" // "Build"
)

type apiPolicy struct {
	IsEnabled  bool `json:"isEnabled"`
	IsBlocking bool `json:"isBlocking"`
	IsDeleted  bool `json:"isDeleted"`
	Type       struct {
		ID string `json:"id"`
	} `json:"type"`
	Settings struct {
		MinimumApproverCount int `json:"minimumApproverCount"`
	} `json:"settings"`
}

// List: GET {project}/_apis/git/policy/configurations?repositoryId={id}&refName=refs/heads/{default}
// (scope vso.code): "returns all policy configurations that apply to a particular
// branch in a repository". A branch with any enabled required (blocking) policy
// is changed only by pull requests unless the pusher may bypass policies ("You
// can't push changes directly to branches with required branch policies unless
// you have permissions to bypass branch policies", the branch policies page),
// which the API does not tell for the token's owner. The rule carries the
// minimum approver count (policy "Minimum approval count"; a required reviewers
// policy counts one) and one required result for a build policy.
func (r rules) List(ctx context.Context, repo forge.Repository) ([]forge.RefRule, error) {
	o, project, _, err := r.p.split(repo.Path)
	if err != nil {
		return nil, err
	}
	if repo.DefaultBranch == "" {
		return nil, fmt.Errorf("%w: the repository's default branch is not known", forge.ErrInvalid)
	}
	id, err := r.p.repoID(ctx, repo)
	if err != nil {
		return nil, err
	}
	ref := "refs/heads/" + repo.DefaultBranch
	var in page[apiPolicy]
	if _, err := r.p.c.Get(ctx, r.p.projectPath(o, project)+"/_apis/git/policy/configurations"+qs("repositoryId", id, "refName", ref), &in); err != nil {
		return nil, err
	}
	rule := forge.RefRule{Pattern: ref}
	found := false
	for _, pol := range in.Value {
		if !pol.IsEnabled || pol.IsDeleted || !pol.IsBlocking {
			continue
		}
		found = true
		switch pol.Type.ID {
		case policyMinimumApprovers:
			rule.RequiredReviews = max(rule.RequiredReviews, pol.Settings.MinimumApproverCount)
		case policyRequiredReviewers:
			rule.RequiredReviews = max(rule.RequiredReviews, 1)
		case policyBuild:
			rule.RequiredResults = append(rule.RequiredResults, "(a passing build)")
		}
	}
	if !found {
		return []forge.RefRule{}, nil
	}
	rule.ProposalsOnly = true
	return []forge.RefRule{rule}, nil
}

// Set is refused: branch policies are changed on the host (see Capabilities).
func (r rules) Set(context.Context, forge.Repository, []forge.RefRule) error {
	d := r.p.Caps[forge.CapRules]
	return &forge.ErrUnsupported{Capability: forge.CapRules, Reason: d.Reason, Workaround: d.Workaround}
}
