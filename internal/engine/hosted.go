package engine

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// HTML reports (schema 18): directories of HTML a step publishes, kept as
// artifacts of kind html-report. The API serves them sandboxed; see
// internal/api/hosted.go for how, and why.

// HTMLReport is one HTML report of a run.
type HTMLReport struct {
	Step     string `json:"step"`
	Attempt  int    `json:"attempt"`
	Name     string `json:"name"`
	Artifact string `json:"artifact"` // its ID
	Index    string `json:"index"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	// HasIndex says the index page is among its files.
	HasIndex  bool  `json:"has_index"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// RunHTMLReports lists a run's HTML reports: those kept by the latest
// attempt of each step, and those a step declares that were not kept, with
// why (in Missing).
type RunHTMLReports struct {
	Reports []HTMLReport `json:"reports"`
	// Declared are the reports the pipeline declares, as step/name.
	Declared []string `json:"declared"`
}

// RunHTMLReports lists a run's HTML reports.
func (e *Engine) RunHTMLReports(runID string) (*RunHTMLReports, error) {
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	out := &RunHTMLReports{Reports: []HTMLReport{}, Declared: []string{}}
	index := map[string]string{} // step\x00name → index
	for _, s := range st.Pipeline.Steps {
		if s.Reports == nil {
			continue
		}
		for _, h := range s.Reports.HTML {
			out.Declared = append(out.Declared, s.ID+"/"+h.Name)
			index[s.ID+"\x00"+h.Name] = h.Index
		}
	}
	if !e.knows(schemaReports) || len(out.Declared) == 0 {
		return out, nil
	}
	rows, err := e.db.Query(`SELECT `+artifactCols+` FROM artifacts WHERE run_id = ? AND kind = ? AND status = ? ORDER BY step_id, name, attempt DESC`,
		runID, KindHTMLReport, artKept)
	if err != nil {
		return nil, err
	}
	var list []*artifactRow
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, a := range list {
		name := strings.TrimPrefix(a.Name, pipeline.HTMLReportPrefix)
		k := a.StepID + "\x00" + name
		if seen[k] {
			continue // an older attempt's
		}
		seen[k] = true
		h := HTMLReport{Step: a.StepID, Attempt: a.Attempt, Name: name, Artifact: a.ID, Index: nonEmpty(index[k], "index.html"),
			Files: a.Files, Bytes: a.Bytes, ExpiresAt: a.ExpiresAt}
		var one int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM artifact_files WHERE artifact_id = ? AND path = ?`, a.ID,
			hostedPath(st, a, h.Index)).Scan(&one); err != nil {
			return nil, err
		}
		h.HasIndex = one > 0
		out.Reports = append(out.Reports, h)
	}
	return out, nil
}

// hostedPath is where a page of an HTML report is in its artifact: below
// the report's directory, as the runner kept it.
func hostedPath(st *RunState, a *artifactRow, page string) string {
	dir := ""
	if def := st.Pipeline.Step(a.StepID); def != nil && def.Reports != nil {
		for _, h := range def.Reports.HTML {
			if pipeline.HTMLReportPrefix+h.Name == a.Name {
				dir = h.Path
			}
		}
	}
	return path.Join(dir, page)
}

// HostedFile is a file of an HTML report, opened.
type HostedFile struct {
	*ArtifactFile
	RunID string
}

// OpenHostedFile opens a page or other file of an HTML report, by the
// report's artifact ID and the path below its directory. Anything that is
// not a kept HTML report, or a path that is not one of its files, is not
// found.
func (e *Engine) OpenHostedFile(ctx context.Context, artifactID, page string, offset int64) (*HostedFile, error) {
	if !e.knows(schemaReports) {
		return nil, fmt.Errorf("%w: report", ErrNotFound)
	}
	a, err := artifactByID(e.db, artifactID)
	if err != nil {
		return nil, err
	}
	if a.Kind != KindHTMLReport || a.Status != artKept {
		return nil, fmt.Errorf("%w: report", ErrNotFound)
	}
	clean := path.Clean("/" + page)[1:]
	if clean == "" || clean != page {
		return nil, fmt.Errorf("%w: %s is not a file of the report", ErrNotFound, page)
	}
	st, err := e.runState(a.RunID)
	if err != nil {
		return nil, err
	}
	f, err := e.openArtifactFile(ctx, a, hostedPath(st, a, clean), offset)
	if err != nil {
		return nil, err
	}
	return &HostedFile{ArtifactFile: f, RunID: a.RunID}, nil
}

// HTMLReportRun says which run an HTML report artifact belongs to, if it
// is a kept HTML report.
func (e *Engine) HTMLReportRun(artifactID string) (string, error) {
	a, err := artifactByID(e.db, artifactID)
	if err != nil {
		return "", err
	}
	if a.Kind != KindHTMLReport || a.Status != artKept {
		return "", fmt.Errorf("%w: report", ErrNotFound)
	}
	return a.RunID, nil
}
