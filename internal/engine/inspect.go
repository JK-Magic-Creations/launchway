package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Looking into a delivery (schema 60).
//
// A person or tool asks to see what a delivery made: the tree of its
// objects, one object's manifest, the logs of a pod or a ReplicaSet, or to
// restart, pause, resume or scale its workload. The server never talks to
// the cluster. It records an inspect request; a deploy runner serving the
// delivery's Airfield takes it (a long poll, like commands), looks, and
// answers with a result or, for a followed log, a stream of chunks. All of
// it goes through the database, so whichever server holds the viewer's
// connection sees what the runner (connected to another) sent.
//
// Answers are cached for a few seconds by the request itself: a second
// asker within the age limit gets the first's answer, and an identical
// request already waiting is shared.

// Kinds of inspect request.
const (
	InspectTree     = "tree"
	InspectManifest = "manifest"
	InspectLogs     = "logs"
	InspectControl  = "control"
)

// States of an inspect request.
const (
	InspectPending  = "pending"
	InspectTaken    = "taken"
	InspectDone     = "done"
	InspectFailed   = "failed"
	InspectCanceled = "canceled"
)

// Limits on inspect requests.
const (
	// MaxOpenInspects bounds the requests one Airfield has waiting or running.
	MaxOpenInspects = 64
	// MaxStreamsPerUser bounds followed logs one person may have open.
	MaxStreamsPerUser = 4
	// MaxStreamsPerTarget bounds followed logs one Airfield may have open.
	MaxStreamsPerTarget = 24
	// InspectIdle is how long a followed log without a reader keeps running.
	InspectIdle = 45 * time.Second
	// InspectTakeFor is how long a runner holds a one-shot request.
	InspectTakeFor = 30 * time.Second
	// InspectFollowFor is how long a followed log may run in all.
	InspectFollowFor = 15 * time.Minute
	// MaxInspectResult bounds an answer.
	MaxInspectResult = 8 << 20
	// MaxChunkBytes bounds one chunk.
	MaxChunkBytes = 512 << 10
)

// InspectTarget is a deployment as an inspect request needs it.
type InspectTarget struct {
	RunID       string              `json:"run_id"`
	Deployment  string              `json:"deployment"`
	Project     string              `json:"project"`
	Target      string              `json:"target"`
	Application string              `json:"application"`
	Environment string              `json:"environment"`
	Step        string              `json:"step"`
	Rollout     pipeline.RolloutRef `json:"rollout"`
	Since       int64               `json:"since"`
	Closed      bool                `json:"closed"`
	Mode        string              `json:"mode,omitempty"`
}

// InspectTargetOf says what a deployment is.
func (e *Engine) InspectTargetOf(runID, depID string) (InspectTarget, error) {
	var t InspectTarget
	if !e.knows(schemaInspect) {
		return t, fmt.Errorf("%w: this server does not know inspect requests", ErrConflict)
	}
	err := e.readRun(runID, func(c *cmd) error {
		stepID, ds := c.st.deploymentStep(depID)
		if ds == nil {
			return fmt.Errorf("%w: deployment %s in run %s", ErrNotFound, depID, runID)
		}
		t = InspectTarget{RunID: runID, Deployment: depID, Target: ds.Dep.Target, Application: ds.Dep.Application, Environment: ds.Dep.Environment,
			Step: stepID, Since: ds.Dep.OpenedAt, Closed: ds.Dep.Closed, Mode: ds.Dep.Mode}
		if def := c.st.Pipeline.Step(stepID); def != nil && def.Rollout != nil {
			t.Rollout = *def.Rollout
		}
		return nil
	})
	if err != nil {
		return t, err
	}
	t.Project, err = e.RunProject(runID)
	return t, err
}

// InspectRequest is what is asked.
type InspectRequest struct {
	Target InspectTarget
	Kind   string
	Params json.RawMessage
	Actor  string
	Follow bool
}

// InspectJob is a request as a deploy runner takes it.
type InspectJob struct {
	ID          string              `json:"id"`
	Kind        string              `json:"kind"`
	Follow      bool                `json:"follow,omitempty"`
	Target      string              `json:"target"`
	Project     string              `json:"project"`
	Application string              `json:"application"`
	RunID       string              `json:"run_id"`
	Deployment  string              `json:"deployment"`
	Rollout     pipeline.RolloutRef `json:"rollout"`
	Since       int64               `json:"since"`
	Params      json.RawMessage     `json:"params,omitempty"`
	Actor       string              `json:"actor,omitempty"`
	Until       int64               `json:"until"` // Unix ms: the runner stops by then
}

func paramsKey(kind string, p json.RawMessage) string {
	h := sha256.Sum256(append([]byte(kind+"\x00"), p...))
	return hex.EncodeToString(h[:6])
}

var lastInspectPurge atomic.Int64

