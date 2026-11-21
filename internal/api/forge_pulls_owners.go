package api

import (
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// Code owners: who owns the files a pull request changes, and the check of
// the owners file itself. The file is read from the target branch.

// pullOwners says who owns each file the pull request changes.
func (s *Server) pullOwners(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	files, err := s.Forge.Pulls.ChangedPaths(r.Context(), repo.ID, p)
	if err != nil {
		s.failPull(w, err)
		return
	}
	view, _, err := s.Forge.Pulls.Owners(r.Context(), repo.ID, p, files)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": pulls.OwnersFile, "branch": p.TargetBranch, "owners": view})
}

// checkOwnersFile parses text sent as an owners file and says what is wrong
// with it, in plain words, without storing anything.
func (s *Server) checkOwnersFile(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	doc := pulls.ParseOwners(in.Text)
	writeJSON(w, http.StatusOK, map[string]any{"file": pulls.OwnersFile, "valid": len(doc.Problems) == 0, "rules": doc.Rules, "problems": doc.Problems})
}
