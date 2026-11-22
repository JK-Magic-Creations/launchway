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
