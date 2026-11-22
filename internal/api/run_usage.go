package api

import "net/http"

// runUsage answers what each step of a run used and where its memory and CPU
// came from (declared, learned from earlier runs, the default, or raised after
// a kill), attempt by attempt. ?step= limits it to one step.
func (s *Server) runUsage(w http.ResponseWriter, r *http.Request) {
	out, err := s.Engine.RunUsage(r.PathValue("id"), r.URL.Query().Get("step"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": out})
}
