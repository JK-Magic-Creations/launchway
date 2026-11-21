package api

import (
	"encoding/json"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// The library that ships with Launchway is listed with the others (see
// source.Manager.Libraries); the pipeline file's JSON Schema is served here.

func (s *Server) starterRoutes() []route {
	return []route{
		// The JSON Schema of the pipeline file, for editors.
		{"GET /schema/pipeline.json", public, "", s.pipelineSchema},
	}
}

// pipelineSchema serves the JSON Schema of the pipeline file, so an editor
// can complete keys and mark unknown ones: point it at
// https://<server>/schema/pipeline.json. It describes the file's shape and
// holds nothing of the installation, so it needs no sign-in.
func (s *Server) pipelineSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/schema+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(pipeline.Schema())
}

// listLibraries lists the shared libraries pipelines may use, by name.
func (s *Server) listLibraries(w http.ResponseWriter, r *http.Request) {
	type lib struct {
		Name      string `json:"name"`
		LastFetch int64  `json:"last_fetch"`
		Builtin   bool   `json:"builtin,omitempty"`
	}
	out := []lib{}
	if s.Projects != nil {
		for _, l := range s.Projects.Libraries() {
			out = append(out, lib{Name: l.Name, LastFetch: l.LastFetch, Builtin: l.Builtin})
		}
	}
	writeJSON(w, http.StatusOK, out)
}
