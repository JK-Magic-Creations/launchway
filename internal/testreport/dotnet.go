package testreport

import (
	"encoding/xml"
	"strings"
)

// The .NET formats: NUnit 3 (<test-run>), xUnit.net v2 (<assemblies>) and
// Visual Studio's TRX (<TestRun>).

// classOf returns the part of a full test name before ".name" (the class),
// or "".
func classOf(full, name string) string {
	if name != "" && strings.HasSuffix(full, "."+name) {
		return full[:len(full)-len(name)-1]
	}
	return ""
}

// ---- NUnit 3 ----

// NUnit 3: a case per <test-case>. Suite is the test's class (its full
// name less the test's name, else classname), Name its name (parameters
// included). Result Passed / Failed / Skipped / Inconclusive / Warning;
// a Failed case labelled Error, Invalid or Cancelled is Errored; Skipped
// and Inconclusive are Skipped (with the reason); Warning is Passed.
// Category properties are tags, other properties are properties, and
// attachments are paths (as NUnit wrote them, usually absolute).

type nunitParser struct {
	lim      Limits
	col      *collector
	ms       int64
	fixtures []string // the full names of the fixtures (suites with a class) around
}

func parseNUnit(dl *deadline, d *xml.Decoder, root xml.StartElement, lim Limits) (*Report, error) {
	p := &nunitParser{lim: lim, col: newCollector(FormatNUnit, lim)}
	w := newXMLWalker(dl, d, lim, p.col, p)
	w.run(root)
	return finishXML(w, p.col, p.ms)
}

func (p *nunitParser) start(t xml.StartElement) bool {
	if t.Name.Local == "test-suite" {
		f := ""
		if _, ok := attr(t, "classname"); ok {
			f, _ = attr(t, "fullname")
			f, _ = rawCut(f, 4*p.lim.MaxNameBytes)
		}
		p.fixtures = append(p.fixtures, f)
	}
	return t.Name.Local == "test-case"
}

func (p *nunitParser) end(name string) {
	if name == "test-suite" && len(p.fixtures) > 0 {
		p.fixtures = p.fixtures[:len(p.fixtures)-1]
	}
}

func (p *nunitParser) tree(n *xnode) {
	var st Status
	switch strings.ToLower(n.attr("result")) {
	case "passed", "warning":
		st = Passed
	case "failed":
		st = Failed
		switch strings.ToLower(n.attr("label")) {
		case "error", "invalid", "cancelled":
			st = Errored
		}
	default:
		st = Skipped
	}
	ms := secondsToMs(parseSeconds(n.attr("duration")))
	p.ms = addMs(p.ms, ms)
	p.col.add(st, false, func() Case {
		name := n.attr("name")
		suite := classOf(n.attr("fullname"), name)
		for i := len(p.fixtures) - 1; i >= 0 && suite == ""; i-- {
			suite = p.fixtures[i]
		}
		if suite == "" {
			suite = n.attr("classname")
		}
		name = strings.TrimPrefix(name, suite+".")
		cs := Case{
			Suite:      cleanName(suite, p.lim.MaxNameBytes),
			Name:       cleanName(name, p.lim.MaxNameBytes),
			DurationMs: ms,
		}
		res := n.kid("failure")
		if res == nil {
			res = n.kid("reason")
		}
		if res != nil {
			msg, cut := res.kid("message").str()
			cs.Message = cleanMessage(msg, p.lim.MaxMessageBytes, cut)
			trace, cut := res.kid("stack-trace").str()
			cs.Details = cleanBlock(trace, p.lim.MaxDetailsBytes, cut)
		}
		if st == Failed || st == Errored {
			out, cut := n.kid("output").end()
			cs.Output = cleanTail(out, p.lim.MaxOutputBytes, cut)
		}
		for _, pr := range n.path("properties").all("property") {
			if strings.EqualFold(pr.attr("name"), "Category") {
				p.col.addTags(&cs, pr.attr("value"))
			} else {
				p.col.addProperty(&cs, pr.attr("name"), pr.attr("value"))
			}
		}
		for _, a := range n.path("attachments").all("attachment") {
			path := strings.TrimSpace(a.kid("filePath").s())
			p.col.addAttachmentPath(&cs, strings.TrimSpace(a.kid("description").s()), "", path)
		}
		return cs
	})
}