// EnqueueInspect records a request. It refuses when the Airfield already
// has too many open, or the person too many followed logs.
func (e *Engine) EnqueueInspect(rq InspectRequest) (string, error) {
	if !e.knows(schemaInspect) {
		return "", fmt.Errorf("%w: this server does not know inspect requests", ErrConflict)
	}
	if len(rq.Params) == 0 {
		rq.Params = json.RawMessage(`{}`)
	}
	now := e.now().UnixMilli()
	e.purgeInspect(now)
	var open, mine, streams int
	if err := e.db.QueryRow(`SELECT count(*) FROM inspect_requests WHERE target = ? AND state IN ('pending','taken')`, rq.Target.Target).Scan(&open); err != nil {
		return "", err
	}
	if open >= MaxOpenInspects {
		return "", fmt.Errorf("%w: this Airfield already has %d requests to look at deliveries waiting; try again in a moment", ErrConflict, open)
	}
	if rq.Follow {
		if err := e.db.QueryRow(`SELECT count(*) FROM inspect_requests WHERE actor = ? AND follow = 1 AND state IN ('pending','taken') AND seen_at > ?`, rq.Actor, now-int64(InspectIdle/time.Millisecond)).Scan(&mine); err != nil {
			return "", err
		}
		if err := e.db.QueryRow(`SELECT count(*) FROM inspect_requests WHERE target = ? AND follow = 1 AND state IN ('pending','taken') AND seen_at > ?`, rq.Target.Target, now-int64(InspectIdle/time.Millisecond)).Scan(&streams); err != nil {
			return "", err
		}
		if mine >= MaxStreamsPerUser {
			return "", fmt.Errorf("%w: you already follow %d logs; close one first", ErrConflict, mine)
		}
		if streams >= MaxStreamsPerTarget {
			return "", fmt.Errorf("%w: this Airfield already streams %d logs", ErrConflict, streams)
		}
	}
	id := ids.New("ins")
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO inspect_requests(id, target, run_id, deployment_id, project, kind, params, actor, state, follow, seen_at, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, rq.Target.Target, rq.Target.RunID, rq.Target.Deployment, rq.Target.Project, rq.Kind, string(rq.Params), rq.Actor, InspectPending, boolInt(rq.Follow), now, now)
	if err != nil {
		return "", err
	}
	e.notify()
	return id, nil
}

// purgeInspect removes finished requests a day old, at most once a minute.
func (e *Engine) purgeInspect(now int64) {
	last := lastInspectPurge.Load()
	if now-last < 60_000 || !lastInspectPurge.CompareAndSwap(last, now) {
		return
	}
	e.purgeInspectNow(now)
}

func (e *Engine) purgeInspectNow(now int64) {
	cut := now - int64(24*time.Hour/time.Millisecond)
	unlock := e.writeLock()
	defer unlock()
	_, _ = e.db.Exec(`DELETE FROM inspect_chunks WHERE request_id IN (SELECT id FROM inspect_requests WHERE created_at < ?)`, cut)
	_, _ = e.db.Exec(`DELETE FROM inspect_requests WHERE created_at < ?`, cut)
	// A request nobody took, or whose runner went away, ends.
	_, _ = e.db.Exec(`UPDATE inspect_requests SET state = 'canceled', error = 'no deploy runner answered', done_at = ? WHERE state = 'pending' AND created_at < ?`, now, now-int64(2*time.Minute/time.Millisecond))
	_, _ = e.db.Exec(`UPDATE inspect_requests SET state = 'canceled', error = 'the deploy runner stopped answering', done_at = ? WHERE state = 'taken' AND taken_until < ?`, now, now-5000)
}

// TakeInspect hands a deploy runner the oldest request of its targets,
// waiting up to wait for one.
func (e *Engine) TakeInspect(ctx context.Context, r RunnerInfo, targets []string, wait time.Duration) (*InspectJob, error) {
	if !e.knows(schemaInspect) {
		return nil, fmt.Errorf("%w: this server does not know inspect requests", ErrConflict)
	}
	if len(targets) == 0 {
		return nil, nil
	}
	deadline := time.Now().Add(wait)
	for {
		ch := e.waitChan()
		select {
		case <-e.stop:
			return nil, ErrClosed
		default:
		}
		e.touchRunner(r)
		job, err := e.tryTakeInspect(r, targets)
		if err != nil || job != nil {
			return job, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil
		}
		t := time.NewTimer(min(left, 400*time.Millisecond)) // other servers' requests are found by looking
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-e.stop:
			t.Stop()
			return nil, ErrClosed
		}
	}
}

