// Package api serves Launchway's HTTP API: one part for runners, one for the
// web UI and the command line, and the UI itself.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/hub"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// Server holds what the handlers need.
type Server struct {
	Engine      *engine.Engine
	Projects    *source.Manager
	RunnerToken string
	// RunnerPools are the deploy runner pools, each with its own token.
	RunnerPools []RunnerPool
	// Actor is who everything is done as when sign-in is off (Auth is nil).
	Actor string
	// Auth turns sign-in on. Nil means none: every request acts as Actor,
	// with full rights.
	Auth *Auth
	// Drafts says who may start draft runs on each project, and whether
	// their steps may read secrets. Nil gives auth.DefaultDraftRule
	// everywhere. It applies with or without sign-in.
	Drafts *auth.DraftRules
	// TrustProxy believes X-Forwarded-Proto and X-Forwarded-For, as set by a
	// reverse proxy that every client must go through.
	TrustProxy bool
	UI         fs.FS // the built web UI; may be nil
	Version    string
	Log        *slog.Logger
	Now        func() time.Time // for tests
	// StreamPing is how often the live stream sends a keep-alive and checks
	// that the caller is still signed in; default 15s.
	StreamPing time.Duration
	// TokenMaxDays is the longest an API token may last; 0 is 365 days.
	TokenMaxDays int
	// WaiveRole is the role needed to waive a policy's rule
	// (policy.waivers.allow); nil means admin, NoRole nobody.
	WaiveRole *auth.Role
	// ReportsOrigin, when set (https://reports.example.com), is a separate
	// origin that alone serves hosted HTML reports (see reports.go).
	ReportsOrigin string
	// AI, when models are configured, lets people ask one to explain a
	// failed step; the server reads model keys from AISecrets (ai.secrets),
	// its own stores. Nil: the explain button says it is off.
	AI        *ai.Client
	AISecrets *secrets.Resolver
	// RunnersRole is the role needed on every project to manage runners:
	// make join tokens, revoke and rotate runners (runner_admin); nil means
	// admin, NoRole nobody.
	RunnersRole *auth.Role
	// Settings is the configuration kept in the database (schema 23),
	// served under /api/v1/config; nil on a server that keeps none.
	Settings *settings.Service

	sealerMu  sync.Mutex
	sealer    *auth.Sealer // seals the state of sign-ins in progress
	reportKey []byte       // signs links to HTML reports

	// MCP turns on the Model Context Protocol endpoint, /mcp; nil is off.
	MCP *MCPOptions

	mux      *http.ServeMux // the routes, which MCP tools call in process
	checks   checkLimiter
	live     atomic.Pointer[Live] // see Reconfigure
	draining atomic.Bool          // see Drain
	stopMu   sync.Mutex
	stopped  chan struct{} // closed by Stop; see longLived
}

// Auth configures sign-in.
type Auth struct {
	OIDC        *auth.OIDC
	Policy      *auth.Policy
	IdleTimeout time.Duration // a session unused for this long ends
	Lifetime    time.Duration // every session ends this long after sign-in
	// SecureCookies marks cookies Secure even on requests that do not look
	// like HTTPS, because the site is served over HTTPS (its redirect URL
	// is https).
	SecureCookies bool
}

// access says who may call a route; see guard.
type access int

const (
	public         access = iota // anyone; the handler decides
	runnerOnly                   // the runner token
	signedIn                     // anyone signed in; lists show only what they may see
	readAny                      // a viewer of at least one project
	projectRead                  // a viewer of the project in {name}
	projectOperate               // an operator of the project in {name}
	projectDraft                 // whoever the drafts setting allows to start draft runs of the project in {name}
	runRead                      // a viewer of the project of the run in {id}
	runOperate                   // an operator of the project of the run in {id}
	administer                   // an admin of every project
)

