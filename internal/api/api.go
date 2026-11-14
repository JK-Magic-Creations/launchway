// Package api serves Slipway's HTTP API: one part for runners, one for the
// web UI and the command line, and the UI itself.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"slipway/internal/engine"
	"slipway/internal/hub"
	"slipway/internal/pipeline"
	"slipway/internal/source"
)

// Server holds what the handlers need.
type Server struct {
	Engine      *engine.Engine
	Projects    *source.Manager
	RunnerToken string
	Actor       string // who UI actions are recorded as, until sign-in exists
	UI          fs.FS  // the built web UI; may be nil
	Version     string
	Log         *slog.Logger
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	mux := http.NewServeMux()

	// Runner API.
	mux.HandleFunc("POST /api/runner/v1/lease", s.runnerAuth(s.lease))
	mux.HandleFunc("POST /api/runner/v1/start", s.runnerAuth(s.start))
	mux.HandleFunc("POST /api/runner/v1/heartbeat", s.runnerAuth(s.heartbeat))
	mux.HandleFunc("POST /api/runner/v1/logs", s.runnerAuth(s.logs))
	mux.HandleFunc("POST /api/runner/v1/finish", s.runnerAuth(s.finish))

	// UI and CLI API.
	mux.HandleFunc("GET /api/v1/info", s.info)
	mux.HandleFunc("GET /api/v1/projects", s.listProjects)
	mux.HandleFunc("GET /api/v1/projects/{name}", s.getProject)
	mux.HandleFunc("GET /api/v1/projects/{name}/refs", s.projectRefs)
	mux.HandleFunc("POST /api/v1/projects/{name}/runs", s.startRun)
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.getRun)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.runEvents)
	mux.HandleFunc("GET /api/v1/runs/{id}/pipeline-file", s.runPipelineFile)
	mux.HandleFunc("GET /api/v1/runs/{id}/steps/{step}/logs", s.stepLogs)
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/v1/runs/{id}/resume", s.resumeRun)
	mux.HandleFunc("GET /api/v1/runners", s.listRunners)
	mux.HandleFunc("POST /api/v1/validate", s.validate)
	mux.HandleFunc("GET /api/v1/stream", s.stream)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such API endpoint")
	})

	mux.Handle("/", s.ui())
	return s.recoverer(mux)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// fail maps engine errors onto HTTP statuses.
func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrLeaseLost):
		writeErr(w, http.StatusGone, err.Error())
	case errors.Is(err, engine.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrConflict):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "conflict: "))
	case errors.Is(err, engine.ErrClosed):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		s.Log.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	if err := dec.Decode(v); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("panic serving a request", "path", r.URL.Path, "panic", v)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) runnerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.RunnerToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.RunnerToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "a valid runner token is required")
			return
		}
		next(w, r)
	}
}

// ---- runner API ----