func (e *Engine) tryTakeInspect(r RunnerInfo, targets []string) (*InspectJob, error) {
	now := e.now().UnixMilli()
	args := []any{}
	for _, t := range targets {
		args = append(args, t)
	}
	rows, err := e.db.Query(`SELECT id, target, run_id, deployment_id, project, kind, params, actor, follow FROM inspect_requests
		WHERE state = 'pending' AND target IN (`+inList(len(targets))+`) ORDER BY created_at, id LIMIT 5`, args...)
	if err != nil {
		return nil, err
	}
	cands, err := scanInspectJobs(rows)
	if err != nil {
		return nil, err
	}
	for _, j := range cands {
		until := now + int64(InspectTakeFor/time.Millisecond)
		if j.Follow {
			until = now + int64(InspectFollowFor/time.Millisecond)
		}
		unlock := e.writeLock()
		res, err := e.db.Exec(`UPDATE inspect_requests SET state = 'taken', runner = ?, taken_until = ? WHERE id = ? AND state = 'pending'`, nonEmpty(r.Name, r.ID), until, j.ID)
		unlock()
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue // another runner or server was first
		}
		j.Until = until
		t, err := e.InspectTargetOf(j.RunID, j.Deployment)
		if err != nil { // the run is gone: nothing to look at
			_ = e.FinishInspect(r, targets, j.ID, "", "the deployment is no longer there")
			continue
		}
		j.Application, j.Rollout, j.Since = t.Application, t.Rollout, t.Since
		return &j, nil
	}
	return nil, nil
}

func (e *Engine) inspectRow(id string, targets []string) (state, target string, err error) {
	err = e.db.QueryRow(`SELECT state, target FROM inspect_requests WHERE id = ?`, id).Scan(&state, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: inspect request %s", ErrNotFound, id)
	}
	if err == nil && !slices.Contains(targets, target) {
		return "", "", fmt.Errorf("%w: this runner does not serve target %s", ErrForbidden, target)
	}
	return state, target, err
}

