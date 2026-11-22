package codecommit

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Report and Check are the shapes of the connection test.
type (
	Report = forge.HostReport
	Check  = forge.PermissionCheck
)

const notChecked = "Not checked: Launchway does not write to test, and the host does not report what an IAM policy allows without trying it. If the permission is missing the host answers 403 (AccessDeniedException) when the feature is first used, and the message says so"

// Probe says whether the credentials can read what Launchway reads, and what it
// cannot tell. It reads only: the repositories (ListRepositories,
// BatchGetRepositories), the branches and pull requests and approval rule
// templates of the first. Writes are listed with the IAM actions each needs.
func (p *Provider) Probe(ctx context.Context) (Report, error) {
	w, err := p.Identity().Whoami(ctx)
	if err != nil {
		return Report{}, err
	}
	r := Report{Account: w.Principal.Login, RateRemaining: -1}
	var first *forge.Repository
	for cursor, n := "", 0; n < 5; n++ {
		page, err := p.Repositories().List(ctx, cursor)
		if err != nil {
			r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "codecommit:ListRepositories and codecommit:BatchGetRepositories", Detail: err.Error()})
			return r, nil
		}
		for i := range page.Items {
			r.Repositories++
			if first == nil {
				first = &page.Items[i]
			}
		}
		if cursor = page.Next; cursor == "" {
			break
		}
	}
	read := Check{Feature: "Choose a repository and run its Sorties", Needs: "codecommit:ListRepositories, codecommit:BatchGetRepositories, codecommit:GetBranch, codecommit:ListBranches, codecommit:GetCommit and codecommit:GitPull on the repositories", OK: r.Repositories > 0}
	if !read.OK {
		read.Detail = "the credentials reach no repository of this region and the allowed accounts; check the region of the base address, the owners list and the IAM policy"
	}
	r.Checks = append(r.Checks, read)
	if first != nil {
		name, _ := p.repoName(*first)
		_, err := p.branches(ctx, name)
		c := Check{Feature: "Read branches", Needs: "codecommit:ListBranches and codecommit:GetBranch", OK: err == nil}
		if err != nil {
			c.Detail = err.Error()
		}
		r.Checks = append(r.Checks, c)
		page, err := p.Proposals().List(ctx, *first, forge.ProposalOpen, "")
		c = Check{Feature: "Follow pull requests (polling)", Needs: "codecommit:ListPullRequests and codecommit:GetPullRequest", OK: err == nil}
		if err != nil {
			c.Detail = err.Error()
		} else {
			c.Detail = "polling; CodeCommit sends no webhook"
			_ = page
		}
		r.Checks = append(r.Checks, c)
		_, err = p.Rules().List(ctx, *first)
		c = Check{Feature: "Read approval rules", Needs: "codecommit:ListAssociatedApprovalRuleTemplatesForRepository, codecommit:GetApprovalRuleTemplate and codecommit:GetPullRequestApprovalStates", OK: err == nil}
		if err != nil {
			c.Detail = err.Error()
		}
		r.Checks = append(r.Checks, c)
	}
	for _, wf := range []struct{ feature, needs string }{
		{"Summary comment on pull requests (results go here: CodeCommit has no commit status)", "codecommit:PostCommentForPullRequest and codecommit:UpdateComment"},
		{"Tags (made by Git push: no tag API)", "codecommit:GitPull and codecommit:GitPush"},
		{"File changes and deploy pull requests", "codecommit:GetBranch, codecommit:CreateBranch, codecommit:PutFile and codecommit:CreatePullRequest"},
		{"Close and edit pull requests", "codecommit:UpdatePullRequestStatus, codecommit:UpdatePullRequestTitle and codecommit:UpdatePullRequestDescription"},
	} {
		r.Checks = append(r.Checks, Check{Feature: wf.feature, Needs: wf.needs, OK: true, Detail: notChecked})
	}
	r.Checks = append(r.Checks, Check{Feature: "Webhooks from the host", Needs: "Nothing: CodeCommit sends none", OK: true,
		Detail: "Launchway follows this host by polling"})
	return r, nil
}
