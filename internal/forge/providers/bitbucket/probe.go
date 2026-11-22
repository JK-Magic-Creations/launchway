package bitbucket

import (
	"context"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

// Report and Check are the shapes of the connection test.
type (
	Report = forge.HostReport
	Check  = forge.PermissionCheck
)

// Probe asks the host who the credential is and which of the permissions
// Launchway's features need it has. It reads only: GET /user (scope account),
// the repositories of the allowed workspaces (GET /repositories/{workspace}?role=member),
// the account's permission on each (GET /user/workspaces/{workspace}/permissions/repositories:
// admin, write or read; scopes account and repository), and the branch
// restrictions of one repository (scope repository:admin). The host reports
// neither the token's scopes nor an expiry through the API, so a repository,
// project or workspace access token, which has no account, is judged by what
// its reads allow and the report says the writes cannot be checked without
// making one. Webhook delivery cannot be probed.
func (p *Provider) Probe(ctx context.Context) (Report, error) {
	w, err := p.Identity().Whoami(ctx)
	if err != nil {
		return Report{}, err
	}
	r := Report{Account: w.Principal.Login, RateRemaining: -1}
	var all []apiRepo
	ws, err := p.workspaces(ctx)
	if err != nil {
		r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Scopes account and repository (or a repository access token naming its owners)", Detail: err.Error()})
		return r, nil
	}
	for _, name := range ws {
		var in page[apiRepo]
		if _, err := p.c.Get(ctx, "/repositories/"+restkit.Esc(name)+"?role=member&pagelen=50", &in); err != nil {
			r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Scope repository", Detail: err.Error()})
			return r, nil
		}
		all = append(all, in.Values...)
	}
	r.Repositories = len(all)
	writable := false
	canTell := false
	for _, name := range ws {
		var perms page[struct {
			Permission string `json:"permission"`
		}]
		a, err := p.c.Get(ctx, "/user/workspaces/"+restkit.Esc(name)+"/permissions/repositories?pagelen=50", &perms, http.StatusForbidden, http.StatusNotFound, http.StatusUnauthorized)
		if err != nil || a.Status >= 400 {
			continue
		}
		canTell = true
		for _, pm := range perms.Values {
			writable = writable || pm.Permission == "write" || pm.Permission == "admin"
		}
	}
	r.Checks = append(r.Checks, Check{Feature: "Choose a repository and run its pipelines", Needs: "Scope repository (read)", OK: r.Repositories > 0,
		Detail: map[bool]string{true: "", false: "the token reaches no repository of the allowed workspaces; widen the owner list or grant it a repository"}[r.Repositories > 0]})
	for _, wf := range []struct{ feature, needs string }{
		{"Commit statuses on each run", "Scope repository (the host's build status call needs it)"},
		{"Summary comment on pull requests", "Scope pullrequest"},
		{"Tags and file changes", "Scope repository:write, and write access to the repository"},
		{"File changes and deploy pull requests", "Scopes repository:write and pullrequest:write, and write access to the repository"},
	} {
		c := Check{Feature: wf.feature, Needs: wf.needs, OK: canTell && writable}
		if !c.OK {
			c.Detail = "Bitbucket reports no token scopes through its API, and this token cannot list its permissions (that needs the account scope): check the token's scopes against this list. A refusal (403) shows when the feature is used"
			if canTell {
				c.Detail = "the account can read but not write any repository it reaches"
			}
		}
		r.Checks = append(r.Checks, c)
	}
	prot := Check{Feature: "Refuse a direct write to a protected branch", Needs: "Scope repository:admin (the host lists branch restrictions for repository administrators)"}
	if len(all) > 0 {
		a, err := p.c.Get(ctx, "/repositories/"+all[0].FullName+"/branch-restrictions?pagelen=1", nil, http.StatusForbidden, http.StatusNotFound)
		prot.OK = err == nil && a.Status < 300
	}
	if !prot.OK {
		prot.Detail = "the token cannot read branch restrictions; Launchway cannot tell a protected branch and will let the host refuse the push"
	}
	r.Checks = append(r.Checks, prot)
	r.Checks = append(r.Checks, Check{Feature: "Webhooks from the host", Needs: "Scope webhook, and administration of the repository, to add a webhook with the connection's webhook secret", OK: true,
		Detail: "Launchway cannot test delivery from here; without webhooks it polls the repository instead. The host reports no token expiry"})
	return r, nil
}