func (s *Server) lease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner      engine.RunnerInfo `json:"runner"`
		WaitSeconds int               `json:"wait_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	wait := time.Duration(min(max(in.WaitSeconds, 0), 50)) * time.Second
	job, err := s.Engine.Lease(r.Context(), in.Runner, wait)
	if err != nil {
		if r.Context().Err() != nil {
			return // the runner went away; nobody to answer
		}
		s.fail(w, err)
		return
	}
	if job != nil && r.Context().Err() != nil {
		// Leased in the instant the runner disconnected: hand it straight back.
		_ = s.Engine.Release(job.LeaseID)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type leaseReq struct {
	LeaseID string           `json:"lease_id"`
	Lines   []engine.LogLine `json:"lines"`
	Result  engine.Result    `json:"result"`
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var in leaseReq
	if !decode(w, r, &in) {
		return
	}
	cancel, err := s.Engine.Start(in.LeaseID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancel": cancel})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var in leaseReq
	if !decode(w, r, &in) {
		return
	}
	cancel, err := s.Engine.Heartbeat(in.LeaseID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancel": cancel})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	var in leaseReq
	if !decode(w, r, &in) {
		return
	}
	if err := s.Engine.AppendLogs(in.LeaseID, in.Lines); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request) {
	var in leaseReq
	if !decode(w, r, &in) {
		return
	}
	if err := s.Engine.Finish(in.LeaseID, in.Result); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- UI and CLI API ----

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": s.Version, "actor": s.Actor, "projects": len(s.projects())})
}

func (s *Server) projects() []source.ProjectStatus {
	if s.Projects == nil {
		return []source.ProjectStatus{}
	}
	return s.Projects.List()
}

type projectView struct {
	source.ProjectStatus
	LastRun *engine.RunSummary `json:"last_run"`
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	out := []projectView{}
	for _, p := range s.projects() {
		v := projectView{ProjectStatus: p}
		if runs, err := s.Engine.ListRuns(engine.RunFilter{Project: p.Name, Limit: 1}); err == nil && len(runs) > 0 {
			v.LastRun = runs[0]
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	p, err := s.Projects.Get(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) projectRefs(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	refs, err := s.Projects.Refs(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if refs == nil {
		refs = []source.Ref{}
	}
	writeJSON(w, http.StatusOK, refs)
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	var in struct {
		Ref string `json:"ref"`
	}
	if !decode(w, r, &in) {
		return
	}
	run, err := s.Projects.StartRun(r.Context(), r.PathValue("name"), in.Ref, s.Actor)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	runs, err := s.Engine.ListRuns(engine.RunFilter{Project: q.Get("project"), Status: q.Get("status"), Limit: limit})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	d, err := s.Engine.GetRun(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.Engine.Events(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	// The first event carries the whole pipeline; the UI shows that elsewhere.
	out := make([]engine.Event, len(evs))
	for i, ev := range evs {
		ev.Data.Pipeline = nil
		out[i] = ev
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) runPipelineFile(w http.ResponseWriter, r *http.Request) {
	d, err := s.Engine.GetRun(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if d.Source.Kind != "git" || s.Projects == nil {
		writeErr(w, http.StatusNotFound, "the pipeline file is only kept for runs from a Git project")
		return
	}
	text, err := s.Projects.ReadPipeline(r.Context(), d.Project, d.SHA)
	if err != nil {
		s.fail(w, fmt.Errorf("%w: %v", engine.ErrNotFound, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (s *Server) stepLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	attempt, _ := strconv.Atoi(q.Get("attempt"))
	after, _ := strconv.Atoi(q.Get("after"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	lines, att, err := s.Engine.Logs(r.PathValue("id"), r.PathValue("step"), attempt, after, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempt": att, "lines": lines})
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Cancel(r.PathValue("id"), s.Actor); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Resume(r.PathValue("id"), s.Actor); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Engine.Runners()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

// validate checks a pipeline file without running it.
func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := pipeline.Parse(body)
	if err != nil {
		var ps pipeline.Problems
		if !errors.As(err, &ps) {
			ps = pipeline.Problems{{Message: err.Error()}}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "problems": ps})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "graph": p, "levels": p.Levels()})
}

// stream sends live changes as server-sent events. With ?run=ID it sends only
// that run's changes, including log lines; otherwise it sends run changes.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	runID := r.URL.Query().Get("run")
	ch, stop := s.Engine.Hub().Subscribe(func(m hub.Message) bool {
		if runID != "" {
			return m.RunID == runID
		}
		return m.Type == "run"
	})
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 2000\n: connected\n\n")
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case m, ok := <-ch:
			if !ok {
				return // fell behind; the browser reconnects and refetches
			}
			data, err := json.Marshal(m.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Type, data)
			flusher.Flush()
		}
	}
}

// ui serves the single-page app, sending index.html for client-side routes.
func (s *Server) ui() http.Handler {
	if s.UI == nil {
		return http.HandlerFunc(noUI)
	}
	if _, err := fs.Stat(s.UI, "index.html"); err != nil {
		return http.HandlerFunc(noUI)
	}
	files := http.FileServerFS(s.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if _, err := fs.Stat(s.UI, path); err == nil {
				if strings.HasPrefix(path, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.UI, "index.html")
	})
}

func noUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "Slipway is running, but this build has no web UI. Build it with: make web")
}
