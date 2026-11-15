package testreport

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// JUnit XML has no single standard; this reader follows what pytest, Maven
// surefire, jest-junit, vitest, gotestsum and friends write.

type encodingKind int

const (
	encUTF8 encodingKind = iota
	encLatin1
)

// encodingOf names the kind of a declared encoding, or says it is not
// supported.
func encodingOf(label string) (encodingKind, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		// ASCII is a subset of UTF-8, and files labelled ASCII often
		// hold UTF-8 anyway.
		return encUTF8, true
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1":
		return encLatin1, true
	}
	return encUTF8, false
}

// declaredEncoding reads the encoding from the XML declaration, if any.
func declaredEncoding(data []byte) (encodingKind, error) {
	if !bytes.HasPrefix(data, []byte("<?xml")) {
		return encUTF8, nil
	}
	head := data[:min(len(data), 4096)]
	end := bytes.Index(head, []byte("?>"))
	if end < 0 {
		return encUTF8, nil // the decoder will say what is wrong
	}
	label := declParam(string(head[5:end]), "encoding")
	kind, ok := encodingOf(label)
	if !ok {
		return 0, fmt.Errorf("%w: the XML is in encoding %s, which is not supported; write it as UTF-8", ErrNotReport, cleanName(label, 40))
	}
	return kind, nil
}

// declParam finds name="value" or name='value' in an XML declaration.
func declParam(decl, name string) string {
	i := strings.Index(decl, name)
	if i < 0 {
		return ""
	}
	s := strings.TrimLeft(decl[i+len(name):], " \t\r\n")
	if !strings.HasPrefix(s, "=") {
		return ""
	}
	s = strings.TrimLeft(s[1:], " \t\r\n")
	if s == "" || (s[0] != '"' && s[0] != '\'') {
		return ""
	}
	q := s[0]
	j := strings.IndexByte(s[1:], q)
	if j < 0 {
		return ""
	}
	return s[1 : 1+j]
}

// charsetReader lets the decoder read the few 8-bit encodings that test
// tools still write. Nothing outside the standard library is needed for
// ISO-8859-1: each byte is the code point of the same number.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	kind, ok := encodingOf(label)
	if !ok {
		return nil, fmt.Errorf("encoding %s is not supported", cleanName(label, 40))
	}
	if kind == encUTF8 {
		return input, nil
	}
	return &latin1Reader{r: input}, nil
}

type latin1Reader struct {
	r       io.Reader
	in, out []byte
	pending []byte
	err     error
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	for len(l.pending) == 0 {
		if l.err != nil {
			return 0, l.err
		}
		if l.in == nil {
			l.in = make([]byte, 4096)
		}
		n, err := l.r.Read(l.in)
		l.err = err
		out := l.out[:0]
		for _, b := range l.in[:n] {
			if b < utf8.RuneSelf {
				out = append(out, b)
			} else {
				out = utf8.AppendRune(out, rune(b))
			}
		}
		l.out = out
		l.pending = out
		if n == 0 && err == nil {
			return 0, nil
		}
	}
	n := copy(p, l.pending)
	l.pending = l.pending[n:]
	return n, nil
}

// prepareXML makes real-world report bytes acceptable to the strict decoder
// without changing what they mean. Test tools often write raw control
// characters, broken UTF-8 and references such as &#27; into captured
// output, and the decoder rejects the whole file for one of them.
//
//   - invalid UTF-8 and characters XML forbids become U+FFFD (UTF-8 input only)
//   - a raw control character c becomes &#xE0cc; (a private-use character
//     that cleanText removes again, after using an escape to strip colours)
//   - a reference to a forbidden character becomes the same, or &#xFFFD;
//   - an & that does not start one of the five predefined entities or a
//     character reference becomes &amp;, so &nbsp; or a DOCTYPE entity
//     shows up as plain text and is never expanded
//
// Text inside CDATA sections and comments is left alone apart from the
// character fixes. It returns nil if the time ran out.
func prepareXML(dl *deadline, src []byte, latin bool) []byte {
	out := make([]byte, 0, len(src)+len(src)/32)
	inCDATA, inComment := false, false
	next := 1 << 20
	for i := 0; i < len(src); {
		if i >= next {
			if dl.expired() {
				return nil
			}
			next = i + 1<<20
		}
		b := src[i]
		switch {
		case inCDATA:
			if b == ']' && bytes.HasPrefix(src[i:], []byte("]]>")) {
				out = append(out, "]]>"...)
				i += 3
				inCDATA = false
				continue
			}
		case inComment:
			if b == '-' && bytes.HasPrefix(src[i:], []byte("-->")) {
				out = append(out, "-->"...)
				i += 3
				inComment = false
				continue
			}
		case b == '<':
			if bytes.HasPrefix(src[i:], []byte("<![CDATA[")) {
				out = append(out, "<![CDATA["...)
				i += 9
				inCDATA = true
				continue
			}
			if bytes.HasPrefix(src[i:], []byte("<!--")) {
				out = append(out, "<!--"...)
				i += 4
				inComment = true
				continue
			}
		case b == '&':
			var n int
			out, n = appendRef(out, src[i:])
			i += n
			continue
		}
		switch {
		case b >= 0x20 && b < utf8.RuneSelf, b == '\t', b == '\n', b == '\r':
			out = append(out, b)
			i++
		case b < 0x20:
			if !inCDATA && !inComment {
				out = appendCtrlRef(out, int(b))
			} else if latin {
				out = append(out, '?')
			} else {
				out = utf8.AppendRune(out, ctrlBase+rune(b))
			}
			i++
		case latin:
			out = append(out, b)
			i++
		default:
			r, size := utf8.DecodeRune(src[i:])
			if (r == utf8.RuneError && size <= 1) || !isXMLChar(r) {
				out = utf8.AppendRune(out, utf8.RuneError)
			} else {
				out = append(out, src[i:i+size]...)
			}
			i += max(size, 1)
		}
	}
	return out
}

