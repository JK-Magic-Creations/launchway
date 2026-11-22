package bitbucketdc

import (
	"context"
	"net/url"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Report and Check are the shapes of the connection test.
type (
	Report = forge.HostReport
	Check  = forge.PermissionCheck
)

// level counts the repositories the token holds a permission on: GET
// /api/latest/repos?permission=REPO_READ|REPO_WRITE|REPO_ADMIN (the search
// resource limits the list to repositories the requesting user holds that
// permission on; "projectkey" is available since 8.0), for each allowed
// project key. ok is false when the host refused or did not understand the call.
func (p *Provider) level(ctx context.Context, permission string) (n int, all []apiRepo, ok bool) {
	keys := p.conn.Owners
	if len(keys) == 0 {
		keys = []string{""}
	}
	for _, k := range keys {
		q := url.Values{"permission": {permission}, "limit": {"50"}}
		if k != "" {
			q.Set("projectkey", k)
		}
		var in page[apiRepo]
		if _, err := p.c.Get(ctx, "/api/latest/repos?"+q.Encode(), &in); err != nil {
			return 0, nil, false
		}
		n += len(in.Values)
		all = append(all, in.Values...)
	}
	return n, all, true
}

// Probe asks the host which of the permissions Launchway's features need the
// token has. It reads only. The reference has no call that names a token, its
// kind, scopes or expiry, so the test reports what the token can do: the
// repositories it holds repository read, write and admin permission on (the
// search resource's permission filter), whether the repository allows
// auto-merge (GET .../settings/auto-merge) and whether branch permissions can
// be read (GET /branch-permissions/latest/.../restrictions needs repository
// admin). It cannot tell a project token from a repository or personal one, how
// long it lasts, or whether the webhook is set up.
func (p *Provider) Probe(ctx context.Context) (Report, error) {
	w, err := p.Identity().Whoami(ctx)
	if err != nil {
		return Report{}, err
	}
	r := Report{Account: w.Principal.Login, RateRemaining: -1}
	read, _, ok := p.level(ctx, "REPO_READ")
	if !ok {
		r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Project read and repository read (an HTTP access token), on a Bitbucket of version 8.0 or later", Detail: "the host would not list repositories for the allowed projects"})
		return r, nil
	}
	r.Repositories = read
	write, wrepos, wok := p.level(ctx, "REPO_WRITE")
	admin, _, aok := p.level(ctx, "REPO_ADMIN")
	r.Checks = append(r.Checks, Check{Feature: "Choose a repository and run its Sorties", Needs: "Project read and repository read", OK: read > 0,
		Detail: map[bool]string{true: "", false: "the token reaches no repository of the allowed projects; widen the owner list or grant it a repository"}[read > 0]})
	for _, f := range []struct{ feature, needs string }{
		{"Commit statuses on each run", "Repository read (the host's build status call needs only that)"},
		{"Summary comment on pull requests", "Repository read"},
	} {
		r.Checks = append(r.Checks, Check{Feature: f.feature, Needs: f.needs, OK: read > 0, Detail: map[bool]string{true: "", false: "the token reaches no repository"}[read > 0]})
	}
	for _, f := range []struct{ feature, needs string }{
		{"Tags and file changes", "Repository write"},
		{"File changes and deploy pull requests", "Repository write (a pull request is opened and updated by its author, who is the token's user)"},
	} {
		c := Check{Feature: f.feature, Needs: f.needs, OK: wok && write > 0}
		if !c.OK {
			c.Detail = "the token can read but does not hold write permission on any repository it reaches"
		}
		r.Checks = append(r.Checks, c)
	}
	am := Check{Feature: "Auto-merge of a deploy pull request (write_back.merge: auto)", Needs: "Repository write, and auto-merge enabled in the repository's or project's settings"}
	switch {
	case !wok || write == 0:
		am.Detail = "the token holds no write permission, which merging needs"
	default:
		var set struct {
			Enabled bool `json:"enabled"`
		}
		if rp, err := p.repoPathOf(wrepos[0].Project.Key + "/" + wrepos[0].Slug); err == nil {
			if _, err := p.c.Get(ctx, rp+"/settings/auto-merge", &set); err == nil {
				am.OK = set.Enabled
			}
		}
		if !am.OK {
			am.Detail = "auto-merge is not enabled for " + wrepos[0].Project.Key + "/" + wrepos[0].Slug + " (checked on this one repository): turn it on in the settings on Bitbucket, or use write_back.merge: person"
		}
	}
	r.Checks = append(r.Checks, am)
	prot := Check{Feature: "Refuse a direct write to a protected branch", Needs: "Repository admin (the host lists branch permissions only for repository administrators)", OK: aok && admin > 0}
	if !prot.OK {
		prot.Detail = "the token cannot read branch permissions; Launchway cannot tell a protected branch and will let the host refuse the push (the merge checks are still read)"
	}
	r.Checks = append(r.Checks, prot)
	r.Checks = append(r.Checks, Check{Feature: "Webhooks from the host", Needs: "Repository admin (or project admin for a project-level webhook), for whoever adds the webhook with the connection's webhook secret", NotChecked: true,
		Detail: "Launchway cannot test delivery from here; without webhooks it polls the repository instead. The host's API names no token kind, scopes or expiry, so these cannot be checked: a refusal (403) shows when a feature is used"})
	return r, nil
}
