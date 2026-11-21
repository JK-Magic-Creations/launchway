package api

// Configuration as code (schema 48): the run reports of Ansible and plan
// steps, the drift per environment, and the built-in Terraform/OpenTofu
// state backend. The tools run in step containers; this file keeps and
// serves what they produce. Parsing is in internal/cac.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/cac/ansible"
	"github.com/jk-magic-creations/launchway/internal/cac/tfplan"
	"github.com/jk-magic-creations/launchway/internal/cac/tfstate"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// ConfigArtifact is the artifact name configuration steps keep their report
// files under.
const ConfigArtifact = "launchway-config"

// stateCredentialTTL bounds a state credential; it also dies with its run.
const stateCredentialTTL = 3 * time.Hour

func (s *Server) cacRoutes() []route {
	return []route{
		// The state backend protocol (Terraform and OpenTofu "http" backend).
		// It authenticates itself with HTTP basic credentials: a run's state
		// credential or a person's API token.
		{"GET /api/state/v1/{project}/{workspace}", public, "", s.stateGet},
		{"POST /api/state/v1/{project}/{workspace}", public, "", s.statePost},
		{"PUT /api/state/v1/{project}/{workspace}", public, "", s.statePost},
		{"DELETE /api/state/v1/{project}/{workspace}", public, "", s.stateDelete},
		{"LOCK /api/state/v1/{project}/{workspace}/lock", public, "", s.stateLock},
		{"UNLOCK /api/state/v1/{project}/{workspace}/lock", public, "", s.stateUnlock},
		{"POST /api/runner/v1/config/state-credential", runnerOnly, "", s.stateCredential},

		// People and agents.
		{"GET /api/v1/projects/{name}/state", projectRead, "", s.stateList},
		{"GET /api/v1/projects/{name}/state/{workspace}", projectRead, "", s.stateShow},
		{"GET /api/v1/projects/{name}/state/{workspace}/versions", projectRead, "", s.stateVersions},
		{"GET /api/v1/projects/{name}/state/{workspace}/pull", projectOperate, "state.pull", s.statePull},
		{"PUT /api/v1/projects/{name}/state/{workspace}/push", projectOperate, "state.push", s.statePush},
		{"POST /api/v1/projects/{name}/state/{workspace}/lock", projectOperate, "state.lock", s.stateManualLock},
		{"DELETE /api/v1/projects/{name}/state/{workspace}/lock", projectOperate, "state.unlock", s.stateForceUnlock},
		{"POST /api/v1/projects/{name}/state/{workspace}/rollback", projectOperate, "state.rollback", s.stateRollback},

		{"GET /api/v1/runs/{id}/config-reports", runRead, "", s.configReports},
		{"GET /api/v1/runs/{id}/config-reports/{step}", runRead, "", s.configReport},
		{"POST /api/v1/runs/{id}/config-reports/{step}/rerun-failed", runOperate, "config.rerun_failed", s.configRerunFailed},
		{"GET /api/v1/projects/{name}/configuration", projectRead, "", s.configurationOverview},
	}
}

// ---- state store ----

type engineStateDB struct{ db *engine.Database }

