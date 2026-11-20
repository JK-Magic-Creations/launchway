package coverage

import "fmt"

// Merge adds up the reports of one step (several coverage files, such as
// one LCOV file per package) into one report. Files with the same Path
// are combined:
//
//   - where every report that has the file has line data of the same kind
//     for a counter (LCOV/Cobertura/JaCoCo/Clover lines, Go blocks,
//     branches, functions), the data is united: a line, block, branch or
//     function counts once and is covered when any report covers it;
//   - otherwise (Istanbul json-summary, a file's own counters, line data of
//     different kinds, or reports that came back from JSON without their
//     line data) that counter is summed, and the Warning says how many
//     files were summed.
//
// Totals add up every file, including those beyond MaxFiles; Files holds at
// most lim.MaxFiles files, sorted by Path. Format and Unit are the reports'
// common ones, or FormatMixed and UnitMixed. Nil reports are skipped.
func Merge(reports []*Report, lim Limits) *Report {
	lim = lim.withDefaults()
	byPath := map[string][]*fileAcc{}
	var order []string
	formats, units := map[string]bool{}, map[string]bool{}
	var warnings, sources []string
	var extra Totals
	extraFiles := 0
	srcSeen := map[string]bool{}
	for i, r := range reports {
		if r == nil {
			continue
		}
		formats[r.Format] = true
		units[r.Unit] = true
		if r.Warning != "" && len(warnings) < 5 {
			warnings = append(warnings, fmt.Sprintf("report %d: %s", i+1, r.Warning))
		}
		for _, s := range r.Sources {
			if !srcSeen[s] && len(sources) < MaxSources {
				srcSeen[s] = true
				sources = append(sources, s)
			}
		}
		add := func(f *fileAcc) {
			if _, ok := byPath[f.path]; !ok {
				order = append(order, f.path)
			}
			byPath[f.path] = append(byPath[f.path], f)
		}
		if r.FileCount == 0 && len(r.Files) == 0 {
			// No files, perhaps a json-summary with only its "total".
			extra = extra.add(r.Totals)
			continue
		}
		if r.all != nil {
			for _, f := range r.all {
				add(f)
			}
			continue
		}
		// A report without its line data (decoded from JSON, say): only its
		// listed files' counters are known, and the rest of its totals.
		var listed Totals
		for _, f := range r.Files {
			t := Totals{Lines: f.Lines.norm(), Branches: f.Branches.norm(), Functions: f.Functions.norm()}
			listed = listed.add(t)
			a := &fileAcc{path: f.Path, pkg: f.Package, fb: t}
			a.finalize()
			add(a)
		}
		if n := r.FileCount - len(r.Files); n > 0 {
			extraFiles += n
			extra = extra.add(Totals{
				Lines:     minus(r.Totals.Lines, listed.Lines),
				Branches:  minus(r.Totals.Branches, listed.Branches),
				Functions: minus(r.Totals.Functions, listed.Functions),
			})
		}
	}

	all := make([]*fileAcc, 0, len(order))
	summed := 0
	for _, p := range order {
		parts := byPath[p]
		if len(parts) == 1 {
			all = append(all, parts[0])
			continue
		}
		m, didSum := combine(parts)
		if didSum {
			summed++
		}
		all = append(all, m)
	}
	r := buildReport(common(formats, FormatMixed), common(units, UnitMixed), all, lim)
	r.Totals = r.Totals.add(extra)
	if extraFiles > 0 {
		r.FileCount += extraFiles
		r.Truncated = true
	}
	if summed > 0 {
		warnings = append(warnings, fmt.Sprintf("%d files appear in several reports without line data to combine; their counters were added together", summed))
	}
	if len(units) > 1 {
		warnings = append(warnings, "the reports count different units (Go counts statements, the others lines); the line totals mix them")
	}
	r.Warning = joinWarnings(warnings)
	if len(sources) > 0 {
		r.Sources = sources
	}
	return r
}

func minus(a, b Counter) Counter {
	return Counter{Covered: max(a.Covered-b.Covered, 0), Total: max(a.Total-b.Total, 0)}.norm()
}

func common(set map[string]bool, mixed string) string {
	if len(set) == 1 {
		for k := range set {
			return k
		}
	}
	if len(set) == 0 {
		return ""
	}
	return mixed
}

// combine puts several reports' data for one file together; it says
// whether any counter had to be summed.
func combine(parts []*fileAcc) (*fileAcc, bool) {
	m := &fileAcc{path: parts[0].path}
	for _, p := range parts {
		if m.pkg == "" {
			m.pkg = p.pkg
		}
	}
	all := func(bit uint8) bool {
		for _, p := range parts {
			if p.has&bit == 0 {
				return false
			}
		}
		return true
	}
	summed := false
	switch {
	case all(hasLines):
		m.has |= hasLines
		for _, p := range parts {
			m.lines = append(m.lines, p.lines...)
		}
	case all(hasBlocks):
		m.has |= hasBlocks
		for _, p := range parts {
			m.blocks = append(m.blocks, p.blocks...)
		}
	default:
		n := 0
		for _, p := range parts {
			m.fb.Lines = m.fb.Lines.add(p.out.Lines)
			if p.out.Lines.Total > 0 {
				n++
			}
		}
		summed = summed || n > 1
	}
	if all(hasBranches) {
		m.has |= hasBranches
		for _, p := range parts {
			m.branches = append(m.branches, p.branches...)
		}
	} else {
		n := 0
		for _, p := range parts {
			m.fb.Branches = m.fb.Branches.add(p.out.Branches)
			if p.out.Branches.Total > 0 {
				n++
			}
		}
		summed = summed || n > 1
	}
	if all(hasFuncs) {
		m.has |= hasFuncs
		for _, p := range parts {
			m.funcs = append(m.funcs, p.funcs...)
		}
	} else {
		n := 0
		for _, p := range parts {
			m.fb.Functions = m.fb.Functions.add(p.out.Functions)
			if p.out.Functions.Total > 0 {
				n++
			}
		}
		summed = summed || n > 1
	}
	m.finalize()
	return m, summed
}
