package gitlab

import (
	"context"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type results struct{ p *Provider }

// maxField is the documented limit of description and target_url (255).
const maxField = 255

// Publish: POST /projects/:id/statuses/:sha with state (pending, running,
// success, failed, canceled, skipped), name, target_url and description, the
// last two at most 255 characters
// (https://docs.gitlab.com/api/commits/ , "Post the build status to a commit").
// The host has no state for an error apart from a failure, so Errored is
// published as failed; the summary says which.
func (r results) Publish(ctx context.Context, repo forge.Repository, res forge.Result) error {
	if err := res.Validate(); err != nil {
		return err
	}
	pp, err := r.p.projectPath(repo)
	if err != nil {
		return err
	}
	state := map[forge.State]string{forge.Pending: "pending", forge.Passing: "success", forge.Failing: "failed", forge.Errored: "failed"}[res.State]
	desc := strings.Join(strings.Fields(res.Summary), " ")
	if res.State == forge.Errored {
		desc = strings.TrimSpace("error. " + desc)
	}
	if len([]rune(desc)) > maxField {
		desc = string([]rune(desc)[:maxField-1]) + "…"
	}
	body := map[string]any{"state": state, "name": res.Name, "description": desc}
	if res.URL != "" && len(res.URL) <= maxField {
		body["target_url"] = res.URL
	}
	_, err = r.p.c.Call(ctx, http.MethodPost, pp+"/statuses/"+restkit.Esc(res.SHA), body, nil)
	return err
}

// List: GET /projects/:id/repository/commits/:sha/statuses (the latest status of each name).
func (r results) List(ctx context.Context, repo forge.Repository, sha string) ([]forge.Result, error) {
	pp, err := r.p.projectPath(repo)
	if err != nil {
		return nil, err
	}
	var in []struct {
		Name        string `json:"name"`
		Status      string `json:"status"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	if _, err := r.p.c.Get(ctx, pp+"/repository/commits/"+restkit.Esc(sha)+"/statuses?per_page=100", &in); err != nil {
		return nil, err
	}
	out := []forge.Result{}
	for _, s := range in {
		st, ok := map[string]forge.State{"success": forge.Passing, "failed": forge.Failing, "canceled": forge.Errored}[s.Status]
		if !ok {
			st = forge.Pending
		}
		out = append(out, forge.Result{Producer: "host", Name: s.Name, SHA: sha, State: st, Summary: s.Description, URL: s.TargetURL})
	}
	return out, nil
}
