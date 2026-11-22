package api

import (
	"context"
	"encoding/json"
	"errors"
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

type releaseReqKey struct{}

// hostedTagger is how the built-in provider's Releases group writes a tag into
// a repository stored here (forge.TagWriter): under the rules of the person
// cutting the release, whose request rides in the context.
type hostedTagger struct{ s *Server }

func (h hostedTagger) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (string, error) {
	r, _ := ctx.Value(releaseReqKey{}).(*http.Request)
	if r == nil {
		return "", errors.New("a tag of a hosted repository is written on behalf of a signed-in person")
	}
	return h.s.createHostedTag(r, repo, t.Name, t.Commit, t.Message)
}

// releaseGroupOf is the Releases group a Flight's tags and releases are written
// through, with its repository: the outside host's provider for an attached
// Flight, the built-in provider for a repository stored here. One path for both.
func (s *Server) releaseGroupOf(ctx context.Context, project string) (g forge.Releases, repo forge.Repository, outside bool, ok bool, err error) {
	if s.Hosts != nil && s.Hosts.IsAttached(project) {
		g, repo, err = s.Hosts.Releases(ctx, project)
		return g, repo, true, err == nil, err
	}
	repo, found := s.hostedRepoOfFlight(ctx, project)
	if !found || s.Forge == nil {
		return nil, repo, false, false, nil
	}
	p, isProvider := s.Forge.Provider.(interface{ Releases() forge.Releases })
	if !isProvider {
		return nil, repo, false, false, nil
	}
	if ts, can := s.Forge.Provider.(forge.TagSetter); can {
		ts.SetTags(hostedTagger{s})
	}
	return p.Releases(), repo, false, true, nil
}

// createTagThroughGroup makes the annotated tag of a release through the
// Releases group of the Flight's repository, for a hosted and an outside
// repository alike. outside says which, for the words of a refusal.
func (s *Server) createTagThroughGroup(r *http.Request, project, tag, sha, message string) (obj string, outside, ok bool, err error) {
	ctx := context.WithValue(r.Context(), releaseReqKey{}, r)
	g, repo, outside, ok, err := s.releaseGroupOf(ctx, project)
	if err != nil || !ok {
		return "", outside, ok, err
	}
	ref, err := g.CreateTag(ctx, repo, forge.TagSpec{Name: tag, Commit: sha, Message: message})
	return ref.Commit, outside, true, err
}

// hostedOrOutside says whether a Flight's tags are written through a provider's
// Releases group: it keeps its code on an outside host, or in a repository stored here.
func (s *Server) hostedOrOutside(ctx context.Context, project string) bool {
	return (s.Hosts != nil && s.Hosts.IsAttached(project)) || s.isHosted(ctx, project)
}
