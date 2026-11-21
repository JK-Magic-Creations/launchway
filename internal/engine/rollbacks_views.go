package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// DeploymentRollback is what a deployment made by a rollback run restored.
type DeploymentRollback struct {
	Headline    string `json:"headline"`
	ToVersion   string `json:"to_version"`
	FromVersion string `json:"from_version"`
	Actor       string `json:"actor"`
	Reason      string `json:"reason"`
	Emergency   bool   `json:"emergency,omitempty"`
	// NeedsReview: an approver has not acknowledged it yet.
	NeedsReview bool        `json:"needs_review,omitempty"`
	Review      *ReviewView `json:"review,omitempty"`
}

// addRollbackInfo adds to deployments what schema 32 knows of them: that a
// rollback made them or undid them, and what a person may do with each.
func (e *Engine) addRollbackInfo(vs []DeploymentView) error {
	if !e.knows(schemaRollback) || len(vs) == 0 {
		return nil
	}
	type key struct{ project, env, service string }
	type unitInfo struct {
		plan  *RollbackPlan
		stack []unitDep
		err   error
	}
	units := map[key]*unitInfo{}
	for i := range vs {
		v := &vs[i]
		if v.Mode == "rollback" {
			var rb DeploymentRollback
			var emergency, required int
			var reviewedAt int64
			var reviewedBy, comment string
			err := e.db.QueryRow(`SELECT from_version, to_version, actor, reason, emergency, review_required, reviewed_by, reviewed_at, review_comment
				FROM rollbacks WHERE run_id = ?`, v.RunID).Scan(&rb.FromVersion, &rb.ToVersion, &rb.Actor, &rb.Reason, &emergency, &required, &reviewedBy, &reviewedAt, &comment)
			if err == nil {
				rb.Emergency = emergency != 0
				rb.NeedsReview = required != 0 && reviewedAt == 0
				if reviewedAt > 0 {
					rb.Review = &ReviewView{By: reviewedBy, At: reviewedAt, Comment: comment}
				}
				rb.Headline = RollbackHeadline(&pipeline.Restore{Emergency: rb.Emergency, Actor: rb.Actor, Reason: rb.Reason,
					From: pipeline.RestoreFrom{Version: rb.FromVersion}, To: pipeline.RestoreTo{Version: rb.ToVersion}}, v.Project, v.Environment, v.Service)
				v.Rollback = &rb
			}
		}
		by, err := e.rollbackRefs(`SELECT r.run_id, r.run_number, r.actor, r.reason FROM rollback_reverts v JOIN rollbacks r ON r.run_id = v.run_id
			WHERE v.deployment = ? ORDER BY r.run_number`, v.ID)
		if err != nil {
			return err
		}
		v.RolledBackBy = by
		k := key{v.Project, v.Environment, v.Service}
		u := units[k]
		if u == nil {
			u = &unitInfo{}
			u.plan, _, u.stack, u.err = e.rollbackPlan(k.project, k.env, k.service)
			units[k] = u
		}
		if u.err != nil {
			continue // an environment no longer configured: no action is offered
		}
		v.Actions = []Action{deploymentRollbackAction(v, u.plan, u.stack)}
	}
	return nil
}

// deploymentRollbackAction is the rollback action of one deployment: the
// unit's own when the deployment is what the service runs now, and why not
// otherwise.
func deploymentRollbackAction(v *DeploymentView, plan *RollbackPlan, stack []unitDep) Action {
	no := func(reason, instead string) Action {
		return Action{Name: "rollback", Who: "operator", Reason: reason, Instead: instead}
	}
	where := fmt.Sprintf("roll back %s in %s from the environment or application page", v.Service, v.Environment)
	switch {
	case v.Mode == "rollback":
		return no("this deployment was itself a rollback", where+" if it needs to go further back")
	case v.DeployedAt == 0:
		return no("this deployment did not succeed, so there is nothing of it to roll back", "")
	case len(v.RolledBackBy) > 0:
		return no(fmt.Sprintf("this deployment was already rolled back by run #%d", v.RolledBackBy[0].Number), "")
	case v.Verdict == VerdictRolledBack:
		return no("this deployment was reverted by its own pipeline's rollback step", "")
	}
	idx := -1
	for i, s := range stack {
		if s.ID == v.ID {
			idx = i
		}
	}
	switch {
	case idx < 0:
		return no("this deployment is no longer part of what the service runs", "")
	case idx != len(stack)-1:
		return no(fmt.Sprintf("%s runs %s now, which was deployed after this; this deployment is not what to roll back from", v.Service, stack[len(stack)-1].Version), where)
	}
	return plan.Action
}
