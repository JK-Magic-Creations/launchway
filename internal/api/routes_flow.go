package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// rerunPlan says what re-running the run from ?from= would run again and
// reuse, or, as an error, why it cannot be re-run from there.
func (s *Server) rerunPlan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	plan, err := s.Engine.PlanRerunFrom(r.PathValue("id"), q["from"], q.Get("failed") == "true")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// rerunRun creates a new run that re-runs a finished one from a step:
// {"from": "<step>"}, from several, {"from": ["a", "b"]}, or from what
// failed, {"failed": true}. The steps and every step after them run again;
// the others are reused with their outcomes and outputs. A draft run's re-run
// runs its unsaved text again, so it needs the right to start draft runs,
// and whether its steps may read secrets is decided by the current setting.
func (s *Server) rerunRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From   json.RawMessage `json:"from"`
		Failed bool            `json:"failed"`
	}
	if !decode(w, r, &in) {
		return
	}
	var froms []string
	if len(in.From) > 0 && string(in.From) != "null" {
		var one string
		if json.Unmarshal(in.From, &one) == nil {
			froms = []string{one}
		} else if err := json.Unmarshal(in.From, &froms); err != nil {
			writeErr(w, http.StatusBadRequest, "from is a step, or a list of steps: {\"from\": [\"a\", \"b\"]}")
			return
		}
	}
	froms = slices.DeleteFunc(froms, func(f string) bool { return f == "" })
	if len(froms) == 0 && !in.Failed {
		writeErr(w, http.StatusBadRequest, "say which step to re-run from: {\"from\": \"<step>\"}")
		return
	}
	id := identityFrom(r)
	sum, err := s.Engine.Summary(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	rq := engine.RerunRequest{Run: sum.ID, Failed: in.Failed, Actor: id.User}
	if len(froms) > 0 {
		rq.From, rq.Froms = froms[0], froms[1:]
	}
	detail := fmt.Sprintf("re-run of #%d from step %s", sum.Number, strings.Join(froms, ", "))
	if in.Failed {
		detail = fmt.Sprintf("re-run of #%d from the failed steps", sum.Number)
		if len(froms) > 0 {
			detail += " and " + strings.Join(froms, ", ")
		}
	}
	if sum.Draft {
		detail += ", draft run, pipeline " + sum.PipelineHash
	}
	setAudit(r, "", detail)
	if sum.Draft {
		if !s.authorize(id, auth.RunDraft, sum.Project) {
			writeErr(w, http.StatusForbidden, s.draftRefusal(sum.Project))
			return
		}
		secrets := s.Drafts.For(sum.Project).Secrets
		rq.DraftSecrets = &secrets
	}
	run, err := s.Engine.Rerun(rq)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "", fmt.Sprintf("%s: new Sortie #%d %s", detail, run.Number, run.ID))
	writeJSON(w, http.StatusCreated, run)
}

// validate checks a pipeline file without running it. With ?project=NAME
// (which the caller must be able to see) or ?expand=1, templates are
// expanded from the libraries' current versions, as a run would expand
// them; otherwise a step that uses a template is one node of the graph.
func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	project := q.Get("project")
	expand := (project != "" || q.Get("expand") == "1") && s.Projects != nil
	if project != "" {
		if _, err := s.Projects.Get(project); err != nil || !s.authorize(identityFrom(r), auth.Read, project) {
			s.fail(w, fmt.Errorf("%w: Flight %s", engine.ErrNotFound, project))
			return
		}
	}
	var p *pipeline.Pipeline
	if expand {
		p, err = s.Projects.CheckFor(r.Context(), project, string(body), nil)
	} else {
		p, err = pipeline.Parse(body)
	}
	if err != nil {
		var ps pipeline.Problems
		if !errors.As(err, &ps) {
			if !errors.Is(err, engine.ErrInvalid) {
				s.fail(w, err)
				return
			}
			ps = pipeline.Problems{{Message: strings.TrimPrefix(err.Error(), "invalid: ")}}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "problems": ps, "expanded": expand})
		return
	}
	// The second pass, against this server's configuration: the
	// environments deploy steps name.
	if ps := s.Engine.CheckDeploys(project, p); len(ps) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "problems": ps, "expanded": expand})
		return
	}
	// And against the installation's limits on pipelines.
	if ps := s.Engine.CheckRunLimits(p); len(ps) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "problems": ps, "expanded": expand})
		return
	}
	out := map[string]any{"ok": true, "graph": p, "levels": p.Levels(), "expanded": expand, "notes": p.PathNotes()}
	// What is valid but will not work as written for this project: a
	// pipeline that filters pull requests by the branch they target needs
	// the project's forge to say which that is.
	if project != "" && s.Projects != nil && p.On.FiltersPullRequests() && !s.Projects.HasForge(project) {
		out["warnings"] = []string{"the pipeline filters pull requests by the branch they target (on.pull_request.into), and Flight " + project +
			" has no forge connection: no pull request will run until it has one"}
	}
	if ws := outputWarnings(p); len(ws) > 0 {
		warnings, _ := out["warnings"].([]string)
		out["warnings"] = append(warnings, ws...)
	}
	writeJSON(w, http.StatusOK, out)
}

// flowRoutes are the routes flow control added (schema 24): a project's
// lanes, and the installation's limits on pipelines, which anyone signed in
// reads and an administrator changes.
func (s *Server) flowRoutes() []route {
	return []route{
		{"GET /api/v1/projects/{name}/lanes", projectRead, "", s.projectLanes},
		{"GET /api/v1/run-limits", signedIn, "", s.getRunLimits},
		{"PUT /api/v1/run-limits", administer, "run_limits.set", s.setRunLimits},
	}
}