// InspectChunk stores lines a runner sends of a followed log. It says
// whether the runner should stop: the viewer left, or the request ended.
func (e *Engine) InspectChunk(r RunnerInfo, targets []string, id, body string) (stop bool, err error) {
	state, _, err := e.inspectRow(id, targets)
	if err != nil {
		return true, err
	}
	if state != InspectTaken {
		return true, nil
	}
	if len(body) > MaxChunkBytes {
		return false, fmt.Errorf("%w: a chunk is larger than %d bytes", ErrInvalid, MaxChunkBytes)
	}
	now := e.now().UnixMilli()
	var seen int64
	_ = e.db.QueryRow(`SELECT seen_at FROM inspect_requests WHERE id = ?`, id).Scan(&seen)
	if now-seen > int64(InspectIdle/time.Millisecond) {
		unlock := e.writeLock()
		_, _ = e.db.Exec(`UPDATE inspect_requests SET state = 'canceled', error = 'nobody was watching', done_at = ? WHERE id = ? AND state = 'taken'`, now, id)
		unlock()
		return true, nil
	}
	if body == "" {
		return false, nil
	}
	for try := 0; try < 5; try++ {
		var next int64
		if err := e.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM inspect_chunks WHERE request_id = ?`, id).Scan(&next); err != nil {
			return false, err
		}
		unlock := e.writeLock()
		_, err := e.db.Exec(`INSERT INTO inspect_chunks(request_id, seq, body, created_at) VALUES(?,?,?,?)`, id, next, body, now)
		unlock()
		if err == nil {
			return false, nil
		}
	}
	return false, errors.New("could not store the chunk")
}

// FinishInspect records a runner's answer (result, a JSON document) or why
// it could not answer.
func (e *Engine) FinishInspect(r RunnerInfo, targets []string, id, result, errMsg string) error {
	state, _, err := e.inspectRow(id, targets)
	if err != nil {
		return err
	}
	if state != InspectTaken {
		return nil // canceled or already answered
	}
	if len(result) > MaxInspectResult {
		result, errMsg = "", fmt.Sprintf("the answer was larger than %d MiB", MaxInspectResult>>20)
	}
	st := InspectDone
	if errMsg != "" {
		st = InspectFailed
	}
	unlock := e.writeLock()
	defer unlock()
	_, err = e.db.Exec(`UPDATE inspect_requests SET state = ?, result = ?, error = ?, done_at = ? WHERE id = ? AND state = 'taken'`, st, result, errMsg, e.now().UnixMilli(), id)
	return err
}

// InspectOutcome is a finished request.
type InspectOutcome struct {
	State  string
	Result json.RawMessage
	Error  string
}

// AwaitInspect waits for a request to finish. A request not finished by
// timeout is canceled.
func (e *Engine) AwaitInspect(ctx context.Context, id string, timeout time.Duration) (*InspectOutcome, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	for {
		var st, res, er string
		err := e.db.QueryRow(`SELECT state, result, error FROM inspect_requests WHERE id = ?`, id).Scan(&st, &res, &er)
		if err != nil {
			return nil, err
		}
		switch st {
		case InspectDone, InspectFailed, InspectCanceled:
			return &InspectOutcome{State: st, Result: json.RawMessage(res), Error: er}, nil
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			e.CancelInspect(id, "no deploy runner answered in time: is one running for this Airfield?")
			return &InspectOutcome{State: InspectCanceled, Error: "no deploy runner answered in time: is one running for this Airfield?"}, nil
		case <-ctx.Done():
			e.CancelInspect(id, "the viewer left")
			return nil, ctx.Err()
		}
	}
}

// CancelInspect ends a request that is not finished.
func (e *Engine) CancelInspect(id, why string) {
	unlock := e.writeLock()
	defer unlock()
	_, _ = e.db.Exec(`UPDATE inspect_requests SET state = 'canceled', error = ?, done_at = ? WHERE id = ? AND state IN ('pending','taken')`, why, e.now().UnixMilli(), id)
}

// RecentInspect returns a one-shot request's answer if an identical one
// finished within maxAge, else the id of an identical one still open, else
// "" "" (nothing to reuse).
func (e *Engine) RecentInspect(t InspectTarget, kind string, params json.RawMessage, maxAge time.Duration) (outcome *InspectOutcome, openID string, err error) {
	now := e.now().UnixMilli()
	rows, err := e.db.Query(`SELECT id, state, result, error, params, done_at FROM inspect_requests WHERE deployment_id = ? AND kind = ? AND follow = 0 AND created_at > ? ORDER BY created_at DESC LIMIT 12`,
		t.Deployment, kind, now-int64((maxAge+InspectTakeFor)/time.Millisecond))
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	want := paramsKey(kind, params)
	for rows.Next() {
		var id, st, res, er, p string
		var done int64
		if err := rows.Scan(&id, &st, &res, &er, &p, &done); err != nil {
			return nil, "", err
		}
		if paramsKey(kind, json.RawMessage(p)) != want {
			continue
		}
		switch st {
		case InspectPending, InspectTaken:
			return nil, id, nil
		case InspectDone:
			if now-done <= int64(maxAge/time.Millisecond) {
				return &InspectOutcome{State: st, Result: json.RawMessage(res)}, "", nil
			}
		}
	}
	return nil, "", rows.Err()
}

// InspectChunk is one piece of a followed log.
type InspectChunkRow struct {
	Seq  int64  `json:"seq"`
	Body string `json:"body"`
}

// InspectChunks returns the chunks after seq, and the request's state; it
// also marks the request as watched, which keeps the runner reading.
func (e *Engine) InspectChunks(id string, after int64) (chunks []InspectChunkRow, state, why, result string, err error) {
	now := e.now().UnixMilli()
	unlock := e.writeLock()
	_, _ = e.db.Exec(`UPDATE inspect_requests SET seen_at = ? WHERE id = ?`, now, id)
	unlock()
	if err = e.db.QueryRow(`SELECT state, error, result FROM inspect_requests WHERE id = ?`, id).Scan(&state, &why, &result); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = fmt.Errorf("%w: inspect request %s", ErrNotFound, id)
		}
		return
	}
	rows, err := e.db.Query(`SELECT seq, body FROM inspect_chunks WHERE request_id = ? AND seq > ? ORDER BY seq LIMIT 200`, id, after)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c InspectChunkRow
		if err = rows.Scan(&c.Seq, &c.Body); err != nil {
			return
		}
		chunks = append(chunks, c)
	}
	err = rows.Err()
	return
}

// InspectRequestOf says whose request it is, for the access check on a
// stream.
func (e *Engine) InspectRequestOf(id string) (runID, depID, actor string, err error) {
	err = e.db.QueryRow(`SELECT run_id, deployment_id, actor FROM inspect_requests WHERE id = ?`, id).Scan(&runID, &depID, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("%w: inspect request %s", ErrNotFound, id)
	}
	return
}

// OpenInspects counts the requests of a target that are waiting or running.
func (e *Engine) OpenInspects(target string) (int, error) {
	var n int
	err := e.db.QueryRow(`SELECT count(*) FROM inspect_requests WHERE target = ? AND state IN ('pending','taken')`, target).Scan(&n)
	return n, err
}

func scanInspectJobs(rows *sql.Rows) ([]InspectJob, error) {
	defer rows.Close()
	var out []InspectJob
	for rows.Next() {
		var j InspectJob
		var params string
		var follow int
		if err := rows.Scan(&j.ID, &j.Target, &j.RunID, &j.Deployment, &j.Project, &j.Kind, &params, &j.Actor, &follow); err != nil {
			return nil, err
		}
		j.Params, j.Follow = json.RawMessage(params), follow == 1
		out = append(out, j)
	}
	return out, rows.Err()
}
