package coverage

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// xmlReader is one XML coverage format. start is called for every element
// with the name of its parent ("" for the root), end with the element's
// name, text with character data.
type xmlReader interface {
	start(t xml.StartElement, parent string)
	end(name string)
	text(b []byte)
}

func parseXML(dl *deadline, data []byte, lim Limits) (*Report, error) {
	kind, err := declaredEncoding(data)
	if err != nil {
		return nil, err
	}
	clean := prepareXML(dl, data, kind == encLatin1)
	if clean == nil {
		return nil, dl.stopError()
	}
	d := xml.NewDecoder(bytes.NewReader(clean))
	d.Strict = true
	d.CharsetReader = charsetReader

	c := newCollector("", lim)
	var h xmlReader
	var names []string
	var root *xml.StartElement // a <coverage> root not yet known as Cobertura or Clover
	rootSeen := false
loop:
	for {
		if dl.tick() {
			line, _ := d.InputPos()
			if !c.any() {
				return nil, dl.stopError()
			}
			c.warn("parsing %s; coverage after line %d is missing", dl.what(), line)
			break
		}
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF && len(names) == 0 {
				if !rootSeen {
					return nil, fmt.Errorf("%w: the XML has no root element", ErrNotReport)
				}
				break
			}
			line, msg := xmlErrorText(d, err)
			if !rootSeen {
				return nil, fmt.Errorf("%w: the XML could not be read (line %d: %s)", ErrNotReport, line, msg)
			}
			if strings.Contains(msg, "unexpected EOF") {
				c.warn("the file ends part-way through (XML error at line %d: %s); coverage after that is missing", line, msg)
			} else {
				c.warn("the XML is broken at line %d (%s); coverage after that is missing", line, msg)
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(names) >= lim.MaxDepth {
				line, _ := d.InputPos()
				if !rootSeen {
					return nil, fmt.Errorf("%w: the XML nests elements more than %d deep", ErrNotReport, lim.MaxDepth)
				}
				c.warn("the XML nests elements more than %d deep at line %d; coverage after that is missing", lim.MaxDepth, line)
				break loop
			}
			parent := ""
			if len(names) > 0 {
				parent = names[len(names)-1]
			}
			if !rootSeen {
				rootSeen = true
				switch t.Name.Local {
				case "report":
					c.format, h = FormatJaCoCo, &jacocoReader{c: c, lim: lim}
				case "coverage":
					switch coverageFlavour(t) {
					case FormatClover:
						c.format, h = FormatClover, &cloverReader{c: c, lim: lim}
					case FormatCobertura:
						c.format, h = FormatCobertura, &coberturaReader{c: c, lim: lim}
					default:
						tt := t.Copy()
						root = &tt
						names = append(names, t.Name.Local)
						continue
					}
				default:
					return nil, fmt.Errorf("%w: the XML's root element is <%s>; %s", ErrNotReport, cleanName(t.Name.Local, 60), knownFormats)
				}
			} else if h == nil {
				// The first child of an undecided <coverage> decides.
				h = decideCoverage(c, lim, t.Name.Local == "project", root)
			}
			h.start(t, parent)
			names = append(names, t.Name.Local)
		case xml.EndElement:
			if len(names) == 0 {
				continue
			}
			names = names[:len(names)-1]
			if h != nil {
				h.end(t.Name.Local)
			}
		case xml.CharData:
			if h != nil {
				h.text(t)
			}
		}
	}
	if h == nil && root != nil {
		_, gen := attr(*root, "generated")
		decideCoverage(c, lim, gen, root)
	}
	return c.finish(), nil
}

// coverageFlavour tells Cobertura and Clover apart by the root's
// attributes, or returns "" when they do not say.
func coverageFlavour(t xml.StartElement) string {
	if _, ok := attr(t, "clover"); ok {
		return FormatClover
	}
	for _, a := range []string{"line-rate", "branch-rate", "lines-valid", "lines-covered", "branches-valid"} {
		if _, ok := attr(t, a); ok {
			return FormatCobertura
		}
	}
	return ""
}

