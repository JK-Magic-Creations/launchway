package testreport

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// TAP (13 and 14; node:test, tap, bats and others). A case per test line
// ("ok N - description", "not ok N - description"); its name is the
// description (or "test N"). Subtests are indented under their parent: a
// test line with subtests is a suite, not a case, and its subtests'
// Suite is the parent names joined with " › " (when the parent failed
// while all its subtests passed, it is kept as an Errored case too). A
// "# SKIP reason" directive makes the case Skipped; "# TODO reason" makes a
// failing case Skipped (with the reason) and leaves a passing one Passed.
// The YAML block after a test line gives the message (message, or error
// as node:test writes it), the details (stack, else the block) and the
// duration (duration_ms); bats' "# in N ms" and tap's "# time=Nms" give it
// too, and comment lines right after a failing test (bats writes the
// failing command there) are added to its details. "Bail out!" ends the
// run with a warning. Lines longer than 1 MiB are skipped.

var (
	reTAPTest = regexp.MustCompile(`^(not )?ok\b\s*(\d+)?\s*(.*)$`)
	reTAPPlan = regexp.MustCompile(`^\d+\.\.\d+`)
	reTAPTime = regexp.MustCompile(`(?i)^(?:in\s+([\d.]+)\s*ms|time=([\d.]+)\s*(ms|s)?)\s*$`)
	reTAPDir  = regexp.MustCompile(`(?i)^(skip\S*|todo)\b\s*(.*)$`)
	// the totals node:test and tap print at the end
	reTAPSummary = regexp.MustCompile(`^# (tests|suites|pass|fail|cancelled|skipped|todo|duration_ms) [\d.]+$`)
)

// looksLikeTAP says whether the first line that is not a comment is a TAP
// version line, a plan or a test line.
func looksLikeTAP(b []byte) bool {
	for n := 0; n < 50 && len(b) > 0; n++ {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			b = nil
		}
		t := bytes.TrimSpace(line)
		if len(t) == 0 || t[0] == '#' {
			continue
		}
		return bytes.HasPrefix(t, []byte("TAP version")) || reTAPPlan.Match(t) ||
			bytes.HasPrefix(t, []byte("ok ")) || bytes.HasPrefix(t, []byte("not ok")) || bytes.Equal(t, []byte("ok"))
	}
	return false
}

// tapCase is a test line read and not yet placed.
type tapCase struct {
	st    Status
	name  string
	suite string
	cs    *Case // nil past MaxCases
	ms    int64
	diag  headBuf
	notes headBuf // comment lines after a failing test
	todo  bool
	done  bool   // finalize has run
	dir   string // the directive's reason
}

type tapFrame struct {
	indent int
	cases  []*tapCase
}

type tapParser struct {
	lim    Limits
	col    *collector
	frames []*tapFrame
	built  int
	ms     int64
}