// ---- xUnit.net v2 ----

// xUnit.net v2: a case per <test>. Suite is the test's type, Name the test's
// name without the type in front (parameters included). Result Pass, Fail,
// Skip or NotRun (Skipped). Traits named Category are tags, others are
// properties. An assembly's <errors> (a fixture or collection that failed
// to set up or clean up) become Errored cases named after the error.

type xunitParser struct {
	lim      Limits
	col      *collector
	ms       int64
	assembly string
}

func parseXUnit(dl *deadline, d *xml.Decoder, root xml.StartElement, lim Limits) (*Report, error) {
	p := &xunitParser{lim: lim, col: newCollector(FormatXUnit, lim)}
	w := newXMLWalker(dl, d, lim, p.col, p)
	w.run(root)
	return finishXML(w, p.col, p.ms)
}

func (p *xunitParser) start(t xml.StartElement) bool {
	switch t.Name.Local {
	case "assembly":
		p.assembly, _ = attr(t, "name")
		p.assembly, _ = rawCut(p.assembly, 4*p.lim.MaxNameBytes)
	case "test", "error":
		return true
	}
	return false
}

func (p *xunitParser) end(string) {}

func (p *xunitParser) tree(n *xnode) {
	if n.name == "error" {
		p.col.add(Errored, false, func() Case {
			name := n.attr("name")
			if name == "" {
				name = n.attr("type")
			}
			cs := Case{
				Suite: cleanName(baseName(p.assembly), p.lim.MaxNameBytes),
				Name:  cleanName("["+n.attr("type")+"] "+name, p.lim.MaxNameBytes),
			}
			f := n.kid("failure")
			msg, cut := f.kid("message").str()
			cs.Message = cleanMessage(msg, p.lim.MaxMessageBytes, cut)
			trace, cut := f.kid("stack-trace").str()
			cs.Details = cleanBlock(trace, p.lim.MaxDetailsBytes, cut)
			return cs
		})
		return
	}
	var st Status
	switch n.attr("result") {
	case "Pass":
		st = Passed
	case "Fail":
		st = Failed
	default:
		st = Skipped
	}
	ms := secondsToMs(parseSeconds(n.attr("time")))
	p.ms = addMs(p.ms, ms)
	p.col.add(st, false, func() Case {
		typ := n.attr("type")
		name := n.attr("name")
		if typ != "" && strings.HasPrefix(name, typ+".") {
			name = name[len(typ)+1:]
		}
		cs := Case{
			Suite:      cleanName(typ, p.lim.MaxNameBytes),
			Name:       cleanName(name, p.lim.MaxNameBytes),
			DurationMs: ms,
		}
		if f := n.kid("failure"); f != nil {
			msg, cut := f.kid("message").str()
			if strings.TrimSpace(msg) == "" {
				msg = f.attr("exception-type")
			}
			cs.Message = cleanMessage(msg, p.lim.MaxMessageBytes, cut)
			trace, cut := f.kid("stack-trace").str()
			cs.Details = cleanBlock(trace, p.lim.MaxDetailsBytes, cut)
		} else if r := n.kid("reason"); r != nil {
			msg, cut := r.str()
			cs.Message = cleanMessage(msg, p.lim.MaxMessageBytes, cut)
		}
		if st == Failed {
			out, cut := n.kid("output").end()
			cs.Output = cleanTail(out, p.lim.MaxOutputBytes, cut)
		}
		for _, t := range n.path("traits").all("trait") {
			if strings.EqualFold(t.attr("name"), "Category") {
				p.col.addTags(&cs, t.attr("value"))
			} else {
				p.col.addProperty(&cs, t.attr("name"), t.attr("value"))
			}
		}
		if f := n.attr("source-file"); f != "" {
			cs.File = cleanName(f, maxFileBytes)
		}
		return cs
	})
}
