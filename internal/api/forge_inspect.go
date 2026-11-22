package api

import (
	"fmt"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/source"
	standardsservice "github.com/jk-magic-creations/launchway/internal/standards/service"
)

// Inspecting a hosted repository by hand. An Inspection is a run (the checks Standing orders call for, run in
// parallel, and the verdict), and a run belongs to a project, so what a repository can be inspected through is
// the project that watches it. The answer to "can I?" says why not in a sentence a person can act on.

func (s *Server) inspectionRoutes() []route {
	const base = "/api/v1/repositories/{owner}/{name}/inspections"
	return []route{
		{"GET " + base, repoRead, "", s.repoInspections},
		{"POST " + base, repoWrite, "repository.inspect", s.inspectRepo},
	}
}

type inspectionRun struct {
	ID        string `json:"id"`
	Number    int    `json:"number"`
	Status    string `json:"status"`
	SHA       string `json:"sha"`
	CreatedAt int64  `json:"created_at"`
}

type inspectionState struct {
	// Available says an Inspection can be started now; Reason says why not, otherwise.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Project is the project that watches the repository, and the one its findings are kept under.
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch,omitempty"`
	// Runs are the latest Inspections started by hand, newest first.
	Runs []inspectionRun `json:"runs"`
}

// watcherOf is the project that watches a repository, among those the caller may read.
func (s *Server) watcherOf(id *identity, repo forge.Repository) (name, branch string) {
	if s.Projects == nil {
		return "", ""
	}
	for _, p := range s.Projects.List() {
		if addressIsRepo(p.URL, repo.Path) && s.authorize(id, auth.Read, p.Name) {
			return p.Name, p.DefaultBranch
		}
	}
	return "", ""
}

// inspectionState says whether an Inspection can start, and why not when it cannot.
func (s *Server) inspectionState(r *http.Request, repo forge.Repository) inspectionState {
	st := inspectionState{Runs: []inspectionRun{}}
	id := identityFrom(r)
	st.Project, st.Branch = s.watcherOf(id, repo)
	if st.Branch == "" {
		st.Branch = repo.DefaultBranch
	}
	switch {
	case s.Standards == nil || s.Engine == nil || !s.Engine.StandingsKnown():
		st.Reason = "Standing orders are not available on this server, so there are no checks to run. Start a server of this release on its database."
	case standardsservice.SwitchedOff():
		st.Reason = "Standing orders are switched off for this installation (" + standardsservice.OffEnv + "=off), so no check runs. An administrator removes that setting to turn them on."
	case st.Project == "":
		st.Reason = "No project watches this repository, and an Inspection is a run, which belongs to a project. Add a project that reads " + nonEmpty(repo.Clone.HTTPS, repo.Path) + " (see the server's configuration), then come back."
	case !s.authorize(id, auth.Operate, st.Project):
		st.Reason = "You need the operator role on the project " + st.Project + " to start an Inspection."
	default:
		st.Available = true
		if rs, err := s.Engine.Runners(); err == nil {
			online := false
			for _, r := range rs {
				online = online || r.Online && (r.Kind == "" || r.Kind == "build")
			}
			if !online {
				st.Available, st.Reason = false, "No Station is online to take the run. Start one (Settings, Stations), or check that the built-in one is running."
			}
		}
	}
	if st.Project != "" && s.Engine != nil {
		if runs, err := s.Engine.ListRuns(engine.RunFilter{Project: st.Project, Limit: 30}); err == nil {
			for _, run := range runs {
				if run.Pipeline == source.InspectionPipelineName && len(st.Runs) < 5 {
					st.Runs = append(st.Runs, inspectionRun{ID: run.ID, Number: run.Number, Status: string(run.Status), SHA: run.SHA, CreatedAt: run.CreatedAt})
				}
			}
		}
	}
	return st
}

func (s *Server) repoInspections(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.inspectionState(r, repo))
}

// inspectRepo starts an Inspection of the repository's default branch (or the branch named in the body).
func (s *Server) inspectRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	var in struct {
		Ref string `json:"ref"`
	}
	if !decode(w, r, &in) {
		return
	}
	st := s.inspectionState(r, repo)
	setAudit(r, repo.Path, "inspection of "+nonEmpty(in.Ref, st.Branch))
	if !st.Available {
		code := http.StatusConflict
		if st.Project != "" && !s.authorize(identityFrom(r), auth.Operate, st.Project) {
			code = http.StatusForbidden
		}
		writeErr(w, code, st.Reason)
		return
	}
	run, err := s.Projects.InspectNow(r.Context(), source.StartRequest{Project: st.Project, Ref: in.Ref, Actor: identityFrom(r).User})
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, run.ID, fmt.Sprintf("inspection #%d of %s at %s", run.Number, st.Project, shortSHA(run.SHA)))
	writeJSON(w, http.StatusCreated, map[string]any{"run": run, "project": st.Project})
}
