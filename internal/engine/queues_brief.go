package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// attachNeeds gives each step brief the steps it needs, read from the
// pipeline each run was created with (the graph's edges), in one query.
func (e *Engine) attachNeeds(runs []*RunSummary) {
	if len(runs) == 0 {
		return
	}
	args := make([]any, len(runs))
	marks := make([]string, len(runs))
	byID := map[string]*RunSummary{}
	for i, r := range runs {
		args[i], marks[i], byID[r.ID] = r.ID, "?", r
	}
	rows, err := e.db.Query(`SELECT run_id, data FROM events WHERE type = ? AND run_id IN (`+strings.Join(marks, ",")+`)`, append([]any{RunCreated}, args...)...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, data string
		if rows.Scan(&id, &data) != nil {
			return
		}
		var p struct {
			Pipeline struct {
				Steps []struct {
					ID    string   `json:"id"`
					Needs []string `json:"needs"`
				} `json:"steps"`
			} `json:"pipeline"`
		}
		if json.Unmarshal([]byte(data), &p) != nil {
			continue
		}
		needs := map[string][]string{}
		for _, s := range p.Pipeline.Steps {
			needs[s.ID] = s.Needs
		}
		r := byID[id]
		for i := range r.Steps {
			r.Steps[i].Needs = needs[r.Steps[i].ID]
		}
	}
	_ = rows.Err() // a brief without its needs is still a brief
}

// decorateWait says on a queued step's view what it waits for, with a stable
// code beside the sentence: room (the step namespace's quota, nodes with
// space), a runner, or the pause of a retry.
func (e *Engine) decorateWait(v *StepView, st *RunState, def *pipeline.Step) {
	if v.Status != StepQueuedS || !e.knows(schemaQueues) {
		return
	}
	s := st.Steps[v.ID]
	switch {
	case s != nil && s.WaitCode != "":
		v.WaitingFor, v.WaitingCode = e.explainWait(st, s), s.WaitCode
	case v.WaitingFor != "":
		v.WaitingCode = WaitNoRunner // the step has a runner of its own and none may take it (see waitingFor)
	case v.NotBefore > e.now().UnixMilli():
		v.WaitingFor, v.WaitingCode = "waiting out the pause before trying again", WaitBackoff
	case targetOf(s) != "":
		// A deploy, rollback or verify step is taken by a deploy runner serving its target, not by a build runner.
		v.WaitingFor, v.WaitingCode = e.deployWait(targetOf(s))
	default:
		v.WaitingFor, v.WaitingCode = e.runnerWait()
	}
}

// runnerWait says why a queued step has not been taken: no runner has been
// seen lately, or every runner is busy. Empty when a runner is free (the step
// is about to start).
func (e *Engine) runnerWait() (string, string) {
	rows, err := e.db.Query(`SELECT r.id, (SELECT COUNT(*) FROM leases l WHERE l.runner = r.id) FROM runners r
		WHERE r.last_seen >= ? AND r.kind <> ?`, e.now().Add(-RunnerOffline).UnixMilli(), PoolDeploy)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	total, busy := 0, 0
	for rows.Next() {
		var id string
		var n int
		if rows.Scan(&id, &n) != nil {
			return "", ""
		}
		total++
		if n > 0 {
			busy++
		}
	}
	if rows.Err() != nil {
		return "", ""
	}
	switch {
	case total == 0:
		return "waiting for a runner: none has been seen lately", WaitNoRunner
	case busy == total:
		return fmt.Sprintf("waiting for a free runner: all %d are busy", total), WaitRunnersBusy
	}
	return "", ""
}
