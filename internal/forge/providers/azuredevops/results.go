package azuredevops

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type results struct{ p *Provider }

// Genre is the genre of every status Launchway posts. A status check policy
// names its status "genre/name" ("type the genre/name value directly",
// https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies), so the
// policy for a Sortie's status "launchway/ci" is "launchway/launchway/ci".
const Genre = "launchway"

type statusState = string

var stateOf = map[forge.State]statusState{forge.Pending: "pending", forge.Passing: "succeeded", forge.Failing: "failed", forge.Errored: "error"}

func statusBody(res forge.Result) map[string]any {
	body := map[string]any{"state": stateOf[res.State], "description": strings.Join(strings.Fields(res.Summary), " "),
		"context": map[string]any{"name": res.Name, "genre": Genre}}
	if res.URL != "" {
		body["targetUrl"] = res.URL
	}
	return body
}

// Publish posts the result as a commit status: POST .../commits/{commitId}/statuses
// with state (notSet, pending, succeeded, failed, error, notApplicable),
// description, targetUrl and context.name and context.genre (scopes
// vso.code_write or vso.code_status;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/statuses/create).
// The same context posted again is the same status, updated. When the token can
// also read pull requests, the status is posted on each active pull request
// whose head is that commit: POST .../pullRequests/{id}/statuses with the same
// body (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-statuses/create),
// which is what a status check policy reads; that part is best effort, so a
// token that can post statuses but not read pull requests still publishes the
// commit status.
func (r results) Publish(ctx context.Context, repo forge.Repository, res forge.Result) error {
	if err := res.Validate(); err != nil {
		return err
	}
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return err
	}
	body := statusBody(res)
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/commits/"+restkit.Esc(res.SHA)+"/statuses"+qs(), body, nil); err != nil {
		return err
	}
	var in page[apiPull]
	if _, err := r.p.c.Get(ctx, rp+"/pullrequests"+qs("searchCriteria.status", "active", "$top", "100"), &in); err != nil {
		return nil
	}
	for _, pr := range in.Value {
		if pr.LastMergeSourceCommit.CommitID == res.SHA {
			_, _ = r.p.c.Call(ctx, http.MethodPost, rp+"/pullRequests/"+strconv.Itoa(pr.ID)+"/statuses"+qs(), body, nil)
		}
	}
	return nil
}

// List: GET .../commits/{commitId}/statuses?latestOnly=true (scope vso.code),
// the latest status of each context.
func (r results) List(ctx context.Context, repo forge.Repository, sha string) ([]forge.Result, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var in page[struct {
		State       string `json:"state"`
		Description string `json:"description"`
		TargetURL   string `json:"targetUrl"`
		Context     struct {
			Name  string `json:"name"`
			Genre string `json:"genre"`
		} `json:"context"`
	}]
	if _, err := r.p.c.Get(ctx, rp+"/commits/"+restkit.Esc(sha)+"/statuses"+qs("latestOnly", "true", "top", "100"), &in); err != nil {
		return nil, err
	}
	out := []forge.Result{}
	for _, s := range in.Value {
		name := s.Context.Name
		if s.Context.Genre != "" && s.Context.Genre != Genre {
			name = s.Context.Genre + "/" + name
		}
		st := map[string]forge.State{"pending": forge.Pending, "succeeded": forge.Passing, "failed": forge.Failing, "error": forge.Errored}[s.State]
		out = append(out, forge.Result{Producer: "host", Name: name, SHA: sha, State: st, Summary: s.Description, URL: strings.TrimSpace(s.TargetURL)})
	}
	return out, nil
}
