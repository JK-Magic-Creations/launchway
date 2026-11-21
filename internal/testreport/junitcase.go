package testreport

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// The JUnit reader proper: Maven Surefire and Failsafe (flaky and rerun
// elements), Gradle, pytest (xunit1 and xunit2, with record_property),
// Jest, Vitest, PHPUnit, RSpec, Playwright, Cypress (mocha-junit-reporter),
// .NET JunitXml.TestLogger, gotestsum and others.

// The kinds of element the reader keeps track of.
type jkind uint8

const (
	kOther jkind = iota
	kSuites
	kSuite
	kCase
	kResult
	kOutput
	kProps
	kProp
)

// The result elements a testcase may hold.
const (
	rFailure = iota
	rError
	rSkipped
	rFlakyFailure
	rFlakyError
	rRerunFailure
	rRerunError
	nResults
)

var resultElements = map[string]int{
	"failure":      rFailure,
	"error":        rError,
	"skipped":      rSkipped,
	"flakyFailure": rFlakyFailure,
	"flakyError":   rFlakyError,
	"rerunFailure": rRerunFailure,
	"rerunError":   rRerunError,
}

type textSink interface{ write([]byte) }

type jframe struct {
	kind    jkind
	name    string // testsuite name
	capture textSink
	suite   *jsuite // a testsuite's own properties
	prop    *jprop  // a <property> whose value is its text
}

// rawProp is a property as read, before cleaning.
type rawProp struct{ name, value string }

type jsuite struct {
	props []rawProp
	n     int  // properties seen
	jvm   bool // the properties are a JVM's system properties (Surefire, Gradle, Ant)
}

type jprop struct {
	name  string
	value headBuf
}

type jresult struct {
	msg    string
	hasMsg bool
	msgCut bool
	body   headBuf
}

// jrerun is one flaky or rerun element: an extra run of the case.
type jrerun struct {
	st  Status
	msg string
}

type jcase struct {
	name, classname, file string
	ms                    int64
	results               [nResults]*jresult
	stdout, stderr        tailBuf
	props                 []rawProp
	attach                []string
	reruns                []jrerun
	nReruns               int
}

type junitParser struct {
	lim   Limits
	dl    *deadline
	col   *collector
	stack []jframe
	cur   *jcase
	ms    int64
}

func parseJUnit(dl *deadline, d *xml.Decoder, root xml.StartElement, lim Limits) (*Report, error) {
	p := &junitParser{lim: lim, dl: dl, col: newCollector(FormatJUnit, lim)}
	return p.run(d, root)
}

func (p *junitParser) run(d *xml.Decoder, root xml.StartElement) (*Report, error) {
	p.stack = append(p.stack, p.start(root))
	for {
		if p.dl.tick() {
			line, _ := d.InputPos()
			if p.col.rep.Counts.Total == 0 {
				return nil, p.dl.stopError()
			}
			p.col.warn("parsing %s; results after line %d are missing", p.dl.what(), line)
			break
		}
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF && len(p.stack) == 0 {
				break
			}
			line, msg := xmlErrorText(d, err)
			if strings.Contains(msg, "unexpected EOF") {
				p.col.warn("the file ends part-way through (XML error at line %d: %s); results after that are missing", line, msg)
			} else {
				p.col.warn("the XML is broken at line %d (%s); results after that are missing", line, msg)
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(p.stack) >= p.lim.MaxDepth {
				line, _ := d.InputPos()
				p.col.warn("the XML nests elements more than %d deep at line %d; results after that are missing", p.lim.MaxDepth, line)
				return p.finish(), nil
			}
			p.stack = append(p.stack, p.start(t))
		case xml.EndElement:
			if len(p.stack) == 0 {
				continue
			}
			f := p.stack[len(p.stack)-1]
			p.stack = p.stack[:len(p.stack)-1]
			switch f.kind {
			case kCase:
				p.finishCase()
			case kProp:
				if f.prop != nil {
					p.addProp(f.prop.name, string(f.prop.value.b))
				}
			}
		case xml.CharData:
			if n := len(p.stack); n > 0 && p.stack[n-1].capture != nil {
				p.stack[n-1].capture.write(t)
			}
		}
	}
	return p.finish(), nil
}

func (p *junitParser) finish() *Report {
	r := p.col.finish()
	r.DurationMs = p.ms
	return r
}

func xmlErrorText(d *xml.Decoder, err error) (int, string) {
	var se *xml.SyntaxError
	if errors.As(err, &se) {
		return se.Line, cutHead(cleanName(se.Msg, 200), 200, false)
	}
	line, _ := d.InputPos()
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		return line, "unexpected EOF"
	}
	return line, cutHead(cleanName(err.Error(), 200), 200, false)
}