func parseTAP(dl *deadline, data []byte, lim Limits) (*Report, error) {
	p := &tapParser{lim: lim, col: newCollector(FormatTAP, lim)}
	var last *tapCase // the test line the YAML block or comments belong to
	inYAML, yamlIndent := false, 0
	lineNo, longLines := 0, 0
	tests := 0
	for pos := 0; pos < len(data); {
		if dl.tick() {
			if tests == 0 {
				return nil, dl.stopError()
			}
			p.col.warn("parsing %s; results after line %d are missing", dl.what(), lineNo)
			break
		}
		var line []byte
		if nl := bytes.IndexByte(data[pos:], '\n'); nl >= 0 {
			line, pos = data[pos:pos+nl], pos+nl+1
		} else {
			line, pos = data[pos:], len(data)
		}
		lineNo++
		if len(line) > maxGoLine {
			longLines++
			continue
		}
		line = bytes.TrimRight(line, "\r")
		indent := 0
		for _, c := range line {
			if c == ' ' {
				indent++
			} else if c == '\t' {
				indent += 4
			} else {
				break
			}
		}
		t := strings.TrimSpace(string(line))
		if inYAML {
			if t == "..." {
				inYAML = false
				continue
			}
			if last != nil {
				if indent >= yamlIndent {
					last.diag.writeString(string(line[min(len(line), yamlIndent):]))
				} else {
					last.diag.writeString(t)
				}
				last.diag.writeString("\n")
			}
			continue
		}
		switch {
		case t == "":
			continue
		case t == "---" && last != nil && last.diag.b == nil:
			inYAML, yamlIndent = true, indent
			last.diag.max = 4 * max(p.lim.MaxDetailsBytes, p.lim.MaxMessageBytes)
			continue
		case strings.HasPrefix(t, "Bail out!"):
			p.col.warn("the test run bailed out: %s", cleanName(strings.TrimSpace(strings.TrimPrefix(t, "Bail out!")), 300))
			pos = len(data)
			continue
		case strings.HasPrefix(t, "#"):
			if last != nil && last.st == Failed && !strings.HasPrefix(t, "# Subtest") && !reTAPSummary.MatchString(t) {
				if last.notes.max == 0 {
					last.notes.max = 4 * p.lim.MaxDetailsBytes
				}
				last.notes.writeString(strings.TrimSpace(strings.TrimPrefix(t, "#")) + "\n")
			}
			continue
		}
		m := reTAPTest.FindStringSubmatch(t)
		if m == nil {
			p.finalize(last)
			last = nil
			continue
		}
		tests++
		p.finalize(last)
		p.flushTop()
		last = p.test(indent, m)
	}
	p.finalize(last)
	if longLines > 0 {
		p.col.warn("%d lines longer than 1 MiB were skipped", longLines)
	}
	// Whatever is still open is placed at the top.
	p.place(-1, nil)
	r := p.col.finish()
	r.DurationMs = p.ms
	return r, nil
}

// test reads a test line and places the subtests above it.
func (p *tapParser) test(indent int, m []string) *tapCase {
	ok := m[1] == ""
	desc := strings.TrimSpace(m[3])
	desc = strings.TrimPrefix(desc, "- ")
	desc = strings.TrimPrefix(desc, "-")
	directive := ""
	if i := unescapedHash(desc); i >= 0 {
		directive = strings.TrimSpace(desc[i+1:])
		desc = strings.TrimSpace(desc[:i])
	}
	desc = strings.ReplaceAll(desc, `\#`, "#")
	if desc == "" {
		desc = "test " + m[2]
	}
	tc := &tapCase{st: Passed, name: desc}
	if !ok {
		tc.st = Failed
	}
	if dm := reTAPDir.FindStringSubmatch(directive); dm != nil {
		tc.dir = dm[2]
		if strings.EqualFold(dm[1], "todo") {
			tc.todo = true
			if !ok {
				tc.st = Skipped
			}
		} else {
			tc.st = Skipped
		}
	} else if tm := reTAPTime.FindStringSubmatch(directive); tm != nil {
		if tm[1] != "" {
			tc.ms = msToMs(jsonNumber(tm[1]))
		} else if strings.EqualFold(tm[3], "s") {
			tc.ms = secondsToMs(jsonNumber(tm[2]))
		} else {
			tc.ms = msToMs(jsonNumber(tm[2]))
		}
	}
	p.place(indent, tc)
	return tc
}

// unescapedHash finds the " #" that starts a directive.
func unescapedHash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] != '\\') {
			return i
		}
	}
	return -1
}

// place puts a test line at its indent: the frames deeper than it hold its
// subtests, which move up into its own frame with its name in front of
// their suites. With tc nil, everything left is emitted.
func (p *tapParser) place(indent int, tc *tapCase) {
	var kids []*tapCase
	for len(p.frames) > 0 && p.frames[len(p.frames)-1].indent > indent {
		f := p.frames[len(p.frames)-1]
		p.frames = p.frames[:len(p.frames)-1]
		kids = append(f.cases, kids...)
	}
	if tc == nil {
		for _, k := range kids {
			p.emit(k)
		}
		return
	}
	var dst *tapFrame
	if n := len(p.frames); n > 0 && p.frames[n-1].indent == indent {
		dst = p.frames[n-1]
	} else {
		dst = &tapFrame{indent: indent}
		p.frames = append(p.frames, dst)
	}
	add := func(c *tapCase) { dst.cases = append(dst.cases, c) }
	keepParent := true
	if len(kids) > 0 {
		keepParent = false
		anyBad := false
		for _, k := range kids {
			if k.st == Failed || k.st == Errored {
				anyBad = true
			}
			if k.suite == "" {
				k.suite = tc.name
			} else if len(k.suite) < 4*p.lim.MaxNameBytes {
				k.suite = tc.name + " › " + k.suite
			}
			if k.cs != nil {
				k.cs.Suite = cleanName(k.suite, p.lim.MaxNameBytes)
			}
			add(k)
		}
		if tc.st == Failed && !anyBad {
			tc.st = Errored
			keepParent = true
		}
	}
	if keepParent {
		if p.built < p.lim.MaxCases {
			p.built++
			tc.cs = &Case{Name: cleanName(tc.name, p.lim.MaxNameBytes)}
		}
		add(tc)
	} else {
		tc.cs = nil // a suite: its YAML block is still read, then dropped
	}
}

