package testreport

import (
	"encoding/xml"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Robot Framework's output.xml (RF 6 and 7). A case per <test>: Suite is
// the names of the suites around it joined with ".", File the innermost
// suite's source, Name the test's name. Status PASS / FAIL / SKIP / NOT
// RUN; the status element's text is the message. Tags come from <tag>
// (RF 7) or <tags><tag> (RF 6). Keywords and control structures (FOR and
// its iterations, IF/ELSE branches, TRY/EXCEPT, WHILE, GROUP, RETURN, VAR)
// become steps with their depth; ones that did not run are left out. A
// keyword's FAIL or ERROR message is its step's error, its other messages
// its text; images in HTML messages (Selenium screenshots) are attachments
// by path. Durations come from elapsed (RF 7) or starttime/endtime (RF 6).

type robotSuite struct {
	name, source string
}

type robotParser struct {
	lim    Limits
	col    *collector
	ms     int64
	suites []robotSuite
	depth  int // elements open outside tests
	stats  int // inside <statistics> or <errors>
}

func parseRobot(dl *deadline, d *xml.Decoder, root xml.StartElement, lim Limits) (*Report, error) {
	p := &robotParser{lim: lim, col: newCollector(FormatRobot, lim)}
	w := newXMLWalker(dl, d, lim, p.col, p, "status", "tag", "tags")
	w.run(root)
	return finishXML(w, p.col, p.ms)
}

func (p *robotParser) start(t xml.StartElement) bool {
	switch t.Name.Local {
	case "statistics", "errors":
		p.stats++
	case "suite":
		if p.stats == 0 {
			name, _ := attr(t, "name")
			src, _ := attr(t, "source")
			name, _ = rawCut(name, p.lim.MaxNameBytes)
			src, _ = rawCut(src, maxFileBytes)
			p.suites = append(p.suites, robotSuite{name, src})
		}
	case "test":
		return p.stats == 0
	}
	return false
}

func (p *robotParser) end(name string) {
	switch name {
	case "statistics", "errors":
		p.stats--
	case "suite":
		if p.stats == 0 && len(p.suites) > 0 {
			p.suites = p.suites[:len(p.suites)-1]
		}
	}
}

func robotStatus(s string) Status {
	switch strings.ToUpper(s) {
	case "PASS":
		return Passed
	case "FAIL":
		return Failed
	}
	return Skipped
}

const rf6Time = "20060102 15:04:05.000"

// robotMs reads a status element's duration.
func robotMs(s *xnode) int64 {
	if s == nil {
		return 0
	}
	if e := s.attr("elapsed"); e != "" {
		return secondsToMs(parseSeconds(e))
	}
	a, err1 := time.Parse(rf6Time, s.attr("starttime"))
	b, err2 := time.Parse(rf6Time, s.attr("endtime"))
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0
	}
	return secondsToMs(b.Sub(a).Seconds())
}

func (p *robotParser) tree(n *xnode) {
	status := n.lastKid("status")
	st := robotStatus(status.attr("status"))
	ms := robotMs(status)
	p.ms = addMs(p.ms, ms)
	p.col.add(st, false, func() Case {
		var names []string
		file := ""
		for _, s := range p.suites {
			if s.name != "" {
				names = append(names, s.name)
			}
			if s.source != "" {
				file = s.source
			}
		}
		cs := Case{
			Suite:      cleanName(strings.Join(names, "."), p.lim.MaxNameBytes),
			Name:       cleanName(n.attr("name"), p.lim.MaxNameBytes),
			File:       cleanName(file, maxFileBytes),
			DurationMs: ms,
		}
		if st != Passed {
			msg, cut := status.str()
			cs.Message = cleanMessage(firstLine(cleanText(msg)), p.lim.MaxMessageBytes, false)
			if strings.Contains(strings.TrimSpace(msg), "\n") {
				cs.Details = cleanBlock(msg, p.lim.MaxDetailsBytes, cut)
			}
		}
		for _, t := range n.all("tag") {
			p.col.addTags(&cs, t.s())
		}
		for _, t := range n.path("tags").all("tag") {
			p.col.addTags(&cs, t.s())
		}
		p.body(&cs, n, 0)
		return cs
	})
}