func (d engineStateDB) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return d.db.Exec(ctx, q, a...)
}
func (d engineStateDB) Query(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return d.db.Query(ctx, q, a...)
}
func (d engineStateDB) QueryRow(ctx context.Context, q string, a ...any) *sql.Row {
	return d.db.QueryRow(ctx, q, a...)
}
func (d engineStateDB) BeginTx(ctx context.Context) (tfstate.Tx, error) {
	t, err := d.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// stateStore is the state store of this server, sealing under its keyring.
func (s *Server) stateStore() (*tfstate.Store, error) {
	svc, err := s.secretStore()
	if err != nil {
		return nil, err
	}
	return &tfstate.Store{DB: engineStateDB{s.Engine.Database()}, Sealer: svc.Keys(), Now: s.Now}, nil
}

func (s *Server) stateTokens(ctx context.Context) (tfstate.Tokens, error) {
	key, err := s.sharedSecret(ctx, "config_state_credential_key")
	if err != nil {
		return tfstate.Tokens{}, err
	}
	return tfstate.Tokens{Key: []byte(key), Now: s.Now}, nil
}

// stateAuth authenticates the backend protocol.
type stateAuth struct{ s *Server }

func (a stateAuth) Authenticate(r *http.Request, project, workspace string, write bool) (tfstate.Actor, error) {
	_, pass, ok := r.BasicAuth()
	if !ok || pass == "" {
		return tfstate.Actor{}, tfstate.ErrUnauthorized
	}
	if strings.HasPrefix(pass, tfstate.TokenPrefix) {
		tk, err := a.s.stateTokens(r.Context())
		if err != nil {
			return tfstate.Actor{}, err
		}
		c, err := tk.Verify(pass)
		if err != nil {
			a.s.stateDenied(r, project, workspace, "an invalid or expired state credential")
			return tfstate.Actor{}, tfstate.ErrUnauthorized
		}
		if c.Project != project || c.Workspace != workspace || (write && c.ReadOnly) {
			a.s.stateDenied(r, project, workspace, "a state credential used outside what it was made for")
			return tfstate.Actor{}, tfstate.ErrForbidden
		}
		// The credential belongs to a step: once the run has ended it is no
		// longer good for anything.
		sum, err := a.s.Engine.Summary(c.RunID)
		if err != nil || sum.Project != project || (sum.Status != engine.RunRunning && sum.Status != engine.RunQueued) {
			a.s.stateDenied(r, project, workspace, "a state credential of a run that has ended")
			return tfstate.Actor{}, tfstate.ErrForbidden
		}
		return tfstate.Actor{Name: "run:" + c.RunID + "/" + c.Step, RunID: c.RunID}, nil
	}
	if strings.HasPrefix(pass, tokenPrefix) {
		id, err := a.s.tokenIdentity(r, pass, true)
		if err != nil || id == nil {
			return tfstate.Actor{}, tfstate.ErrUnauthorized
		}
		act := auth.Read
		if write {
			act = auth.Operate
		}
		if !a.s.authorize(id, act, project) {
			a.s.stateDenied(r, project, workspace, "a token without the role this needs")
			return tfstate.Actor{}, tfstate.ErrForbidden
		}
		return tfstate.Actor{Name: id.User}, nil
	}
	return tfstate.Actor{}, tfstate.ErrUnauthorized
}

func (s *Server) stateDenied(r *http.Request, project, workspace, why string) {
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "state-client", Source: s.clientAddr(r), RequestID: reqID(r),
		Action: "state.denied", Project: project, Target: workspace, Outcome: "denied", Detail: why})
}

func (s *Server) stateHandler() (*tfstate.Handler, error) {
	st, err := s.stateStore()
	if err != nil {
		return nil, err
	}
	return &tfstate.Handler{Store: st, Auth: stateAuth{s}, Log: func(m string, err error) { s.Log.Error(m, "err", err) }, Audit: func(r *http.Request, action, project, ws string, who tfstate.Actor, detail string) {
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: who.Name, Source: s.clientAddr(r), RequestID: reqID(r),
			Action: action, Project: project, Target: ws, Outcome: "ok", Detail: detail})
	}}, nil
}

func (s *Server) serveState(w http.ResponseWriter, r *http.Request, lock bool) {
	h, err := s.stateHandler()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "the state store is not available: "+err.Error())
		return
	}
	if lock && r.Method == "LOCK" {
		s.releaseEndedLock(r, h.Store, r.PathValue("project"), r.PathValue("workspace"))
	}
	h.Serve(w, r, r.PathValue("project"), r.PathValue("workspace"), lock)
}

// releaseEndedLock frees a lock whose run has ended. Terraform has no lock
// heartbeat, so a crashed or cancelled runner would otherwise leave the
// workspace locked until someone force-unlocked it.
func (s *Server) releaseEndedLock(r *http.Request, st *tfstate.Store, project, workspace string) {
	ws, err := st.Workspace(r.Context(), project, workspace)
	if err != nil || ws.Locked == nil || ws.Locked.RunID == "" {
		return
	}
	sum, err := s.Engine.Summary(ws.Locked.RunID)
	if err == nil && (sum.Status == engine.RunRunning || sum.Status == engine.RunQueued) {
		return
	}
	if _, err := st.ForceUnlock(r.Context(), project, workspace); err == nil {
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "launchway", Source: s.clientAddr(r), RequestID: reqID(r),
			Action: "state.lock_released", Project: project, Target: workspace, Outcome: "ok", Detail: "the run that held lock " + ws.Locked.ID + " has ended"})
	}
}

