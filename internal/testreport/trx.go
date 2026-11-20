package testreport

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// Visual Studio TRX. A case per <UnitTestResult> (for a data-driven test,
// per inner result instead of the parent). TRX writes the results before
// the test definitions that name each test's class, categories and
// properties, so the results are kept (the first MaxCases in full, the
// rest as a status) and turned into cases at the end. Suite is the class
// (TestMethod className), Name the testName less "class." in front.
// Outcomes: Passed (and Warning, Completed, PassedButRunAborted) are
// Passed, Failed is Failed, Timeout, Aborted, Error and Disconnected are
// Errored, NotExecuted, Inconclusive and the rest are Skipped. TestCategory
// items are tags; Properties and Owners are properties.
//
// Result files: vstest stores a result's files under
// <runDeploymentRoot>/In/<relativeResultsDirectory>/ next to the .trx
// file, and ResultFile paths are relative to that directory (they usually
// start with the computer name). So a relative ResultFile path becomes
// "<runDeploymentRoot>/In/<relativeResultsDirectory or executionId>/<path>"
// with backslashes turned into slashes: a path relative to the TRX file's
// directory. An absolute path is kept as written.

type trxRecord struct {
	st     Status
	testID string
	name   string
	cs     *Case // nil for a result past MaxCases
}

type trxDef struct {
	class string
	tags  []string
	props []rawProp
}

type trxParser struct {
	lim     Limits
	col     *collector
	ms      int64
	root    string // the run's deployment root
	records []trxRecord
	over    []Status // results past MaxCases
	stored  int
	defs    map[string]*trxDef
}

func parseTRX(dl *deadline, d *xml.Decoder, root xml.StartElement, lim Limits) (*Report, error) {
	p := &trxParser{lim: lim, col: newCollector(FormatTRX, lim), defs: map[string]*trxDef{}}
	w := newXMLWalker(dl, d, lim, p.col, p)
	w.full = func() bool { return p.stored >= lim.MaxCases }
	w.run(root)
	if w.stopped && len(p.records) == 0 && len(p.over) == 0 {
		return nil, dl.stopError()
	}
	for i := range p.records {
		r := &p.records[i]
		p.col.add(r.st, false, func() Case {
			cs := *r.cs
			def := p.defs[r.testID]
			name := r.name
			if def != nil {
				cs.Suite = cleanName(def.class, p.lim.MaxNameBytes)
				if def.class != "" && strings.HasPrefix(name, def.class+".") {
					name = name[len(def.class)+1:]
				}
				p.col.addTags(&cs, def.tags...)
				for _, pr := range def.props {
					p.col.addProperty(&cs, pr.name, pr.value)
				}
			}
			cs.Name = cleanName(name, p.lim.MaxNameBytes)
			return cs
		})
	}
	for _, st := range p.over {
		p.col.add(st, false, nil)
	}
	r := p.col.finish()
	r.DurationMs = p.ms
	return r, nil
}

func (p *trxParser) start(t xml.StartElement) bool {
	switch t.Name.Local {
	case "Deployment":
		p.root, _ = attr(t, "runDeploymentRoot")
		p.root, _ = rawCut(p.root, MaxPathBytes)
	case "UnitTestResult", "UnitTest":
		return true
	}
	return false
}

func (p *trxParser) end(string) {}

func (p *trxParser) tree(n *xnode) {
	if n.name == "UnitTest" {
		id := n.attr("id")
		if id == "" || len(p.defs) >= 4*p.lim.MaxCases {
			return
		}
		def := &trxDef{class: n.path("TestMethod").attr("className")}
		for _, c := range n.path("TestCategory").all("TestCategoryItem") {
			if len(def.tags) < MaxTags {
				def.tags = append(def.tags, c.attr("TestCategory"))
			}
		}
		for _, o := range n.path("Owners").all("Owner") {
			def.props = append(def.props, rawProp{"owner", o.attr("name")})
		}
		for _, pr := range n.path("Properties").all("Property") {
			if len(def.props) <= MaxProperties {
				def.props = append(def.props, rawProp{strings.TrimSpace(pr.kid("Key").s()), strings.TrimSpace(pr.kid("Value").s())})
			}
		}
		p.defs[id] = def
		return
	}
	inner := n.path("InnerResults").all("UnitTestResult")
	if len(inner) == 0 {
		p.result(n)
		return
	}
	for _, in := range inner {
		if in.attr("testId") == "" && n.attr("testId") != "" {
			in.attrs = append(in.attrs, xml.Attr{Name: xml.Name{Local: "testId"}, Value: n.attr("testId")})
		}
		p.result(in)
	}
}

func trxStatus(outcome string) Status {
	switch strings.ToLower(outcome) {
	case "passed", "warning", "completed", "passedbutrunaborted":
		return Passed
	case "failed":
		return Failed
	case "timeout", "aborted", "error", "disconnected":
		return Errored
	}
	return Skipped
}

// trxDuration reads "hh:mm:ss.fffffff".
func trxDuration(s string) int64 {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.ParseFloat(parts[0], 64)
	m, err2 := strconv.ParseFloat(parts[1], 64)
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 {
		return 0
	}
	return secondsToMs(h*3600 + m*60 + sec)
}

// trxPath places a result file's path relative to the TRX file.
func (p *trxParser) trxPath(n *xnode, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	norm := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(norm, "/") || (len(norm) > 2 && norm[1] == ':' && norm[2] == '/') {
		return path // absolute: as written
	}
	dir := n.attr("relativeResultsDirectory")
	if dir == "" {
		dir = n.attr("executionId")
	}
	prefix := "In/"
	if p.root != "" {
		prefix = strings.ReplaceAll(p.root, `\`, "/") + "/In/"
	}
	if dir != "" {
		prefix += strings.ReplaceAll(dir, `\`, "/") + "/"
	}
	return prefix + norm
}

func (p *trxParser) result(n *xnode) {
	st := trxStatus(n.attr("outcome"))
	ms := trxDuration(n.attr("duration"))
	p.ms = addMs(p.ms, ms)
	if p.stored >= p.lim.MaxCases {
		p.over = append(p.over, st)
		return
	}
	name, _ := rawCut(n.attr("testName"), 4*p.lim.MaxNameBytes)
	rec := trxRecord{st: st, testID: n.attr("testId"), name: name}
	if len(rec.testID) > 100 {
		rec.testID = ""
	}
	{
		p.stored++
		cs := &Case{DurationMs: ms}
		out := n.kid("Output")
		ei := out.kid("ErrorInfo")
		msg, cut := ei.kid("Message").str()
		if st != Passed {
			cs.Message = cleanMessage(msg, p.lim.MaxMessageBytes, cut)
			if cs.Message == "" {
				var tm []string
				for _, m := range out.path("TextMessages").all("Message") {
					if len(tm) < 20 {
						tm = append(tm, m.s())
					}
				}
				cs.Message = cleanMessage(strings.Join(tm, "\n"), p.lim.MaxMessageBytes, false)
			}
			trace, cut := ei.kid("StackTrace").str()
			cs.Details = cleanBlock(trace, p.lim.MaxDetailsBytes, cut)
		}
		if st == Failed || st == Errored {
			so, c1 := out.kid("StdOut").end()
			se, c2 := out.kid("StdErr").end()
			cs.Output = cleanTail(joinOutput(so, se), p.lim.MaxOutputBytes, c1 || c2)
		}
		for _, f := range n.path("ResultFiles").all("ResultFile") {
			p.col.addAttachmentPath(cs, "", "", p.trxPath(n, f.attr("path")))
		}
		rec.cs = cs
	}
	p.records = append(p.records, rec)
}
