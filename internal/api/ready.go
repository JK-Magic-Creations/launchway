package api

import (
	"context"
	"net/http"
	"time"
)

// readyDatabase finishes /readyz: the database must answer, be at a schema
// this server may use, and be writable. A reachable but read-only database
// (a standby, or one set read-only) is not ready: a replica attached to it
// must not receive requests, so that a load balancer drains it. On
// PostgreSQL the answer also says whether the database is a standby and the
// replication state that can be seen.
func (s *Server) readyDatabase(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	// Reading the schema checks that the database answers, and catches a
	// newer server having applied a breaking migration since this one
	// started.
	schema, err := s.Engine.Schema(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": "the database did not answer: " + err.Error()})
		return
	}
	if msg := schema.Refusal(); msg != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "incompatible", "error": msg, "schema": schema})
		return
	}
	state := s.Engine.DatabaseState
	if s.dbState != nil {
		state = s.dbState
	}
	db, err := state(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": "the database did not answer: " + err.Error()})
		return
	}
	if !db.Writable {
		msg := "the database is read-only: it cannot take writes"
		if db.Standby {
			msg = "the database is a standby (in recovery): it follows a primary and cannot take writes"
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "read_only", "error": msg, "schema": schema, "database": db})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "schema": schema, "database": db})
}
