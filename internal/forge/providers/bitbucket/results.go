package bitbucket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type results struct{ p *Provider }

// key is the status's identifier, unique within the commit: derived from the name so that a name's status is updated in place.
func key(name string) string {
	h := sha256.Sum256([]byte(name))
	return "lw-" + hex.EncodeToString(h[:8])
}

// Publish: PUT /repositories/{w}/{r}/commit/{commit}/statuses/build/{key} to update
// the build status of that key, and, when the host has none (404), POST
// .../statuses/build to create it, with key, state (INPROGRESS, SUCCESSFUL,
// FAILED, STOPPED), name, url and description (scope repository). The host has
// no error state apart from stopped, so an errored result is STOPPED.
func (r results) Publish(ctx context.Context, repo forge.Repository, res forge.Result) error {
	if err := res.Validate(); err != nil {
		return err
	}
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return err
	}
	state := map[forge.State]string{forge.Pending: "INPROGRESS", forge.Passing: "SUCCESSFUL", forge.Failing: "FAILED", forge.Errored: "STOPPED"}[res.State]
	body := map[string]any{"key": key(res.Name), "state": state, "name": res.Name, "description": strings.Join(strings.Fields(res.Summary), " ")}
	if res.URL != "" {
		body["url"] = res.URL
	}
	base := rp + "/commit/" + restkit.Esc(res.SHA) + "/statuses/build"
	st, err := r.p.c.Call(ctx, http.MethodPut, base+"/"+key(res.Name), body, nil, http.StatusNotFound)
	if err != nil || st != http.StatusNotFound {
		return err
	}
	_, err = r.p.c.Call(ctx, http.MethodPost, base, body, nil)
	return err
}

// List: GET /repositories/{w}/{r}/commit/{commit}/statuses (scope repository).
func (r results) List(ctx context.Context, repo forge.Repository, sha string) ([]forge.Result, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var in page[struct {
		Name        string `json:"name"`
		State       string `json:"state"`
		Description string `json:"description"`
		URL         string `json:"url"`
	}]
	if _, err := r.p.c.Get(ctx, rp+"/commit/"+restkit.Esc(sha)+"/statuses?pagelen=100", &in); err != nil {
		return nil, err
	}
	out := []forge.Result{}
	for _, s := range in.Values {
		st := map[string]forge.State{"INPROGRESS": forge.Pending, "SUCCESSFUL": forge.Passing, "FAILED": forge.Failing, "STOPPED": forge.Errored}[s.State]
		out = append(out, forge.Result{Producer: "host", Name: s.Name, SHA: sha, State: st, Summary: s.Description, URL: s.URL})
	}
	return out, nil
}
