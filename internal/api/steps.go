package api

import (
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// The custom steps of a library (see internal/stepkit): a list at a version,
// and a step's page. Both are generated from the steps' manifests.

func (s *Server) libraryVersion(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no libraries are configured")
		return "", false
	}
	version := r.URL.Query().Get("version")
	if version != "" && !pipeline.ValidVersion(version) {
		writeErr(w, http.StatusBadRequest, "the version is not a tag, branch or commit id")
		return "", false
	}
	return version, true
}

// libraryStepList lists a library's custom steps at ?version= (default: its newest tag).
func (s *Server) libraryStepList(w http.ResponseWriter, r *http.Request) {
	version, ok := s.libraryVersion(w, r)
	if !ok {
		return
	}
	out, err := s.Projects.LibrarySteps(r.Context(), r.PathValue("library"), version)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// StepUseView is how a Flight uses a step, with the update on offer.
type StepUseView struct {
	engine.CustomStepUse
	// Behind says the Flight's version is not the newest tag; Changes are what the newest changes, and
	// Widens that it asks for more than the version in use.
	Newest  string              `json:"newest,omitempty"`
	Changes []source.StepChange `json:"changes,omitempty"`
	Widens  bool                `json:"widens,omitempty"`
}

// libraryStepPage is a step's page: its manifest, versions with what changed, and who uses it (the Flights the caller may see).
func (s *Server) libraryStepPage(w http.ResponseWriter, r *http.Request) {
	version, ok := s.libraryVersion(w, r)
	if !ok {
		return
	}
	lib, step := r.PathValue("library"), r.PathValue("step")
	page, err := s.Projects.LibraryStep(r.Context(), lib, step, version)
	if err != nil {
		s.fail(w, err)
		return
	}
	uses, err := s.Engine.CustomStepUses(lib, step, 200)
	if err != nil {
		s.fail(w, err)
		return
	}
	see := s.visibleFilter(r)
	used := []StepUseView{}
	for _, u := range uses {
		if !see(u.Project) {
			continue
		}
		v := StepUseView{CustomStepUse: u}
		if newest, cs, widens, err := s.Projects.StepUpdateNotice(r.Context(), lib, step, u.Commit); err == nil {
			v.Newest, v.Changes, v.Widens = newest, cs, widens
		}
		used = append(used, v)
	}
	writeJSON(w, http.StatusOK, struct {
		*source.StepPage
		UsedBy []StepUseView `json:"used_by"`
	}{page, used})
}
