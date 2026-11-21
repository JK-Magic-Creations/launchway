package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/preview"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Dependency updates of hosted repositories (internal/depupdate). The server
// looks at a repository's default branch and says what is in it, what is
// newer and what is vulnerable, and what the updater would open. It changes
// nothing: the updater itself runs as a pipeline step on a runner (the command
// line's `updates run`), and the pull requests it opens are ordinary ones.

// UpdatesService is what the API needs of the dependency updater.
type UpdatesService interface {
	Report(ctx context.Context, repoID, repoPath, branch string, immediate bool) (preview.Report, string, error)
}

func (s *Server) updateRoutes() []route {
	const b = "/api/v1/repositories/{owner}/{name}"
	return []route{
		{"GET " + b + "/dependencies", repoRead, "", s.listDependencies},
		{"GET " + b + "/updates/preview", repoRead, "", s.previewUpdates},
	}
}

// dependencyReport is the answer of both routes.
type dependencyReport struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	preview.Report
	Available []string `json:"ecosystems_available"`
}

func (s *Server) updatesOn(w http.ResponseWriter) bool {
	if !s.forgeOn(w) {
		return false
	}
	if s.Forge.Updates == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dependency updates are not available on this server", "code": "feature_off",
			"next": "Turn Git hosting on (forge.hosting); dependency updates read the repositories it hosts."})
		return false
	}
	return true
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, immediate bool) {
	if !s.updatesOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	if repo.DefaultBranch == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this repository has no default branch yet", "code": "empty_repository", "next": "Push a first commit; the updater reads the default branch."})
		return
	}
	rep, commit, err := s.Forge.Updates.Report(r.Context(), repo.ID, repo.Path, repo.DefaultBranch, immediate)
	if err != nil {
		switch {
		case errors.Is(err, forge.ErrNotFound):
			s.failForge(w, err)
		case strings.Contains(err.Error(), "does not exist"):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "no_branch", "next": "Push the default branch, or change the repository's default branch."})
		default:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the dependency report could not be made: " + err.Error(), "code": "report_failed", "next": "Check the registries and vulnerability data the server is set to use (LAUNCHWAY_UPDATES_*), then try again."})
		}
		return
	}
	writeJSON(w, http.StatusOK, dependencyReport{Repository: repo.Path, Branch: repo.DefaultBranch, Commit: commit, Report: rep, Available: depupdate.Available()})
}

func (s *Server) listDependencies(w http.ResponseWriter, r *http.Request) { s.report(w, r, false) }

func (s *Server) previewUpdates(w http.ResponseWriter, r *http.Request) { s.report(w, r, true) }
