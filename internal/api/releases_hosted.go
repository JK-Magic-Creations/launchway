package api

import (
	"context"
	"encoding/json"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"net/http"
	"net/http/httptest"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// hostedRepoOfFlight returns the hosted repository a Flight reads, when the Flight's address is one this server
// hosts. A Flight on a hosted repository needs no push settings to be tagged: the server writes the tag itself,
// the way it writes a branch made in the browser, under the same rules.
func (s *Server) hostedRepoOfFlight(ctx context.Context, project string) (forge.Repository, bool) {
	if s.Forge == nil || s.Forge.Provider == nil || s.Projects == nil {
		return forge.Repository{}, false
	}
	for _, p := range s.projects() {
		if p.Name == project {
			return s.hostedRepoOf(ctx, p.URL)
		}
	}
	return forge.Repository{}, false
}

// canTag says whether a release of the Flight can have its tag made by Launchway: the Flight has push settings, or
// its repository is one this server hosts.
func (s *Server) canTag(ctx context.Context, project string) bool {
	if s.Projects == nil {
		return false
	}
	if s.Projects.WritableForReleases(project) {
		return true
	}
	if s.Hosts != nil && s.Hosts.IsAttached(project) {
		return true // the tag is made on the outside host through the connection
	}
	_, ok := s.hostedRepoOfFlight(ctx, project)
	return ok
}

// createHostedTag makes the annotated tag in a hosted repository and lets the mirrors and the Flight's triggers know,
// exactly as a push would. It returns the tag object's id.
func (s *Server) createHostedTag(r *http.Request, repo forge.Repository, tag, sha, message string) (string, error) {
	id := identityFrom(r)
	obj, err := s.filesService().CreateAnnotatedTag(r.Context(), repo.ID, tag, sha, message, nonEmpty(id.Profile.Name, id.User), s.writePolicy(r.Context(), r, repo))
	if err != nil {
		return "", err
	}
	s.afterServerCommit(r.Context(), gitCaller{id: id, user: id.User}, repo, "refs/tags/"+tag, "0000000000000000000000000000000000000000", obj)
	return obj, nil
}

func (s *Server) isHosted(ctx context.Context, project string) bool {
	_, ok := s.hostedRepoOfFlight(ctx, project)
	return ok
}

// refusal turns an error into the status and words the usual error answer would carry, for the places that decide
// before they answer.
func (s *Server) refusal(err error) (*engine.Release, int, string) {
	rec := httptest.NewRecorder()
	s.failFiles(rec, err)
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return nil, rec.Code, body.Error
}
