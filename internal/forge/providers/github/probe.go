package github

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Report and Check are the shapes of the connection test.
type (
	Report = forge.HostReport
	Check  = forge.PermissionCheck
)

// Probe asks the host who the credential is and which of the permissions
// Launchway's features need it has. It reads only: GET /user (account and the
// x-ratelimit-remaining header), the token expiry header
// github-authentication-token-expiration
// (https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation),
// GET /user/repos (each repository's permissions object:
// https://docs.github.com/en/rest/repos/repos#get-a-repository), and the
// branch protection read for one repository. Webhook delivery cannot be
// probed; the report says so.
func (p *Provider) Probe(ctx context.Context) (Report, error) {
	var u struct {
		Login string `json:"login"`
	}
	a, err := p.c.Header(ctx, "/user", &u)
	if err != nil {
		return Report{}, err
	}
	r := Report{Account: u.Login, RateRemaining: -1}
	if v := a.Header.Get("X-RateLimit-Remaining"); v != "" {
		var n int
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				n = -1
				break
			}
			n = n*10 + int(ch-'0')
		}
		r.RateRemaining = n
	}
	if v := a.Header.Get("GitHub-Authentication-Token-Expiration"); v != "" {
		if t, err := time.Parse("2006-01-02 15:04:05 MST", v); err == nil {
			t = t.UTC()
			r.TokenExpires = &t
		}
	}
	var in []apiRepo
	if _, err := p.c.CallPage(ctx, "/user/repos?per_page=50", &in); err != nil {
		r.Checks = append(r.Checks, Check{Feature: "Choose a repository", Needs: "Metadata: read (every token has it) and access to at least one repository", Detail: err.Error()})
		return r, nil
	}
	var firstPush, first *apiRepo
	for i := range in {
		if !p.ownerAllowed(in[i].Owner.Login) {
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
	r.Checks = append(r.Checks, Check{Feature: "Choose a repository and run its pipelines", Needs: "Contents: read, Metadata: read", OK: r.Repositories > 0,
		Detail: map[bool]string{true: "", false: "the token reaches no repository of the allowed owners; grant it a repository, or widen the owner list"}[r.Repositories > 0]})
	writeNeeds := map[string]string{
		"Commit statuses on each run":           "Commit statuses: read and write",
		"Summary comment on pull requests":      "Pull requests: read and write",
		"Tags, releases and release files":      "Contents: read and write",
		"File changes and deploy pull requests": "Contents: read and write, Pull requests: read and write",
	}
	for _, f := range []string{"Commit statuses on each run", "Summary comment on pull requests", "Tags, releases and release files", "File changes and deploy pull requests"} {
		c := Check{Feature: f, Needs: writeNeeds[f], OK: firstPush != nil}
		if !c.OK {
			c.Detail = "the token can read but not push to any repository it reaches, so it lacks write permission; the host does not list which write permission, so check the token's settings against this list"
		}
		r.Checks = append(r.Checks, c)
	}
	prot := Check{Feature: "Refuse a direct write to a protected branch", Needs: "Administration: read (to read branch protection)"}
	if first != nil && first.DefaultBranch != "" {
		rp := "/repos/" + esc(first.Owner.Login) + "/" + esc(first.Name)
		a, err := p.c.Header(ctx, rp+"/branches/"+escPath(first.DefaultBranch)+"/protection", nil)
		switch {
		case err == nil, a.Status == http.StatusNotFound && strings.Contains(a.Message(), "not protected"):
			prot.OK = true
		default:
			prot.Detail = "the token cannot read branch protection; Launchway cannot tell a protected branch and will let the host refuse the push"
		}
	} else {
		prot.Detail = "no repository to check against"
	}
	r.Checks = append(r.Checks, prot)
	r.Checks = append(r.Checks, Check{Feature: "Webhooks from the host", Needs: "Repository webhooks: read and write, set up by you on the host with the connection's webhook secret", OK: true,
		Detail: "Launchway cannot test delivery from here; without webhooks it polls the repository instead"})
	return r, nil
}
