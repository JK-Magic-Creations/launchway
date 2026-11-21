package engine

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// SkipPaths is the skip kind of a step that a path rule kept from running
// (when: { changed }): the run still succeeds, and a required check for the
// step counts as satisfied.
const SkipPaths = "paths"

// skipKindOf is the skip kind for the reason a step's when condition gave.
func skipKindOf(reason string) string {
	if strings.HasPrefix(reason, pipeline.PathSkipPrefix) {
		return SkipPaths
	}
	if strings.HasPrefix(reason, "restricted: ") {
		return SkipRestricted
	}
	return "condition"
}

// PathSkippedHeads are the commits of a ref that a path filter kept from
// starting a run (see OutcomePathRule).
func (e *Engine) PathSkippedHeads(project, ref string) []string {
	rows, err := e.db.Query(`SELECT DISTINCT sha FROM push_decisions WHERE project = ? AND ref = ? AND outcome = ?`, project, ref, OutcomePathRule)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sha string
		if rows.Scan(&sha) == nil {
			out = append(out, sha)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}
