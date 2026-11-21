package api

import "net/http"

import "github.com/jk-magic-creations/launchway/internal/forge/mirror"

func (s *Server) verifyRepos(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.mirrors(w)
	if !ok {
		return
	}
	var in mirror.SourceRequest
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, in.Connection, "")
	res, err := ms.Verify(r.Context(), in)
	if err != nil {
		s.failMirror(w, err)
		return
	}
	setAudit(r, in.Connection, map[bool]string{true: "every ref equal", false: "differences found"}[res.Equal])
	writeJSON(w, http.StatusOK, res)
}
