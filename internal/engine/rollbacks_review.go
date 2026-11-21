package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// persistRollback writes what schema 32 derives from a rollback run's
// events: its row in rollbacks and the list of deployments it undoes, the
// deployment it opened, and a review.
func (c *cmd) persistRollback() error {
	st := c.st
	rs := st.restoreStep()
	if rs == nil {
		return nil
	}
	r := rs.Restore
	for _, ev := range c.events {
		var err error
		switch ev.Type {
		case RunCreated:
			bi := func(b bool) int {
				if b {
					return 1
				}
				return 0
			}
			_, err = c.tx.Exec(`INSERT INTO rollbacks(run_id, project, run_number, environment, service, to_deployment, to_version, to_run_id,
					to_run_number, from_deployment, from_version, reason, actor, emergency, review_required, created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING`,
				st.ID, st.Project, st.Number, rs.Environment, rs.Service, r.To.Deployment, r.To.Version, r.To.Run, r.To.RunNumber,
				r.From.Deployment, r.From.Version, r.Reason, r.Actor, bi(r.Emergency), bi(r.ReviewRequired), ev.At)
			for i, rv := range r.Reverts {
				if err != nil {
					break
				}
				_, err = c.tx.Exec(`INSERT INTO rollback_reverts(run_id, deployment, position) VALUES(?,?,?) ON CONFLICT(run_id, deployment) DO NOTHING`,
					st.ID, rv.Deployment, i)
			}
		case DeployOpened:
			if ev.Data.Step == rs.ID {
				_, err = c.tx.Exec(`UPDATE rollbacks SET deployment = ? WHERE run_id = ?`, ev.Data.Deploy.ID, st.ID)
			}
		case RunReviewed:
			_, err = c.tx.Exec(`UPDATE rollbacks SET reviewed_by = ?, reviewed_at = ?, review_comment = ? WHERE run_id = ?`,
				ev.Data.Actor, ev.At, ev.Data.Comment, st.ID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ReviewView is an approver's acknowledgment.
type ReviewView struct {
	By      string `json:"by"`
	At      int64  `json:"at"`
	Comment string `json:"comment"`
}

// RollbackRunView is what a rollback run is, for its page.
type RollbackRunView struct {
	Environment string `json:"environment"`
	Service     string `json:"service"`
	// Headline says it in one sentence: "Emergency rollback of dev: v1.4.0 →
	// v1.3.2, by ann: the reason".
	Headline       string                   `json:"headline"`
	To             pipeline.RestoreTo       `json:"to"`
	From           pipeline.RestoreFrom     `json:"from"`
	Reverts        []pipeline.RestoreRevert `json:"reverts"`
	Reason         string                   `json:"reason"`
	Actor          string                   `json:"actor"`
	Emergency      bool                     `json:"emergency"`
	ReviewRequired bool                     `json:"review_required"`
	Review         *ReviewView              `json:"review,omitempty"`
	// Note says what a rollback does not do.
	Note string `json:"note"`
}

// RollbackNote is said wherever a rollback is confirmed or reported.
const RollbackNote = "A rollback restores the version only. It never restores a database or configuration."

// RunRef names a run.
type RunRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Actor  string `json:"actor,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func (st *RunState) rollbackView() *RollbackRunView {
	rs := st.restoreStep()
	if rs == nil {
		return nil
	}
	r := rs.Restore
	v := &RollbackRunView{Environment: rs.Environment, Service: rs.Service, Headline: RollbackHeadline(r, st.Project, rs.Environment, rs.Service),
		To: r.To, From: r.From, Reverts: r.Reverts, Reason: r.Reason, Actor: r.Actor, Emergency: r.Emergency, ReviewRequired: r.ReviewRequired,
		Note: RollbackNote}
	if v.Reverts == nil {
		v.Reverts = []pipeline.RestoreRevert{}
	}
	if st.Review != nil {
		v.Review = &ReviewView{By: st.Review.By, At: st.Review.At, Comment: st.Review.Comment}
	}
	return v
}

// rolledBackBy lists the rollback runs that undid a deployment of the run,
// for the original run's "rolled back later by run #N".
func (e *Engine) rolledBackBy(runID string) ([]RunRef, error) {
	return e.rollbackRefs(`SELECT DISTINCT r.run_id, r.run_number, r.actor, r.reason FROM rollback_reverts v
		JOIN rollbacks r ON r.run_id = v.run_id JOIN deployments d ON d.id = v.deployment WHERE d.run_id = ? ORDER BY r.run_number`, runID)
}

func (e *Engine) rollbackRefs(q string, args ...any) ([]RunRef, error) {
	if !e.knows(schemaRollback) {
		return nil, nil
	}
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunRef
	for rows.Next() {
		var r RunRef
		if err := rows.Scan(&r.ID, &r.Number, &r.Actor, &r.Reason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// reviewRefusal says why a person may not acknowledge a rollback run, or "".
func (e *Engine) reviewRefusal(st *RunState, actor string, groups []string) string {
	rs := st.restoreStep()
	env := e.live().envs[rs.Environment]
	if env.Reviewers.Restricts() && !env.Reviewers.Allows(actor, groups) {
		return fmt.Sprintf("only %s may acknowledge a rollback of %s", env.Reviewers.Describe(), rs.Environment)
	}
	if !st.selfApproval(e) && actor != "" && actor == st.Trigger.Actor {
		return "you asked for this rollback, so someone else must review it (approvals.allow_self is off)"
	}
	return ""
}

// AcknowledgeRollback records an approver's review of an emergency rollback,
// with the comment that says what they looked at. It refuses what is not
// waiting for review, and a person who may not review it.
func (e *Engine) AcknowledgeRollback(runID, actor string, groups []string, comment string) error {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return fmt.Errorf("%w: say what you checked: a review needs a comment, which is recorded", ErrInvalid)
	}
	if len(comment) > maxReasonLen {
		return fmt.Errorf("%w: the comment is longer than %d characters", ErrInvalid, maxReasonLen)
	}
	return e.withRun(runID, func(c *cmd) error {
		st := c.st
		rs := st.restoreStep()
		switch {
		case rs == nil:
			return fmt.Errorf("%w: run #%d is not a rollback run", ErrConflict, st.Number)
		case !rs.Restore.ReviewRequired:
			return fmt.Errorf("%w: run #%d does not need review", ErrConflict, st.Number)
		case st.Review != nil:
			return fmt.Errorf("%w: run #%d was already reviewed by %s", ErrConflict, st.Number, st.Review.By)
		}
		if why := c.e.reviewRefusal(st, actor, groups); why != "" {
			return fmt.Errorf("%w: %s", ErrForbidden, why)
		}
		c.emit(RunReviewed, Payload{Actor: actor, Comment: comment})
		return nil
	})
}

// ReviewItem is an emergency rollback waiting for review.
type ReviewItem struct {
	Run         string `json:"run"`
	Number      int    `json:"number"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Reason      string `json:"reason"`
	Actor       string `json:"actor"`
	At          int64  `json:"at"`
	// Acknowledge is whether the person asking may acknowledge it now, and
	// if not, why.
	Acknowledge Action `json:"acknowledge"`
}

// RollbacksNeedingReview lists the emergency rollbacks no approver has
// acknowledged yet, newest first, with what the person asking may do with
// each (their role on the project is checked by the caller).
func (e *Engine) RollbacksNeedingReview(actor string, groups []string, visible func(project string) bool) ([]ReviewItem, error) {
	out := []ReviewItem{}
	if !e.knows(schemaRollback) {
		return out, nil
	}
	rows, err := e.db.Query(`SELECT run_id, run_number, project, environment, service, from_version, to_version, reason, actor, created_at
		FROM rollbacks WHERE review_required = 1 AND reviewed_at = 0 ORDER BY created_at DESC, run_id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	all, err := scanReviewRows(rows, visible)
	if err != nil {
		return nil, err
	}
	for _, it := range all {
		it.Acknowledge = Action{Name: "acknowledge", Who: "operator", Available: true}
		err := e.readRun(it.Run, func(c *cmd) error {
			if why := c.e.reviewRefusal(c.st, actor, groups); why != "" {
				it.Acknowledge.Available, it.Acknowledge.Reason = false, why
			}
			return nil
		})
		if errors.Is(err, ErrNeedsNewer) || errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// EnvironmentOfRollback returns the environment a rollback run is about, for
// the API's access checks.
func (e *Engine) EnvironmentOfRollback(runID string) (string, error) {
	var env string
	err := e.db.QueryRow(`SELECT environment FROM rollbacks WHERE run_id = ?`, runID).Scan(&env)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: run %s is not a rollback run", ErrConflict, runID)
	}
	return env, err
}

// scanReviewRows reads and closes the rows of the review query, keeping the projects the person may see.
func scanReviewRows(rows *sql.Rows, visible func(string) bool) ([]ReviewItem, error) {
	defer rows.Close()
	var all []ReviewItem
	for rows.Next() {
		var it ReviewItem
		if err := rows.Scan(&it.Run, &it.Number, &it.Project, &it.Environment, &it.Service, &it.FromVersion, &it.ToVersion, &it.Reason, &it.Actor, &it.At); err != nil {
			return nil, err
		}
		if visible == nil || visible(it.Project) {
			all = append(all, it)
		}
	}
	return all, rows.Err()
}
