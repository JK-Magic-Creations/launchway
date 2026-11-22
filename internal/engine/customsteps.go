package engine

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
)

// persistCustomSteps records which version of which custom step each step of
// a new run came from (schema 68).
func (c *cmd) persistCustomSteps() error {
	for _, ev := range c.events {
		if ev.Type != RunCreated {
			continue
		}
		for _, s := range c.st.Pipeline.Steps {
			cs := s.Custom
			if cs == nil {
				continue
			}
			if _, err := c.tx.Exec(`INSERT INTO custom_step_uses(run_id, step_id, project, name, library, commit_sha, digest, network, write_paths, secrets, created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
				c.st.ID, s.ID, c.st.Project, cs.Name, cs.Library, cs.Commit, cs.Digest,
				strings.Join(cs.Network, ","), strings.Join(cs.Write, ","), strings.Join(cs.Secrets, ","), c.now); err != nil {
				return err
			}
		}
	}
	return nil
}

var _ = pipeline.SchemaCustomSteps

// keepNotes is what a runner said a custom step wrote, as far as the server
// keeps it: known kinds and levels, a bounded number and size, and a file
// that is a path inside the workspace. A runner's word is not trusted.
func keepNotes(in []StepNote) []StepNote {
	var out []StepNote
	bytes := 0
	for _, n := range in {
		if len(out) >= MaxStepNotes || bytes >= MaxStepNoteBytes {
			break
		}
		switch n.Kind {
		case NoteSummary:
			n.Level, n.File, n.Line = "", "", 0
		case NoteAnnotation:
			if n.Level != "note" && n.Level != "warning" && n.Level != "error" {
				n.Level = "note"
			}
			if n.File == "" || stepkit.CheckWorkspacePath(n.File) != "" || n.Line < 0 || n.Line > 1e7 {
				continue
			}
		default:
			continue
		}
		if len(n.Text) > 64<<10 || strings.TrimSpace(n.Text) == "" {
			continue
		}
		bytes += len(n.Text)
		out = append(out, n)
	}
	return out
}

// CustomStepUse is how a Flight uses one version of a custom step: how many
// runs, and the latest.
type CustomStepUse struct {
	Project   string `json:"project"`
	Commit    string `json:"commit"`
	Digest    string `json:"digest"`
	Runs      int    `json:"runs"`
	LastRunAt int64  `json:"last_run_at"`
	LastRunID string `json:"last_run_id"`
}

// CustomStepUses lists which Flights ran which versions of a library's step,
// newest use first (schema 68). Runs of one Flight at one commit are one entry.
func (e *Engine) CustomStepUses(library, name string, limit int) ([]CustomStepUse, error) {
	if !e.knows(schemaCustomSteps) {
		return []CustomStepUse{}, nil
	}
	rows, err := e.db.Query(`SELECT project, commit_sha, digest, count(DISTINCT run_id), max(created_at)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		_ = e.db.QueryRow(`SELECT run_id FROM custom_step_uses WHERE library = ? AND name = ? AND project = ? AND commit_sha = ? ORDER BY created_at DESC LIMIT 1`,
			library, name, out[i].Project, out[i].Commit).Scan(&out[i].LastRunID)
	}
	return out, nil
}