var reRobotImg = regexp.MustCompile(`(?i)<(?:img[^>]*\ssrc|a[^>]*\shref)\s*=\s*"([^"]+\.(?:png|jpe?g|gif|webp|svg|mp4|webm))"`)

// body turns the steps inside an element into steps at depth.
func (p *robotParser) body(cs *Case, n *xnode, depth int) {
	for _, k := range n.kids {
		var name, keyword string
		switch k.name {
		case "kw":
			name = k.attr("name")
			keyword = k.attr("owner")
			if keyword == "" {
				keyword = k.attr("library")
			}
			switch t := strings.ToUpper(k.attr("type")); t {
			case "SETUP", "TEARDOWN":
				name = t + " " + name
			}
		case "setup", "teardown":
			name = strings.ToUpper(k.name) + " " + k.attr("name")
			keyword = k.attr("owner")
		case "for":
			var vars, vals []string
			for _, v := range k.all("var") {
				vars = append(vars, v.s())
			}
			for _, v := range k.all("value") {
				vals = append(vals, v.s())
			}
			name = "FOR " + strings.Join(vars, " ") + " " + k.attr("flavor") + " " + strings.Join(vals, " ")
		case "iter":
			var vars []string
			for _, v := range k.all("var") {
				vars = append(vars, v.attr("name")+"="+v.s())
			}
			name = "ITERATION " + strings.Join(vars, " ")
		case "if", "try":
			// The branches are the steps.
			p.body(cs, k, depth)
			continue
		case "branch":
			name = strings.TrimSpace(k.attr("type") + " " + k.attr("condition") + " " + k.attr("pattern"))
		case "while":
			name = strings.TrimSpace("WHILE " + k.attr("condition"))
		case "group":
			name = "GROUP " + k.attr("name")
		case "return", "break", "continue", "error":
			name = strings.ToUpper(k.name)
		case "var":
			if k.kid("status") == nil {
				continue // a keyword's assignment, not a VAR statement
			}
			name = "VAR " + k.attr("name")
		default:
			continue
		}
		status := k.lastKid("status")
		if status == nil || strings.EqualFold(status.attr("status"), "NOT RUN") {
			continue
		}
		var errText string
		var text strings.Builder
		var attach []int
		for _, m := range k.all("msg") {
			s, _ := m.str()
			lvl := strings.ToUpper(m.attr("level"))
			if m.attr("html") == "true" || strings.Contains(s, "<img") {
				for _, mm := range reRobotImg.FindAllStringSubmatch(s, 10) {
					if i := p.col.addAttachmentPath(cs, "", "", mm[1]); i >= 0 && !slices.Contains(attach, i) {
						attach = append(attach, i)
					}
				}
			}
			if (lvl == "FAIL" || lvl == "ERROR") && errText == "" {
				errText = s
				continue
			}
			if text.Len() < 4*p.lim.MaxStepTextBytes {
				text.WriteString(s)
				text.WriteByte('\n')
			}
		}
		st := robotStatus(status.attr("status"))
		if errText == "" && st == Failed {
			errText = status.s()
		}
		if !p.col.addStep(cs, stepIn{
			depth:   depth,
			keyword: keyword,
			name:    name,
			status:  st,
			ms:      robotMs(status),
			err:     errText,
			text:    text.String(),
			textCut: text.Len() >= 4*p.lim.MaxStepTextBytes,
			attach:  attach,
		}) {
			if depth > MaxStepDepth {
				return
			}
			if len(cs.Steps) >= p.lim.MaxSteps {
				return
			}
			continue
		}
		p.body(cs, k, depth+1)
	}
}