// flushTop emits the cases placed at the top level, all of them complete.
func (p *tapParser) flushTop() {
	if len(p.frames) > 0 && p.frames[0].indent == 0 {
		for _, c := range p.frames[0].cases {
			p.emit(c)
		}
		p.frames[0].cases = p.frames[0].cases[:0]
	}
}

// emit hands a placed case to the collector.
func (p *tapParser) emit(c *tapCase) {
	p.ms = addMs(p.ms, c.ms)
	p.col.add(c.st, false, func() Case {
		if c.cs == nil {
			return Case{Name: cleanName(c.name, p.lim.MaxNameBytes), Suite: cleanName(c.suite, p.lim.MaxNameBytes)}
		}
		cs := *c.cs
		cs.DurationMs = c.ms
		return cs
	})
}

// finalize reads what followed a test line: its YAML block and comments.
func (p *tapParser) finalize(c *tapCase) {
	if c == nil || c.done {
		return
	}
	c.done = true
	block := string(c.diag.b)
	fields := tapYAML(block)
	if d, ok := fields["duration_ms"]; ok {
		c.ms = msToMs(jsonNumber(d))
	}
	if c.cs == nil {
		c.diag = headBuf{max: -1}
		return
	}
	cs := c.cs
	defer func() { c.notes = headBuf{} }()
	msg := fields["message"]
	if msg == "" {
		msg = fields["error"]
	}
	if c.st == Skipped && msg == "" {
		msg = c.dir
		if c.todo {
			msg = "TODO " + msg
		}
	}
	if c.st != Passed {
		cs.Message = cleanMessage(msg, p.lim.MaxMessageBytes, false)
		if strings.Contains(strings.TrimSpace(msg), "\n") {
			cs.Message = cleanMessage(firstLine(cleanText(msg)), p.lim.MaxMessageBytes, false)
		}
		details := fields["stack"]
		if c.st == Failed || c.st == Errored {
			details = block
		}
		cs.Details = cleanBlock(joinOutput(details, string(c.notes.b)), p.lim.MaxDetailsBytes, c.diag.over)
	}
	c.diag = headBuf{max: -1}
}

// tapYAML reads the top-level keys of a diagnostic block: plain and quoted
// scalars, and block scalars (| and >).
func tapYAML(s string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines) && len(out) < 100; i++ {
		l := lines[i]
		if l == "" || l[0] == ' ' || l[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
			var b strings.Builder
			for i+1 < len(lines) && (lines[i+1] == "" || lines[i+1][0] == ' ') {
				i++
				b.WriteString(strings.TrimPrefix(strings.TrimPrefix(lines[i], "  "), ""))
				b.WriteByte('\n')
			}
			out[k] = strings.TrimRight(dedent(b.String()), "\n")
			continue
		}
		if v == "" {
			continue // a mapping or list: not read
		}
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		} else if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			} else {
				v = v[1 : len(v)-1]
			}
		}
		out[k] = v
	}
	return out
}

// dedent removes the indentation every line shares.
func dedent(s string) string {
	common := -1
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if common < 0 || n < common {
			common = n
		}
	}
	if common <= 0 {
		return s
	}
	var b strings.Builder
	for i, l := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if len(l) >= common {
			l = l[common:]
		}
		b.WriteString(l)
	}
	return b.String()
}