func (s *Server) stateGet(w http.ResponseWriter, r *http.Request)    { s.serveState(w, r, false) }
func (s *Server) statePost(w http.ResponseWriter, r *http.Request)   { s.serveState(w, r, false) }
func (s *Server) stateDelete(w http.ResponseWriter, r *http.Request) { s.serveState(w, r, false) }
func (s *Server) stateLock(w http.ResponseWriter, r *http.Request)   { s.serveState(w, r, true) }
func (s *Server) stateUnlock(w http.ResponseWriter, r *http.Request) { s.serveState(w, r, true) }

// stateCredential gives a running step the address and credential of one
// workspace's state: bound to the lease's run and step, to the lease's
// project and to the one workspace asked for.
func (s *Server) stateCredential(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID   string `json:"lease_id"`
		Workspace string `json:"workspace"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	if in.Workspace == "" {
		in.Workspace = "default"
	}
	if !tfstate.ValidWorkspace(in.Workspace) {
		writeErr(w, http.StatusBadRequest, "a workspace name is lower case letters, digits, - and _")
		return
	}
	runID, step, _, err := s.Engine.LeaseStep(r.Context(), in.LeaseID)
	if err != nil {
		s.fail(w, err)
		return
	}
	sum, err := s.Engine.Summary(runID)
	if err != nil {
		s.fail(w, err)
		return
	}
	svc, err := s.secretStore()
	if err != nil || !svc.Keys().Enabled() {
		writeErr(w, http.StatusConflict, tfstate.ErrNoKey.Error())
		return
	}
	tk, err := s.stateTokens(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	// A pull request or a draft is code nobody reviewed: it may read state
	// to plan, never write it.
	ro := sum.Event == "pull_request" || sum.Draft
	tok := tk.Mint(tfstate.Claims{Project: sum.Project, Workspace: in.Workspace, RunID: runID, Step: step, ReadOnly: ro}, stateCredentialTTL)
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + nonEmpty(callerID(r), "shared"), Source: s.clientAddr(r), RequestID: reqID(r),
		Action: "state.credential", Project: sum.Project, Target: runID + "/" + step, Outcome: "ok", Detail: fmt.Sprintf("workspace %s, read only %v", in.Workspace, ro)})
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "workspace": in.Workspace, "read_only": ro, "path": "/api/state/v1/" + sum.Project + "/" + in.Workspace})
}

// ---- state for people ----

func (s *Server) withState(w http.ResponseWriter) *tfstate.Store {
	st, err := s.stateStore()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "the state store is not available: "+err.Error())
		return nil
	}
	return st
}

func (s *Server) failState(w http.ResponseWriter, err error) {
	var le *tfstate.LockedError
	switch {
	case errors.As(err, &le):
		writeJSON(w, http.StatusLocked, map[string]any{"error": "the state is locked", "lock": le.Held})
	case errors.Is(err, tfstate.ErrNotFound):
		writeErr(w, http.StatusNotFound, "there is no such state")
	case errors.Is(err, tfstate.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, tfstate.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, tfstate.ErrNoKey):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		s.fail(w, err)
	}
}

func (s *Server) stateList(w http.ResponseWriter, r *http.Request) {
	st := s.withState(w)
	if st == nil {
		return
	}
	l, err := st.Workspaces(r.Context(), r.PathValue("name"))
	if err != nil {
		s.failState(w, err)
		return
	}
	if l == nil {
		l = []tfstate.Workspace{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": l, "sealed": s.stateSealed()})
}

func (s *Server) stateSealed() bool {
	svc, err := s.secretStore()
	return err == nil && svc.Keys().Enabled()
}

// stateShow describes a state without a single value: resources, output
// names, the version. The raw document is statePull.
func (s *Server) stateShow(w http.ResponseWriter, r *http.Request) {
	st := s.withState(w)
	if st == nil {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("version"))
	ws, err := st.Workspace(r.Context(), r.PathValue("name"), r.PathValue("workspace"))
	if err != nil {
		s.failState(w, err)
		return
	}
	sum, err := st.Show(r.Context(), r.PathValue("name"), r.PathValue("workspace"), n)
	if err != nil {
		s.failState(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": ws, "state": sum})
}

func (s *Server) stateVersions(w http.ResponseWriter, r *http.Request) {
	st := s.withState(w)
	if st == nil {
		return
	}
	v, err := st.Versions(r.Context(), r.PathValue("name"), r.PathValue("workspace"))
	if err != nil {
		s.failState(w, err)
		return
	}
	if v == nil {
		v = []tfstate.Version{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": v})
}

// statePull returns the raw state: the one place it leaves in clear. It
// needs a reason, which is audited with who asked.
func (s *Server) statePull(w http.ResponseWriter, r *http.Request) {
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if reason == "" {
		writeErr(w, http.StatusBadRequest, "state holds secrets in clear text; say why you need it: ?reason=...")
		return
	}
	st := s.withState(w)
	if st == nil {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("version"))
	v, body, err := st.Pull(r.Context(), r.PathValue("name"), r.PathValue("workspace"), n)
	if err != nil {
		s.failState(w, err)
		return
	}
	setAudit(r, r.PathValue("workspace"), fmt.Sprintf("version %d (serial %d), because: %s", v.Version, v.Serial, clip(reason, 200)))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Server) statePush(w http.ResponseWriter, r *http.Request) {
	st := s.withState(w)
	if st == nil {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tfstate.MaxStateBytes))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "reading the state: "+err.Error())
		return
	}
	adopt := r.URL.Query().Get("adopt") == "true"
	v, err := st.Put(r.Context(), r.PathValue("name"), r.PathValue("workspace"), "", body, tfstate.Actor{Name: identityFrom(r).User}, "push", adopt)
	if err != nil {
		s.failState(w, err)
		return
	}
	setAudit(r, r.PathValue("workspace"), fmt.Sprintf("pushed as version %d (serial %d, %d resources)%s", v.Version, v.Serial, v.Resources, map[bool]string{true: ", adopting its lineage"}[adopt]))
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) stateManualLock(w http.ResponseWriter, r *http.Request) {
	st := s.withState(w)
	if st == nil {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	info, _ := json.Marshal(map[string]string{"ID": hex.EncodeToString(id), "Operation": "manual", "Who": identityFrom(r).User, "Info": in.Reason, "Created": s.Now().UTC().Format(time.RFC3339)})
	l, err := st.Lock(r.Context(), r.PathValue("name"), r.PathValue("workspace"), info, tfstate.Actor{Name: identityFrom(r).User})
	if err != nil {
		s.failState(w, err)
		return
	}
	setAudit(r, r.PathValue("workspace"), "locked by hand: "+clip(in.Reason, 200))
	writeJSON(w, http.StatusCreated, l)
}

// stateForceUnlock releases a lock whoever holds it. A lock held by a run
// that is still going is refused: cancel the run first. A reason is required.
func (s *Server) stateForceUnlock(w http.ResponseWriter, r *http.Request) {
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if reason == "" {
		writeErr(w, http.StatusBadRequest, "say why: ?reason=...; the holder is told")
		return
	}
	st := s.withState(w)
	if st == nil {
		return
	}
	ws, err := st.Workspace(r.Context(), r.PathValue("name"), r.PathValue("workspace"))
	if err != nil {
		s.failState(w, err)
		return
	}
	if ws.Locked == nil {
		writeJSON(w, http.StatusOK, map[string]any{"unlocked": false, "detail": "it was not locked"})
		return
	}
	if ws.Locked.RunID != "" {
		if sum, err := s.Engine.Summary(ws.Locked.RunID); err == nil && (sum.Status == engine.RunRunning || sum.Status == engine.RunQueued) {
			writeErr(w, http.StatusConflict, fmt.Sprintf("run #%d still holds this lock and is going; cancel it first (it releases the lock when it ends)", sum.Number))
			return
		}
	}
	held, err := st.ForceUnlock(r.Context(), r.PathValue("name"), r.PathValue("workspace"))
	if err != nil {
		s.failState(w, err)
		return
	}
	setAudit(r, r.PathValue("workspace"), fmt.Sprintf("released lock %s held by %s since %d, because: %s", held.ID, held.Actor, held.CreatedAt, clip(reason, 200)))
	writeJSON(w, http.StatusOK, map[string]any{"unlocked": true, "was": held})
}

func (s *Server) stateRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int    `json:"version"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || strings.TrimSpace(in.Reason) == "" {
		writeErr(w, http.StatusBadRequest, `give the version to restore and why: {"version": 3, "reason": "..."}`)
		return
	}
	st := s.withState(w)
	if st == nil {
		return
	}
	v, err := st.Rollback(r.Context(), r.PathValue("name"), r.PathValue("workspace"), in.Version, tfstate.Actor{Name: identityFrom(r).User})
	if err != nil {
		s.failState(w, err)
		return
	}
	setAudit(r, r.PathValue("workspace"), fmt.Sprintf("restored version %d as version %d (serial %d), because: %s", in.Version, v.Version, v.Serial, clip(in.Reason, 200)))
	writeJSON(w, http.StatusOK, map[string]any{"version": v,
		"note": "this changed the stored state only; no real resource changed. The next plan shows the difference."})
}

