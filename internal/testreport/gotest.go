package testreport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// maxGoLine is the longest go test -json line that is read; longer lines
// are skipped.
const maxGoLine = 1 << 20

// goEvent is one line of go test -json output.
type goEvent struct {
	Action      string
	Package     string
	ImportPath  string // build-output and build-fail events (Go 1.24 and later)
	Test        string
	Elapsed     json.RawMessage
	Output      string
	FailedBuild string
}

// lineFacts remembers the most useful lines seen in some output, to pick a
// message from later.
type lineFacts struct {
	fileLine string // "x_test.go:12: msg" or "./x.go:3:5: undefined: y"
	first    string // the first line that is not a framework line
	limit    int
}

func (f *lineFacts) see(trimmed string) {
	if f.fileLine == "" && looksLikeFileLine(trimmed) {
		f.fileLine, _ = rawCut(trimmed, f.limit)
	}
	if f.first == "" && trimmed != "" {
		f.first, _ = rawCut(trimmed, f.limit)
	}
}

type goTest struct {
	name  string
	out   tailBuf
	facts lineFacts
	done  bool
	// loose is what the package printed, with no test named, while this test ran:
	// the output of a subprocess or a goroutine that outlived its test, which go test
	// cannot tie to a test. It is shown for a failed test that printed nothing itself.
	loose      tailBuf
	looseFacts lineFacts
}

type goPkg struct {
	name        string
	tests       map[string]*goTest
	order       []*goTest // tests in the order they started
	out         tailBuf
	facts       lineFacts // package-level output, framework lines left out
	panicLine   string
	panicRest   headBuf
	inPanic     bool
	active      []*goTest         // tests that started and have not finished
	kids        map[string]*goKid // failed subtests, by the name of the test that ran them
	buildText   bool              // the output says [build failed] or [setup failed]
	badTests    bool              // a test failed or did not finish
	finished    bool
	finishedBad bool
}

// goKid is what a parent test says about its failed subtests.
type goKid struct {
	n     int
	lines []string
}

// maxLooseTests is how many tests may run at once for package output to be
// shown with them: with more, it cannot be said which one printed it.
const maxLooseTests = 4

type goBuild struct {
	out    tailBuf
	facts  lineFacts
	failed bool
	used   bool
}

type goParser struct {
	lim      Limits
	col      *collector
	pkgs     map[string]*goPkg
	pkgOrder []*goPkg
	builds   map[string]*goBuild
	buildOrd []string
	live     int // tests and packages being tracked
	maxLive  int
	full     bool
	pkgMs    int64
	pkgTimed bool
	topMs    int64
}

func parseGoTest(dl *deadline, data []byte, lim Limits) (*Report, error) {
	g := &goParser{
		lim:     lim,
		col:     newCollector(FormatGoTest, lim),
		pkgs:    map[string]*goPkg{},
		builds:  map[string]*goBuild{},
		maxLive: 4 * lim.MaxCases,
	}
	events, lineNo, longLines, firstLong := 0, 0, 0, 0
	for pos := 0; pos < len(data); {
		if dl.tick() {
			if events == 0 {
				return nil, dl.stopError()
			}
			g.col.warn("parsing %s; results after line %d are missing", dl.what(), lineNo)
			break
		}
		var line []byte
		last := false
		if nl := bytes.IndexByte(data[pos:], '\n'); nl >= 0 {
			line, pos = data[pos:pos+nl], pos+nl+1
		} else {
			line, pos, last = data[pos:], len(data), true
		}
		lineNo++
		if len(line) > maxGoLine {
			if longLines == 0 {
				firstLong = lineNo
			}
			longLines++
			continue
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev goEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			if last {
				g.col.warn("the file ends part-way through line %d, which was ignored; results after it are missing", lineNo)
			}
			continue
		}
		if ev.Action == "" {
			continue
		}
		events++
		g.handle(&ev)
	}
	if events == 0 {
		return nil, fmt.Errorf("%w: the file is JSON, but no go test -json events were found", ErrNotReport)
	}
	if longLines == 1 {
		g.col.warn("line %d is longer than 1 MiB and was skipped", firstLong)
	} else if longLines > 1 {
		g.col.warn("%d lines longer than 1 MiB were skipped (the first at line %d)", longLines, firstLong)
	}
	g.finishAll()
	r := g.col.finish()
	if g.pkgTimed {
		r.DurationMs = g.pkgMs
	} else {
		r.DurationMs = g.topMs
	}
	return r, nil
}

// stripBuildSuffix turns "example.com/x [example.com/x.test]" into
// "example.com/x".
func stripBuildSuffix(p string) string {
	if i := strings.IndexByte(p, ' '); i >= 0 {
		p = p[:i]
	}
	return p
}

