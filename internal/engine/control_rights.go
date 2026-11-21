package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// rightsFresh is how long a deploy runner's self-check is believed.
const rightsFresh = 5 * 60 * 1000

// cannotControl says why a command would fail: the deploy runner that
// observed the deployment reported, in its latest observation, that the
// cluster does not let it change the rollout's status. It is empty when it
// may, when nothing was reported, or when the report is old.
func (r deploymentRow) cannotControl(now int64) string {
	ri := r.snap.Rights
	if ri == nil || ri.Control == nil || *ri.Control || r.observedAt == 0 || now-r.observedAt > rightsFresh {
		return ""
	}
	return nonEmpty(ri.ControlWhy, "the deploy runner reports that it may not change the rollout's status")
}

// rightsError is a conflict because the deploy runner may not apply the
// command (it says what an administrator must enable).
type rightsError struct{ why string }

func (e rightsError) Error() string        { return ErrConflict.Error() + ": " + e.why }
func (e rightsError) Is(target error) bool { return target == ErrConflict }

// CommandByTake returns the command a deploy runner's take belongs to.
func (e *Engine) CommandByTake(take string) (*CommandView, error) {
	var id string
	err := e.db.QueryRow(`SELECT id FROM deploy_commands WHERE take_id = ?`, take).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: take %s", ErrNotFound, take)
	}
	if err != nil {
		return nil, err
	}
	return e.command(id)
}

// Control is whether a command can be given now, and why not.
type Control struct {
	Action    string `json:"action"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // why not, in plain words
	// Forbidden says the reason is who is asking (protected environment),
	// not where the rollout is.
	Forbidden bool `json:"forbidden,omitempty"`
	// RunnerCannot says the reason is the deploy runner: the cluster does
	// not let it change the rollout's status. Reason says what an
	// administrator must enable.
	RunnerCannot bool `json:"runner_cannot,omitempty"`
}

// Controls says, for each command, whether actor could give it to a
// deployment now, as far as the deployment and the protected-environment
// rule decide; the caller adds the person's role.
func (e *Engine) Controls(runID, depID, actor string) (env Environment, out []Control, err error) {
	if !e.knows(schemaControl) {
		return env, nil, fmt.Errorf("%w: this server does not know commands on deployments", ErrConflict)
	}
	err = e.readRun(runID, func(c *cmd) error {
		_, ds := c.st.deploymentStep(depID)
		if ds == nil {
			return fmt.Errorf("%w: deployment %s in run %s", ErrNotFound, depID, runID)
		}
		env = c.e.live().envs[ds.Dep.Environment]
		for _, a := range pipeline.RolloutActions {
			ctl := Control{Action: a, Available: true}
			if _, _, err := c.commandRefusal(depID, a, actor); err != nil {
				var re rightsError
				switch {
				case errors.As(err, &re):
					ctl.Available, ctl.RunnerCannot, ctl.Reason = false, true, re.why
				case errors.Is(err, ErrForbidden):
					ctl.Available, ctl.Forbidden, ctl.Reason = false, true, strings.TrimPrefix(err.Error(), ErrForbidden.Error()+": ")
				case errors.Is(err, ErrConflict):
					ctl.Available, ctl.Reason = false, strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")
				default:
					return err
				}
			}
			out = append(out, ctl)
		}
		return nil
	})
	return env, out, err
}