// ---- reports ----

// syncConfigReports reads the report files configuration steps kept as the
// artifact launchway-config and stores their parsed, redacted form, once per
// step attempt. It is cheap when there is nothing to do, so listing a run's
// reports calls it first.
func (s *Server) syncConfigReports(ctx context.Context, runID string) error {
	arts, err := s.Engine.RunArtifacts(runID)
	if err != nil {
		return err
	}
	var sum *engine.RunSummary
	for _, a := range arts.Artifacts {
		if a.Name != ConfigArtifact || a.Status != "kept" {
			continue
		}
		for _, f := range a.Files {
			base := path.Base(f.Path)
			kind := ""
			switch base {
			case "report.json":
				kind = engine.CacAnsible
			case "plan.json":
				kind = engine.CacPlan
			default:
				continue
			}
			done, err := s.Engine.HasCacReport(ctx, runID, a.Step, a.Attempt, kind)
			if err != nil {
				return err
			}
			if done {
				continue
			}
			if sum == nil {
				if sum, err = s.Engine.Summary(runID); err != nil {
					return err
				}
			}
			body, err := s.readArtifactFile(ctx, runID, a.ID, f.Path)
			if err != nil {
				return err
			}
			rep := engine.CacReport{RunID: runID, Step: a.Step, Attempt: a.Attempt, Project: sum.Project, Commit: sum.SHA, Kind: kind}
			meta := s.readSidecar(ctx, runID, a, f.Path)
			if rep.Environment = meta["environment"]; rep.Environment == "" {
				rep.Environment = ""
			}
			switch kind {
			case engine.CacAnsible:
				p, err := ansible.Parse(body)
				if err != nil {
					rep.Status, rep.Recap = "unreadable", err.Error()
					rep.Summary, rep.Document = "{}", "{}"
					break
				}
				rep.Check, rep.Status, rep.Recap = p.Check, outcomeOf(p), p.Summary.Recap
				if p.Environment != "" {
					rep.Environment = p.Environment
				}
				sm, _ := json.Marshal(map[string]any{"summary": p.Summary, "failure": p.Failure, "failed_hosts": p.FailedHosts(), "drift": driftOf(p)})
				doc, _ := json.Marshal(p)
				rep.Summary, rep.Document = string(sm), string(doc)
			case engine.CacPlan:
				p, err := tfplan.Parse(body)
				if err != nil {
					rep.Status, rep.Recap = "unreadable", err.Error()
					rep.Summary, rep.Document = "{}", "{}"
					break
				}
				rep.Status, rep.Recap = "planned", p.Summary
				if meta["state_backend"] == "none" {
					rep.Recap += " (made without state: every resource looks new)"
				}
				if p.Risk.Level == "none" {
					rep.Status = "no-changes"
				}
				rep.Check = true // a plan changes nothing
				sm, _ := json.Marshal(map[string]any{"counts": p.Counts, "risk": p.Risk, "workspace": meta["workspace"], "tool": meta["tool"],
					"state_serial": meta["state_serial"], "exit_code": meta["exit_code"]})
				doc, _ := json.Marshal(map[string]any{"plan": p, "meta": meta})
				rep.Summary, rep.Document = string(sm), string(doc)
			}
			if _, err := s.Engine.SaveCacReport(ctx, rep); err != nil {
				return err
			}
		}
	}
	return nil
}