func (g *goParser) handle(ev *goEvent) {
	switch ev.Action {
	case "build-output", "build-fail":
		path := stripBuildSuffix(ev.ImportPath)
		if path == "" {
			path = ev.Package
		}
		b := g.build(path)
		if b == nil {
			return
		}
		if ev.Action == "build-fail" {
			b.failed = true
			return
		}
		b.out.writeString(ev.Output)
		forLines(ev.Output, func(line string) {
			t := strings.TrimSpace(line)
			if !strings.HasPrefix(t, "# ") {
				b.facts.see(t)
			}
		})
		return
	}
	if ev.Package == "" {
		return
	}
	p := g.pkg(ev.Package)
	if p == nil {
		return
	}
	switch ev.Action {
	case "run", "pause", "cont":
		if ev.Test != "" {
			g.test(p, ev.Test)
		}
	case "output":
		if ev.Test != "" {
			if t := g.test(p, ev.Test); t != nil {
				t.addOutput(ev.Output)
			}
			p.scanPanic(ev.Output)
		} else {
			p.addOutput(ev.Output)
		}
	case "pass", "fail", "skip":
		el, timed := secondsToMs(parseSeconds(strings.Trim(string(ev.Elapsed), `"`))), len(ev.Elapsed) > 0
		if ev.Test != "" {
			g.finishTest(p, ev.Test, ev.Action, el)
		} else {
			g.finishPkg(p, ev.Action, el, timed, ev.FailedBuild)
		}
	}
}

func (g *goParser) tooMany() bool {
	if g.live < g.maxLive {
		return false
	}
	if !g.full {
		g.full = true
		g.col.warn("more than %d tests and packages were open at once; later ones were not tracked", g.maxLive)
	}
	return true
}

func (g *goParser) build(path string) *goBuild {
	if b, ok := g.builds[path]; ok {
		return b
	}
	if path == "" || g.tooMany() {
		return nil
	}
	b := &goBuild{
		out:   tailBuf{max: 4 * g.lim.MaxOutputBytes},
		facts: lineFacts{limit: 4 * g.lim.MaxMessageBytes},
	}
	g.builds[path] = b
	g.buildOrd = append(g.buildOrd, path)
	g.live++
	return b
}

func (g *goParser) pkg(name string) *goPkg {
	if p, ok := g.pkgs[name]; ok {
		return p
	}
	if g.tooMany() {
		return nil
	}
	p := &goPkg{
		name:      name,
		tests:     map[string]*goTest{},
		kids:      map[string]*goKid{},
		out:       tailBuf{max: 4 * g.lim.MaxOutputBytes},
		facts:     lineFacts{limit: 4 * g.lim.MaxMessageBytes},
		panicRest: headBuf{max: 4 * g.lim.MaxDetailsBytes},
	}
	g.pkgs[name] = p
	g.pkgOrder = append(g.pkgOrder, p)
	g.live++
	return p
}

func (g *goParser) newTest(name string) *goTest {
	return &goTest{
		name:       name,
		out:        tailBuf{max: 4 * g.lim.MaxOutputBytes},
		facts:      lineFacts{limit: 4 * g.lim.MaxMessageBytes},
		loose:      tailBuf{max: g.lim.MaxOutputBytes},
		looseFacts: lineFacts{limit: 4 * g.lim.MaxMessageBytes},
	}
}

func (g *goParser) test(p *goPkg, name string) *goTest {
	if t, ok := p.tests[name]; ok {
		return t
	}
	if g.tooMany() {
		return nil
	}
	t := g.newTest(name)
	p.tests[name] = t
	p.order = append(p.order, t)
	p.active = append(p.active, t)
	g.live++
	return t
}

// forLines calls fn for each line of s, newline included.
func forLines(s string, fn func(string)) {
	for s != "" {
		line := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s = s[:i+1], s[i+1:]
		} else {
			s = ""
		}
		fn(line)
	}
}

// isFrameworkLine says whether a trimmed output line was written by the
// test framework rather than the test.
func isFrameworkLine(trimmed, test string) bool {
	for _, p := range [...]string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME"} {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	for _, p := range [...]string{"--- PASS: ", "--- FAIL: ", "--- SKIP: "} {
		if rest, ok := strings.CutPrefix(trimmed, p); ok && strings.HasPrefix(rest, test) {
			if len(rest) == len(test) || rest[len(test)] == ' ' {
				return true
			}
		}
	}
	return false
}

// looksLikeFileLine matches "x_test.go:12: msg" and "./x.go:3:5: msg", but
// not stack trace lines such as "/src/x.go:12 +0x1d".
func looksLikeFileLine(s string) bool {
	i := strings.Index(s, ".go:")
	if i <= 0 || strings.ContainsAny(s[:i], " \t") {
		return false
	}
	j := i + 4
	k := j
	for k < len(s) && s[k] >= '0' && s[k] <= '9' {
		k++
	}
	return k > j && k < len(s) && s[k] == ':'
}

