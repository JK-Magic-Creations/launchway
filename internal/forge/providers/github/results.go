package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type results struct{ p *Provider }

const maxDescription = 140

// Publish: POST /repos/{o}/{r}/statuses/{sha} with state (error, failure,
// pending, success), target_url, description and context
// (https://docs.github.com/en/rest/commits/statuses#create-a-commit-status).
// The host keeps at most 1,000 statuses per commit and context.
func (r results) Publish(ctx context.Context, repo forge.Repository, res forge.Result) error {
	if err := res.Validate(); err != nil {
		return err
	}
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return err
	}
	state := map[forge.State]string{forge.Pending: "pending", forge.Passing: "success", forge.Failing: "failure", forge.Errored: "error"}[res.State]
	desc := strings.Join(strings.Fields(res.Summary), " ")
	if len([]rune(desc)) > maxDescription {
		desc = string([]rune(desc)[:maxDescription-1]) + "…"
	}
	body := map[string]any{"state": state, "context": res.Name, "description": desc}
	if res.URL != "" {
		body["target_url"] = res.URL
	}
	_, err = r.p.c.Call(ctx, http.MethodPost, rp+"/statuses/"+esc(res.SHA), body, nil)
	return err
}

// List: GET /repos/{o}/{r}/commits/{ref}/statuses
// (https://docs.github.com/en/rest/commits/statuses#list-commit-statuses-for-a-reference).
// The host lists newest first; the newest of each context is kept.
func (r results) List(ctx context.Context, repo forge.Repository, sha string) ([]forge.Result, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var in []struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	if _, err := r.p.c.Call(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/statuses?per_page=100", rp, esc(sha)), nil, &in); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []forge.Result{}
	for _, s := range in {
		if seen[s.Context] {
			continue
		}
		seen[s.Context] = true
		st := map[string]forge.State{"pending": forge.Pending, "success": forge.Passing, "failure": forge.Failing, "error": forge.Errored}[s.State]
		out = append(out, forge.Result{Producer: "host", Name: s.Context, SHA: sha, State: st, Summary: s.Description, URL: s.TargetURL})
	}
	return out, nil
}
