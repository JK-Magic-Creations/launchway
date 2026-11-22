package engine

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
	"github.com/jk-magic-creations/launchway/internal/store"
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
			if err := store.SaveCustomStepUse(c.tx, store.CustomStepUseRow{Run: c.st.ID, Step: s.ID, Project: c.st.Project, Name: cs.Name,
				Library: cs.RecordedLibrary(), Commit: cs.Commit, Digest: cs.Digest, Network: strings.Join(cs.Network, ","),
				Write: strings.Join(cs.Write, ","), Secrets: strings.Join(cs.Secrets, ","), At: c.now}); err != nil {
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
	rows, err := store.CustomStepUses(e.db, library, name, limit)
	if err != nil {
		return nil, err
	}
	out := make([]CustomStepUse, len(rows))
	for i, r := range rows {
		out[i] = CustomStepUse{Project: r.Project, Commit: r.Commit, Digest: r.Digest, Runs: r.Runs, LastRunAt: r.LastRunAt,
			LastRunID: store.LastCustomStepRun(e.db, library, name, r.Project, r.Commit)}
	}
	return out, nil
}

// stepNotes is what a custom step's attempt kept of the notes its runner reported; nothing for any other step.
func (c *cmd) stepNotes(step string, in []StepNote) []StepNote {
	if def := c.st.Pipeline.Step(step); def == nil || def.Custom == nil || !c.e.knows(schemaCustomSteps) {
		return nil
	}
	return keepNotes(in)
}
