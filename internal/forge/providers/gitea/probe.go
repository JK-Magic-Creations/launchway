package gitea

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
// Launchway's features need it has. It reads only: GET /user, GET
// /repos/search for the account (each repository's permissions object: admin,
// push, pull), and the branch protection list for one repository. The host has
// no call that tells a token's scopes or expiry, so the report says so and
// judges write features by the account's push permission; a token whose scope
// is too narrow shows up when a call answers 403. Webhook delivery cannot be probed.
func (p *Provider) Probe(ctx context.Context) (Report, error) {
	var u apiUser
	if _, err := p.c.Get(ctx, "/user", &u); err != nil {
		return Report{}, err
	}
	r := Report{Account: u.Login, RateRemaining: -1}
	in, _, err := p.search(ctx, 1)
	if err != nil {
		r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Token scope read:repository", Detail: err.Error()})
		return r, nil
	}
	var first, firstPush *apiRepo
	for i := range in {
		if !restkit.OwnerAllowed(p.conn, in[i].Owner.Login) {
			continue
		}
		r.Repositories++
		if first == nil {
			first = &in[i]
		}
		if firstPush == nil && in[i].Permissions.Push {
			firstPush = &in[i]
		}
	}
	r.Checks = append(r.Checks, Check{Feature: "Choose a repository and run its pipelines", Needs: "Token scope read:repository; read access to the repositories", OK: r.Repositories > 0,
		Detail: map[bool]string{true: "", false: "the account owns or contributes to no repository of the allowed owners; add it to one, or widen the owner list"}[r.Repositories > 0]})
	for _, w := range []struct{ feature, needs string }{
		{"Commit statuses on each run", "Token scope write:repository; write access to the repository"},
		{"Summary comment on pull requests", "Token scopes write:repository and write:issue; write access to the repository"},
		{"Tags, releases and release files", "Token scope write:repository; write access to the repository"},
		{"File changes and deploy pull requests", "Token scope write:repository; write access to the repository"},
	} {
		c := Check{Feature: w.feature, Needs: w.needs, OK: firstPush != nil}
		if !c.OK {
			c.Detail = "the account can read but not push to any repository it reaches, so it lacks write access; the host does not list a token's scopes, so check the token's settings against this list"
		}
		r.Checks = append(r.Checks, c)
	}
	prot := Check{Feature: "Refuse a direct write to a protected branch", Needs: "Token scope read:repository; the account must be able to read the repository's branch protection (an administrator of it)"}
	if first != nil {
		a, err := p.c.Get(ctx, "/repos/"+restkit.Esc(first.Owner.Login)+"/"+restkit.Esc(first.Name)+"/branch_protections", nil, http.StatusForbidden, http.StatusNotFound)
		prot.OK = err == nil && a.Status < 300
		if !prot.OK {
			prot.Detail = "the token cannot read branch protection; Launchway cannot tell a protected branch and will let the host refuse the push"
		}
	} else {
		prot.Detail = "no repository to check against"
	}
	r.Checks = append(r.Checks, prot)
	r.Checks = append(r.Checks, Check{Feature: "Webhooks from the host", Needs: "Administrator of the repository, to add a webhook with the connection's webhook secret", OK: true,
		Detail: "Launchway cannot test delivery from here; without webhooks it polls the repository instead. The host reports no token expiry"})
	return r, nil
}
