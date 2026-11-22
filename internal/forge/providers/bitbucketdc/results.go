package bitbucketdc

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

// key is the status's identifier: derived from the name so that a name's
// status is updated in place (a build status is stored by its key).
func key(name string) string {
	h := sha256.Sum256([]byte(name))
	return "lw-" + hex.EncodeToString(h[:8])
}

// Publish: POST /api/latest/projects/{k}/repos/{s}/commits/{commitId}/builds
// ("Store a build status", since 7.14; REPO_READ on the repository) with key,
// state (CANCELLED, FAILED, INPROGRESS, SUCCESSFUL, UNKNOWN), url (all three
// required), name and description. A status with the same key is replaced.
// The host has no errored state: an errored result is CANCELLED.
func (r results) Publish(ctx context.Context, repo forge.Repository, res forge.Result) error {
	if err := res.Validate(); err != nil {
		return err
	}
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return err
	}
	state := map[forge.State]string{forge.Pending: "INPROGRESS", forge.Passing: "SUCCESSFUL", forge.Failing: "FAILED", forge.Errored: "CANCELLED"}[res.State]
	link := res.URL
	if link == "" { // the address is mandatory: the install's own when the result has none
		link = r.p.root()
	}
	body := map[string]any{"key": key(res.Name), "state": state, "name": res.Name, "url": link, "description": strings.Join(strings.Fields(res.Summary), " ")}
	_, err = r.p.c.Call(ctx, http.MethodPost, rp+"/commits/"+restkit.Esc(res.SHA)+"/builds", body, nil)
	return err
}

// List: GET /build-status/latest/commits/{commitId} (the only call of the
// reference that lists every status of a commit; deprecated in 7.14 in favour
// of a resource that gets one status by its key).
func (r results) List(ctx context.Context, _ forge.Repository, sha string) ([]forge.Result, error) {
	var in page[struct {
		Name        string `json:"name"`
		Key         string `json:"key"`
		State       string `json:"state"`
		Description string `json:"description"`
		URL         string `json:"url"`
	}]
	if _, err := r.p.c.Get(ctx, "/build-status/latest/commits/"+restkit.Esc(sha)+"?limit=100", &in); err != nil {
		return nil, err
	}
	out := []forge.Result{}
	for _, s := range in.Values {
		st := map[string]forge.State{"INPROGRESS": forge.Pending, "SUCCESSFUL": forge.Passing, "FAILED": forge.Failing, "CANCELLED": forge.Errored}[s.State]
		name := s.Name
		if name == "" {
			name = s.Key
		}
		out = append(out, forge.Result{Producer: "host", Name: name, SHA: sha, State: st, Summary: s.Description, URL: s.URL})
	}
	return out, nil
}