// route is one endpoint. Every endpoint is listed in routes, with who may
// call it; a test checks that each one is covered by the permission table.
type route struct {
	pattern string
	access  access
	audit   string // the action recorded in the audit trail; "" for calls that change nothing
	handler http.HandlerFunc
}

func (s *Server) routes() []route {
	return append(append(append(append(append(append(s.mainRoutes(), s.laterRoutes()...), s.configRoutes()...), s.flowRoutes()...), s.cacheRoutes()...), s.launchRoutes()...), s.approvalSettingsRoutes()...)
}

func (s *Server) mainRoutes() []route {
	return []route{
		// Health checks for load balancers and orchestrators.
		{"GET /healthz", public, "", s.healthz},
		{"GET /readyz", public, "", s.readyz},

		// Runner API.
		{"POST /api/runner/v1/lease", runnerOnly, "", s.lease},
		{"POST /api/runner/v1/start", runnerOnly, "", s.start},
		{"POST /api/runner/v1/heartbeat", runnerOnly, "", s.heartbeat},
		{"POST /api/runner/v1/logs", runnerOnly, "", s.logs},
		{"POST /api/runner/v1/finish", runnerOnly, "", s.finish},
		{"POST /api/runner/v1/runs", runnerOnly, "", s.runInfos},
		{"POST /api/runner/v1/leave", runnerOnly, "", s.leave},
		{"POST /api/runner/v1/deploy/lease", runnerOnly, "", s.deployLease},
		{"POST /api/runner/v1/deploy/watch", runnerOnly, "", s.deployWatch},
		{"POST /api/runner/v1/deploy/observe", runnerOnly, "", s.deployObserve},
		{"POST /api/runner/v1/deploy/commands", runnerOnly, "", s.deployCommands},
		{"POST /api/runner/v1/deploy/command-done", runnerOnly, "", s.deployCommandDone},
		// Enrolment (schema 20): a join token, or an assertion signed by the
		// runner's key, in the body; the handlers check and audit them.
		{"POST /api/runner/v2/join", public, "", s.runnerJoin},
		{"POST /api/runner/v2/token", public, "", s.runnerToken},
		{"POST /api/runner/v2/rotate", public, "", s.runnerRotate},

		// Signing in and out.
		{"GET /auth/login", public, "", s.login},
		{"GET /auth/callback", public, "", s.callback},
		{"POST /auth/logout", public, "", s.logout},

		// UI and CLI API.
		{"GET /api/v1/info", public, "", s.info},
		{"GET /api/v1/me", signedIn, "", s.me},
		{"GET /api/v1/projects", signedIn, "", s.listProjects},
		{"GET /api/v1/projects/{name}", projectRead, "", s.getProject},
		{"GET /api/v1/projects/{name}/refs", projectRead, "", s.projectRefs},
		{"POST /api/v1/projects/{name}/runs", projectOperate, "run.start", s.startRun},
		{"GET /api/v1/projects/{name}/pipeline", projectRead, "", s.readPipeline},
		{"GET /api/v1/projects/{name}/tests", projectRead, "", s.projectTests},
		{"GET /api/v1/projects/{name}/tests/history", projectRead, "", s.testHistory},
		{"POST /api/v1/projects/{name}/pipeline", projectOperate, "pipeline.save", s.savePipeline},
		{"POST /api/v1/projects/{name}/draft-runs", projectDraft, "run.draft", s.startDraft},
		{"GET /api/v1/runs", signedIn, "", s.listRuns},
		{"GET /api/v1/runs/{id}", runRead, "", s.getRun},
		{"GET /api/v1/runs/{id}/events", runRead, "", s.runEvents},
		{"GET /api/v1/runs/{id}/pipeline-file", runRead, "", s.runPipelineFile},
		{"GET /api/v1/runs/{id}/steps/{step}/logs", runRead, "", s.stepLogs},
		{"GET /api/v1/runs/{id}/tests", runRead, "", s.runTests},
		{"GET /api/v1/runs/{id}/tests/cases", runRead, "", s.runTestCases},
		{"GET /api/v1/runs/{id}/tests/cases/{case}", runRead, "", s.runTestCase},
		{"POST /api/v1/runs/{id}/cancel", runOperate, "run.cancel", s.cancelRun},
		{"POST /api/v1/runs/{id}/resume", runOperate, "run.resume", s.resumeRun},
		{"GET /api/v1/runs/{id}/rerun", runRead, "", s.rerunPlan},
		{"POST /api/v1/runs/{id}/rerun", runOperate, "run.rerun", s.rerunRun},
		{"POST /api/v1/runs/{id}/steps/{step}/approve", runOperate, "approval.approve", s.decide(true)},
		{"POST /api/v1/runs/{id}/steps/{step}/reject", runOperate, "approval.reject", s.decide(false)},
		{"GET /api/v1/approvals", signedIn, "", s.listApprovals},
		{"GET /api/v1/runners", readAny, "", s.listRunners},
		{"GET /api/v1/runner-pools", readAny, "", s.listPools},
		// Managing runners needs the role runner_admin names on every
		// project, which the handlers check.
		{"GET /api/v1/runner-join-tokens", signedIn, "", s.listJoinTokens},
		{"POST /api/v1/runner-join-tokens", signedIn, "runner.join_token.create", s.createJoinToken},
		{"POST /api/v1/runner-join-tokens/{id}/revoke", signedIn, "runner.join_token.revoke", s.revokeJoinToken},
		{"POST /api/v1/runners/{id}/revoke", signedIn, "runner.revoke", s.revokeRunner},
		{"POST /api/v1/runners/{id}/rotate", signedIn, "runner.rotate", s.rotateRunner},
		{"GET /api/v1/environments", signedIn, "", s.listEnvironments},
		{"GET /api/v1/deployments", signedIn, "", s.listDeployments},
		{"GET /api/v1/runs/{id}/deployments/{dep}/controls", runRead, "", s.controls},
		{"POST /api/v1/runs/{id}/deployments/{dep}/promote", runOperate, "deployment.promote", s.command("promote")},
		{"POST /api/v1/runs/{id}/deployments/{dep}/promote-full", runOperate, "deployment.promote_full", s.command("promote-full")},
		{"POST /api/v1/runs/{id}/deployments/{dep}/abort", runOperate, "deployment.abort", s.command("abort")},
		{"POST /api/v1/runs/{id}/deployments/{dep}/retry", runOperate, "deployment.retry", s.command("retry")},
		{"POST /api/v1/runs/{id}/deployments/{dep}/commands/{cmd}/withdraw", runOperate, "deployment.withdraw", s.withdrawCommand},
		{"POST /api/v1/validate", readAny, "", s.validate},
		{"GET /api/v1/stream", signedIn, "", s.stream},
		{"GET /api/v1/tokens", signedIn, "", s.listTokens},
		{"POST /api/v1/tokens", signedIn, "token.create", s.createToken},
		{"DELETE /api/v1/tokens/{id}", signedIn, "token.revoke", s.revokeToken},
		{"GET /api/v1/audit", administer, "", s.auditLog},
		{"GET /api/v1/projects/{name}/inputs", projectRead, "", s.projectInputs},
		{"GET /api/v1/tokens/limits", signedIn, "", s.tokenLimits},
		{"GET /api/v1/admin/tokens", administer, "", s.listAllTokens},
		{"DELETE /api/v1/admin/tokens/{id}", administer, "token.revoke", s.revokeAnyToken},
		{"GET /api/v1/projects/{name}/schedules", projectRead, "", s.projectSchedules},
		{"GET /api/v1/libraries", readAny, "", s.listLibraries},
		{"GET /api/v1/libraries/{library}/templates", readAny, "", s.libraryTemplates},
		// Webhooks prove themselves with a secret, not a session or token;
		// their handlers check and audit them (see hooks.go).
		{"POST /api/v1/projects/{name}/git-hook", public, "", s.gitHook},
		{"POST /api/v1/projects/{name}/webhooks/{trigger}", public, "", s.webhookTrigger},

		// Artifacts, findings and SBOMs (schema 12).
		{"POST /api/runner/v1/artifacts/begin", runnerOnly, "", s.beginArtifact},
		{"PUT /api/runner/v1/artifacts/chunk", runnerOnly, "", s.artifactChunk},
		{"POST /api/runner/v1/artifacts/commit", runnerOnly, "", s.commitArtifact},
		{"POST /api/runner/v1/artifacts/download", runnerOnly, "", s.fetchArtifact},
		{"GET /api/v1/artifact-store", readAny, "", s.artifactStore},
		{"GET /api/v1/runs/{id}/artifacts", runRead, "", s.runArtifacts},
		{"GET /api/v1/runs/{id}/artifacts/{artifact}/file", runRead, "", s.downloadArtifactFile},
		{"GET /api/v1/runs/{id}/artifacts/{artifact}/archive", runRead, "", s.downloadArtifactArchive},
		{"DELETE /api/v1/runs/{id}/artifacts/{artifact}", runOperate, "artifact.delete", s.deleteArtifact},
		{"GET /api/v1/runs/{id}/findings", runRead, "", s.runFindings},
		{"GET /api/v1/projects/{name}/security", projectRead, "", s.projectSecurity},
		{"GET /api/v1/components", signedIn, "", s.searchComponents},
	}
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.StreamPing <= 0 {
		s.StreamPing = 15 * time.Second
	}
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.Handle(rt.pattern, s.guard(rt))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such API endpoint")
	})
	mux.Handle("/", s.ui())
	s.mux = mux
	return s.recoverer(securityHeaders(s.splitOrigin(mux)))
}

