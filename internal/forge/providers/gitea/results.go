package gitea

import (
	"context"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type results struct{ p *Provider }

// maxDescription is Launchway's own cut; the documentation states no limit.
const maxDescription = 140

// Publish: POST /repos/{o}/{r}/statuses/{sha} with state (pending, success,
// error, failure), context, description and target_url (repoCreateStatus).
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
	_, err = r.p.c.Call(ctx, http.MethodPost, rp+"/statuses/"+restkit.Esc(res.SHA), body, nil)
	return err
}

// List: GET /repos/{o}/{r}/statuses/{sha} (repoListStatuses); the newest of
// each context (the highest id) is kept.
func (r results) List(ctx context.Context, repo forge.Repository, sha string) ([]forge.Result, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var in []struct {
		ID          int64  `json:"id"`
		Status      string `json:"status"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	if _, err := r.p.c.Get(ctx, rp+"/statuses/"+restkit.Esc(sha)+"?limit=50", &in); err != nil {
		return nil, err
	}
	best := map[string]int{}
	for i, s := range in {
		if j, ok := best[s.Context]; !ok || s.ID > in[j].ID {
			best[s.Context] = i
		}
	}
	out := []forge.Result{}
	for i, s := range in {
		if best[s.Context] != i {
			continue
		}
		st := map[string]forge.State{"pending": forge.Pending, "success": forge.Passing, "failure": forge.Failing, "error": forge.Errored}[s.Status]
		out = append(out, forge.Result{Producer: "host", Name: s.Context, SHA: sha, State: st, Summary: s.Description, URL: s.TargetURL})
	}
	return out, nil
}
