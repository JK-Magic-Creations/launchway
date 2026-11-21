package api

// The installation's limits on pipelines (schema 24): for now the longest
// timeout a pipeline may ask for. They are the server's configuration, kept
// in the database; anyone signed in may read them (a pipeline's author
// needs to know what is allowed), and only an administrator changes them.
// Every change is audited with the values before and after.

import (
	"fmt"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func (s *Server) getRunLimits(w http.ResponseWriter, r *http.Request) {
	l, err := s.Engine.RunLimits()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// setRunLimits replaces the limits: {"max_run_timeout": "48h"}; a value left
// out, or empty, is not set.
func (s *Server) setRunLimits(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MaxRunTimeout string `json:"max_run_timeout"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, "run limits", "")
	before, after, err := s.Engine.SetRunLimits(engine.RunLimits{MaxRunTimeout: in.MaxRunTimeout}, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "run limits", clip(runLimitsText(before)+" -> "+runLimitsText(after), 600))
	writeJSON(w, http.StatusOK, after)
}

func runLimitsText(l engine.RunLimits) string {
	return fmt.Sprintf("max_run_timeout=%q", l.MaxRunTimeout)
}