func attr(t xml.StartElement, name string) (string, bool) {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

// addProp files a property with the case being read, or else with the
// innermost suite.
func (p *junitParser) addProp(name, value string) {
	name, _ = rawCut(name, 4*MaxPropertyName)
	value, _ = rawCut(value, 4*MaxPropertyText)
	if p.cur != nil {
		if len(p.cur.props) <= MaxProperties {
			p.cur.props = append(p.cur.props, rawProp{name, value})
		}
		return
	}
	for i := len(p.stack) - 1; i >= 0; i-- {
		if s := p.stack[i].suite; s != nil {
			s.n++
			if strings.HasPrefix(name, "java.") || strings.HasPrefix(name, "os.") {
				s.jvm = true
			}
			if len(s.props) <= MaxProperties {
				s.props = append(s.props, rawProp{name, value})
			}
			return
		}
	}
}

// attachSink keeps a case's output and picks out the attachments it names
// the plain-text way many report writers use: [[ATTACHMENT|path]].
type attachSink struct {
	out *tailBuf
	c   *jcase
	max int
}

var attachMark = []byte("[[ATTACHMENT|")

func (s *attachSink) write(b []byte) {
	s.out.write(b)
	for {
		i := bytes.Index(b, attachMark)
		if i < 0 {
			return
		}
		b = b[i+len(attachMark):]
		j := bytes.Index(b, []byte("]]"))
		if j < 0 {
			return
		}
		if j <= 4*MaxPathBytes && len(s.c.attach) < s.max {
			s.c.attach = append(s.c.attach, string(b[:j]))
		}
		b = b[j+2:]
	}
}

// start decides what a new element is and where its text goes.
func (p *junitParser) start(t xml.StartElement) jframe {
	local := t.Name.Local
	var parent jframe
	if n := len(p.stack); n > 0 {
		parent = p.stack[n-1]
	}
	if local == "properties" && (parent.kind == kSuite || parent.kind == kCase) {
		return jframe{kind: kProps}
	}
	if local == "property" && parent.kind == kProps {
		name, _ := attr(t, "name")
		if v, ok := attr(t, "value"); ok {
			p.addProp(name, v)
			return jframe{kind: kOther}
		}
		pr := &jprop{name: name, value: headBuf{max: 4 * MaxPropertyText}}
		return jframe{kind: kProp, prop: pr, capture: &pr.value}
	}
	if p.cur == nil {
		switch local {
		case "testsuites":
			return jframe{kind: kSuites}
		case "testsuite":
			name, _ := attr(t, "name")
			name, _ = rawCut(name, 4*p.lim.MaxNameBytes)
			return jframe{kind: kSuite, name: name, suite: &jsuite{}}
		case "testcase":
			c := &jcase{
				stdout: tailBuf{max: 4 * p.lim.MaxOutputBytes},
				stderr: tailBuf{max: 4 * p.lim.MaxOutputBytes},
			}
			v, _ := attr(t, "name")
			c.name, _ = rawCut(v, 4*p.lim.MaxNameBytes)
			v, _ = attr(t, "classname")
			c.classname, _ = rawCut(v, 4*p.lim.MaxNameBytes)
			v, _ = attr(t, "file")
			c.file, _ = rawCut(v, 4*maxFileBytes)
			v, _ = attr(t, "time")
			c.ms = secondsToMs(parseSeconds(v))
			p.cur = c
			return jframe{kind: kCase}
		}
		return jframe{kind: kOther}
	}
	if parent.kind == kCase {
		if r, ok := resultElements[local]; ok {
			if r >= rFlakyFailure {
				p.cur.nReruns++
				if len(p.cur.reruns) <= MaxAttempts {
					st := Failed
					if r == rFlakyError || r == rRerunError {
						st = Errored
					}
					m, _ := attr(t, "message")
					m, _ = rawCut(m, 4*p.lim.MaxMessageBytes)
					p.cur.reruns = append(p.cur.reruns, jrerun{st: st, msg: m})
				}
			}
			if p.cur.results[r] != nil {
				return jframe{kind: kResult} // only the first of each kind is kept
			}
			res := &jresult{body: headBuf{max: 4 * max(p.lim.MaxDetailsBytes, p.lim.MaxMessageBytes)}}
			if m, ok := attr(t, "message"); ok {
				res.hasMsg = true
				res.msg, res.msgCut = rawCut(m, 4*p.lim.MaxMessageBytes)
			}
			p.cur.results[r] = res
			return jframe{kind: kResult, capture: &res.body}
		}
		switch local {
		case "system-out":
			return jframe{kind: kOutput, capture: &attachSink{out: &p.cur.stdout, c: p.cur, max: p.lim.MaxAttachments}}
		case "system-err":
			return jframe{kind: kOutput, capture: &attachSink{out: &p.cur.stderr, c: p.cur, max: p.lim.MaxAttachments}}
		}
		return jframe{kind: kOther}
	}
	// Inside a result (for example surefire's <stackTrace>): the text
	// belongs to the result, but a rerun's own output does not.
	if local == "system-out" || local == "system-err" {
		return jframe{kind: kOther}
	}
	return jframe{kind: kOther, capture: parent.capture}
}

func (p *junitParser) suiteNames() string {
	var names []string
	for _, f := range p.stack {
		if f.kind == kSuite {
			if n := cleanName(f.name, p.lim.MaxNameBytes); n != "" {
				names = append(names, n)
			}
		}
	}
	return strings.Join(names, " / ")
}

// attachmentProperty says whether a property names a file the case
// attaches (a screenshot, a video).
func attachmentProperty(name, value string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "attachment", "screenshot", "video", "trace":
		v := strings.TrimSpace(value)
		return v != "" && !strings.Contains(v, "://") && !strings.ContainsAny(v, "\n")
	}
	return false
}

