package api

import (
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"net/http"
	"strconv"
)

func (s *Server) stepLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	attempt, _ := strconv.Atoi(q.Get("attempt"))
	after, _ := strconv.Atoi(q.Get("after"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	// ?tail=1 reads the last lines rather than the first; ?q= only lines
	// that contain it, ignoring case.
	search := q.Get("q")
	if len(search) > 200 {
		writeErr(w, http.StatusBadRequest, "the search text is longer than 200 bytes")
		return
	}
	if msg := engine.CheckLogStream(q.Get("stream"), q.Get("service")); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	lines, att, err := s.Engine.QueryLogs(r.PathValue("id"), r.PathValue("step"),
		engine.LogQuery{Attempt: attempt, After: after, Limit: limit, Tail: q.Get("tail") == "1", Search: search, Stream: q.Get("stream"), Service: q.Get("service")})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempt": att, "lines": lines})
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Cancel(r.PathValue("id"), identityFrom(r).User); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// resumeRun resumes a failed or canceled run. Resuming a draft run runs its
// unsaved text again, so it needs the right to start draft runs, and
// whether its steps may read secrets is decided again by the current
// setting.
func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	sum, err := s.Engine.Summary(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if sum.Draft {
		if !s.authorize(id, auth.RunDraft, sum.Project) {
			writeErr(w, http.StatusForbidden, s.draftRefusal(sum.Project))
			return
		}
		setAudit(r, "", "draft run, pipeline "+sum.PipelineHash)
		err = s.Engine.ResumeDraft(sum.ID, id.User, s.cur().Drafts.For(sum.Project).Secrets)
	} else {
		err = s.Engine.Resume(sum.ID, id.User)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// decide approves or rejects an approval step. The body is optional:
// {"comment": "..."}.
func (s *Server) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Comment string `json:"comment"`
		}
		if !decode(w, r, &in) {
			return
		}
		setAudit(r, r.PathValue("id")+"/"+r.PathValue("step"), "")
		// An approval that names a deployment needs the role on its
		// environment too.
		if env, err := s.Engine.EnvironmentOf(r.PathValue("id"), r.PathValue("step"), ""); err != nil {
			s.fail(w, err)
			return
		} else if env != "" {
			project, err := s.Engine.RunProject(r.PathValue("id"))
			if err != nil {
				s.fail(w, err)
				return
			}
			if msg := s.envRefusal(identityFrom(r), project, env); msg != "" {
				writeErr(w, http.StatusForbidden, msg)
				return
			}
		}
		id := identityFrom(r)
		d, err := s.Engine.DecideAs(r.PathValue("id"), r.PathValue("step"), approve, engine.Decider{Actor: id.User, Groups: id.Groups}, in.Comment)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, decisionOut{OK: true, Decision: d})
	}
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	list, err := s.Engine.Approvals()
	if err != nil {
		s.fail(w, err)
		return
	}
	id := identityFrom(r)
	out := list[:0]
	for _, a := range list {
		if s.authorize(id, auth.Read, a.Project) {
			out = append(out, a)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
