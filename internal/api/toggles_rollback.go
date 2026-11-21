package api

import (
	"fmt"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/togglestore"
)

// Toggles and rollbacks. A rollback restores a version, and a toggle that was
// changed since that version went live is left behind unless somebody notices.
// So the rollback page lists the toggle changes made in the environment since
// the version being restored was deployed, and the person may revert them with
// the rollback (revert_toggles: true) or separately (POST
// .../toggle-revert-since). Reverting a toggle is a retreat: it needs no approval.

// rollbackToggles is what the rollback options say about toggles.
type rollbackToggles struct {
	// Since is when the version the rollback would restore went live (Unix ms).
	Since int64 `json:"since"`
	// Changes are the toggle changes made in the environment after that, newest
	// first, that are still in effect.
	Changes []togglestore.Change `json:"changes"`
	Note    string               `json:"note"`
}

// rollbackTarget picks the deployment a rollback would restore: the one named,
// or else the previous successful one.
func rollbackTarget(plan *engine.RollbackPlan, to string) *engine.RollbackTarget {
	for i := range plan.Action.Targets {
		t := &plan.Action.Targets[i]
		if (to != "" && (t.Deployment == to || t.Run == to)) || (to == "" && t.Previous) {
			return t
		}
	}
	if to == "" && len(plan.Action.Targets) > 0 {
		return &plan.Action.Targets[0]
	}
	return nil
}

// withToggles adds the toggles' part to a rollback plan.
func (s *Server) withToggles(r *http.Request, plan *engine.RollbackPlan) any {
	if s.Toggles == nil || len(plan.Action.Targets) == 0 {
		return plan
	}
	t := rollbackTarget(plan, r.URL.Query().Get("to"))
	if t == nil {
		return plan
	}
	changes, err := s.Toggles.ChangesSince(r.Context(), plan.Project, plan.Environment, t.At)
	if err != nil {
		s.Log.Error("toggle changes for a rollback page", "err", err)
		return plan
	}
	note := "None changed since then."
	if len(changes) > 0 {
		note = fmt.Sprintf("%d toggle change(s) were made here after version %s went live. A rollback restores the version only: revert them with it, or on their own, if they belong to what you are undoing.", len(changes), t.Version)
	}
	return struct {
		*engine.RollbackPlan
		Toggles rollbackToggles `json:"toggles"`
	}{plan, rollbackToggles{Since: t.At, Changes: changes, Note: note}}
}

// toggleSince is when the version a rollback restores went live.
func (s *Server) toggleSince(project, env, service, to string) (int64, bool) {
	if s.Toggles == nil {
		return 0, false
	}
	plan, err := s.Engine.RollbackPlan(project, env, service)
	if err != nil {
		return 0, false
	}
	if t := rollbackTarget(plan, to); t != nil {
		return t.At, true
	}
	return 0, false
}
