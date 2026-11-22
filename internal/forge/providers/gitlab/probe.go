package gitlab

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

// Report and Check are the shapes of the connection test.
type (
	Report = forge.HostReport
	Check  = forge.PermissionCheck
)

// Roles, as the host numbers them (https://docs.gitlab.com/api/projects/ :
// min_access_level runs "from 10 (Guest) to 50 (Owner)"; protected branches:
// 30 Developer, 40 Maintainer).
const (
	guest      = 10
	developer  = 30
	maintainer = 40
)

// Probe asks the host who the credential is and which of the permissions
// Launchway's features need it has. It reads only: GET /user, GET
// /personal_access_tokens/self (scopes and expiry; a project or group token has
// no such record and the report says the scopes are unknown), GET
// /projects?membership=true (each project's permissions.project_access and
// group_access levels), and the protected branch read for one project.
// Webhook delivery cannot be probed; the report says so.
func (p *Provider) Probe(ctx context.Context) (Report, error) {
	var u struct {
		Username string `json:"username"`
	}
	if _, err := p.c.Get(ctx, "/user", &u); err != nil {
		return Report{}, err
	}
	r := Report{Account: u.Username, RateRemaining: -1}
	scopes := []string(nil)
	scopesKnown := false
	if t, ok := p.tokenSelf(ctx); ok {
		scopes, scopesKnown = t.Scopes, true
		if t.ExpiresAt != nil && *t.ExpiresAt != "" {
			// "Tokens expire at midnight UTC on the expiry date."
			if d, err := time.Parse("2006-01-02", *t.ExpiresAt); err == nil {
				d = d.Add(24 * time.Hour).UTC()
				r.TokenExpires = &d
			}
		}
	}
	has := func(s ...string) bool { // any of the scopes
		if !scopesKnown {
			return true
		}
		for _, w := range s {
			if slices.Contains(scopes, w) {
				return true
			}
		}
		return false
	}
	scopeNote := "the token's scopes are " + strings.Join(scopes, ", ")

	var in []apiProject
	if _, err := p.c.Get(ctx, "/projects?membership=true&per_page=50", &in); err != nil {
		r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Scope read_api (or api) and the Guest role or higher on a project", Detail: err.Error()})
		return r, nil
	}
	best, first := 0, (*apiProject)(nil)
	for i := range in {
		if !restkit.OwnerAllowed(p.conn, topOwner(in[i].PathWithNamespace)) {
			continue
		}
		r.Repositories++
		if first == nil {
			first = &in[i]
		}
		best = max(best, in[i].level())
	}
	check := func(feature, needs string, role int, scope bool, why string) {
		c := Check{Feature: feature, Needs: needs, OK: r.Repositories > 0 && best >= role && scope}
		switch {
		case r.Repositories == 0:
			c.Detail = "the token reaches no project of the allowed owners; make it a member of a project, or widen the owner list"
		case best < role:
			c.Detail = "the token's best role on the projects it reaches is " + roleName(best) + "; this needs " + roleName(role) + " or higher"
		case !scope:
			c.Detail = why + " (" + scopeNote + ")"
		}
		r.Checks = append(r.Checks, c)
	}
	read := has("api", "read_api")
	check("Choose a repository and run its pipelines", "Scopes read_api and read_repository (or api); the Guest role or higher", guest, read && has("api", "read_repository"),
		"the token lacks read_api (or api) or read_repository (or api)")
	write := has("api")
	check("Commit statuses on each run", "Scope api; the Developer role or higher", developer, write, "the token needs the api scope")
	check("Summary comment on merge requests", "Scope api; the Developer role or higher (the lowest role that may comment is Planner, the lowest that may post a status is Developer)", developer, write, "the token needs the api scope")
	check("Tags, releases and release files", "Scope api; the Maintainer role (the Releases API page says Developer, the permissions page says Maintainer for managing releases)", maintainer, write,
		"the token needs the api scope")
	check("File changes and deploy merge requests", "Scope api; the Developer role or higher", developer, write, "the token needs the api scope")

	prot := Check{Feature: "Refuse a direct write to a protected branch", Needs: "Scope read_api (or api); read access to the project's protected branches (the documentation names no role: Maintainer manages them)"}
	if first != nil && first.DefaultBranch != "" && read {
		a, err := p.c.Get(ctx, "/projects/"+restkit.Esc(first.PathWithNamespace)+"/protected_branches/"+restkit.Esc(first.DefaultBranch), nil, http.StatusNotFound, http.StatusForbidden)
		switch {
		case err == nil && a.Status == http.StatusNotFound, err == nil && a.Status < 300:
			prot.OK = true
		default:
			prot.Detail = "the token cannot read protected branches; Launchway cannot tell a protected branch and will let the host refuse the push"
		}
	} else {
		prot.Detail = "no project to check against, or the token cannot read the API"
	}
	r.Checks = append(r.Checks, prot)
	r.Checks = append(r.Checks, Check{Feature: "Webhooks from the host", Needs: "The Maintainer or Owner role on the project, to add a project webhook with the connection's webhook secret as its secret token", OK: true,
		Detail: "Launchway cannot test delivery from here; without webhooks it polls the project instead"})
	return r, nil
}

func roleName(n int) string {
	switch {
	case n >= 50:
		return "Owner"
	case n >= maintainer:
		return "Maintainer"
	case n >= developer:
		return "Developer"
	case n >= 20:
		return "Reporter"
	case n >= guest:
		return "Guest"
	}
	return "no role"
}
