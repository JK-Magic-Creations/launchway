package api

import (
	"context"
	"net/http"
	"time"
)

// info is public. With sign-in on it says only what is needed to sign in.
// The schema versions (this build's, and the database's) are included, so
// that an upgrade or its rollback can be checked; the database's is left
// out if it cannot be read.
func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"version": s.Version}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if schema, err := s.Engine.Schema(ctx); err == nil {
		out["schema"] = schema
	} else {
		out["schema"] = map[string]int{"code": schema.Code}
	}
	if s.Auth != nil {
		s.localInfo(out)
	} else {
		out["auth_mode"], out["actor"], out["projects"] = "none", s.Actor, len(s.projects())
		s.addSignInOff(out)
	}
	writeJSON(w, http.StatusOK, out)
}
