package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/deploy/inspect"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Looking into a delivery: the tree of what it made, manifests, logs, and
// a few controls on its workload (see engine/inspect.go for how a request
// reaches the cluster). Who may do what:
//
//	tree, manifests   a viewer of the project (names and keys of Secrets,
//	                  redacted ConfigMap data and manifests)
//	logs, controls    an operator of the project, from a binding that covers
//	                  the deployment's environment (a protected environment
//	                  needs the operator role there)
//
// Every request is audited with who asked and for what object. The tree
// view is recorded once per person and deployment every few minutes, not
// on every refresh.

const (
	treeMaxAge     = 3 * time.Second
	askTimeout     = 20 * time.Second
	manifestMaxAge = 5 * time.Second
	viewAuditEvery = 5 * time.Minute
)

// TreeView is what the tree route returns.
type TreeView struct {
	Tree    *inspect.Tree     `json:"tree"`
	Actions []inspect.Allowed `json:"actions"`
	// Deployment says what the page is about.
	Run         string `json:"run"`
	Deployment  string `json:"deployment"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Application string `json:"application"`
	Target      string `json:"target"`
	Closed      bool   `json:"closed"`
	Protected   bool   `json:"protected"`
	// CanLogs and CanControl say what this person may do; Why says what
	// they would need.
	CanLogs    bool   `json:"can_logs"`
	CanControl bool   `json:"can_control"`
	Why        string `json:"why,omitempty"`
}

var viewSeen struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// auditView records that someone looked at a deployment's objects, at most
// once per viewAuditEvery.
func (s *Server) auditView(r *http.Request, user, project, target string) {
	key := user + "|" + target
	viewSeen.mu.Lock()
	if viewSeen.m == nil || len(viewSeen.m) > 5000 {
		viewSeen.m = map[string]time.Time{}
	}
	last, ok := viewSeen.m[key]
	now := s.Now()
	if ok && now.Sub(last) < viewAuditEvery {
		viewSeen.mu.Unlock()
		return
	}
	viewSeen.m[key] = now
	viewSeen.mu.Unlock()
	s.record(&engine.AuditEntry{At: now.UnixMilli(), User: user, Source: s.clientAddr(r), RequestID: reqID(r), Action: "deployment.inspect",
		Project: project, Target: target, Outcome: "ok"})
}

// inspectAsk asks a deploy runner and waits for the answer.
func (s *Server) inspectAsk(ctx context.Context, t engine.InspectTarget, kind string, params any, actor string, maxAge time.Duration) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if maxAge > 0 {
		out, openID, err := s.Engine.RecentInspect(t, kind, raw, maxAge)
		if err != nil {
			return nil, err
		}
		if out != nil {
			return out.Result, nil
		}
		if openID != "" {
			o, err := s.Engine.AwaitInspect(ctx, openID, askTimeout)
			if err != nil {
				return nil, err
			}
			return inspectResult(o)
		}
	}
	id, err := s.Engine.EnqueueInspect(engine.InspectRequest{Target: t, Kind: kind, Params: raw, Actor: actor})
	if err != nil {
		return nil, err
	}
	o, err := s.Engine.AwaitInspect(ctx, id, askTimeout)
	if err != nil {
		return nil, err
	}
	return inspectResult(o)
}

// errRunner is a failure on the deploy runner's side, with words for the page.
type errRunner struct {
	msg      string
	timedOut bool
}

func (e *errRunner) Error() string { return e.msg }

func inspectResult(o *engine.InspectOutcome) (json.RawMessage, error) {
	switch o.State {
	case engine.InspectDone:
		return o.Result, nil
	case engine.InspectCanceled:
		return nil, &errRunner{msg: o.Error, timedOut: true}
	}
	return nil, &errRunner{msg: o.Error}
}

func (s *Server) failInspect(w http.ResponseWriter, err error) {
	var er *errRunner
	if errors.As(err, &er) {
		code := http.StatusBadGateway
		if er.timedOut {
			code = http.StatusGatewayTimeout
		}
		writeErr(w, code, er.msg)
		return
	}
	s.fail(w, err)
}

// inspectTarget finds the deployment and checks the caller's rights: logs
// and controls need the operator role on the environment as well.
func (s *Server) inspectTarget(r *http.Request) (engine.InspectTarget, error) {
	return s.Engine.InspectTargetOf(r.PathValue("id"), r.PathValue("dep"))
}

func (s *Server) canOperate(id *identity, t engine.InspectTarget) (bool, string) {
	if !s.authorize(id, auth.Operate, t.Project) {
		return false, fmt.Sprintf("you need the operator role on %s to do this", t.Project)
	}
	if msg := s.envRefusal(id, t.Project, t.Environment); msg != "" {
		return false, msg
	}
	return true, ""
}

// inspectTreeH returns the live tree of a deployment's objects.
func (s *Server) inspectTreeH(w http.ResponseWriter, r *http.Request) {
	t, err := s.inspectTarget(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	id := identityFrom(r)
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout+5*time.Second)
	defer cancel()
	raw, err := s.inspectAsk(ctx, t, engine.InspectTree, struct{}{}, id.User, treeMaxAge)
	if err != nil {
		s.failInspect(w, err)
		return
	}
	var ans argocd.Answer
	if err := json.Unmarshal(raw, &ans); err != nil || ans.Tree == nil {
		writeErr(w, http.StatusBadGateway, "the deploy runner's answer could not be read")
		return
	}
	v := TreeView{Tree: ans.Tree, Actions: ans.Actions, Run: t.RunID, Deployment: t.Deployment, Project: t.Project, Environment: t.Environment,
		Application: t.Application, Target: t.Target, Closed: t.Closed}
	for _, env := range s.Engine.Environments() {
		if env.Name == t.Environment {
			v.Protected = env.Protected
		}
	}
	v.CanLogs, v.Why = s.canOperate(id, t)
	v.CanControl = v.CanLogs
	if !v.CanControl {
		for i := range v.Actions {
			v.Actions[i].OK, v.Actions[i].Why = false, v.Why
		}
	}
	s.auditView(r, id.User, t.Project, t.RunID+"/"+t.Deployment)
	writeJSON(w, http.StatusOK, v)
}

// inspectManifestH returns one object's manifest, redacted.
func (s *Server) inspectManifestH(w http.ResponseWriter, r *http.Request) {
	t, err := s.inspectTarget(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	obj := r.URL.Query().Get("object")
	setAudit(r, t.RunID+"/"+t.Deployment+"/"+obj, "")
	if obj == "" {
		writeErr(w, http.StatusBadRequest, "say which object: ?object=Kind/namespace/name")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout+5*time.Second)
	defer cancel()
	raw, err := s.inspectAsk(ctx, t, engine.InspectManifest, argocd.ManifestParams{Object: obj}, identityFrom(r).User, manifestMaxAge)
	if err != nil {
		s.failInspect(w, err)
		return
	}
	var ans argocd.Answer
	if err := json.Unmarshal(raw, &ans); err != nil {
		writeErr(w, http.StatusBadGateway, "the deploy runner's answer could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": obj, "yaml": ans.Text})
}

func logParams(r *http.Request) argocd.LogParams {
	q := r.URL.Query()
	p := argocd.LogParams{Pod: q.Get("pod"), Container: q.Get("container"), ReplicaSet: q.Get("replica_set"), Role: q.Get("role"), Job: q.Get("job"),
		Previous: q.Get("previous") == "true"}
	p.Tail, _ = strconv.Atoi(q.Get("tail"))
	p.SinceSeconds, _ = strconv.Atoi(q.Get("since_seconds"))
	p.Tail = min(max(p.Tail, 0), inspect.MaxTail)
	return p
}

func logTargetName(p argocd.LogParams) string {
	for _, v := range []string{p.Pod, p.ReplicaSet, p.Job, p.Role} {
		if v != "" {
			return v
		}
	}
	return ""
}

// inspectLogsH returns the tail of the logs of a pod, a ReplicaSet, a
// role (stable, canary, ...) or a Job.
func (s *Server) inspectLogsH(w http.ResponseWriter, r *http.Request) {
	t, err := s.inspectTarget(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	p := logParams(r)
	setAudit(r, t.RunID+"/"+t.Deployment+"/"+logTargetName(p), "read logs")
	if ok, why := s.canOperate(identityFrom(r), t); !ok {
		writeErr(w, http.StatusForbidden, why)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout+5*time.Second)
	defer cancel()
	raw, err := s.inspectAsk(ctx, t, engine.InspectLogs, p, identityFrom(r).User, 0)
	if err != nil {
		s.failInspect(w, err)
		return
	}
	var ans argocd.Answer
	if err := json.Unmarshal(raw, &ans); err != nil {
		writeErr(w, http.StatusBadGateway, "the deploy runner's answer could not be read")
		return
	}
	if ans.Lines == nil {
		ans.Lines = []inspect.LogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": ans.Lines, "notes": ans.Notes})
}

// inspectLogStreamH follows logs as server-sent events: lines (a JSON list
// of {t, src, line}) as they arrive, and an end event.
func (s *Server) inspectLogStreamH(w http.ResponseWriter, r *http.Request) {
	t, err := s.inspectTarget(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	p := logParams(r)
	setAudit(r, t.RunID+"/"+t.Deployment+"/"+logTargetName(p), "followed logs")
	id := identityFrom(r)
	if ok, why := s.canOperate(id, t); !ok {
		writeErr(w, http.StatusForbidden, why)
		return
	}
	rc := http.NewResponseController(w)
	raw, _ := json.Marshal(p)
	reqID, err := s.Engine.EnqueueInspect(engine.InspectRequest{Target: t, Kind: engine.InspectLogs, Params: raw, Actor: id.User, Follow: true})
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx, cancel := s.longLived(r)
	defer cancel()
	defer s.Engine.CancelInspect(reqID, "the viewer left")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 5000\n: connected\n\n")
	_ = rc.Flush()
	var after int64
	poll := time.NewTicker(300 * time.Millisecond)
	defer poll.Stop()
	deadline := time.After(inspect.MaxFollow)
	lastPing := time.Now()
	for {
		chunks, state, why, result, err := s.Engine.InspectChunks(reqID, after)
		if err != nil {
			return
		}
		for _, c := range chunks {
			after = c.Seq
			fmt.Fprintf(w, "event: lines\ndata: %s\n\n", c.Body)
		}
		if len(chunks) > 0 {
			_ = rc.Flush()
		}
		if len(chunks) == 0 && (state == engine.InspectDone || state == engine.InspectFailed || state == engine.InspectCanceled) {
			var notes []string
			var ans argocd.Answer
			if json.Unmarshal([]byte(result), &ans) == nil {
				notes = ans.Notes
			}
			end, _ := json.Marshal(map[string]any{"state": state, "error": why, "notes": notes})
			fmt.Fprintf(w, "event: end\ndata: %s\n\n", end)
			_ = rc.Flush()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			fmt.Fprint(w, "event: end\ndata: {\"state\":\"done\",\"notes\":[\"the stream reached its time limit; open it again to carry on\"]}\n\n")
			_ = rc.Flush()
			return
		case <-poll.C:
		}
		if time.Since(lastPing) > s.StreamPing && s.StreamPing > 0 || time.Since(lastPing) > 15*time.Second {
			lastPing = time.Now()
			if s.Auth != nil {
				again, err := s.identify(r, false)
				if err != nil || again == nil {
					return
				}
				if ok, _ := s.canOperate(again, t); !ok {
					return
				}
			}
			fmt.Fprint(w, ": ping\n\n")
			_ = rc.Flush()
		}
	}
}

// controlBody is a control request.
type controlBody struct {
	Action   string `json:"action"`
	Object   string `json:"object"`
	Replicas int    `json:"replicas"`
	Reason   string `json:"reason"`
}

// inspectControlH pauses, resumes, restarts or scales a workload of the
// delivery. The reason is kept in the Logbook.
func (s *Server) inspectControlH(w http.ResponseWriter, r *http.Request) {
	var in controlBody
	if !decode(w, r, &in) {
		return
	}
	t, err := s.inspectTarget(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	setAudit(r, t.RunID+"/"+t.Deployment+"/"+in.Object, clip(in.Action+": "+in.Reason, 300))
	id := identityFrom(r)
	if ok, why := s.canOperate(id, t); !ok {
		writeErr(w, http.StatusForbidden, why)
		return
	}
	switch in.Action {
	case inspect.ActPause, inspect.ActResume, inspect.ActRestart, inspect.ActScale:
	default:
		writeErr(w, http.StatusBadRequest, "action must be pause, resume, restart or scale (promote, abort and retry are the rollout commands)")
		return
	}
	if in.Reason == "" {
		writeErr(w, http.StatusBadRequest, "say why: a control action needs a reason, which is kept in the Logbook")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout+5*time.Second)
	defer cancel()
	raw, err := s.inspectAsk(ctx, t, engine.InspectControl, argocd.ControlParams{Action: in.Action, Object: in.Object, Replicas: in.Replicas, Reason: in.Reason}, id.User, 0)
	if err != nil {
		s.failInspect(w, err)
		return
	}
	var ans argocd.Answer
	if err := json.Unmarshal(raw, &ans); err != nil {
		writeErr(w, http.StatusBadGateway, "the deploy runner's answer could not be read")
		return
	}
	code := http.StatusOK
	switch ans.Result {
	case inspect.NotApplied:
		code = http.StatusConflict
	case inspect.Failed:
		code = http.StatusBadGateway
	}
	outcome := "ok"
	if ans.Result == inspect.NotApplied || ans.Result == inspect.Failed {
		outcome = "failed"
	}
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner", Source: s.clientAddr(r), RequestID: reqID(r), Action: "deployment.control_outcome",
		Project: t.Project, Target: t.RunID + "/" + t.Deployment + "/" + in.Object, Outcome: outcome, Detail: clip(in.Action+" asked for by "+id.User+": "+ans.Result+": "+ans.Detail, 300)})
	writeJSON(w, code, map[string]string{"result": ans.Result, "detail": ans.Detail})
}

// ---- for deploy runners ----

func (s *Server) deployInspect(w http.ResponseWriter, r *http.Request) {
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
	job, err := s.Engine.TakeInspect(ctx, in.Runner, targets, wait)
	switch {
	case err != nil && ctx.Err() != nil, err == nil && job == nil:
		w.WriteHeader(http.StatusNoContent)
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusOK, job)
	}
}

func (s *Server) deployInspectChunk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner engine.RunnerInfo `json:"runner"`
		ID     string            `json:"id"`
		Body   string            `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	targets, ok := s.deployRunner(w, r, &in.Runner)
	if !ok {
		return
	}
	stop, err := s.Engine.InspectChunk(in.Runner, targets, in.ID, in.Body)
	if err != nil && !errors.Is(err, engine.ErrNotFound) {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stop": stop})
}

func (s *Server) deployInspectDone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner engine.RunnerInfo `json:"runner"`
		ID     string            `json:"id"`
		Result string            `json:"result"`
		Error  string            `json:"error"`
	}
	if !decode(w, r, &in) {
		return
	}
	targets, ok := s.deployRunner(w, r, &in.Runner)
	if !ok {
		return
	}
	if err := s.Engine.FinishInspect(in.Runner, targets, in.ID, in.Result, in.Error); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