func decideCoverage(c *collector, lim Limits, clover bool, root *xml.StartElement) xmlReader {
	var h xmlReader
	if clover {
		c.format, h = FormatClover, &cloverReader{c: c, lim: lim}
	} else {
		c.format, h = FormatCobertura, &coberturaReader{c: c, lim: lim}
	}
	h.start(*root, "")
	return h
}

// --- Cobertura ---

// coberturaReader reads Cobertura XML. Lines come from each class's
// <lines> (deduplicated by line number across the classes of one file);
// a class with no class-level lines uses its methods' lines instead, as
// the Cobertura format treats method lines as a subset of class lines.
// Branches come from each line's condition-coverage "(covered/total)".
// Functions are the <method> elements: covered when any of their lines
// has hits above 0.
type coberturaReader struct {
	c   *collector
	lim Limits

	pkg       string
	cur       *fileAcc
	className string
	classHad  bool      // the class has class-level <line> elements
	mLines    []lineRec // lines of the class's methods
	mBranches []brRec
	inMethod  bool
	method    fnRec
	inSource  bool
	src       []byte
}

func (r *coberturaReader) start(t xml.StartElement, parent string) {
	switch t.Name.Local {
	case "source":
		if parent == "sources" {
			r.inSource, r.src = true, r.src[:0]
		}
	case "package":
		r.pkg = rawCut(attrv(t, "name"), 4*r.lim.MaxPathBytes)
	case "class":
		fn := attrv(t, "filename")
		name := attrv(t, "name")
		if strings.TrimSpace(fn) == "" {
			fn = name
		}
		r.cur = r.c.file(fn, r.pkg)
		r.cur.has |= hasLines | hasBranches | hasFuncs
		r.className = rawCut(name, 4*r.lim.MaxPathBytes)
		r.classHad, r.mLines, r.mBranches = false, r.mLines[:0], r.mBranches[:0]
	case "method":
		if r.cur != nil {
			r.inMethod = true
			r.method = fnRec{name: rawCut(r.className+"."+attrv(t, "name")+attrv(t, "signature"), 8*r.lim.MaxPathBytes)}
		}
	case "line":
		if r.cur == nil {
			return
		}
		n, ok := parseLineNo(attrv(t, "number"))
		if !ok {
			return
		}
		hits := parseCount(attrv(t, "hits"))
		var br *brRec
		if cc, ok := attr(t, "condition-coverage"); ok {
			if cov, tot, ok := parseConditionCoverage(cc); ok {
				br = &brRec{line: n, block: -1, branch: -1, cov: cov, tot: tot}
			}
		}
		if r.inMethod {
			r.method.hits = max(r.method.hits, hits)
			r.mLines = append(r.mLines, lineRec{n, hits})
			if br != nil {
				r.mBranches = append(r.mBranches, *br)
			}
			return
		}
		r.classHad = true
		r.cur.lines = append(r.cur.lines, lineRec{n, hits})
		if br != nil {
			r.cur.branches = append(r.cur.branches, *br)
		}
	}
}

func (r *coberturaReader) end(name string) {
	switch name {
	case "source":
		if r.inSource {
			r.inSource = false
			r.c.addSource(string(r.src))
		}
	case "method":
		if r.inMethod && r.cur != nil {
			r.cur.funcs = append(r.cur.funcs, r.method)
		}
		r.inMethod = false
	case "class":
		if r.cur != nil && !r.classHad {
			r.cur.lines = append(r.cur.lines, r.mLines...)
			r.cur.branches = append(r.cur.branches, r.mBranches...)
		}
		r.cur, r.inMethod = nil, false
	case "package":
		r.pkg = ""
	}
}

func (r *coberturaReader) text(b []byte) {
	if r.inSource {
		room := 4*r.lim.MaxPathBytes - len(r.src)
		r.src = append(r.src, b[:min(len(b), max(room, 0))]...)
	}
}

