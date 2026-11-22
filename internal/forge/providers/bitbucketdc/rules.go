package bitbucketdc

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type rules struct{ p *Provider }

type apiRestriction struct {
	Type    string `json:"type"`
	Matcher struct {
		ID   string `json:"id"`
		Type struct {
			ID string `json:"id"`
		} `json:"type"`
	} `json:"matcher"`
}

// covers says a restriction applies to the branch: its matcher is the branch
// (type BRANCH, id refs/heads/NAME), a pattern the branch fits (PATTERN) or
// every ref (ANY_REF). Matchers by branching model cannot be resolved here.
func (a apiRestriction) covers(branch string) bool {
	switch a.Matcher.Type.ID {
	case "ANY_REF":
		return true
	case "BRANCH":
		return strings.TrimPrefix(a.Matcher.ID, "refs/heads/") == branch
	case "PATTERN":
		return matches(strings.TrimPrefix(a.Matcher.ID, "refs/heads/"), branch)
	}
	return false
}

// List reads, for the default branch: GET /branch-permissions/latest/projects/{k}/repos/{s}/restrictions
// (REPO_ADMIN; types read-only, no-deletes, fast-forward-only, pull-request-only
// and no-creates). pull-request-only and read-only refuse a direct write, so a
// pull request is needed; fast-forward-only forbids force pushes. The merge
// checks come from GET .../settings/pull-requests (REPO_READ): requiredApprovers
// and requiredSuccessfulBuilds, each a count, 0 when the check is off.
func (r rules) List(ctx context.Context, repo forge.Repository) ([]forge.RefRule, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	branch := repo.DefaultBranch
	if branch == "" {
		return nil, fmt.Errorf("%w: the repository's default branch is not known", forge.ErrInvalid)
	}
	var in page[apiRestriction]
	if _, err := r.p.c.Get(ctx, "/branch-permissions/latest"+strings.TrimPrefix(rp, "/api/latest")+"/restrictions?limit=100", &in); err != nil {
		return nil, err
	}
	rule := forge.RefRule{Pattern: "refs/heads/" + branch}
	found := false
	for _, b := range in.Values {
		if !b.covers(branch) {
			continue
		}
		switch b.Type {
		case "pull-request-only", "read-only":
			rule.ProposalsOnly, found = true, true
		case "fast-forward-only":
			rule.Immutable, found = true, true
		}
	}
	var set struct {
		Approvers int `json:"requiredApprovers"`
		Builds    int `json:"requiredSuccessfulBuilds"`
	}
	if _, err := r.p.c.Get(ctx, rp+"/settings/pull-requests", &set, http.StatusForbidden, http.StatusNotFound); err != nil {
		return nil, err
	}
	if set.Approvers > 0 {
		rule.RequiredReviews, found = set.Approvers, true
	}
	if set.Builds > 0 {
		rule.RequiredResults, found = append(rule.RequiredResults, "(a successful build)"), true
	}
	if !found {
		return []forge.RefRule{}, nil
	}
	return []forge.RefRule{rule}, nil
}

func (r rules) Set(context.Context, forge.Repository, []forge.RefRule) error {
	return r.p.Caps.Err(forge.CapRules)
}
