package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/hub"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Deploy runners: leasing actions by target, and observing.
//
// A deploy runner asks for actions of the targets it serves (LeaseDeploy):
// they need no workspace, so they go to any runner serving the target, and
// do not move the run. It also asks which deployments to watch (Watch) and
// reports what it sees (Observe). One runner observes each target at a time:
// it holds a soft lease on the target (target_observers), renewed by every
// Watch and Observe; when it stops, another takes over once the lease
// expires. Observations are not events: the server keeps the latest snapshot
// in the deployment's row, evaluates it, and takes the run's transaction
// only when the step moves to another phase, succeeds or fails.

// noteDeploy tells other servers that a deployment's snapshot changed, so
// they publish it to their browsers. Servers older than schema 11 ignore it
// (their handleNote has no case for it).
const noteDeploy = "deploy"

// kickDeploys wakes this server's reaper to hand free locks to waiting
// deploy steps now rather than at its next tick.
func (e *Engine) kickDeploys() {
	select {
	case e.deployKick <- struct{}{}:
	default:
	}
}

// LeaseDeploy hands the oldest queued deploy or rollback step of the given
// targets to a deploy runner, waiting up to wait for one.
func (e *Engine) LeaseDeploy(ctx context.Context, r RunnerInfo, targets []string, wait time.Duration) (*Job, error) {
	if !e.knows(schemaDeploy) {
		return nil, fmt.Errorf("%w: this server does not know deploy steps", ErrConflict)
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
		job, err := e.tryLeaseDeploy(r, targets)
		if err != nil || job != nil {
			return job, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil
		}
		if e.db.pg {
			left = min(left, recheckEvery)
		}
		t := time.NewTimer(left)
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

func inList(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (e *Engine) tryLeaseDeploy(r RunnerInfo, targets []string) (*Job, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	args := []any{}
	for _, t := range targets {
		args = append(args, t)
	}
	args = append(args, e.schema)
	rows, err := e.db.Query(`SELECT s.run_id, s.step_id, s.attempt FROM steps s JOIN runs ON runs.id = s.run_id
		WHERE s.status = 'queued' AND s.target IN (`+inList(len(targets))+`) AND runs.min_schema <= ?
		ORDER BY s.queued_at, s.run_id, s.position LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	type cand struct {
		run, step string
		attempt   int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.run, &c.step, &c.attempt); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, cd := range cands {
		var job *Job
		err := e.withRun(cd.run, func(c *cmd) error {
			job = nil
			s := c.st.Steps[cd.step]
			def := c.st.Pipeline.Step(cd.step)
			if s == nil || def == nil || !def.IsDeployKind() || s.Status != StepQueuedS || s.Attempt != cd.attempt ||
				c.st.CancelRequested || s.Dep == nil || !slices.Contains(targets, s.Dep.Target) {
				return nil
			}
			leaseID := ids.New("lease")
			// An action continues the step's attempt, whose log may already
			// have lines from an earlier action.
			if _, err := c.tx.Exec(`INSERT INTO leases(id, run_id, step_id, attempt, runner, expires_at, log_seq)
				VALUES(?,?,?,?,?,?, (SELECT COALESCE(MAX(seq), 0) FROM logs WHERE run_id = ? AND step_id = ? AND attempt = ?))`,
				leaseID, cd.run, cd.step, s.Attempt, r.ID, c.leaseUntil(), cd.run, cd.step, s.Attempt); err != nil {
				return err
			}
			c.emit(StepLeased, Payload{Step: cd.step, Attempt: s.Attempt, Lease: leaseID, Runner: r.ID})
			j, err := c.deployJob(cd.step, leaseID)
			if err != nil {
				return err
			}
			job = j
			return nil
		})
		if errors.Is(err, ErrNeedsNewer) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if job != nil {
			return job, nil
		}
	}
	return nil, nil
}

// LeaseTarget returns the target of the step a lease is for: "" for an
// ordinary step. The API uses it to keep runner pools to their own work.
func (e *Engine) LeaseTarget(leaseID string) (string, error) {
	if !e.knows(schemaDeploy) {
		return "", nil
	}
	var target string
	err := e.db.QueryRow(`SELECT s.target FROM leases l JOIN steps s ON s.run_id = l.run_id AND s.step_id = l.step_id
		WHERE l.id = ?`, leaseID).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrLeaseLost
	}
	return target, err
}

// ---- observing ----

// WatchItem is one deployment a deploy runner should observe.
type WatchItem struct {
	// Key names this watch: it changes whenever the step starts watching
	// again, so a snapshot of an earlier watch is never taken for this one.
	Key         string      `json:"key"`
	Deployment  string      `json:"deployment"`
	RunID       string      `json:"run_id"`
	RunNumber   int         `json:"run_number"`
	Project     string      `json:"project"`
	Step        string      `json:"step"`
	Attempt     int         `json:"attempt"`
	Target      string      `json:"target"`
	Application string      `json:"application"`
	Repo        string      `json:"repo"`
	Branch      string      `json:"branch"`
	Spec        deploy.Spec `json:"spec"`
	Deadline    int64       `json:"deadline"`
	// Kind (schema 14) is "" for a step waiting for what is observed,
	// "deployment" for an open deployment no step is watching (an approval
	// may be waiting at its pause), or "unit" for an idle service, which
	// the runner may observe slowly.
	Kind string `json:"kind,omitempty"`
}

// Observation is what a deploy runner saw for one watch.
type Observation struct {
	Key      string          `json:"key"`
	Snapshot deploy.Snapshot `json:"snapshot"`
}

// ObserveResult says what the server did with an observation.
type ObserveResult struct {
	Key      string `json:"key"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"` // why it was not
	Phase    string `json:"phase,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// claimObserver takes or renews the observer lease of each target for the
// runner, and returns the targets it holds.
func (e *Engine) claimObserver(runnerID string, targets []string) ([]string, error) {
	unlock := e.writeLock()
	defer unlock()
	now := e.now().UnixMilli()
	until := now + e.ttl.Milliseconds()
	var held []string
	for _, t := range targets {
		if _, err := e.db.Exec(`INSERT INTO target_observers(target, runner, since, expires_at) VALUES(?,?,?,?)
			ON CONFLICT(target) DO UPDATE SET
				since = CASE WHEN target_observers.runner = excluded.runner THEN target_observers.since ELSE excluded.since END,
				runner = excluded.runner, expires_at = excluded.expires_at
			WHERE target_observers.runner = excluded.runner OR target_observers.expires_at < ?`,
			t, runnerID, now, until, now); err != nil {
			return nil, err
		}
		var who string
		if err := e.db.QueryRow(`SELECT runner FROM target_observers WHERE target = ?`, t).Scan(&who); err != nil {
			return nil, err
		}
		if who == runnerID {
			held = append(held, t)
		}
	}
	return held, nil
}

// Watch returns the deployments a deploy runner should observe: those of
// the targets for which it is the observer. It waits up to wait for the list
// to differ from known (the Version of the list the runner has), so a
// runner learns of a new watch at once.
func (e *Engine) Watch(ctx context.Context, r RunnerInfo, targets []string, known string, wait time.Duration) ([]WatchItem, string, error) {
	if !e.knows(schemaDeploy) {
		return nil, "", fmt.Errorf("%w: this server does not know deploy steps", ErrConflict)
	}
	deadline := time.Now().Add(wait)
	for {
		ch := e.waitChan()
		e.touchRunner(r)
		held, err := e.claimObserver(r.ID, targets)
		if err != nil {
			return nil, "", err
		}
		items, err := e.watchItems(held)
		if err != nil {
			return nil, "", err
		}
		if e.knows(schemaControl) {
			more, err := e.controlWatchItems(r, held)
			if err != nil {
				return nil, "", err
			}
			items = append(items, more...)
			sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
		}
		version := watchVersion(items)
		left := time.Until(deadline)
		// Wake up well within the observer lease, so the runner renews it.
		left = min(left, e.ttl/3)
		if version != known || time.Until(deadline) <= 0 {
			return items, version, nil
		}
		if e.db.pg {
			left = min(left, recheckEvery)
		}
		t := time.NewTimer(max(left, 10*time.Millisecond))
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, "", ctx.Err()
		case <-e.stop:
			t.Stop()
			return nil, "", ErrClosed
		}
	}
}

func watchVersion(items []WatchItem) string {
	h := sha256.New()
	for _, it := range items {
		h.Write([]byte(it.Key + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// watchKey names one watch of a step: its deployment, attempt and the
// commit it waits for.
func watchKey(dep, runID, step string, attempt int, commit string) string {
	return dep + "/" + runID + "/" + step + "/" + strconv.Itoa(attempt) + "/" + commit
}

// watchItems lists the steps watching on the targets.
func (e *Engine) watchItems(targets []string) ([]WatchItem, error) {
	out := []WatchItem{}
	if len(targets) == 0 {
		return out, nil
	}
	args := []any{StepWaitingS, WaitObserve}
	for _, t := range targets {
		args = append(args, t)
	}
	args = append(args, e.schema)
	rows, err := e.db.Query(`SELECT s.run_id FROM steps s JOIN runs r ON r.id = s.run_id
		WHERE s.status = ? AND s.wait_kind = ? AND s.target IN (`+inList(len(targets))+`) AND r.min_schema <= ?
		GROUP BY s.run_id ORDER BY s.run_id`, args...)
	if err != nil {
		return nil, err
	}
	var runIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		runIDs = append(runIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, runID := range runIDs {
		evs, _, err := loadEvents(e.db, runID)
		if err != nil {
			return nil, err
		}
		st := e.fold(evs)
		if st == nil || needsNewer(st) != nil {
			continue
		}
		for _, id := range st.Order {
			if it, ok := watchItemOf(st, id); ok && slices.Contains(targets, it.Target) {
				out = append(out, it)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// watchItemOf is the watch of a step, if it is waiting for observation.
func watchItemOf(st *RunState, id string) (WatchItem, bool) {
	s := st.Steps[id]
	def := st.Pipeline.Step(id)
	if s == nil || def == nil || !def.IsDeployKind() || s.Status != StepWaitingS || s.WaitKind != WaitObserve || !s.Dep.Opened() {
		return WatchItem{}, false
	}
	deployDef := def
	kind := pipeline.KindDeploy
	if def.IsRollback() {
		deployDef = st.Pipeline.Step(def.Deployment)
		if def.IsRestore() {
			deployDef = def // it carries how its service is deployed
		}
		kind = pipeline.KindRollback
	}
	if def.IsRolloutStep() {
		deployDef = st.Pipeline.Step(def.Deployment)
		kind = pipeline.KindRolloutStep
	}
	if deployDef == nil || deployDef.ArgoCD == nil || deployDef.ArgoCD.WriteBack == nil {
		return WatchItem{}, false
	}
	spec := deploy.SpecFor(kind, deployDef, s.Dep.Commit)
	if def.IsRolloutStep() {
		spec.Until, spec.Pauses = def.Until, def.Pauses
	}
	return WatchItem{Key: watchKey(s.Dep.ID, st.ID, id, s.Attempt, s.Dep.Commit), Deployment: s.Dep.ID, RunID: st.ID,
		RunNumber: st.Number, Project: st.Project, Step: id, Attempt: s.Attempt, Target: s.Dep.Target,
		Application: s.Dep.Application, Repo: deployDef.ArgoCD.WriteBack.Repo, Branch: deployDef.ArgoCD.WriteBack.Branch,
		Spec: spec, Deadline: s.Deadline}, true
}

// maxSnapshotBytes bounds a stored snapshot.
const maxSnapshotBytes = 64 << 10

// maxTimeline bounds the timeline rows kept per deployment.
const maxTimeline = 300

// maxPhaseEvents bounds the deploy.phase events per step attempt; beyond
// it, phases are kept in the deployment's row only.
const maxPhaseEvents = 64

// Observe takes what a deploy runner saw. Only the runner observing a
// target may report for it. For each observation the server stores the
// snapshot, evaluates it, and, when the step moves on, records that in the
// run.
func (e *Engine) Observe(r RunnerInfo, targets []string, obs []Observation) ([]ObserveResult, error) {
	if !e.knows(schemaDeploy) {
		return nil, fmt.Errorf("%w: this server does not know deploy steps", ErrConflict)
	}
	held, err := e.claimObserver(r.ID, targets)
	if err != nil {
		return nil, err
	}
	out := make([]ObserveResult, 0, len(obs))
	for _, o := range obs {
		res, err := e.observe(r, held, o)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

func (e *Engine) observe(r RunnerInfo, held []string, o Observation) (ObserveResult, error) {
	res := ObserveResult{Key: o.Key}
	parts := strings.Split(o.Key, "/")
	if len(parts) != 5 {
		res.Reason = "not a watch key"
		return res, nil
	}
	// Only the analysis runs of this deployment, for what is stored and for
	// the verdict alike.
	o.Snapshot = e.ownAnalysis(parts, o.Snapshot)
	if e.knows(schemaControl) {
		switch {
		case parts[0] == "unit":
			return e.observeUnit(r, held, o, parts)
		case parts[2] == "-":
			return e.observeOpen(r, held, o, parts)
		}
	}
	depID, runID, stepID := parts[0], parts[1], parts[2]
	snap, err := json.Marshal(o.Snapshot)
	if err != nil {
		return res, err
	}
	if len(snap) > maxSnapshotBytes {
		res.Reason = fmt.Sprintf("the snapshot is larger than %d KiB", maxSnapshotBytes>>10)
		return res, nil
	}
	var target, phase, detail, stored string
	err = e.db.QueryRow(`SELECT target, phase, detail, snapshot FROM deployments WHERE id = ? AND run_id = ?`, depID, runID).
		Scan(&target, &phase, &detail, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		res.Reason = "no such deployment"
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if !slices.Contains(held, target) {
		res.Reason = "this runner is not the observer of target " + target
		return res, nil
	}
	// The watch must still be the step's current one; read it from the run.
	evs, _, err := loadEvents(e.db, runID)
	if err != nil {
		return res, err
	}
	st := e.fold(evs)
	if st == nil || needsNewer(st) != nil {
		res.Reason = "the run is not watched by this server"
		return res, nil
	}
	item, ok := watchItemOf(st, stepID)
	if !ok || item.Key != o.Key {
		res.Reason = "the deployment is no longer watched for this"
		return res, nil
	}
	hash, err := e.notePodHash(depID, item.Spec.Kind, o.Snapshot)
	if err != nil {
		return res, err
	}
	v := st.evaluate(stepID, item.Spec, o.Snapshot, hash, e.now().UnixMilli())
	res.Accepted, res.Phase, res.Detail = true, v.Phase, v.Detail
	if err := e.storeObservation(depID, stepID, r, snap, stored, phase, detail, v); err != nil {
		return res, err
	}
	s := st.Steps[stepID]
	if v.Outcome == deploy.Continue && v.Phase == s.Dep.Phase {
		return res, nil
	}
	if v.Outcome == deploy.Continue && s.Dep.Phases >= maxPhaseEvents {
		return res, nil // kept in the row only (storeObservation)
	}
	err = e.withRun(runID, func(c *cmd) error {
		s := c.st.Steps[stepID]
		if it, ok := watchItemOf(c.st, stepID); !ok || it.Key != o.Key {
			return nil // it moved on since
		}
		if c.now >= s.Deadline {
			return nil // from the deadline on, only expiry decides
		}
		// Decided again on the run as it is now: commands may have changed.
		v = c.st.evaluate(stepID, item.Spec, o.Snapshot, hash, c.now)
		if v.Outcome == deploy.Command && v.Expect != nil {
			origin := OriginPauses
			if c.st.Pipeline.Step(stepID).IsRolloutStep() && s.Dep.Command == "" {
				origin = OriginStep
			}
			c.giveCommand(stepID, s.Dep.ID, v.Action, "launchway", "", origin, *v.Expect)
		}
		// The last phase is always recorded, so the events say how it ended.
		if (v.Phase != s.Dep.Phase && s.Dep.Phases < maxPhaseEvents) || v.Outcome != deploy.Continue {
			c.emit(DeployPhase, Payload{Step: stepID, Attempt: s.Attempt, Deploy: &DeployEvent{Phase: v.Phase, Detail: v.Detail}})
		}
		switch v.Outcome {
		case deploy.Succeeded:
			outputs := map[string]string{"version": s.Dep.Version}
			def := c.st.Pipeline.Step(stepID)
			if def.IsDeploy() {
				outputs["commit"] = s.Dep.Commit
				if s.Dep.Previous != nil {
					outputs["previous_version"] = s.Dep.Previous.Version
				}
			} else if def.IsRollback() {
				outputs["revert"] = s.Dep.Commit
				if s.Dep.Previous != nil && !def.IsRestore() {
					outputs["version"] = s.Dep.Previous.Version
				}
			}
			c.emit(StepFinished, Payload{Step: stepID, Attempt: s.Attempt, Outcome: string(StepSucceeded), Outputs: outputs})
			c.advance()
		case deploy.Failed:
			c.emit(StepFinished, Payload{Step: stepID, Attempt: s.Attempt, Outcome: string(StepFailed), Reason: v.Detail,
				Why: c.failCause(stepID, s, v, o.Snapshot)})
			c.advance()
		}
		return nil
	})
	if errors.Is(err, ErrNeedsNewer) {
		res.Accepted, res.Reason = false, "the run is not watched by this server"
		return res, nil
	}
	if err == nil && e.knows(schemaControl) && item.Spec.Kind != pipeline.KindRollback {
		err = e.rejectOnAbort(runID, depID, o.Snapshot, hash)
	}
	return res, err
}

// storeObservation keeps a deployment's latest snapshot, adds to its
// timeline when what is observed changed, and tells browsers.
func (e *Engine) storeObservation(depID, stepID string, r RunnerInfo, snap []byte, stored, phase, detail string, v deploy.Verdict) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := e.now().UnixMilli()
	changed := !bytes.Equal(snap, []byte(stored))
	if _, err := tx.Exec(`UPDATE deployments SET snapshot = ?, snapshot_key = ?, observed_at = ?, observed_by = ? WHERE id = ?`,
		string(snap), stepID, now, r.Name, depID); err != nil {
		return err
	}
	if v.Phase != phase || v.Detail != detail {
		// The row says what was last observed even when the run's events
		// stop recording phases.
		if _, err := tx.Exec(`UPDATE deployments SET phase = ?, detail = ? WHERE id = ?`, v.Phase, v.Detail, depID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM deployment_timeline WHERE deployment = ?`, depID).Scan(&n); err != nil {
			return err
		}
		if n < maxTimeline {
			if _, err := tx.Exec(`INSERT INTO deployment_timeline(deployment, step_id, at, phase, detail) VALUES(?,?,?,?,?)`,
				depID, stepID, now, v.Phase, v.Detail); err != nil {
				return err
			}
		}
		changed = true
	}
	e.pubMu.Lock()
	last := e.published[depID]
	publish := changed || now-last >= 10_000
	if publish {
		e.published[depID] = now
	}
	e.pubMu.Unlock()
	var runID string
	if publish {
		if err := tx.QueryRow(`SELECT run_id FROM deployments WHERE id = ?`, depID).Scan(&runID); err != nil {
			return err
		}
		if err := e.notifyOthers(tx, note{Kind: noteDeploy, Run: runID, Step: depID}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if publish {
		e.publishDeployment(runID, depID)
	}
	return nil
}

// publishDeployment sends a deployment's current view to this server's hub.
func (e *Engine) publishDeployment(runID, depID string) {
	vs, err := e.deployments(`d.id = ?`, []any{depID}, 1, true)
	if err != nil || len(vs) == 0 {
		return
	}
	e.hub.Publish(hub.Message{Type: "deployment", RunID: runID, Data: vs[0]})
}

// ---- handing locks over ----

// dispatchLocks gives free locks to the deploy steps waiting for them: the
// newest waiter (queue: newest) or the oldest (queue: all). Every server
// does this; two taking the same lock conflict, and one of them waits again.
func (e *Engine) dispatchLocks() {
	if !e.knows(schemaDeploy) {
		return
	}
	rows, err := e.db.Query(`SELECT q.run_id, q.step_id, q.environment, q.project, q.service, q.run_number, r.event FROM deploy_queue q
		LEFT JOIN deploy_locks l ON l.environment = q.environment AND l.project = q.project AND l.service = q.service
		JOIN runs r ON r.id = q.run_id
		WHERE l.environment IS NULL AND r.min_schema <= ?
		ORDER BY q.environment, q.project, q.service, q.run_number`, e.schema)
	if err != nil {
		e.log.Error("looking for deploy steps waiting for a free lock", "err", err)
		return
	}
	type waiter struct {
		run, step string
		u         unit
		number    int
		rollback  bool // a rollback run's step: it goes first
	}
	first := map[unit]waiter{}
	var order []unit
	for rows.Next() {
		var w waiter
		var event string
		if err := rows.Scan(&w.run, &w.step, &w.u.env, &w.u.project, &w.u.service, &w.number, &event); err != nil {
			rows.Close()
			e.log.Error("looking for deploy steps waiting for a free lock", "err", err)
			return
		}
		w.rollback = event == pipeline.EventRollback
		cur, seen := first[w.u]
		newest := e.live().envs[w.u.env].Queue != "all"
		switch {
		case !seen:
			order = append(order, w.u)
			first[w.u] = w
		case w.rollback && !cur.rollback:
			first[w.u] = w
		case cur.rollback && !w.rollback:
		case newest && w.number > cur.number:
			first[w.u] = w
		}
	}
	rows.Close()
	for _, u := range order {
		w := first[u]
		err := e.withRun(w.run, func(c *cmd) error {
			s := c.st.Steps[w.step]
			def := c.st.Pipeline.Step(w.step)
			if s == nil || def == nil || !(def.IsDeploy() || def.IsRestore()) || s.Status != StepWaitingS || s.WaitKind != WaitLock || c.st.CancelRequested {
				return nil
			}
			if def.IsRestore() {
				c.startRestore(w.step, def)
			} else {
				c.startDeploy(w.step, def)
			}
			c.advance()
			return nil
		})
		if err != nil && !errors.Is(err, ErrNeedsNewer) {
			e.log.Error("handing a free lock to a waiting deploy step", "run", w.run, "step", w.step, "err", err)
		}
	}
}

// ---- reading deployments ----

const deploymentCols = `d.id, d.run_id, d.run_number, d.project, d.step_id, d.rollback_step, d.environment, d.service, d.target,
	d.application, d.mode, d.version, d.previous_id, d.previous_version, d.repo, d.branch, d.commit_sha, d.changed, d.revert_sha,
	d.phase, d.detail, d.verdict, d.opened_at, d.deployed_at, d.closed_at, d.snapshot, d.observed_at, d.observed_by,
	(SELECT COUNT(*) FROM deploy_locks l WHERE l.deployment = d.id)`

// deployments reads deployments, newest first, with their timelines if
// asked.
func (e *Engine) deployments(where string, args []any, limit int, timeline bool) ([]DeploymentView, error) {
	out := []DeploymentView{}
	if !e.knows(schemaDeploy) {
		return out, nil
	}
	rows, err := e.db.Query(`SELECT `+deploymentCols+` FROM deployments d WHERE `+where+` ORDER BY d.opened_at DESC, d.id DESC LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v DeploymentView
		var changed, holding int
		var snap string
		if err := rows.Scan(&v.ID, &v.RunID, &v.RunNumber, &v.Project, &v.Step, &v.RollbackStep, &v.Environment, &v.Service, &v.Target,
			&v.Application, &v.Mode, &v.Version, &v.PreviousID, &v.PreviousVersion, &v.Repo, &v.Branch, &v.Commit, &changed, &v.Revert,
			&v.Phase, &v.Detail, &v.Verdict, &v.OpenedAt, &v.DeployedAt, &v.ClosedAt, &snap, &v.ObservedAt, &v.ObservedBy, &holding); err != nil {
			rows.Close()
			return nil, err
		}
		v.Changed, v.Holding = changed != 0, holding > 0
		if snap != "" {
			v.Snapshot = json.RawMessage(snap)
		}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := e.addControl(out); err != nil {
		return nil, err
	}
	if err := e.addRollbackInfo(out); err != nil {
		return nil, err
	}
	if timeline {
		for i := range out {
			trs, err := e.db.Query(`SELECT at, step_id, phase, detail FROM deployment_timeline WHERE deployment = ? ORDER BY id`, out[i].ID)
			if err != nil {
				return nil, err
			}
			out[i].Timeline = []TimelineEntry{}
			for trs.Next() {
				var t TimelineEntry
				if err := trs.Scan(&t.At, &t.Step, &t.Phase, &t.Detail); err != nil {
					trs.Close()
					return nil, err
				}
				out[i].Timeline = append(out[i].Timeline, t)
			}
			trs.Close()
		}
	}
	return out, nil
}

// RunDeployments returns a run's deployments, newest first.
func (e *Engine) RunDeployments(runID string) ([]DeploymentView, error) {
	return e.deployments(`d.run_id = ?`, []any{runID}, 50, true)
}

// DeploymentFilter narrows a list of deployments.
type DeploymentFilter struct {
	Environment, Project, Service string
	Limit                         int
	// OnlyProjects, if not nil, limits the list to these projects.
	OnlyProjects []string
}

// Deployments lists deployments, newest first.
func (e *Engine) Deployments(f DeploymentFilter) ([]DeploymentView, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	where, args := []string{"1 = 1"}, []any{}
	for _, c := range []struct{ col, v string }{{"d.environment", f.Environment}, {"d.project", f.Project}, {"d.service", f.Service}} {
		if c.v != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	if f.OnlyProjects != nil {
		if len(f.OnlyProjects) == 0 {
			return []DeploymentView{}, nil
		}
		where = append(where, "d.project IN ("+inList(len(f.OnlyProjects))+")")
		for _, p := range f.OnlyProjects {
			args = append(args, p)
		}
	}
	return e.deployments(strings.Join(where, " AND "), args, f.Limit, false)
}

// EnvironmentStates returns every configured environment with its services.
// visible, if not nil, says which projects the caller may see.
func (e *Engine) EnvironmentStates(visible func(project string) bool) ([]EnvironmentView, error) {
	out := []EnvironmentView{}
	units := map[string]map[[2]string]*UnitView{}
	get := func(env, project, service string) *UnitView {
		if visible != nil && !visible(project) {
			return nil
		}
		m := units[env]
		if m == nil {
			m = map[[2]string]*UnitView{}
			units[env] = m
		}
		k := [2]string{project, service}
		if m[k] == nil {
			m[k] = &UnitView{Project: project, Service: service, Waiting: []DeployRef{}}
		}
		return m[k]
	}
	if e.knows(schemaDeploy) {
		rows, err := e.db.Query(`SELECT environment, project, service, current_deployment, current_version, current_run, current_run_number, since
			FROM environment_state`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var env, project, service string
			var c CurrentView
			if err := rows.Scan(&env, &project, &service, &c.Deployment, &c.Version, &c.Run, &c.RunNumber, &c.Since); err != nil {
				rows.Close()
				return nil, err
			}
			if u := get(env, project, service); u != nil {
				u.Current = &c
			}
		}
		rows.Close()
		if err := e.unitLive(get); err != nil {
			return nil, err
		}
		rows, err = e.db.Query(`SELECT l.environment, l.project, l.service, l.deployment, l.run_id, l.run_number, l.step_id, COALESCE(d.version, '')
			FROM deploy_locks l LEFT JOIN deployments d ON d.id = l.deployment`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var env, project, service string
			var r DeployRef
			if err := rows.Scan(&env, &project, &service, &r.Deployment, &r.Run, &r.Number, &r.Step, &r.Version); err != nil {
				rows.Close()
				return nil, err
			}
			if u := get(env, project, service); u != nil {
				u.Lock = &r
			}
		}
		rows.Close()
		rows, err = e.db.Query(`SELECT environment, project, service, run_id, run_number, step_id FROM deploy_queue ORDER BY run_number`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var env, project, service string
			var r DeployRef
			if err := rows.Scan(&env, &project, &service, &r.Run, &r.Number, &r.Step); err != nil {
				rows.Close()
				return nil, err
			}
			if u := get(env, project, service); u != nil {
				u.Waiting = append(u.Waiting, r)
			}
		}
		rows.Close()
		// The newest deployment of each service.
		rows, err = e.db.Query(`SELECT d.environment, d.project, d.service, MAX(d.opened_at) FROM deployments d GROUP BY d.environment, d.project, d.service`)
		if err != nil {
			return nil, err
		}
		type key struct {
			env, project, service string
			at                    int64
		}
		var newest []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.env, &k.project, &k.service, &k.at); err != nil {
				rows.Close()
				return nil, err
			}
			newest = append(newest, k)
		}
		rows.Close()
		for _, k := range newest {
			u := get(k.env, k.project, k.service)
			if u == nil {
				continue
			}
			vs, err := e.deployments(`d.environment = ? AND d.project = ? AND d.service = ? AND d.opened_at = ?`,
				[]any{k.env, k.project, k.service, k.at}, 1, false)
			if err != nil {
				return nil, err
			}
			if len(vs) > 0 {
				u.Last = &vs[0]
			}
		}
	}
	observers := map[string][2]any{}
	if e.knows(schemaDeploy) {
		rows, err := e.db.Query(`SELECT o.target, COALESCE(r.name, o.runner), o.expires_at FROM target_observers o
			LEFT JOIN runners r ON r.id = o.runner`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t, who string
			var until int64
			if err := rows.Scan(&t, &who, &until); err != nil {
				rows.Close()
				return nil, err
			}
			observers[t] = [2]any{who, until}
		}
		rows.Close()
	}
	for _, env := range e.Environments() {
		v := EnvironmentView{Name: env.Name, Target: env.Target, Queue: env.Queue, Projects: env.Projects, Services: []UnitView{}, Protected: env.Protected, MinApprovals: env.MinApprovals}
		if o, ok := observers[env.Target]; ok && o[1].(int64) >= e.now().UnixMilli() {
			v.Observer, v.ObserverExpires = o[0].(string), o[1].(int64)
		}
		for _, u := range units[env.Name] {
			if u.Last != nil && e.knows(schemaRollback) {
				if plan, err := e.RollbackPlan(u.Project, env.Name, u.Service); err == nil {
					a := plan.Action
					u.Rollback = &a
				}
			}
			v.Services = append(v.Services, *u)
		}
		sort.Slice(v.Services, func(i, j int) bool {
			a, b := v.Services[i], v.Services[j]
			if a.Project != b.Project {
				return a.Project < b.Project
			}
			return a.Service < b.Service
		})
		out = append(out, v)
	}
	return out, nil
}

func sortEnvs(list []Environment) {
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
}