func (p *junitParser) finishCase() {
	c := p.cur
	p.cur = nil
	if c == nil {
		return
	}
	p.ms = addMs(p.ms, c.ms)
	r := &c.results
	var st Status
	var main, extra *jresult
	flaky := false
	switch {
	case r[rError] != nil || r[rRerunError] != nil:
		st, main, extra = Errored, r[rError], r[rRerunError]
		if main == nil {
			main, extra = r[rRerunError], nil
		}
	case r[rFailure] != nil || r[rRerunFailure] != nil:
		st, main, extra = Failed, r[rFailure], r[rRerunFailure]
		if main == nil {
			main, extra = r[rRerunFailure], nil
		}
	case r[rSkipped] != nil:
		st, main = Skipped, r[rSkipped]
	case r[rFlakyFailure] != nil || r[rFlakyError] != nil:
		st, flaky, main = Passed, true, r[rFlakyFailure]
		if main == nil {
			main = r[rFlakyError]
		}
	default:
		st = Passed
	}
	// Suite properties are given to each case, unless they are a JVM's
	// system properties or too many to be about the tests.
	var suiteProps []rawProp
	for i := len(p.stack) - 1; i >= 0; i-- {
		if s := p.stack[i].suite; s != nil && !s.jvm && s.n <= MaxProperties {
			suiteProps = append(suiteProps, s.props...)
		}
	}
	p.col.add(st, flaky, func() Case {
		cs := Case{
			Name:       cleanName(c.name, p.lim.MaxNameBytes),
			File:       cleanName(c.file, maxFileBytes),
			DurationMs: c.ms,
		}
		if cs.Suite = cleanName(c.classname, p.lim.MaxNameBytes); cs.Suite == "" {
			cs.Suite = cutHead(p.suiteNames(), p.lim.MaxNameBytes, false)
		}
		if main != nil {
			body := string(main.body.b)
			msg, msgCut := main.msg, main.msgCut
			if !main.hasMsg || strings.TrimSpace(msg) == "" {
				msg, msgCut = firstLine(body), false
				// PHPUnit starts the body with "Class::test".
				if c.name != "" && strings.HasSuffix(msg, "::"+c.name) {
					if _, rest, ok := strings.Cut(strings.TrimLeft(body, " \t\r\n"), "\n"); ok && firstLine(rest) != "" {
						msg = firstLine(rest)
					}
				}
			}
			cs.Message = cleanMessage(msg, p.lim.MaxMessageBytes, msgCut)
			details := body
			if strings.TrimSpace(cleanText(body)) == strings.TrimSpace(cleanText(msg)) {
				details = ""
			}
			if extra != nil {
				more := "rerun: " + extra.msg
				if b := strings.TrimSpace(string(extra.body.b)); b != "" {
					more += "\n" + b
				}
				details = joinOutput(details, "\n"+more)
			}
			cs.Details = cleanBlock(details, p.lim.MaxDetailsBytes, main.body.over)
		}
		if st == Failed || st == Errored {
			out, cut1 := c.stdout.text()
			errOut, cut2 := c.stderr.text()
			cs.Output = cleanTail(joinOutput(out, errOut), p.lim.MaxOutputBytes, cut1 || cut2)
		}
		// Reruns: a flaky case's earlier tries are its flaky elements; a
		// failing case ran first as its <failure> or <error> and then once
		// per rerun element, the last of which is its final result.
		if c.nReruns > 0 {
			setRetries(&cs, c.nReruns)
			if flaky {
				for _, a := range c.reruns {
					p.col.addAttempt(&cs, a.st, 0, a.msg)
				}
			} else if st == Failed || st == Errored {
				first := r[rFailure]
				if st == Errored {
					first = r[rError]
				}
				if first != nil {
					p.col.addAttempt(&cs, st, 0, first.msg)
				}
				for i, a := range c.reruns {
					if i == c.nReruns-1 {
						break
					}
					p.col.addAttempt(&cs, a.st, 0, a.msg)
				}
			}
		}
		for _, pr := range c.props {
			p.col.addProperty(&cs, pr.name, pr.value)
			if attachmentProperty(pr.name, pr.value) {
				p.col.addAttachmentPath(&cs, pr.name, "", pr.value)
			}
		}
		for _, pr := range suiteProps {
			p.col.addProperty(&cs, pr.name, pr.value)
		}
		for _, a := range c.attach {
			p.col.addAttachmentPath(&cs, "", "", a)
		}
		return cs
	})
}
