package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/debrief"
)

// The coverage of a change: of the lines a pull request added or modified,
// how many a test ran. The lines come from the pull request's diff (the API
// reads it from the hosted repository and passes them in); which lines are
// covered comes from the line data of the coverage reports (schema 44).

// DefaultChangeFloor is the lowest share of a change's lines that should be
// covered. It advises: the Debrief says when a change is under it, and
// nothing fails.
const DefaultChangeFloor = 80.0

// ChangedLines are the lines a change added or modified, by file, as ranges
// of line numbers on the new side ("10-14, 30").
type ChangedLines struct {
	BaseRef string
	Files   map[string]string
	// Floor is the advised share, 0 for DefaultChangeFloor.
	Floor float64
}

// debriefCoverage summarises a run's coverage for the Debrief, and the
// coverage of the change when the change's lines are known.
func (e *Engine) debriefCoverage(runID string, o DebriefOptions) (*debrief.Coverage, error) {
	if !e.knows(schemaReports) {
		return nil, nil
	}
	rc, err := e.RunCoverage(runID, o.DefaultRef)
	if err != nil || rc.Totals == nil {
		return nil, err
	}
	pc := func(c CoverageCounter) debrief.Ratio {
		r := debrief.Ratio{Covered: int(c.Covered), Total: int(c.Total)}
		if c.Total > 0 {
			r.Percent = float64(c.Covered) * 100 / float64(c.Total)
		}
		return r
	}
	cov := &debrief.Coverage{Lines: pc(rc.Totals.Lines), Branches: pc(rc.Totals.Branches), Functions: pc(rc.Totals.Functions)}
	if rc.Base != nil && rc.Base.Totals.Lines.Total > 0 {
		v := float64(rc.Base.Totals.Lines.Covered) * 100 / float64(rc.Base.Totals.Lines.Total)
		cov.BaseLines = &v
	}
	cf, err := e.RunCoverageFiles(runID, CoverageFileQuery{Sort: "lines", Limit: 10})
	if err == nil {
		cov.Files = cf.Matching
		for _, f := range cf.Files {
			if f.Lines.Total == 0 || f.Lines.Covered == f.Lines.Total {
				continue
			}
			fc := debrief.FileCoverage{Path: f.Path, Covered: int(f.Lines.Covered), Total: int(f.Lines.Total)}
			fc.Percent = float64(fc.Covered) * 100 / float64(fc.Total)
			cov.Least = append(cov.Least, fc)
		}
	}
	if o.Change != nil && len(o.Change.Files) > 0 && e.knows(schemaDebrief) {
		cov.Change = e.changeCoverage(runID, o.Change)
		if cov.Change != nil {
			floor := o.Change.Floor
			if floor <= 0 {
				floor = DefaultChangeFloor
			}
			cov.Floor = &floor
			if cov.Change.Total > 0 && cov.Change.Percent < floor {
				cov.Change.Advisory = fmt.Sprintf("%.0f%% of the changed lines are covered; the advised floor is %.0f%%. Add tests for the lines listed as not covered.", cov.Change.Percent, floor)
			}
		}
	}
	return cov, nil
}

// changeCoverage works out which changed lines are covered.
func (e *Engine) changeCoverage(runID string, ch *ChangedLines) *debrief.ChangeCoverage {
	ef, err := e.coverageTriples(runID)
	if err != nil || len(ef) == 0 {
		return nil
	}
	cond, args := coverageTripleFilter(ef)
	rows, err := e.db.Query(`SELECT cf.path, cf.covered_lines, cf.missed_lines FROM coverage_files cf WHERE `+cond, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type lines struct{ cov, miss debrief.Ranges }
	have := map[string]*lines{}
	for rows.Next() {
		var path, cs, ms string
		if rows.Scan(&path, &cs, &ms) != nil {
			return nil
		}
		l := have[path]
		if l == nil {
			l = &lines{debrief.Ranges{}, debrief.Ranges{}}
			have[path] = l
		}
		// Several steps may cover a file: covered anywhere is covered.
		for n := range debrief.ParseRanges(cs) {
			l.cov[n] = true
		}
		for n := range debrief.ParseRanges(ms) {
			l.miss[n] = true
		}
	}
	out := &debrief.ChangeCoverage{BaseRef: ch.BaseRef, Files: []debrief.FileCoverage{}}
	paths := make([]string, 0, len(ch.Files))
	for p := range ch.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, repoPath := range paths {
		changed := debrief.ParseRanges(ch.Files[repoPath])
		var l *lines
		for rp, v := range have {
			if debrief.PathMatches(rp, repoPath) {
				l = v
				break
			}
		}
		if l == nil || len(changed) == 0 {
			continue
		}
		missed := debrief.Ranges{}
		fc := debrief.FileCoverage{Path: repoPath}
		for n := range changed {
			switch {
			case l.cov[n]:
				fc.Covered++
				fc.Total++
			case l.miss[n]:
				fc.Total++
				missed[n] = true
			}
		}
		if fc.Total == 0 {
			continue
		}
		fc.Percent = float64(fc.Covered) * 100 / float64(fc.Total)
		fc.Missed = missed.String()
		out.Covered += fc.Covered
		out.Total += fc.Total
		out.Files = append(out.Files, fc)
	}
	if out.Total > 0 {
		out.Percent = float64(out.Covered) * 100 / float64(out.Total)
	}
	if len(out.Files) > 50 {
		out.Files = out.Files[:50]
	}
	return out
}

// coverageTriples finds the coverage step attempts that count for a run.
func (e *Engine) coverageTriples(runID string) ([]triple, error) {
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	rows, err := e.effectiveCoverage(st)
	if err != nil {
		return nil, err
	}
	var out []triple
	for _, r := range rows {
		out = append(out, triple{run: r.run, step: r.step, attempt: r.attempt})
	}
	return out, nil
}

// coverageTripleFilter is tripleFilter for coverage_files (as cf).
func coverageTripleFilter(ts []triple) (string, []any) {
	cond, args := tripleFilter(ts)
	return strings.ReplaceAll(cond, "tc.", "cf."), args
}
