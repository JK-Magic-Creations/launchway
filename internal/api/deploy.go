package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// RunnerPool is a set of deploy runners with a token of their own. Only a
// pool's token can take deploy work, and only for the targets the pool may
// serve: the shared runner token, which build runners running anyone's
// code hold, can never claim a target or report that a deployment is
// healthy.
type RunnerPool struct {
	Name    string
	Token   string // "" for a pool that takes only enrolled runners
	Targets []string
	// RequireIdentity refuses the pool's token: only runners enrolled
	// with a join token for the pool may serve it.
	RequireIdentity bool
}

type poolKey struct{}

// callerPool is the runner pool whose token a runner request carried, or
// nil for the shared runner token.
func callerPool(r *http.Request) *RunnerPool {
	p, _ := r.Context().Value(poolKey{}).(*RunnerPool)
	return p
}

// poolTargets is what a deploy runner may serve: the targets it says it
// serves that its pool may serve.
func poolTargets(pool *RunnerPool, asked []string) []string {
	var out []string
	for _, t := range asked {
		if slices.Contains(pool.Targets, t) && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) deployLease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner      engine.RunnerInfo `json:"runner"`
		WaitSeconds int               `json:"wait_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	targets, ok := s.deployRunner(w, r, &in.Runner)
	if !ok {
		return
	}
	wait := time.Duration(min(max(in.WaitSeconds, 0), 50)) * time.Second
	ctx, cancel := s.longLived(r)
	defer cancel()
	job, err := s.Engine.LeaseDeploy(ctx, in.Runner, targets, wait)
	switch {
	case err != nil && r.Context().Err() != nil:
		return
	case err != nil && ctx.Err() != nil:
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	if job != nil && r.Context().Err() != nil {
		_ = s.Engine.Release(job.LeaseID)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) deployWatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner      engine.RunnerInfo `json:"runner"`
		Known       string            `json:"known"`
		WaitSeconds int               `json:"wait_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	targets, ok := s.deployRunner(w, r, &in.Runner)
	if !ok {
		return
	}
	wait := time.Duration(min(max(in.WaitSeconds, 0), 25)) * time.Second
	ctx, cancel := s.longLived(r)
	defer cancel()
	items, version, err := s.Engine.Watch(ctx, in.Runner, targets, in.Known, wait)
	if err != nil {
		if ctx.Err() != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "version": version, "targets": targets})
}

// maxObservations bounds the observations of one request.
const maxObservations = 200

func (s *Server) deployObserve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner       engine.RunnerInfo    `json:"runner"`
		Observations []engine.Observation `json:"observations"`
	}
	if !decode(w, r, &in) {
		return
	}
	targets, ok := s.deployRunner(w, r, &in.Runner)
	if !ok {
		return
	}
	if len(in.Observations) > maxObservations {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("at most %d observations per call", maxObservations))
		return
	}
	res, err := s.Engine.Observe(in.Runner, targets, in.Observations)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

// ---- for people ----

// visibleFilter returns who may see which projects, for lists.
func (s *Server) visibleFilter(r *http.Request) func(string) bool {
	id := identityFrom(r)
	return func(project string) bool { return s.authorize(id, auth.Read, project) }
}

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := s.Engine.EnvironmentStates(s.visibleFilter(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	// People see the environments that list a project they may see; the
	// projects of an environment are configuration, not secrets, but the
	// list is trimmed the same way as everything else.
	see := s.visibleFilter(r)
	out := envs[:0]
	for _, env := range envs {
		visible := len(env.Services) > 0
		for _, p := range env.Projects {
			if see(p) || strings.ContainsAny(p, "*?[") {
				visible = true
			}
		}
		if visible {
			id := identityFrom(r)
			for i := range env.Services {
				u := &env.Services[i]
				if u.Rollback != nil {
					s.roleAction(id, u.Project, env.Name, u.Rollback)
				}
				if u.Last != nil {
					for j := range u.Last.Actions {
						s.roleAction(id, u.Project, env.Name, &u.Last.Actions[j])
					}
				}
			}
			out = append(out, env)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	only, err := s.visibleProjects(identityFrom(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	project := q.Get("project")
	if project != "" && !s.authorize(identityFrom(r), auth.Read, project) {
		writeJSON(w, http.StatusOK, []engine.DeploymentView{})
		return
	}
	list, err := s.Engine.Deployments(engine.DeploymentFilter{Environment: q.Get("environment"), Project: project,
		Service: q.Get("service"), Limit: limit, OnlyProjects: only})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.presentDeployments(identityFrom(r), list)
	writeJSON(w, http.StatusOK, list)
}