// parseConditionCoverage reads "50% (1/2)": the counts in brackets, never
// the percentage.
func parseConditionCoverage(s string) (cov, tot int64, ok bool) {
	i := strings.IndexByte(s, '(')
	j := strings.LastIndexByte(s, ')')
	if i < 0 || j < i {
		return 0, 0, false
	}
	a, b, found := strings.Cut(s[i+1:j], "/")
	if !found {
		return 0, 0, false
	}
	tot = parseCount(b)
	return min(parseCount(a), tot), tot, true
}

// --- JaCoCo ---

// jacocoReader reads JaCoCo XML. For each <sourcefile>, lines come from
// its <line> elements (a line is covered when ci > 0, as JaCoCo's LINE
// counter counts it) and branches from their mb/cb; functions are the
// <method> elements of the classes whose sourcefilename is that file,
// covered when their METHOD (or else INSTRUCTION) counter has covered > 0.
// The sourcefile's own LINE, BRANCH and METHOD counters are used only for a
// kind it has none of that data for.
type jacocoReader struct {
	c   *collector
	lim Limits

	pkg        string
	inClass    bool
	classSrc   string
	className  string
	inMethod   bool
	methodName string
	mMethod    int8 // -1 no METHOD counter, else 0 or 1
	mInstr     bool
	cur        *fileAcc
}

func joinPkg(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "/" + name
}

func (r *jacocoReader) start(t xml.StartElement, parent string) {
	switch t.Name.Local {
	case "package":
		r.pkg = rawCut(attrv(t, "name"), 4*r.lim.MaxPathBytes)
	case "class":
		r.inClass = true
		r.classSrc = rawCut(attrv(t, "sourcefilename"), 4*r.lim.MaxPathBytes)
		r.className = rawCut(attrv(t, "name"), 4*r.lim.MaxPathBytes)
	case "method":
		if r.inClass {
			r.inMethod = true
			r.methodName = rawCut(attrv(t, "name")+attrv(t, "desc")+"@"+attrv(t, "line"), 4*r.lim.MaxPathBytes)
			r.mMethod, r.mInstr = -1, false
		}
	case "sourcefile":
		r.cur = r.c.file(joinPkg(r.pkg, attrv(t, "name")), r.pkg)
	case "counter":
		typ := attrv(t, "type")
		cov := parseCount(attrv(t, "covered"))
		ctr := Counter{Covered: cov, Total: addSat(parseCount(attrv(t, "missed")), cov)}
		switch {
		case parent == "method" && r.inMethod:
			switch typ {
			case "METHOD":
				r.mMethod = 0
				if cov > 0 {
					r.mMethod = 1
				}
			case "INSTRUCTION":
				r.mInstr = cov > 0
			}
		case parent == "sourcefile" && r.cur != nil:
			switch typ {
			case "LINE":
				r.cur.fb.Lines = r.cur.fb.Lines.add(ctr)
			case "BRANCH":
				r.cur.fb.Branches = r.cur.fb.Branches.add(ctr)
			case "METHOD":
				r.cur.fb.Functions = r.cur.fb.Functions.add(ctr)
			}
		}
	case "line":
		if parent != "sourcefile" || r.cur == nil {
			return
		}
		nr, ok := parseLineNo(attrv(t, "nr"))
		if !ok {
			return
		}
		r.cur.has |= hasLines | hasBranches
		mi, ci := parseCount(attrv(t, "mi")), parseCount(attrv(t, "ci"))
		mb, cb := parseCount(attrv(t, "mb")), parseCount(attrv(t, "cb"))
		if mi > 0 || ci > 0 {
			r.cur.lines = append(r.cur.lines, lineRec{nr, ci})
		}
		if mb > 0 || cb > 0 {
			r.cur.branches = append(r.cur.branches, brRec{line: nr, block: -1, branch: -1, cov: cb, tot: addSat(mb, cb)})
		}
	}
}