func outcomeOf(p *ansible.Report) string {
	switch {
	case p.Summary.Failed > 0:
		return "failed"
	case p.Summary.Unreachable > 0:
		return "unreachable"
	case p.Summary.Changed > 0 && p.Check:
		return "drifted"
	case p.Summary.Changed > 0:
		return "changed"
	}
	return "ok"
}

func driftOf(p *ansible.Report) any {
	if d, ok := p.Drift(); ok {
		return d
	}
	return nil
}

const maxConfigFile = 80 << 20

func (s *Server) readArtifactFile(ctx context.Context, runID, artifactID, p string) ([]byte, error) {
	f, err := s.Engine.OpenArtifactFile(ctx, runID, artifactID, p, 0)
	if err != nil {
		return nil, err
	}
	defer f.Body.Close()
	b, err := io.ReadAll(io.LimitReader(f.Body, maxConfigFile))
	return b, err
}

// readSidecar reads meta.json beside a report file, if the step wrote one.
func (s *Server) readSidecar(ctx context.Context, runID string, a *engine.ArtifactView, reportPath string) map[string]string {
	out := map[string]string{}
	want := path.Join(path.Dir(reportPath), "meta.json")
	for _, f := range a.Files {
		if f.Path == want && f.Size < 64<<10 {
			if b, err := s.readArtifactFile(ctx, runID, a.ID, f.Path); err == nil {
				var raw map[string]any
				if json.Unmarshal(b, &raw) == nil {
					for k, v := range raw {
						out[k] = fmt.Sprint(v)
					}
				}
			}
		}
	}
	return out
}