// ---- shutting down ----

// Drain makes /readyz report that this server is not ready, so that load
// balancers stop sending it new requests. Everything else keeps working.
func (s *Server) Drain() { s.draining.Store(true) }

// Stop ends the requests that would otherwise stay open for a long time,
// runners waiting for work and live update streams, so that a graceful
// shutdown need only wait for ordinary requests. Runners and browsers
// reconnect, to another server if there is one.
func (s *Server) Stop() {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	ch := s.stoppedLocked()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (s *Server) stopping() <-chan struct{} {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	return s.stoppedLocked()
}

func (s *Server) stoppedLocked() chan struct{} {
	if s.stopped == nil {
		s.stopped = make(chan struct{})
	}
	return s.stopped
}

// longLived returns a context for a request that may wait a long time: it
// ends with the request, or when Stop is called.
func (s *Server) longLived(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	stopped := s.stopping()
	go func() {
		select {
		case <-stopped:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
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
	case errors.Is(err, engine.ErrForbidden):
		writeErr(w, http.StatusForbidden, strings.TrimPrefix(err.Error(), "not allowed: "))
	case errors.Is(err, engine.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, engine.ErrNeedsNewer):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrClosed):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.As(err, new(pipeline.InputErrors)):
		var ie pipeline.InputErrors
		errors.As(err, &ie)
		writeInputErrors(w, ie)
	case errors.As(err, new(pipeline.Problems)):
		var ps pipeline.Problems
		errors.As(err, &ps)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "the pipeline has problems; fix them first", "problems": ps})
	default:
		// The details go to the log, not to the caller: they can name
		// hosts, paths or queries the caller has no business seeing.
		s.Log.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "something went wrong on the server; its log has the details")
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

