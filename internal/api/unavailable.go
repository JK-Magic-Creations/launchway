package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// failUnavailable answers a request that could not be served because the
// server is stopping or its database is briefly gone: 503 with Retry-After
// and a plain message. Nothing was lost; the caller repeats the request.
func (s *Server) failUnavailable(w http.ResponseWriter, err error) {
	after := 5
	var ue *engine.UnavailableError
	if errors.As(err, &ue) {
		after = ue.RetryAfter()
	}
	w.Header().Set("Retry-After", strconv.Itoa(after))
	if errors.Is(err, engine.ErrClosed) {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.Log.Warn("the database is not available", "err", err)
	writeErr(w, http.StatusServiceUnavailable, "Launchway cannot reach its database right now. Nothing was lost; try again in a few seconds.")
}
