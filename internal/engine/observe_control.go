package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Observing beyond the steps that wait for it (schema 14).
//
// An open deployment no step is watching (its deploy step stopped at a
// pause, and an approval or a smoke test is going on) is observed all the
// same, so the run page shows the rollout live, commands can be given with
// fresh expectations, and an approval naming the deployment is rejected as
// soon as the rollout is aborted. These watches go to every deploy runner
// (older ones treat them as any other).
//
// An idle service (one an environment runs, with nothing deploying it) is
// observed slowly, only by runners that report the feature "idle", and at most
// maxIdleUnits per target: what they see is kept in its environment_state
// row with its drift, never in the run's events.

// maxIdleUnits bounds the idle services observed per target.
const maxIdleUnits = 100

// FeatureIdle is the feature (RunnerInfo.Features) a deploy runner reports
// to be given idle services: it observes them slowly.
const FeatureIdle = "idle"

// Kinds of watch items.
const (
	WatchDeployment = "deployment"
	WatchUnit       = "unit"
)

// openKey names the watch of an open deployment: a step key with "-" for
// the step.
func openKey(depID, runID, commit string) string { return watchKey(depID, runID, "-", 0, commit) }

func unitKey(env, project, service, depID string) string {
	return WatchUnit + "/" + env + "/" + project + "/" + service + "/" + depID
}

// controlWatchItems lists the open deployments no step watches and, for a
// runner that can, the idle services of the targets.
func (e *Engine) controlWatchItems(r RunnerInfo, targets []string) ([]WatchItem, error) {
	out := []WatchItem{}
	if len(targets) == 0 {
		return out, nil
	}
	args := []any{}
	for _, t := range targets {
		args = append(args, t)
	}
	args = append(args, e.schema)
	rows, err := e.db.Query(`SELECT DISTINCT l.run_id FROM deploy_locks l JOIN deployments d ON d.id = l.deployment
		JOIN runs r ON r.id = l.run_id WHERE d.target IN (`+inList(len(targets))+`) AND r.min_schema <= ? ORDER BY l.run_id`, args...)
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
			if it, ok := openWatchItemOf(st, id); ok && slices.Contains(targets, it.Target) {
				out = append(out, it)
			}
		}
	}
	if !r.Has(FeatureIdle) {
		return out, nil
	}
	units, err := e.idleWatchItems(targets)
	return append(out, units...), err
}

// openWatchItemOf is the watch of a deploy step's open deployment, if no
// step of it is watching.
func openWatchItemOf(st *RunState, id string) (WatchItem, bool) {
	s := st.Steps[id]
	def := st.Pipeline.Step(id)
	if s == nil || def == nil || !def.IsDeploy() || !s.Dep.Opened() || s.Dep.Closed || s.Dep.Commit == "" ||
		def.ArgoCD == nil || def.ArgoCD.WriteBack == nil {
		return WatchItem{}, false
	}
	switch s.Status {
	case StepQueuedS, StepLeasedS, StepRunning:
		return WatchItem{}, false // its action is going on
	}
	for _, m := range st.Pipeline.DeploymentScope(id) {
		if ms := st.Steps[m]; ms.Status == StepWaitingS && ms.WaitKind == WaitObserve {
			return WatchItem{}, false
		}
		if mdef := st.Pipeline.Step(m); mdef.IsRollback() && st.Steps[m].Status != StepPending && !st.Steps[m].Status.Terminal() {
			return WatchItem{}, false
		}
	}
	return WatchItem{Key: openKey(s.Dep.ID, st.ID, s.Dep.Commit), Kind: WatchDeployment, Deployment: s.Dep.ID, RunID: st.ID,
		RunNumber: st.Number, Project: st.Project, Step: id, Attempt: s.Attempt, Target: s.Dep.Target,
		Application: s.Dep.Application, Repo: def.ArgoCD.WriteBack.Repo, Branch: def.ArgoCD.WriteBack.Branch,
		Spec: deploy.SpecFor(pipeline.KindDeploy, def, s.Dep.Commit)}, true
}

// specs caches the watch spec of deployments made before schema 14, read
// from their run once (a deployment's spec never changes).
var specs sync.Map