func (t *goTest) addOutput(out string) {
	forLines(out, func(line string) {
		trimmed := strings.TrimSpace(line)
		if isFrameworkLine(trimmed, t.name) {
			return
		}
		t.facts.see(trimmed)
		t.out.writeString(line)
	})
}

func (p *goPkg) scanPanic(out string) {
	forLines(out, func(line string) { p.panicLine1(line) })
}

func (p *goPkg) panicLine1(line string) {
	trimmed := strings.TrimSpace(line)
	if p.panicLine == "" && strings.HasPrefix(trimmed, "panic:") {
		p.panicLine, _ = rawCut(trimmed, p.facts.limit)
		p.inPanic = true
		return
	}
	if p.inPanic {
		p.panicRest.writeString(line)
	}
}

func (p *goPkg) addOutput(out string) {
	p.out.writeString(out)
	forLines(out, func(line string) {
		p.panicLine1(line)
		t := strings.TrimSpace(line)
		if strings.Contains(t, "[build failed]") || strings.Contains(t, "[setup failed]") {
			p.buildText = true
		}
		if !isPackageNoise(t) {
			p.facts.see(t)
		}
	})
	p.attributeLoose(out)
}

// attributeLoose gives package-level output to the tests running just now, when
// there are few enough for that to mean something.
func (p *goPkg) attributeLoose(out string) {
	if len(p.active) == 0 || len(p.active) > maxLooseTests {
		return
	}
	forLines(out, func(line string) {
		t := strings.TrimSpace(line)
		if t == "" || isPackageNoise(t) {
			return
		}
		for _, at := range p.active {
			at.looseFacts.see(t)
			at.loose.writeString(line)
		}
	})
}

// dropActive forgets a test that finished.
func (p *goPkg) dropActive(t *goTest) {
	for i, a := range p.active {
		if a == t {
			p.active = append(p.active[:i], p.active[i+1:]...)
			return
		}
	}
}

