package store

import "encoding/json"

// What the person notifier needs to know of a run: who started it, who
// approved steps of it, and how the run before it ended. Read-only; the
// statements run on the connection the engine hands in.

// RunActor says who started a run.
func RunActor(q Queryer, runID string) (actor string) {
	rows, err := q.Query(`SELECT actor FROM runs WHERE id = ?`, runID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	if rows.Next() {
		_ = rows.Scan(&actor)
	}
	return
}

// RunApprovers lists the people who approved steps of a run, from its events.
func RunApprovers(q Queryer, runID string) []string {
	rows, err := q.Query(`SELECT data FROM events WHERE run_id = ? AND type IN ('step.approved', 'step.approval_recorded') ORDER BY seq LIMIT 50`, runID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var data string
		if rows.Scan(&data) != nil {
			continue
		}
		var p struct {
			Actor     string `json:"actor"`
			Approvals []struct {
				Actor string `json:"actor"`
			} `json:"approvals"`
		}
		if json.Unmarshal([]byte(data), &p) != nil {
			continue
		}
		names := []string{p.Actor}
		for _, a := range p.Approvals {
			names = append(names, a.Actor)
		}
		for _, n := range names {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// PriorRunStatus returns how the run before a numbered run on the same branch
// of the same Flight ended (succeeded or failed), or "" if there is none.
func PriorRunStatus(q Queryer, project, branch string, number int) (status string) {
	rows, err := q.Query(`SELECT status FROM runs WHERE project = ? AND branch = ? AND number < ? AND status IN ('succeeded', 'failed') ORDER BY number DESC LIMIT 1`, project, branch, number)
	if err != nil {
		return ""
	}
	defer rows.Close()
	if rows.Next() {
		_ = rows.Scan(&status)
	}
	return
}