const hexDigits = "0123456789ABCDEF"

func appendCtrlRef(out []byte, c int) []byte {
	return append(out, '&', '#', 'x', 'E', '0', hexDigits[c>>4], hexDigits[c&15], ';')
}

func isXMLChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// appendRef handles an & at the start of s and says how many bytes of s it
// used.
func appendRef(out, s []byte) ([]byte, int) {
	if len(s) > 1 && s[1] == '#' {
		j, base := 2, 10
		if len(s) > 2 && (s[2] == 'x' || s[2] == 'X') {
			j, base = 3, 16
		}
		start := j
		v, big := 0, false
		for j < len(s) && j-start < 40 {
			d := digitValue(s[j], base)
			if d < 0 {
				break
			}
			if v > 0x10FFFF {
				big = true
			} else {
				v = v*base + d
			}
			j++
		}
		if j == start || j >= len(s) || s[j] != ';' {
			return append(out, "&amp;"...), 1
		}
		j++ // the ;
		switch {
		case !big && isXMLChar(rune(v)):
			return append(out, s[:j]...), j
		case !big && v < 0x20:
			return appendCtrlRef(out, v), j
		default:
			return append(out, "&#xFFFD;"...), j
		}
	}
	for _, name := range [...]string{"&amp;", "&lt;", "&gt;", "&quot;", "&apos;"} {
		if bytes.HasPrefix(s, []byte(name)) {
			return append(out, name...), len(name)
		}
	}
	return append(out, "&amp;"...), 1
}

func digitValue(c byte, base int) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case base == 16 && c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case base == 16 && c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// The kinds of element the reader keeps track of.
type jkind uint8

const (
	kOther jkind = iota
	kSuites
	kSuite
	kCase
	kResult
	kOutput
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
}

type jresult struct {
	msg    string
	hasMsg bool
	msgCut bool
	body   headBuf
}

type jcase struct {
	name, classname, file string
	ms                    int64
	results               [nResults]*jresult
	stdout, stderr        tailBuf
}

type junitParser struct {
	lim   Limits
	dl    *deadline
	col   *collector
	stack []jframe
	cur   *jcase
	ms    int64
}

func parseJUnit(dl *deadline, data []byte, lim Limits) (*Report, error) {
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
	p := &junitParser{lim: lim, dl: dl, col: newCollector(FormatJUnit, lim)}
	return p.run(d)
}

func (p *junitParser) run(d *xml.Decoder) (*Report, error) {
	rootSeen := false
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
				if !rootSeen {
					return nil, fmt.Errorf("%w: the XML nests elements more than %d deep", ErrNotReport, p.lim.MaxDepth)
				}
				p.col.warn("the XML nests elements more than %d deep at line %d; results after that are missing", p.lim.MaxDepth, line)
				return p.finish(), nil
			}
			if len(p.stack) == 0 && !rootSeen {
				if t.Name.Local != "testsuites" && t.Name.Local != "testsuite" {
					return nil, fmt.Errorf("%w: the XML's root element is <%s>, not <testsuites> or <testsuite>", ErrNotReport, cleanName(t.Name.Local, 60))
				}
				rootSeen = true
			}
			p.stack = append(p.stack, p.start(t))
		case xml.EndElement:
			if len(p.stack) == 0 {
				continue
			}
			f := p.stack[len(p.stack)-1]
			p.stack = p.stack[:len(p.stack)-1]
			if f.kind == kCase {
				p.finishCase()
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

// start decides what a new element is and where its text goes.
func (p *junitParser) start(t xml.StartElement) jframe {
	local := t.Name.Local
	var parent jframe
	if n := len(p.stack); n > 0 {
		parent = p.stack[n-1]
	}
	if p.cur == nil {
		switch local {
		case "testsuites":
			return jframe{kind: kSuites}
		case "testsuite":
			name, _ := attr(t, "name")
			name, _ = rawCut(name, 4*p.lim.MaxNameBytes)
			return jframe{kind: kSuite, name: name}
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
			return jframe{kind: kOutput, capture: &p.cur.stdout}
		case "system-err":
			return jframe{kind: kOutput, capture: &p.cur.stderr}
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
		return cs
	})
}
