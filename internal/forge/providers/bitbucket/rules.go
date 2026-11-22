package bitbucket

import (
	"context"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type rules struct{ p *Provider }

// List: GET /repositories/{w}/{r}/branch-restrictions (scope repository:admin),
// keeping the restrictions whose pattern matches the default branch. A "push"
// restriction means a direct push is limited to the people and groups named,
// so a direct write is refused and a pull request is needed; "force" limits
// force pushes; "require_passing_builds_to_merge" and
// "require_approvals_to_merge" (value: how many) are the merge checks.
func (r rules) List(ctx context.Context, repo forge.Repository) ([]forge.RefRule, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	branch := repo.DefaultBranch
	if branch == "" {
		return nil, fmt.Errorf("%w: the repository's default branch is not known", forge.ErrInvalid)
	}
	var in page[struct {
		Kind    string `json:"kind"`
		Pattern string `json:"pattern"`
		Match   string `json:"branch_match_kind"`
		Value   *int   `json:"value"`
	}]
	if _, err := r.p.c.Get(ctx, rp+"/branch-restrictions?pagelen=100", &in); err != nil {
		return nil, err
	}
	rule := forge.RefRule{Pattern: "refs/heads/" + branch}
	found := false
	for _, b := range in.Values {
		if b.Match != "glob" && b.Match != "" || !matches(b.Pattern, branch) {
			continue
		}
		found = true
		switch b.Kind {
		case "push":
			rule.ProposalsOnly = true
		case "force":
			rule.Immutable = true
		case "require_passing_builds_to_merge":
			rule.RequiredResults = append(rule.RequiredResults, "(a passing build)")
		case "require_approvals_to_merge":
			rule.RequiredReviews = 1
			if b.Value != nil {
				rule.RequiredReviews = *b.Value
			}
		}
	}
	if !found {
		return []forge.RefRule{}, nil
	}
	return []forge.RefRule{rule}, nil
}

func (r rules) Set(context.Context, forge.Repository, []forge.RefRule) error {
	return r.p.Caps.Err(forge.CapRules)
}
