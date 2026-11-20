package testreport

import (
	"encoding/xml"
	"strings"
)

// TestNG's testng-results.xml: suite > test > class > test-method. A case
// is one test method run: Suite is the class, Name the method's name, with
// its parameters in brackets when it ran with a data provider (so each row
// has its own history; the values are also properties param0, param1, ...).
// Configuration methods (is-config="true": @BeforeMethod and the like) are
// left out unless they failed; then they are Errored cases. Groups come
// from the suite's <groups> section and become tags. A method marked
// retried="true" is an earlier try of the next run of the same method and
// parameters: it becomes one of that run's Attempts.

type testngParser struct {
	dl     *deadline
	lim    Limits
	col    *collector
	ms     int64
	groups map[string][]string // class + "." + method -> groups
	group  string              // the <group> being read
	class  string
	// earlier tries waiting for their final run, by method key
	retried map[string][]Attempt
	nGroups int
}

func parseTestNG(dl *deadline, d *xml.Decoder, root xml.StartElement, lim Limits) (*Report, error) {
	p := &testngParser{dl: dl, lim: lim, col: newCollector(FormatTestNG, lim), groups: map[string][]string{}, retried: map[string][]Attempt{}}
	w := newXMLWalker(dl, d, lim, p.col, p)
	w.run(root)
	return finishXML(w, p.col, p.ms)
}

// finishXML turns how a walk ended into the report or an error.
func finishXML(w *xmlWalker, col *collector, ms int64) (*Report, error) {
	if w.stopped && col.rep.Counts.Total == 0 {
		return nil, w.dl.stopError()
	}
	r := col.finish()
	r.DurationMs = ms
	return r, nil
}

func (p *testngParser) start(t xml.StartElement) bool {
	switch t.Name.Local {
	case "group":
		p.group, _ = attr(t, "name")
	case "method":
		if p.group != "" && p.nGroups < 4*p.lim.MaxCases {
			name, _ := attr(t, "name")
			class, _ := attr(t, "class")
			k := class + "." + name
			if len(k) < 4096 && len(p.groups[k]) < MaxTags {
				p.groups[k] = append(p.groups[k], p.group)
				p.nGroups++
			}
		}
	case "class":
		p.class, _ = attr(t, "name")
		p.class, _ = rawCut(p.class, 4*p.lim.MaxNameBytes)
	case "test-method":
		return true
	}
	return false
}

func (p *testngParser) end(name string) {
	switch name {
	case "group":
		p.group = ""
	case "class":
		p.class = ""
	}
}

func testngStatus(s string) Status {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "PASS":
		return Passed
	case "FAIL":
		return Failed
	}
	return Skipped
}

func (p *testngParser) tree(n *xnode) {
	st := testngStatus(n.attr("status"))
	config := n.attr("is-config") == "true"
	if config && st != Failed {
		return
	}
	if config {
		st = Errored
	}
	name := n.attr("name")
	var params []string
	for _, pr := range n.path("params").all("param") {
		if len(params) >= MaxProperties {
			break
		}
		params = append(params, strings.TrimSpace(pr.kid("value").s()))
	}
	display := name
	if len(params) > 0 {
		display = name + "[" + cutHead(strings.Join(params, ", "), 200, false) + "]"
	}
	key := p.class + "\x00" + display
	ms := msToMs(jsonNumber(n.attr("duration-ms")))
	exc := n.kid("exception")
	msg := strings.TrimSpace(exc.kid("message").s())
	if n.attr("retried") == "true" {
		// An earlier try; its final run comes later.
		if l := p.retried[key]; len(l) <= MaxAttempts && len(p.retried) < 10000 {
			p.retried[key] = append(l, Attempt{Status: testngStatus(n.attr("status")), DurationMs: ms, Message: msg})
		}
		return
	}
	earlier := p.retried[key]
	delete(p.retried, key)
	p.ms = addMs(p.ms, ms)
	p.col.add(st, false, func() Case {
		cs := Case{
			Suite:      cleanName(p.class, p.lim.MaxNameBytes),
			Name:       cleanName(display, p.lim.MaxNameBytes),
			DurationMs: ms,
		}
		if exc != nil {
			if msg == "" {
				msg = exc.attr("class")
			}
			cs.Message = cleanMessage(firstLine(cleanText(msg)), p.lim.MaxMessageBytes, false)
			if config {
				cs.Message = cleanMessage("configuration method failed: "+cs.Message, p.lim.MaxMessageBytes, false)
			}
			trace, cut := exc.kid("full-stacktrace").str()
			cs.Details = cleanBlock(trace, p.lim.MaxDetailsBytes, cut)
		}
		if st == Failed || st == Errored {
			var out strings.Builder
			for _, l := range n.path("reporter-output").all("line") {
				out.WriteString(strings.TrimSpace(l.s()))
				out.WriteByte('\n')
			}
			cs.Output = cleanTail(out.String(), p.lim.MaxOutputBytes, false)
		}
		p.col.addTags(&cs, p.groups[p.class+"."+name]...)
		for i, v := range params {
			p.col.addProperty(&cs, "param"+itoa(i), v)
		}
		if d := n.attr("description"); d != "" {
			p.col.addProperty(&cs, "description", d)
		}
		if len(earlier) > 0 {
			setRetries(&cs, len(earlier))
			for _, a := range earlier {
				p.col.addAttempt(&cs, a.Status, a.DurationMs, a.Message)
			}
		}
		return cs
	})
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
