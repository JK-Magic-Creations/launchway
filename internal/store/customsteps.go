package store

// Which version of which custom step each step of a run came from (schema 68). custom_step_uses is a
// run-engine table, so until the engine moves into the store (stage S2) its statements run on the
// transaction or connection the engine hands in, as step_usage's do (usage.go). The rows name the run,
// which belongs to one Base.

// CustomStepUseRow is one row of custom_step_uses.
type CustomStepUseRow struct {
	Run, Step, Project, Name, Library, Commit, Digest string
	Network, Write, Secrets                           string // comma-separated lists
	At                                                int64
}

// SaveCustomStepUse records that a step of a run came from a custom step.
func SaveCustomStepUse(x Execer, r CustomStepUseRow) error {
	_, err := x.Exec(`INSERT INTO custom_step_uses(run_id, step_id, project, name, library, commit_sha, digest, network, write_paths, secrets, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.Run, r.Step, r.Project, r.Name, r.Library, r.Commit, r.Digest, r.Network, r.Write, r.Secrets, r.At)
	return err
}

// CustomStepUse is how a Flight uses one version of a library's custom step.
type CustomStepUse struct {
	Project, Commit, Digest string
	Runs                    int
	LastRunAt               int64
	LastRunID               string
}

// CustomStepUses lists which Flights ran which versions of a library's step, newest use first.
func CustomStepUses(q Queryer, library, name string, limit int) ([]CustomStepUse, error) {
	rows, err := q.Query(`SELECT project, commit_sha, digest, count(DISTINCT run_id), max(created_at)
		FROM custom_step_uses WHERE library = ? AND name = ? GROUP BY project, commit_sha, digest
		ORDER BY max(created_at) DESC LIMIT ?`, library, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomStepUse{}
	for rows.Next() {
		var u CustomStepUse
		if err := rows.Scan(&u.Project, &u.Commit, &u.Digest, &u.Runs, &u.LastRunAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// LastCustomStepRun is the latest run of a Flight that used a version of a step.
func LastCustomStepRun(q Queryer, library, name, project, commit string) string {
	rows, err := q.Query(`SELECT run_id FROM custom_step_uses WHERE library = ? AND name = ? AND project = ? AND commit_sha = ? ORDER BY created_at DESC LIMIT 1`,
		library, name, project, commit)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var id string
	if rows.Next() {
		_ = rows.Scan(&id)
	}
	return id
}