func (r *jacocoReader) end(name string) {
	switch name {
	case "method":
		if r.inMethod && r.classSrc != "" {
			covered := r.mMethod == 1 || (r.mMethod == -1 && r.mInstr)
			f := r.c.file(joinPkg(r.pkg, r.classSrc), r.pkg)
			f.has |= hasFuncs
			var hits int64
			if covered {
				hits = 1
			}
			f.funcs = append(f.funcs, fnRec{name: r.className + "." + r.methodName, hits: hits})
		}
		r.inMethod = false
	case "class":
		r.inClass, r.inMethod = false, false
	case "sourcefile":
		r.cur = nil
	case "package":
		r.pkg = ""
	}
}

func (r *jacocoReader) text([]byte) {}

// --- Clover ---

// cloverReader reads Clover XML. For a file with <line> elements, lines
// are the stmt lines (and cond lines that carry a count, as Istanbul
// writes them), branches come from the cond lines and functions from the
// method lines. Istanbul's cond lines (which carry a count) give
// truecount = branches covered and falsecount = branches not covered;
// OpenClover's (no count) give how often the condition was true and false,
// so each is two branches. A file's <metrics> are used only for a kind it
// has no line data for (Istanbul writes no method lines, so its function
// counts come from metrics).
type cloverReader struct {
	c   *collector
	lim Limits

	pkg string
	cur *fileAcc
}

func (r *cloverReader) start(t xml.StartElement, parent string) {
	switch t.Name.Local {
	case "package":
		r.pkg = rawCut(attrv(t, "name"), 4*r.lim.MaxPathBytes)
	case "file":
		p := attrv(t, "path")
		if strings.TrimSpace(p) == "" {
			p = attrv(t, "name")
		}
		r.cur = r.c.file(p, r.pkg)
	case "metrics":
		if parent != "file" || r.cur == nil {
			return
		}
		pair := func(total, covered string) Counter {
			cov := parseCount(attrv(t, covered))
			return Counter{Covered: cov, Total: parseCount(attrv(t, total))}.norm()
		}
		fb := &r.cur.fb
		fb.Lines = fb.Lines.add(pair("statements", "coveredstatements"))
		fb.Branches = fb.Branches.add(pair("conditionals", "coveredconditionals"))
		fb.Functions = fb.Functions.add(pair("methods", "coveredmethods"))
	case "line":
		if parent != "file" || r.cur == nil {
			return
		}
		n, ok := parseLineNo(attrv(t, "num"))
		if !ok {
			return
		}
		f := r.cur
		f.has |= hasLines | hasBranches
		countStr, hasCount := attr(t, "count")
		count := parseCount(countStr)
		switch attrv(t, "type") {
		case "stmt":
			f.lines = append(f.lines, lineRec{n, count})
		case "cond":
			tc, fc := parseCount(attrv(t, "truecount")), parseCount(attrv(t, "falsecount"))
			if hasCount {
				f.lines = append(f.lines, lineRec{n, count})
				f.branches = append(f.branches, brRec{line: n, block: -1, branch: -1, cov: tc, tot: addSat(tc, fc)})
			} else {
				f.branches = append(f.branches,
					brRec{line: n, block: 0, branch: 0, cov: b2i(tc > 0), tot: 1},
					brRec{line: n, block: 0, branch: 1, cov: b2i(fc > 0), tot: 1})
			}
		case "method":
			f.has |= hasFuncs
			name := rawCut(attrv(t, "name"), 4*r.lim.MaxPathBytes)
			f.funcs = append(f.funcs, fnRec{name: fmt.Sprintf("%s@%d", name, n), hits: count})
		}
	}
}

func (r *cloverReader) end(name string) {
	switch name {
	case "file":
		r.cur = nil
	case "package":
		r.pkg = ""
	}
}

func (r *cloverReader) text([]byte) {}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
