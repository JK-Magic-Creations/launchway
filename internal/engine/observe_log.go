package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/deploy"
)

// logPhase writes a change of what a watched deployment is doing into the
// log of the step that watches it, so that the step's own log (the run page)
// tells what it is waiting for and why it failed, not only the timeline. It
// is best effort: a log line that cannot be written never fails an observation.
func (e *Engine) logPhase(tx *txx, depID, stepID string, now int64, v deploy.Verdict) {
	var runID string
	if err := tx.QueryRow(`SELECT run_id FROM deployments WHERE id = ?`, depID).Scan(&runID); err != nil {
		return
	}
	var attempt, seq int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(attempt), 1) FROM logs WHERE run_id = ? AND step_id = ?`, runID, stepID).Scan(&attempt); err != nil {
		return
	}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM logs WHERE run_id = ? AND step_id = ? AND attempt = ?`, runID, stepID, attempt).Scan(&seq); err != nil {
		return
	}
	mark := "watching"
	switch v.Outcome {
	case deploy.Failed:
		mark = "FAILED"
	case deploy.Succeeded:
		mark = "done"
	}
	text := fmt.Sprintf("[%s] %s: %s", mark, v.Phase, v.Detail)
	if len(text) > MaxLogLineBytes {
		text = text[:MaxLogLineBytes]
	}
	_, _ = tx.Exec(`INSERT INTO logs(run_id, step_id, attempt, seq, at, stream, text) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, runID, stepID, attempt, seq+1, now, "stdout", text)
}
