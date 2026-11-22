package azuredevops

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Report and Check are the shapes of the connection test.
type (
	Report = forge.HostReport
	Check  = forge.PermissionCheck
)

// notChecked is said of every feature the test cannot prove with a read.
const notChecked = "Not checked: Azure DevOps reports neither the scopes nor the expiry of a personal access token through the REST calls Launchway uses, and Launchway does not write to test. If the token or its owner lacks this, the host refuses (401 or 403) when the feature is first used, and the message says so"

// Probe says whether the credential can read what Launchway reads, and what it
// cannot tell. It reads only: the projects of each allowed organization (scope
// vso.project), their repositories (vso.code), and the branch policies of the
// first repository's default branch (vso.code). The host reports no token
// scopes, no expiry and no permissions through these calls, so the write
// features are listed with what each needs and are not judged.
func (p *Provider) Probe(ctx context.Context) (Report, error) {
	w, err := p.Identity().Whoami(ctx)
	if err != nil {
		return Report{}, err
	}
	r := Report{Account: w.Principal.Login, RateRemaining: -1}
	orgs, err := p.owners()
	if err != nil {
		r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "The connection's owners list (the organizations it may use)", Detail: err.Error()})
		return r, nil
	}
	var first *forge.Repository
	for _, org := range orgs {
		prs, err := p.projects(ctx, org, 500)
		if err != nil {
			r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Scopes Project and team (read) and Code (read)", Detail: err.Error()})
			return r, nil
		}
		for _, pr := range prs {
			var in page[apiRepo]
			if _, err := p.c.Get(ctx, p.projectPath(org, pr.Name)+"/_apis/git/repositories"+qs(), &in); err != nil {
				r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Scope Code (read), and the Read permission on the project's repositories", Detail: err.Error()})
				return r, nil
			}
			for _, a := range in.Value {
				if a.Project.Name == "" {
					a.Project.Name = pr.Name
				}
				rep := p.repository(org, a)
				r.Repositories++
				if first == nil {
					first = &rep
				}
			}
		}
	}
	read := Check{Feature: "Choose a repository and run its Sorties", Needs: "Scopes Project and team (read) and Code (read); the Readers role on the project", OK: r.Repositories > 0}
	if !read.OK {
		read.Detail = "the token reaches no repository of the allowed organizations; widen the owners list, grant the token's owner the Read permission, or check its organization is selected on the token"
	}
	r.Checks = append(r.Checks, read)
	for _, wf := range []struct{ feature, needs string }{
		{"Commit and pull request statuses on each run", "Scope Code (status), or Code (read and write); the Contributors role"},
		{"Summary comment on pull requests", "Scope Code (read and write), or Pull request threads (read and write); the Contributors role"},
		{"Tags", "Scope Code (full): the annotated tag call needs vso.code_manage; the Contributors role (Create tag)"},
		{"File changes and deploy pull requests", "Scope Code (read and write); the Contributors role (Contribute, Create branch)"},
	} {
		r.Checks = append(r.Checks, Check{Feature: wf.feature, Needs: wf.needs, OK: true, Detail: notChecked})
	}
	prot := Check{Feature: "Refuse a direct write to a protected branch", Needs: "Scope Code (read), and the Read permission"}
	if first != nil && first.DefaultBranch != "" {
		_, err := p.Rules().List(ctx, *first)
		prot.OK = err == nil
		if err != nil {
			prot.Detail = "the token cannot read branch policies: " + err.Error()
		}
	} else {
		prot.Detail = "no repository with a default branch to read policies of"
	}
	if !prot.OK && prot.Detail == "" {
		prot.Detail = "the token cannot read branch policies; Launchway cannot tell a protected branch and will let the host refuse the push"
	}
	r.Checks = append(r.Checks, prot)
	r.Checks = append(r.Checks, Check{Feature: "Webhooks from the host", Needs: "Project Administrators or Project Collection Administrators, to add a service hook subscription whose basic-authentication password (or HTTP header) is the connection's webhook secret", OK: true,
		Detail: "Launchway cannot test delivery from here; without webhooks it polls the repository instead"})
	return r, nil
}