// ---- health ----

// healthz reports that the process is up.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports whether this server can do useful work, which needs the
// database to answer and the server not to be shutting down.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "stopping", "error": "the server is shutting down"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	// Reading the schema checks that the database answers, and catches a
	// newer server having applied a breaking migration since this one
	// started.
	schema, err := s.Engine.Schema(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": "the database did not answer: " + err.Error()})
		return
	}
	if msg := schema.Refusal(); msg != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "incompatible", "error": msg, "schema": schema})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "schema": schema})
}

// ---- runner API ----

func (s *Server) lease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner      engine.RunnerInfo `json:"runner"`
		WaitSeconds int               `json:"wait_seconds"`
	}
	if !decode(w, r, &in) || s.refusePools(w, r) || s.claimsEnrolled(w, r, in.Runner) {
		return
	}
	info := s.callerInfo(r, in.Runner)
	wait := time.Duration(min(max(in.WaitSeconds, 0), 50)) * time.Second
	ctx, cancel := s.longLived(r)
	defer cancel()
	job, err := s.Engine.Lease(ctx, info, wait)
	switch {
	case err != nil && r.Context().Err() != nil:
		return // the runner went away; nobody to answer
	case err != nil && ctx.Err() != nil:
		w.WriteHeader(http.StatusNoContent) // the server is stopping; ask again
		return
	case err != nil:
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
	if !decode(w, r, &in) || !s.leaseAllowed(w, r, in.LeaseID) {
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
	if !decode(w, r, &in) || !s.leaseAllowed(w, r, in.LeaseID) {
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
	if !decode(w, r, &in) || !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	if err := s.Engine.AppendLogs(in.LeaseID, in.Lines); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// finish records how a step attempt ended. Its body may carry the step's
// report files, so it may be larger than other requests: up to what the
// engine accepts of them (base64 in JSON), with room for the rest. A larger
// one is refused with 413, and the runner then reports the result without
// its files. Only a few large ones are read at once.
func (s *Server) finish(w http.ResponseWriter, r *http.Request) {
	limit := s.Engine.ResultLimit()
	if r.ContentLength > limit {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the result is larger than the %d MiB this server accepts", limit>>20))
		return
	}
	if r.ContentLength < 0 || r.ContentLength > 1<<20 {
		select {
		case largeFinishes <- struct{}{}:
			defer func() { <-largeFinishes }()
		case <-r.Context().Done():
			return
		}
	}
	var in leaseReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(&in); err != nil && err != io.EOF {
		if errors.As(err, new(*http.MaxBytesError)) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the result is larger than the %d MiB this server accepts", limit>>20))
			return
		}
		writeErr(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
		return
	}
	if !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	var refused *engine.AuditEntry
	if in.Result.Outcome == engine.OutcomeRefused {
		// Recorded before the lease ends, while it still says which run.
		_, _, runID, _, _ := s.Engine.LeaseHolder(in.LeaseID)
		project, _ := s.Engine.RunProject(runID)
		refused = &engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + nonEmpty(callerID(r), "shared"), Source: s.clientAddr(r),
			Action: "runner.refused", Project: project, Target: runID, Outcome: "ok", Detail: in.Result.Error}
	}
	if err := s.Engine.Finish(in.LeaseID, in.Result); err != nil {
		s.fail(w, err)
		return
	}
	if refused != nil {
		s.record(refused)
	}
	w.WriteHeader(http.StatusNoContent)
}

// largeFinishes bounds how many large results one server reads at once.
var largeFinishes = make(chan struct{}, 4)

// leave forgets a runner that is shutting down, so that other runners may
// take over its runs at once.
func (s *Server) leave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunnerID string `json:"runner_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if id := callerIdentity(r); id != nil {
		// An enrolled runner leaves as itself, whatever it says.
		if id.Ephemeral {
			if _, err := s.Engine.RevokeRunner(id.ID, "runner:"+id.ID, "an ephemeral runner left"); err != nil {
				s.fail(w, err)
				return
			}
			s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + id.ID, Source: s.clientAddr(r), Action: "runner.revoke",
				Target: id.ID, Outcome: "ok", Detail: "an ephemeral runner left"})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		in.RunnerID = id.ID
	} else if enrolled, err := s.Engine.IsEnrolled(in.RunnerID); err != nil {
		s.fail(w, err)
		return
	} else if enrolled {
		writeErr(w, http.StatusForbidden, "runner "+in.RunnerID+" is enrolled; only it may say it leaves")
		return
	}
	if err := s.Engine.RunnerLeft(in.RunnerID); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runInfos(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.IDs) > 5000 {
		writeErr(w, http.StatusBadRequest, "at most 5000 runs per call")
		return
	}
	var infos []engine.RunInfo
	var err error
	if id := callerIdentity(r); id != nil {
		infos, err = s.Engine.RunInfosFor(id.Info(engine.RunnerInfo{}), in.IDs)
	} else {
		infos, err = s.Engine.RunInfos(in.IDs)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

// ---- UI and CLI API ----

// info is public. With sign-in on it says only what is needed to sign in.
// The schema versions (this build's, and the database's) are included, so
// that an upgrade or its rollback can be checked; the database's is left
// out if it cannot be read.
func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"version": s.Version}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if schema, err := s.Engine.Schema(ctx); err == nil {
		out["schema"] = schema
	} else {
		out["schema"] = map[string]int{"code": schema.Code}
	}
	if s.Auth != nil {
		out["auth_mode"] = "oidc"
	} else {
		out["auth_mode"], out["actor"], out["projects"] = "none", s.Actor, len(s.projects())
	}
	writeJSON(w, http.StatusOK, out)
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
	// What the caller may do with the pipeline editor, so the UI can say
	// so. The server checks every request regardless.
	Editor editorRights `json:"editor"`
}

type editorRights struct {
	DraftsAllow  string `json:"drafts_allow"`  // admin, operator or none: who may start draft runs
	MayDraft     bool   `json:"may_draft"`     // the caller may start draft runs
	DraftSecrets bool   `json:"draft_secrets"` // steps of draft runs may read secrets
	MaySave      bool   `json:"may_save"`      // the project can be saved to, and the caller may
	MayForce     bool   `json:"may_force"`     // the caller may save a pipeline that has problems
}

func (s *Server) view(id *identity, p source.ProjectStatus) projectView {
	v := projectView{ProjectStatus: p}
	if runs, err := s.Engine.ListRuns(engine.RunFilter{Project: p.Name, Limit: 1}); err == nil && len(runs) > 0 {
		v.LastRun = runs[0]
	}
	rule := s.cur().Drafts.For(p.Name)
	v.Editor = editorRights{DraftsAllow: "none", MayDraft: s.authorize(id, auth.RunDraft, p.Name), DraftSecrets: rule.Secrets,
		MaySave: p.Push != nil && s.authorize(id, auth.Operate, p.Name), MayForce: s.authorize(id, auth.AdministerProject, p.Name)}
	if rule.Allow != auth.NoRole {
		v.Editor.DraftsAllow = rule.Allow.String()
	}
	return v
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	out := []projectView{}
	for _, p := range s.projects() {
		if !s.authorize(id, auth.Read, p.Name) {
			continue
		}
		out = append(out, s.view(id, p))
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
	writeJSON(w, http.StatusOK, s.view(identityFrom(r), p))
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
		// Libraries overrides the versions of libraries the pipeline uses:
		// library name → tag, branch or commit.
		Libraries map[string]string `json:"libraries"`
		// Inputs are values for the pipeline's inputs, by name; those not
		// given take their defaults.
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if !decode(w, r, &in) {
		return
	}
	given, ie := inputValues(in.Inputs)
	setAudit(r, "", "ref "+in.Ref+overridesDetail(in.Libraries)+inputsDetail(given))
	if ie != nil {
		writeInputErrors(w, ie)
		return
	}
	run, err := s.Projects.StartRunWith(r.Context(), source.StartRequest{Project: r.PathValue("name"), Ref: in.Ref,
		Actor: identityFrom(r).User, Libraries: in.Libraries, Inputs: given})
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, run.ID, fmt.Sprintf("run #%d of %s at %s%s%s", run.Number, nonEmpty(run.Branch, run.Tag), shortSHA(run.SHA), s.libraryDetail(run.ID),
		inputsDetail(given)))
	writeJSON(w, http.StatusCreated, run)
}

// overridesDetail describes requested library overrides for the audit
// trail, before anything is resolved.
func overridesDetail(libs map[string]string) string {
	if len(libs) == 0 {
		return ""
	}
	names := make([]string, 0, len(libs))
	for n := range libs {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + libs[n]
	}
	return ", library overrides " + strings.Join(parts, " ")
}

// libraryDetail describes, for the audit trail, the libraries a run's
// templates came from (with overrides and the commits they resolved to) and
// the templates it used. It is empty for a run that uses none.
func (s *Server) libraryDetail(runID string) string {
	d, err := s.Engine.GetRun(runID)
	if err != nil || d.Graph == nil || len(d.Graph.Libraries) == 0 {
		return ""
	}
	var libs, templates []string
	for _, l := range d.Graph.Libraries {
		v := l.Version
		if l.Override != "" {
			v += " overridden by " + l.Override
		}
		libs = append(libs, fmt.Sprintf("%s=%s@%s at %s", l.Name, l.Repo, v, l.Commit))
	}
	seen := map[string]bool{}
	for _, g := range d.Graph.Groups {
		u := g.Library + "/" + g.Template
		if !seen[u] {
			seen[u] = true
			templates = append(templates, u)
		}
	}
	return "; libraries " + strings.Join(libs, ", ") + "; templates " + strings.Join(templates, ", ")
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	only, err := s.visibleProjects(identityFrom(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	f := engine.RunFilter{Project: q.Get("project"), Status: q.Get("status"), Lane: q.Get("lane"), Limit: limit, OnlyProjects: only}
	if v := q.Get("before"); v != "" {
		// The next page: after the run "<created_at>.<id>", as the last run
		// of the previous page gives them.
		at, id, ok := strings.Cut(v, ".")
		ms, err := strconv.ParseInt(at, 10, 64)
		if !ok || err != nil || id == "" {
			writeErr(w, http.StatusBadRequest, "before is <created_at>.<id> of the last run of the previous page")
			return
		}
		f.Before = &engine.RunCursor{CreatedAt: ms, ID: id}
	}
	runs, err := s.Engine.ListRuns(f)
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
	d.Source = redactSource(d.Source)
	writeJSON(w, http.StatusOK, d)
}

// redactSource removes credentials from the addresses in a run's source.
func redactSource(src engine.Source) engine.Source {
	src.URL = redact.URLs(src.URL)
	if len(src.Libraries) > 0 {
		libs := make([]engine.LibrarySource, len(src.Libraries))
		for i, l := range src.Libraries {
			l.URL = redact.URLs(l.URL)
			libs[i] = l
		}
		src.Libraries = libs
	}
	return src
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.Engine.Events(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	// The first event carries the whole pipeline, and a draft run's text;
	// the UI shows those elsewhere (pipeline-file). The draft's hash stays.
	// Each step.leased event carries what the attempt was given, which the
	// step view shows (inputs).
	out := make([]engine.Event, len(evs))
	for i, ev := range evs {
		ev.Data.Pipeline = nil
		ev.Data.Inputs = nil
		if ev.Data.Draft != nil {
			ev.Data.Draft = &engine.Draft{Hash: ev.Data.Draft.Hash, Secrets: ev.Data.Draft.Secrets}
		}
		if ev.Data.Source != nil {
			src := redactSource(*ev.Data.Source)
			ev.Data.Source = &src
		}
		out[i] = ev
	}
	writeJSON(w, http.StatusOK, out)
}

// runPipelineFile returns the pipeline text a run ran: for a draft run, the
// text kept with the run, which is in no commit; otherwise the file in Git
// at the run's commit.
func (s *Server) runPipelineFile(w http.ResponseWriter, r *http.Request) {
	draft, isDraft, err := s.Engine.DraftText(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if isDraft {
		writeJSON(w, http.StatusOK, map[string]any{"text": draft.Text, "draft": true, "hash": draft.Hash})
		return
	}
	d, err := s.Engine.GetRun(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if o := d.Graph.Origin; o != nil {
		// The pipeline configured on the server, kept with the run.
		writeJSON(w, http.StatusOK, map[string]any{"text": o.Text, "draft": false, "origin": o.Kind})
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
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "draft": false})
}

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
	lines, att, err := s.Engine.QueryLogs(r.PathValue("id"), r.PathValue("step"),
		engine.LogQuery{Attempt: attempt, After: after, Limit: limit, Tail: q.Get("tail") == "1", Search: search})
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
		if err := s.Engine.Decide(r.PathValue("id"), r.PathValue("step"), approve, identityFrom(r).User, in.Comment); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Engine.Runners()
	if err != nil {
		s.fail(w, err)
		return
	}
	// Anyone who can read one project may list the runners, but what a
	// runner holds names other projects' runs and steps, and who enrolled
	// it and with which join token, and which projects it accepts, is for those
	// who manage runners.
	id := identityFrom(r)
	if !s.mayManageRunners(id) {
		for i := range rs {
			rs[i].Identity = nil
			rs[i].Accept = nil
			work := []engine.RunnerWork{}
			for _, wk := range rs[i].Work {
				if s.authorize(id, auth.Read, wk.Project) {
					work = append(work, wk)
				}
			}
			rs[i].Work = work
		}
	}
	writeJSON(w, http.StatusOK, rs)
}

// listLibraries lists the shared libraries pipelines may use, by name.
func (s *Server) listLibraries(w http.ResponseWriter, r *http.Request) {
	type lib struct {
		Name      string `json:"name"`
		LastFetch int64  `json:"last_fetch"`
	}
	out := []lib{}
	if s.Projects != nil {
		for _, l := range s.Projects.Libraries() {
			out = append(out, lib{Name: l.Name, LastFetch: l.LastFetch})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// libraryTemplates lists a library's templates at ?version= (default: its
// newest tag, else its default branch), with their parameters and outputs.
func (s *Server) libraryTemplates(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no libraries are configured")
		return
	}
	version := r.URL.Query().Get("version")
	if version != "" && !pipeline.ValidVersion(version) {
		writeErr(w, http.StatusBadRequest, "the version is not a tag, branch or commit id")
		return
	}
	out, err := s.Projects.LibraryTemplates(r.Context(), r.PathValue("library"), version)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// stream sends live changes as server-sent events. With ?run=ID it sends only
// that run's changes, including log lines; otherwise it sends run changes,
// for the projects the caller may see. With sign-in on, the caller's
// session or token is checked again every ping, and the stream ends when it
// is no longer valid.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	id := identityFrom(r)
	runID := r.URL.Query().Get("run")
	runProject := ""
	if runID != "" {
		p, err := s.Engine.RunProject(runID)
		if err == nil && !s.authorize(id, auth.Read, p) {
			err = fmt.Errorf("%w: run %s", engine.ErrNotFound, runID)
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		runProject = p
	}
	ctx, cancel := s.longLived(r)
	defer cancel()
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
	ping := time.NewTicker(s.StreamPing)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if s.Auth != nil {
				// Without recording use: an open page is not activity.
				again, err := s.identify(r, false)
				if err != nil || again == nil || (runID != "" && !s.authorize(again, auth.Read, runProject)) {
					return
				}
				id = again
			}
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case m, ok := <-ch:
			if !ok {
				return // fell behind; the browser reconnects and refetches
			}
			if runID == "" {
				sum, ok := m.Data.(*engine.RunSummary)
				if !ok || !s.authorize(id, auth.Read, sum.Project) {
					continue
				}
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
	fmt.Fprintln(w, "Launchway is running, but this build has no web UI. Build it with: make web")
}