// isPackageNoise says whether a package-level output line is just the
// summary go test always prints.
func isPackageNoise(t string) bool {
	for _, p := range [...]string{"FAIL", "PASS", "ok ", "ok\t", "?", "exit status", "# ", "=== ", "--- ", "coverage:", "testing: warning: no tests to run"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return t == "ok"
}

func (g *goParser) finishTest(p *goPkg, name, action string, ms int64) {
	t, ok := p.tests[name]
	if ok {
		delete(p.tests, name)
		g.live--
	} else {
		t = g.newTest(name)
	}
	t.done = true
	if !strings.Contains(name, "/") {
		g.topMs = addMs(g.topMs, ms)
	}
	var st Status
	switch action {
	case "pass":
		st = Passed
	case "fail":
		st = Failed
		p.badTests = true
	default:
		st = Skipped
	}
	p.dropActive(t)
	var msg, outText string
	var outCut bool
	if st != Passed {
		msg = t.facts.fileLine
		if msg == "" {
			msg = t.facts.first
		}
	}
	if st == Failed {
		outText, outCut = t.out.text()
		if strings.TrimSpace(outText) == "" {
			// It printed nothing itself: what the package printed meanwhile, a
			// subprocess's output for instance, is the best there is to show.
			if outText, outCut = t.loose.text(); strings.TrimSpace(outText) != "" && msg == "" {
				if msg = t.looseFacts.fileLine; msg == "" {
					msg = t.looseFacts.first
				}
			}
		}
		if kids := p.kids[name]; kids != nil {
			// A parent test fails because a subtest did; say which ones, and why.
			head := fmt.Sprintf("%d subtest%s failed", kids.n, plural(kids.n))
			if msg == "" {
				msg = head + ": " + kids.lines[0]
			}
			outText = head + ":\n" + strings.Join(kids.lines, "\n") + "\n" + outText
		}
		p.noteKid(name, msg)
	}
	g.col.add(st, false, func() Case {
		cs := Case{
			Suite:      cleanName(p.name, g.lim.MaxNameBytes),
			Name:       cleanName(name, g.lim.MaxNameBytes),
			DurationMs: ms,
		}
		if st != Passed {
			cs.Message = cleanMessage(msg, g.lim.MaxMessageBytes, false)
		}
		if st == Failed {
			cs.Output = cleanTail(outText, g.lim.MaxOutputBytes, outCut)
		}
		return cs
	})
	delete(p.kids, name)
	t.loose = tailBuf{}
	t.out = tailBuf{}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// maxKidLines is how many failed subtests a parent test names.
const maxKidLines = 10

// maxKidParents bounds the parents remembered at once.
const maxKidParents = 1000

// noteKid records a failed test with each test above it in the name (go test
// reports "A/b/c" without an event for "A/b" when b only groups c).
func (p *goPkg) noteKid(name, msg string) {
	for i := 0; i < len(name); i++ {
		if name[i] != '/' {
			continue
		}
		parent := name[:i]
		k := p.kids[parent]
		if k == nil {
			if len(p.kids) >= maxKidParents {
				continue
			}
			k = &goKid{}
			p.kids[parent] = k
		}
		k.n++
		if len(k.lines) < maxKidLines {
			line := name[i+1:]
			if msg != "" {
				line += ": " + firstLine(msg)
			}
			k.lines = append(k.lines, line)
		}
	}
}

const notFinished = "the test did not finish: the test binary stopped (a panic, a timeout or a crash)"

func (g *goParser) finishPkg(p *goPkg, action string, ms int64, timed bool, failedBuild string) {
	if p.finished {
		return
	}
	if timed && (action == "pass" || action == "fail") {
		g.pkgMs = addMs(g.pkgMs, ms)
		g.pkgTimed = true
	}
	g.closePkg(p, action == "fail", failedBuild)
	p.release()
	// A package can show up again (go test run twice into one file).
	delete(g.pkgs, p.name)
	g.live--
}

// closePkg reports the tests that never finished and, if the package
// failed with no failing test to show for it, the package itself.
func (g *goParser) closePkg(p *goPkg, failed bool, failedBuild string) {
	p.finished = true
	for _, t := range p.order {
		if t.done {
			continue
		}
		t.done = true
		delete(p.tests, t.name)
		g.live--
		p.badTests = true
		g.col.add(Errored, false, func() Case {
			cs := Case{
				Suite:   cleanName(p.name, g.lim.MaxNameBytes),
				Name:    cleanName(t.name, g.lim.MaxNameBytes),
				Message: notFinished,
			}
			if p.panicLine != "" {
				cs.Message = cleanMessage(notFinished+"\n"+p.panicLine, g.lim.MaxMessageBytes, false)
				cs.Details = cleanBlock(string(p.panicRest.b), g.lim.MaxDetailsBytes, p.panicRest.over)
			}
			out, cut := t.out.text()
			if strings.TrimSpace(out) == "" {
				out, cut = t.loose.text()
			}
			cs.Output = cleanTail(out, g.lim.MaxOutputBytes, cut)
			return cs
		})
	}
	p.order = nil
	if !failed || p.badTests {
		return
	}
	var b *goBuild
	if failedBuild != "" {
		b = g.builds[stripBuildSuffix(failedBuild)]
	}
	if b == nil {
		if bb := g.builds[p.name]; bb != nil && bb.failed {
			b = bb
		}
	}
	name := "[package failed]"
	if b != nil || failedBuild != "" || p.buildText {
		name = "[build failed]"
	}
	if b != nil {
		b.used = true
	}
	g.col.add(Errored, false, func() Case {
		cs := Case{
			Suite: cleanName(p.name, g.lim.MaxNameBytes),
			Name:  name,
		}
		var msg string
		var bOut string
		var bCut bool
		if b != nil {
			msg = b.facts.fileLine
			if msg == "" {
				msg = b.facts.first
			}
			bOut, bCut = b.out.text()
		}
		for _, m := range []string{p.facts.fileLine, p.panicLine, p.facts.first} {
			if msg == "" {
				msg = m
			}
		}
		if msg == "" {
			msg = "the package failed, and no test result says why"
		}
		cs.Message = cleanMessage(msg, g.lim.MaxMessageBytes, false)
		out, cut := p.out.text()
		cs.Output = cleanTail(joinOutput(bOut, out), g.lim.MaxOutputBytes, cut || bCut)
		return cs
	})
}

// release lets go of a closed package's buffers.
func (p *goPkg) release() {
	p.tests, p.order = nil, nil
	p.out, p.panicRest = tailBuf{}, headBuf{}
}

// finishAll closes everything still open when the stream ends.
func (g *goParser) finishAll() {
	for _, p := range g.pkgOrder {
		if !p.finished {
			g.closePkg(p, false, "")
			p.release()
		}
	}
	for _, path := range g.buildOrd {
		b := g.builds[path]
		if !b.failed || b.used {
			continue
		}
		g.col.add(Errored, false, func() Case {
			msg := b.facts.fileLine
			if msg == "" {
				msg = b.facts.first
			}
			if msg == "" {
				msg = "the package failed to build"
			}
			out, cut := b.out.text()
			return Case{
				Suite:   cleanName(path, g.lim.MaxNameBytes),
				Name:    "[build failed]",
				Message: cleanMessage(msg, g.lim.MaxMessageBytes, false),
				Output:  cleanTail(out, g.lim.MaxOutputBytes, cut),
			}
		})
	}
}
