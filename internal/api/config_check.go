package api

import (
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// POST /api/v1/config/check/repository: reads a repository the way the
// server would (its Git credentials), keeping nothing, so a wizard can offer
// the branches and say whether a pipeline file is there.
func (s *Server) checkRepository(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if !decode(w, r, &b) {
		return
	}
	writeJSON(w, http.StatusOK, source.CheckRepository(r.Context(), b.Repo, b.Branch))
}

// POST /api/v1/config/check/signin: checks sign-in answers against the
// identity provider and writes the auth: block for the configuration file
// (sign-in stays in the file: the server needs it before the database).
func (s *Server) checkSignIn(w http.ResponseWriter, r *http.Request) {
	var b settings.SignInRequest
	if !decode(w, r, &b) {
		return
	}
	writeJSON(w, http.StatusOK, settings.CheckSignIn(r.Context(), b, nil))
}
