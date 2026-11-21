package engine

// Steps with a runner and a workspace of their own (schema 27, see
// pipeline.Step.Runner): leased to any runner that matches the step's own
// selector, whoever holds the run, without taking the run over.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// ownWorkspace names the workspace of a step that chose its runner: derived
// from the run and the step, so that retries of that step reuse it and
// nothing else does.
func ownWorkspace(runID, stepID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + stepID))
	return "own-" + hex.EncodeToString(sum[:])[:16]
}

// leaseOwn leases a step with its own runner to r, if r matches: its pool and
// the step's selector, the project's and event's rules, and the features the
// step needs. It never moves the run to r.
func (c *cmd) leaseOwn(r RunnerInfo, cd leaseCand, s *StepState, def *pipeline.Step) (*Job, error) {
	if !c.runnerCan(r, cd.step) || c.e.refusesRun(r, c.st.Project, c.st.Trigger.Event, def.Runner) != "" {
		return nil, nil // checked in SQL too
	}
	leaseID := ids.New("lease")
	if _, err := c.tx.Exec(`INSERT INTO leases(id, run_id, step_id, attempt, runner, expires_at) VALUES(?,?,?,?,?,?)`,
		leaseID, cd.run, cd.step, s.Attempt, r.ID, c.leaseUntil()); err != nil {
		return nil, err
	}
	c.emit(StepLeased, Payload{Step: cd.step, Attempt: s.Attempt, Lease: leaseID, Runner: r.ID, Inputs: c.inputs(cd.step)})
	job := c.job(cd.step, leaseID)
	job.Workspace = ownWorkspace(cd.run, cd.step)
	return job, c.addArtifactsToJob(job)
}

// persistStepRunners writes the selectors of a new run's steps that chose
// their runner, so that leasing can match them in SQL.
func (c *cmd) persistStepRunners() error {
	for _, ev := range c.events {
		if ev.Type != RunCreated {
			continue
		}
		for _, s := range c.st.Pipeline.Steps {
			if s.Runner == nil {
				continue
			}
			if _, err := c.tx.Exec(`UPDATE steps SET own_workspace = 1, runner_pool = ?, runner_labels = ? WHERE run_id = ? AND step_id = ?`,
				s.Runner.Pool, labelText(s.Runner.Labels), c.st.ID, s.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckStepRunners refuses a run whose steps ask for a pool that does not
// exist, is not a build pool, or does not serve the project and the event.
func (e *Engine) CheckStepRunners(project, event string, p *pipeline.Pipeline) pipeline.Problems {
	var out pipeline.Problems
	for _, s := range p.Steps {
		if s.Runner == nil || s.Runner.Pool == "" {
			continue
		}
		pool, ok := e.live().pools[s.Runner.Pool]
		why := ""
		switch {
		case !ok:
			why = "this server has no runner pool " + s.Runner.Pool
		case pool.Kind != PoolBuild:
			why = "runner pool " + pool.Name + " is a deploy pool: it takes deploy work only"
		default:
			why = e.poolServes(pool, project, event)
		}
		if why != "" {
			out = append(out, pipeline.Problem{Line: s.Line, Path: "steps." + s.ID + ".runner.pool", Message: fmt.Sprintf("%s: %s", s.ID, why)})
		}
	}
	return out
}

// waitingFor says what a queued step with its own runner waits for, when no
// runner seen lately may take it ("" when some runner may, or it is not such
// a step).
func (e *Engine) waitingFor(st *RunState, def *pipeline.Step) string {
	if def == nil || def.Runner == nil {
		return ""
	}
	rows, err := e.db.Query(`SELECT id, pool, kind, auth, labels, accept FROM runners WHERE last_seen >= ?`, e.now().Add(-RunnerOffline).UnixMilli())
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var id, pool, kind, auth, labels, acc string
		if rows.Scan(&id, &pool, &kind, &auth, &labels, &acc) != nil || kind == PoolDeploy || (kind == "" && pool != "") {
			continue
		}
		r := RunnerInfo{ID: id, Pool: pool, Kind: kind, Auth: auth, Labels: strings.Fields(labels), Accept: decodeAccept(acc)}
		if e.refusesRun(r, st.Project, st.Trigger.Event, def.Runner) == "" {
			return ""
		}
	}
	if rows.Err() != nil {
		return ""
	}
	want := "a runner"
	if def.Runner.Pool != "" {
		want += " of pool " + def.Runner.Pool
	}
	if len(def.Runner.Labels) > 0 {
		want += " with labels " + strings.Join(def.Runner.Labels, ", ")
	}
	return "waiting for " + want
}

// decorateOwnRunner fills a step view of a step with its own runner.
func (e *Engine) decorateOwnRunner(v *StepView, st *RunState, def *pipeline.Step) {
	if !def.HasOwnRunner() {
		return
	}
	v.Workspace, v.RunnerSelector = ownWorkspace(st.ID, def.ID), def.Runner
	if v.Status == StepQueuedS {
		v.WaitingFor = e.waitingFor(st, def)
	}
}

// leaseAI hands an ai step to a runner. An ai step needs no workspace: any
// runner that can call models takes it, and the run stays where it is. A
// step that cannot be built is failed and no job is returned.
func (c *cmd) leaseAI(r RunnerInfo, cd leaseCand, s *StepState, def *pipeline.Step) (*Job, error) {
	aj, why := c.aiJob(def)
	if why != "" {
		if c.err != nil {
			return nil, c.err
		}
		c.failStep(cd.step, why)
		c.advance()
		return nil, nil
	}
	leaseID := ids.New("lease")
	if _, err := c.tx.Exec(`INSERT INTO leases(id, run_id, step_id, attempt, runner, expires_at) VALUES(?,?,?,?,?,?)`,
		leaseID, cd.run, cd.step, s.Attempt, r.ID, c.leaseUntil()); err != nil {
		return nil, err
	}
	c.emit(StepLeased, Payload{Step: cd.step, Attempt: s.Attempt, Lease: leaseID, Runner: r.ID, Inputs: c.inputs(cd.step)})
	job := c.job(cd.step, leaseID)
	job.AI = aj
	return job, c.addArtifactsToJob(job)
}