// idleWatchItems lists the idle services of the targets: those whose
// current deployment holds no lock.
func (e *Engine) idleWatchItems(targets []string) ([]WatchItem, error) {
	args := []any{}
	for _, t := range targets {
		args = append(args, t)
	}
	args = append(args, maxIdleUnits*len(targets))
	rows, err := e.db.Query(`SELECT s.environment, s.project, s.service, d.id, d.run_id, d.run_number, d.target, d.application,
			d.repo, d.branch, d.commit_sha, d.watch_spec
		FROM environment_state s JOIN deployments d ON d.id = s.current_deployment
		LEFT JOIN deploy_locks l ON l.environment = s.environment AND l.project = s.project AND l.service = s.service
		WHERE l.environment IS NULL AND d.commit_sha <> '' AND d.target IN (`+inList(len(targets))+`)
		ORDER BY s.environment, s.project, s.service LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	type unitRow struct {
		env, project, service string
		it                    WatchItem
		spec                  string
	}
	var list []unitRow
	perTarget := map[string]int{}
	for rows.Next() {
		var u unitRow
		if err := rows.Scan(&u.env, &u.project, &u.service, &u.it.Deployment, &u.it.RunID, &u.it.RunNumber, &u.it.Target,
			&u.it.Application, &u.it.Repo, &u.it.Branch, &u.it.Spec.Commit, &u.spec); err != nil {
			rows.Close()
			return nil, err
		}
		if perTarget[u.it.Target] >= maxIdleUnits {
			continue
		}
		perTarget[u.it.Target]++
		list = append(list, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []WatchItem{}
	for _, u := range list {
		commit := u.it.Spec.Commit
		var spec deploy.Spec
		switch {
		case u.spec != "":
			if json.Unmarshal([]byte(u.spec), &spec) != nil {
				continue
			}
		default:
			cached, ok := specs.Load(u.it.Deployment)
			if !ok {
				s, err := e.specFromRun(u.it.RunID, u.it.Deployment)
				if err != nil {
					return nil, err
				}
				specs.Store(u.it.Deployment, s)
				cached = s
			}
			spec = cached.(deploy.Spec)
		}
		if spec.Kind == "" {
			continue
		}
		spec.Commit = commit
		it := u.it
		it.Spec, it.Kind, it.Project = spec, WatchUnit, u.project
		it.Key = unitKey(u.env, u.project, u.service, u.it.Deployment)
		out = append(out, it)
	}
	return out, nil
}

// specFromRun reads the watch spec of a deployment from its run's deploy
// step; an empty spec when it cannot be.
func (e *Engine) specFromRun(runID, depID string) (deploy.Spec, error) {
	evs, _, err := loadEvents(e.db, runID)
	if err != nil {
		return deploy.Spec{}, err
	}
	st := e.fold(evs)
	if st == nil {
		return deploy.Spec{}, nil
	}
	id, _ := st.deploymentStep(depID)
	if id == "" {
		// A rollback run's deployment is watched like any other.
		for _, rid := range st.Order {
			if rs := st.Steps[rid]; rs.Dep.Opened() && rs.Dep.ID == depID && st.Pipeline.Step(rid).IsRestore() {
				return deploy.SpecFor(pipeline.KindDeploy, st.Pipeline.Step(rid), ""), nil
			}
		}
		return deploy.Spec{}, nil
	}
	return deploy.SpecFor(pipeline.KindDeploy, st.Pipeline.Step(id), ""), nil
}

// notePodHash records a deployment's pod template hash the first time a
// snapshot shows it, and returns the one recorded.
func (e *Engine) notePodHash(depID, kind string, snap deploy.Snapshot) (string, error) {
	if !e.knows(schemaControl) {
		return "", nil
	}
	var hash string
	if err := e.db.QueryRow(`SELECT pod_hash FROM deployments WHERE id = ?`, depID).Scan(&hash); err != nil {
		return "", err
	}
	if hash != "" || kind == pipeline.KindRollback {
		return hash, nil
	}
	ours := deploy.OurPodHash(snap)
	if ours == "" {
		return "", nil
	}
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`UPDATE deployments SET pod_hash = ? WHERE id = ? AND pod_hash = ''`, ours, depID); err != nil {
		return "", err
	}
	err := e.db.QueryRow(`SELECT pod_hash FROM deployments WHERE id = ?`, depID).Scan(&hash)
	return hash, err
}

// rejectOnAbort rejects the approvals waiting that name a deployment
// whose rollout a snapshot shows aborted.
func (e *Engine) rejectOnAbort(runID, depID string, snap deploy.Snapshot, hash string) error {
	if !deploy.AbortedOn(snap, hash) {
		return nil
	}
	err := e.withRun(runID, func(c *cmd) error {
		deployID, ds := c.st.deploymentStep(depID)
		if ds == nil || ds.Dep.Closed || !c.st.waitingLinked(deployID) {
			return nil
		}
		reason := "rejected automatically: the rollout was aborted"
		if m := snap.Rollout.Message; m != "" {
			reason += " (" + m + ")"
		}
		c.rejectLinked(depID, reason)
		c.advance()
		return nil
	})
	if errors.Is(err, ErrNeedsNewer) {
		return nil
	}
	return err
}

// observeOpen takes an observation of an open deployment no step watches.
func (e *Engine) observeOpen(r RunnerInfo, held []string, o Observation, parts []string) (ObserveResult, error) {
	res := ObserveResult{Key: o.Key}
	depID, runID := parts[0], parts[1]
	snap, err := json.Marshal(o.Snapshot)
	if err != nil {
		return res, err
	}
	if len(snap) > maxSnapshotBytes {
		res.Reason = fmt.Sprintf("the snapshot is larger than %d KiB", maxSnapshotBytes>>10)
		return res, nil
	}
	var target, phase, detail, stored string
	if err := e.db.QueryRow(`SELECT target, phase, detail, snapshot FROM deployments WHERE id = ? AND run_id = ?`, depID, runID).
		Scan(&target, &phase, &detail, &stored); err != nil {
		res.Reason = "no such deployment"
		return res, nil
	}
	if !slices.Contains(held, target) {
		res.Reason = "this runner is not the observer of target " + target
		return res, nil
	}
	evs, _, err := loadEvents(e.db, runID)
	if err != nil {
		return res, err
	}
	st := e.fold(evs)
	if st == nil || needsNewer(st) != nil {
		res.Reason = "the run is not watched by this server"
		return res, nil
	}
	deployID, _ := st.deploymentStep(depID)
	item, ok := openWatchItemOf(st, deployID)
	if !ok || item.Key != o.Key {
		res.Reason = "the deployment is no longer watched for this"
		return res, nil
	}
	hash, err := e.notePodHash(depID, pipeline.KindDeploy, o.Snapshot)
	if err != nil {
		return res, err
	}
	v := deploy.Evaluate(item.Spec, o.Snapshot)
	if v.Outcome == deploy.Succeeded && (v.Phase == deploy.PhaseHealthy || v.Phase == deploy.PhaseLeftovers) {
		v.Detail = "the rollout is complete; " + v.Detail
	}
	res.Accepted, res.Phase, res.Detail = true, v.Phase, v.Detail
	if err := e.storeObservation(depID, deployID, r, snap, stored, phase, detail, v); err != nil {
		return res, err
	}
	return res, e.rejectOnAbort(runID, depID, o.Snapshot, hash)
}

// LiveView is what deploy runners last saw of an idle service.
type LiveView struct {
	Deployment string          `json:"deployment"` // the deployment it was compared with
	Snapshot   json.RawMessage `json:"snapshot,omitempty"`
	ObservedAt int64           `json:"observed_at"`
	ObservedBy string          `json:"observed_by"`
	// Drift is "" (none), out_of_sync (Argo CD says the live state differs
	// from Git), differs (what runs is not the deployment's version) or
	// missing (the application is gone); DriftDetail says how.
	Drift       string `json:"drift,omitempty"`
	DriftDetail string `json:"drift_detail,omitempty"`
}

// Drift values.
const (
	DriftOutOfSync = "out_of_sync"
	DriftDiffers   = "differs"
	DriftMissing   = "missing"
)

// driftOf says whether what a snapshot shows differs from a deployment
// whose pod template hash is hash. A snapshot that saw nothing says
// nothing.
func driftOf(s deploy.Snapshot, hash string) (string, string) {
	if s.Error != "" || s.App == nil {
		return "", ""
	}
	app := s.App
	switch {
	case !app.Found:
		return DriftMissing, "the Argo CD application does not exist"
	case app.Contains != nil && !*app.Contains:
		return DriftDiffers, fmt.Sprintf("Argo CD is at %s, which does not include the deployment's commit", shortRev(app.Revision))
	}
	if ro := s.Rollout; ro != nil && ro.Found && hash != "" && ro.ObservedKnown {
		switch {
		case ro.CurrentPodHash != "" && ro.CurrentPodHash != hash:
			return DriftDiffers, fmt.Sprintf("the rollout runs pod template %s, not the deployment's %s", ro.CurrentPodHash, hash)
		case ro.StableRS != "" && ro.StableRS != hash:
			return DriftDiffers, fmt.Sprintf("the rollout's stable pods are %s, not the deployment's %s", ro.StableRS, hash)
		}
	}
	if app.Sync == "OutOfSync" {
		return DriftOutOfSync, "Argo CD reports that what runs differs from Git (OutOfSync)"
	}
	return "", ""
}

func shortRev(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	if s == "" {
		return "an unknown revision"
	}
	return s
}

// observeUnit takes an observation of an idle service.
func (e *Engine) observeUnit(r RunnerInfo, held []string, o Observation, parts []string) (ObserveResult, error) {
	res := ObserveResult{Key: o.Key}
	env, project, service, depID := parts[1], parts[2], parts[3], parts[4]
	snap, err := json.Marshal(o.Snapshot)
	if err != nil {
		return res, err
	}
	if len(snap) > maxSnapshotBytes {
		res.Reason = fmt.Sprintf("the snapshot is larger than %d KiB", maxSnapshotBytes>>10)
		return res, nil
	}
	var target, hash string
	if err := e.db.QueryRow(`SELECT target, pod_hash FROM deployments WHERE id = ?`, depID).Scan(&target, &hash); err != nil {
		res.Reason = "no such deployment"
		return res, nil
	}
	if !slices.Contains(held, target) {
		res.Reason = "this runner is not the observer of target " + target
		return res, nil
	}
	drift, detail := driftOf(o.Snapshot, hash)
	unlock := e.writeLock()
	defer unlock()
	result, err := e.db.Exec(`UPDATE environment_state SET snapshot = ?, observed_at = ?, observed_by = ?, observed_deployment = ?,
			drift = ?, drift_detail = ?
		WHERE environment = ? AND project = ? AND service = ? AND current_deployment = ?`,
		string(snap), e.now().UnixMilli(), r.Name, depID, drift, detail, env, project, service, depID)
	if err != nil {
		return res, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		res.Reason = "the service runs another deployment now"
		return res, nil
	}
	res.Accepted, res.Phase, res.Detail = true, nonEmpty(drift, "in_line"), detail
	return res, nil
}

// unitLive adds what was last seen of idle services to their views.
func (e *Engine) unitLive(get func(env, project, service string) *UnitView) error {
	if !e.knows(schemaControl) {
		return nil
	}
	rows, err := e.db.Query(`SELECT environment, project, service, observed_deployment, snapshot, observed_at, observed_by, drift, drift_detail
		FROM environment_state WHERE observed_at > 0 AND observed_deployment = current_deployment`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var env, project, service, snap string
		var l LiveView
		if err := rows.Scan(&env, &project, &service, &l.Deployment, &snap, &l.ObservedAt, &l.ObservedBy, &l.Drift, &l.DriftDetail); err != nil {
			return err
		}
		if snap != "" {
			l.Snapshot = json.RawMessage(snap)
		}
		if u := get(env, project, service); u != nil {
			u.Live = &l
		}
	}
	return rows.Err()
}

// ownAnalysis keeps a snapshot's analysis runs that belong to the
// deployment a watch key names (see deploy.OwnAnalysis).
func (e *Engine) ownAnalysis(parts []string, s deploy.Snapshot) deploy.Snapshot {
	if len(s.Analysis) == 0 {
		return s
	}
	depID := parts[0]
	if parts[0] == WatchUnit {
		depID = parts[4]
	}
	var since int64
	if err := e.db.QueryRow(`SELECT opened_at FROM deployments WHERE id = ?`, depID).Scan(&since); err != nil {
		since = 0
	}
	return deploy.OwnAnalysis(s, since)
}
