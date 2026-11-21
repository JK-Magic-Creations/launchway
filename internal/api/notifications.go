package api

import (
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

// listNotifications is the delivery log of notifications: what was sent
// where, what is waiting to be sent again, and what could not be sent and why.
// Only the projects the caller may read are listed.
func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 100
	}
	id := identityFrom(r)
	out, err := s.Engine.Notifications(limit, func(project string) bool {
		return project == "" || s.authorize(id, auth.Read, project)
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