func (s *Server) configReports(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.syncConfigReports(r.Context(), id); err != nil {
		s.Log.Warn("reading configuration reports", "run", id, "err", err)
	}
	l, err := s.Engine.CacReportsOfRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": l})
}

func (s *Server) configReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.syncConfigReports(r.Context(), id)
	rep, err := s.Engine.CacReportOf(r.Context(), id, r.PathValue("step"), r.URL.Query().Get("kind"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report": rep, "summary": json.RawMessage(rep.Summary), "document": json.RawMessage(rep.Document)})
}

// configRerunFailed starts a run of the same project and branch that limits
// the Ansible step to the hosts that failed or could not be reached. The
// pipeline names the input it takes the limit from ({"input": "limit"}).
func (s *Server) configRerunFailed(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	id, step := r.PathValue("id"), r.PathValue("step")
	_ = s.syncConfigReports(r.Context(), id)
	rep, err := s.Engine.CacReportOf(r.Context(), id, step, engine.CacAnsible)
	if err != nil {
		s.fail(w, err)
		return
	}
	var doc ansible.Report
	if err := json.Unmarshal([]byte(rep.Document), &doc); err != nil {
		writeErr(w, http.StatusConflict, "this report cannot be read, so there is nothing to retry from")
		return
	}
	failed := doc.FailedHosts()
	if len(failed) == 0 {
		writeErr(w, http.StatusConflict, "no host failed or was unreachable in this run: nothing to run again")
		return
	}
	sum, err := s.Engine.Summary(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	ref := nonEmpty(sum.Branch, sum.Tag)
	inputs := map[string]string{"limit": strings.Join(failed, ",")}
	if d, err := s.Engine.GetRun(id); err == nil && d.TriggerDetail != nil {
		for k, v := range d.TriggerDetail.Inputs {
			if _, set := inputs[k]; !set {
				inputs[k] = v
			}
		}
	}
	run, err := s.Projects.StartRunWith(r.Context(), source.StartRequest{Project: sum.Project, Ref: ref, Actor: identityFrom(r).User, Inputs: inputs})
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, run.ID, fmt.Sprintf("run #%d of %s on the %d host(s) that failed in run #%d: %s", run.Number, nonEmpty(run.Branch, run.Tag), len(failed), sum.Number, strings.Join(failed, ", ")))
	writeJSON(w, http.StatusCreated, map[string]any{"run": run, "hosts": failed})
}

// configurationOverview is what the Configuration page of a project shows:
// the latest Ansible runs and plans per environment, drift from check-mode
// runs, and the state workspaces.
func (s *Server) configurationOverview(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	ctx := r.Context()
	if runs, err := s.Engine.ListRuns(engine.RunFilter{Project: project, Limit: 8}); err == nil {
		for _, run := range runs {
			_ = s.syncConfigReports(ctx, run.ID)
		}
	}
	type env struct {
		Name    string            `json:"name"`
		Ansible *engine.CacReport `json:"ansible,omitempty"`
		Drift   *engine.CacReport `json:"drift,omitempty"`
		Plan    *engine.CacReport `json:"plan,omitempty"`
	}
	envs := map[string]*env{}
	order := []string{}
	get := func(n string) *env {
		if e, ok := envs[n]; ok {
			return e
		}
		e := &env{Name: n}
		envs[n] = e
		order = append(order, n)
		return e
	}
	lat, err := s.Engine.LatestCacReports(ctx, project, "", nil, 60)
	if err != nil {
		s.fail(w, err)
		return
	}
	for i := range lat {
		rep := lat[i]
		e := get(rep.Environment)
		switch {
		case rep.Kind == engine.CacPlan && e.Plan == nil:
			e.Plan = &rep
		case rep.Kind == engine.CacAnsible && rep.Check && e.Drift == nil:
			e.Drift = &rep
		case rep.Kind == engine.CacAnsible && !rep.Check && e.Ansible == nil:
			e.Ansible = &rep
		}
	}
	out := make([]*env, 0, len(order))
	for _, n := range order {
		out = append(out, envs[n])
	}
	var ws []tfstate.Workspace
	if st, err := s.stateStore(); err == nil {
		ws, _ = st.Workspaces(ctx, project)
	}
	if ws == nil {
		ws = []tfstate.Workspace{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": out, "state": ws, "sealed": s.stateSealed()})
}

// declaresConfig says whether a run has a step that keeps configuration reports.
func declaresConfig(d *engine.RunDetail) bool {
	if d.Graph == nil {
		return false
	}
	for _, st := range d.Graph.Steps {
		for _, a := range st.Artifacts {
			if a.Name == ConfigArtifact {
				return true
			}
		}
	}
	return false
}

// addConfigRecap puts the recap of a run's configuration reports on its
// launch line: "12 hosts: 9 changed, 3 ok, 0 failed". Nothing is added to a
// run that has none.
func (s *Server) addConfigRecap(ctx context.Context, d *engine.RunDetail, line *LaunchLine) {
	if !declaresConfig(d) {
		return
	}
	if d.Status.Terminal() {
		_ = s.syncConfigReports(ctx, d.ID)
	}
	reps, err := s.Engine.CacReportsOfRun(ctx, d.ID)
	if err != nil || len(reps) == 0 {
		return
	}
	// The newest report of each step wins; the line shows the last one, and
	// the worst state of all.
	state := stationDone
	var parts []string
	step := ""
	for _, r := range reps {
		switch r.Status {
		case "failed", "unreachable", "unreadable":
			state = stationFailed
		case "drifted":
			if state != stationFailed {
				state = stationWaiting
			}
		}
		parts = append(parts, r.Recap)
		step = r.Step
	}
	line.Stations = append(line.Stations, Station{Key: "configuration", Label: "Configuration", State: state, Detail: strings.Join(parts, "; "), Step: step})
}
