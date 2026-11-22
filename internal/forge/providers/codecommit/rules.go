package codecommit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type rules struct{ p *Provider }

// templateRule is the content of an approval rule template: a JSON text with
// the destination references it applies to and the approvers it needs
// (https://docs.aws.amazon.com/codecommit/latest/APIReference/API_GetApprovalRuleTemplate.html,
// whose sample shows {"Version","DestinationReferences","Statements":[{"Type":"Approvers",
// "NumberOfApprovalsNeeded","ApprovalPoolMembers"}]}).
type templateRule struct {
	DestinationReferences []string `json:"DestinationReferences"`
	Statements            []struct {
		Type      string `json:"Type"`
		Approvals int    `json:"NumberOfApprovalsNeeded"`
	} `json:"Statements"`
}

// List reads the approval rule templates associated with the repository:
// ListAssociatedApprovalRuleTemplatesForRepository (permission
// codecommit:ListAssociatedApprovalRuleTemplatesForRepository, resource *) and
// GetApprovalRuleTemplate of each (codecommit:GetApprovalRuleTemplate). A rule
// is one per destination reference of a template (or every branch when it names
// none) with the approvals it needs. It is not "proposals only": a template gates
// the merge of a pull request, and nothing in it stops a push (that is IAM policy).
func (r rules) List(ctx context.Context, repo forge.Repository) ([]forge.RefRule, error) {
	name, err := r.p.repoName(repo)
	if err != nil {
		return nil, err
	}
	out := []forge.RefRule{}
	next := ""
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return nil, errTooManyPages
		}
		in := map[string]any{"repositoryName": name}
		if next != "" {
			in["nextToken"] = next
		}
		var names struct {
			Names []string `json:"approvalRuleTemplateNames"`
			Next  string   `json:"nextToken"`
		}
		if err := r.p.invoke(ctx, "ListAssociatedApprovalRuleTemplatesForRepository", in, &names); err != nil {
			return nil, err
		}
		for _, n := range names.Names {
			var t struct {
				Template struct {
					Content string `json:"approvalRuleTemplateContent"`
				} `json:"approvalRuleTemplate"`
			}
			if err := r.p.invoke(ctx, "GetApprovalRuleTemplate", map[string]any{"approvalRuleTemplateName": n}, &t); err != nil {
				return nil, err
			}
			var c templateRule
			if err := json.Unmarshal([]byte(t.Template.Content), &c); err != nil {
				return nil, fmt.Errorf("the approval rule template %q has content Launchway cannot read", n)
			}
			need := 0
			for _, s := range c.Statements {
				if s.Type == "Approvers" {
					need = max(need, s.Approvals)
				}
			}
			refs := c.DestinationReferences
			if len(refs) == 0 {
				refs = []string{"refs/heads/*"}
			}
			for _, ref := range refs {
				out = append(out, forge.RefRule{Pattern: ref, RequiredReviews: need})
			}
		}
		if next = names.Next; next == "" {
			return out, nil
		}
	}
}

// Set is refused: approval rule templates are changed on the host.
func (r rules) Set(context.Context, forge.Repository, []forge.RefRule) error {
	d := r.p.Caps[forge.CapRules]
	return &forge.ErrUnsupported{Capability: forge.CapRules, Reason: d.Reason, Workaround: d.Workaround}
}

type reviews struct{ p *Provider }

// Reviews reads the approval states of the pull request's current revision:
// GetPullRequestApprovalStates with the pull request's revisionId (permission
// codecommit:GetPullRequestApprovalStates;
// https://docs.aws.amazon.com/codecommit/latest/APIReference/API_GetPullRequestApprovalStates.html),
// each {userArn, approvalState} an approval ("APPROVE") or a revoked one
// ("REVOKE"). Launchway never approves for anyone: the other calls are refused.
func (rv reviews) Reviews(ctx context.Context, repo forge.Repository, number int) ([]forge.Review, error) {
	name, err := rv.p.repoName(repo)
	if err != nil {
		return nil, err
	}
	pr := proposals(rv)
	a, err := pr.get(ctx, fmt.Sprint(number))
	if err != nil {
		return nil, err
	}
	if !pr.owns(a, name) {
		return nil, fmt.Errorf("%w: pull request %d is not in %s", forge.ErrNotFound, number, repo.Path)
	}
	var out struct {
		Approvals []struct {
			ARN   string `json:"userArn"`
			State string `json:"approvalState"`
		} `json:"approvals"`
	}
	if err := rv.p.invoke(ctx, "GetPullRequestApprovalStates", map[string]any{"pullRequestId": a.ID, "revisionId": a.Revision}, &out); err != nil {
		return nil, err
	}
	rs := []forge.Review{}
	for _, x := range out.Approvals {
		v := forge.Comment
		if x.State == "APPROVE" {
			v = forge.Approve
		}
		rs = append(rs, forge.Review{ID: a.ID + ":" + x.ARN, Proposal: a.ID, Revision: 1, Author: forge.Principal{Login: login(x.ARN), Unlinked: true}, Verdict: v,
			Body: "approval state " + x.State})
	}
	return rs, nil
}
