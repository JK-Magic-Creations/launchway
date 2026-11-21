package debrief

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"github.com/go-pdf/fpdf"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

// PDF typesets the Debrief as an A4 document: a cover, contents, an
// executive summary with a chart, the failures in detail, flaky and
// quarantined tests, slowest tests, suites, coverage and the trend, with
// page numbers. It is made in the server's own process; the Go fonts are
// embedded (as subsets) so the file looks the same everywhere and its text
// can be selected and searched. The file is not tagged for accessibility
// (the library cannot write tags); the HTML export is the accessible form,
// and this page says so in the PDF's keywords.
func PDF(r *Report) ([]byte, error) {
	d := &pdfDoc{r: r}
	d.init()
	d.cover()
	d.pdf.AddPage() // the contents, written last when the pages are known
	d.summary()
	d.failures()
	d.flaky()
	d.quarantined()
	d.slowest()
	d.suites()
	d.coverage()
	d.trend()
	d.notes()
	d.contents()
	var buf bytes.Buffer
	if err := d.pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var (
	fontOnce sync.Once
	fontReg  *sfnt.Font
)

// hasGlyph says whether the embedded fonts have a character.
func hasGlyph(r rune) bool {
	fontOnce.Do(func() { fontReg, _ = sfnt.Parse(goregular.TTF) })
	if fontReg == nil {
		return r < 0x250
	}
	var b sfnt.Buffer
	i, err := fontReg.GlyphIndex(&b, r)
	return err == nil && i != 0
}

// pdfText makes text printable in the embedded fonts: characters the font
// lacks become ?, and control characters go.
func pdfText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		case r == 0xFFFD || !hasGlyph(r):
			return '?'
		}
		return r
	}, s)
}

type tocEntry struct {
	title string
	level int
	page  int
	link  int
}

type pdfDoc struct {
	r   *Report
	pdf *fpdf.Fpdf
	toc []tocEntry
}

const (
	pageW   = 210.0
	margin  = 16.0
	bodyW   = pageW - 2*margin
	lineH   = 5.0
	monoLH  = 4.2
	maxFail = 60
)

type rgb struct{ r, g, b int }

var (
	ink     = rgb{27, 31, 35}
	muted   = rgb{84, 92, 102}
	lineCol = rgb{200, 204, 209}
	headBg  = rgb{236, 238, 241}
	passCol = rgb{23, 102, 58}
	failCol = rgb{168, 33, 27}
	warnCol = rgb{138, 83, 0}
	codeBg  = rgb{243, 244, 246}
	accent  = rgb{31, 95, 191}
)

func (d *pdfDoc) init() {
	p := fpdf.New("P", "mm", "A4", "")
	p.SetMargins(margin, margin, margin)
	p.SetAutoPageBreak(true, 18)
	p.AddUTF8FontFromBytes("Go", "", goregular.TTF)
	p.AddUTF8FontFromBytes("Go", "B", gobold.TTF)
	p.AddUTF8FontFromBytes("Go", "I", goitalic.TTF)
	p.AddUTF8FontFromBytes("Mono", "", gomono.TTF)
	r := d.r
	title := fmt.Sprintf("Debrief: %s run #%d", r.Project, r.Run.Number)
	p.SetTitle(pdfText(title), true)
	p.SetSubject("Test results report", true)
	p.SetAuthor(pdfText(nonEmpty(r.Install.Name, "Launchway")), true)
	p.SetCreator("Launchway", true)
	p.SetKeywords("test report, accessible version: the HTML export of the same Debrief", true)
	p.SetCreationDate(timeOf(r.Generated))
	p.AliasNbPages("{nb}")
	p.SetFooterFunc(func() {
		if p.PageNo() == 1 {
			return
		}
		p.SetY(-12)
		p.SetFont("Go", "", 8)
		d.color(muted)
		p.CellFormat(bodyW/2, 5, pdfText(fmt.Sprintf("%s, run #%d, %s", r.Project, r.Run.Number, shortSHA(r.Run.SHA))), "", 0, "L", false, 0, "")
		p.CellFormat(bodyW/2, 5, fmt.Sprintf("Page %d of {nb}", p.PageNo()), "", 0, "R", false, 0, "")
	})
	d.pdf = p
}

func (d *pdfDoc) color(c rgb) { d.pdf.SetTextColor(c.r, c.g, c.b) }

func (d *pdfDoc) h1(title string) {
	p := d.pdf
	if p.GetY() > 230 {
		p.AddPage()
	}
	link := p.AddLink()
	p.SetLink(link, p.GetY(), p.PageNo())
	d.toc = append(d.toc, tocEntry{title: title, level: 1, page: p.PageNo(), link: link})
	p.Ln(4)
	p.SetFont("Go", "B", 15)
	d.color(ink)
	p.CellFormat(bodyW, 8, pdfText(title), "", 1, "L", false, 0, "")
	p.SetDrawColor(lineCol.r, lineCol.g, lineCol.b)
	p.Line(margin, p.GetY(), pageW-margin, p.GetY())
	p.Ln(3)
}

func (d *pdfDoc) text(size float64, style string, c rgb, s string) {
	d.pdf.SetFont("Go", style, size)
	d.color(c)
	d.pdf.MultiCell(bodyW, lineH, pdfText(s), "", "L", false)
}

func (d *pdfDoc) cover() {
	p, r := d.pdf, d.r
	p.AddPage()
	p.SetFillColor(31, 41, 55)
	p.Rect(0, 0, pageW, 70, "F")
	p.SetXY(margin, 24)
	p.SetFont("Go", "", 12)
	p.SetTextColor(220, 225, 232)
	p.CellFormat(bodyW, 6, pdfText(nonEmpty(r.Install.Name, "Launchway")), "", 1, "L", false, 0, "")
	p.SetX(margin)
	p.SetFont("Go", "B", 32)
	p.SetTextColor(255, 255, 255)
	p.CellFormat(bodyW, 16, "Debrief", "", 1, "L", false, 0, "")
	p.SetX(margin)
	p.SetFont("Go", "", 13)
	p.CellFormat(bodyW, 7, "Test results report", "", 1, "L", false, 0, "")

	p.SetXY(margin, 90)
	d.color(ink)
	p.SetFont("Go", "B", 22)
	p.MultiCell(bodyW, 10, pdfText(r.Project), "", "L", false)
	p.Ln(2)
	rows := [][2]string{
		{"Run", fmt.Sprintf("#%d", r.Run.Number)},
		{"Branch", nonEmpty(r.Run.Branch, r.Run.Ref)},
		{"Commit", r.Run.SHA},
		{"Started", fmtTime(r.Run.CreatedAt)},
		{"Run result", r.Result},
		{"Tests", fmt.Sprintf("%d, of which %d failed, %d skipped, %d flaky", r.Summary.Total, r.Summary.Failed+r.Summary.Errored, r.Summary.Skipped, r.Summary.Flaky)},
		{"Report made", fmtTime(r.Generated)},
	}
	for _, row := range rows {
		p.SetX(margin)
		p.SetFont("Go", "", 10)
		d.color(muted)
		p.CellFormat(32, 7, row[0], "", 0, "L", false, 0, "")
		p.SetFont("Go", "B", 11)
		d.color(ink)
		p.MultiCell(bodyW-32, 7, pdfText(row[1]), "", "L", false)
	}
	// The verdict, as a word in a box and not only a color.
	p.Ln(10)
	bad := r.Summary.Failed + r.Summary.Errored
	label, col := "ALL TESTS PASSED", passCol
	switch {
	case r.Summary.Total == 0:
		label, col = "NO TEST RESULTS", muted
	case bad > 0:
		label, col = fmt.Sprintf("%d OF %d TESTS FAILED", bad, r.Summary.Total), failCol
	case r.Summary.Flaky > 0 || r.Summary.Quarantined > 0:
		label, col = "PASSED, WITH FLAKY OR QUARANTINED TESTS", warnCol
	}
	p.SetX(margin)
	p.SetDrawColor(col.r, col.g, col.b)
	p.SetLineWidth(0.8)
	p.SetFont("Go", "B", 14)
	d.color(col)
	p.CellFormat(bodyW, 14, label, "1", 1, "C", false, 0, "")
	p.SetLineWidth(0.2)
}

func (d *pdfDoc) summary() {
	p, r := d.pdf, d.r
	p.AddPage() // starts after the contents page
	d.h1("Executive summary")
	d.text(12, "B", ink, Headline(r))
	p.Ln(2)
	s := r.Summary
	d.tableOf([]col{{"Measure", 60, "L"}, {"Value", 40, "R"}}, [][]string{
		{"Tests", fmt.Sprint(s.Total)},
		{"Passed", fmt.Sprintf("%d (%s)", s.Passed, pct(s.Passed, s.Total))},
		{"Failed or errored", fmt.Sprintf("%d (%s)", s.Failed+s.Errored, pct(s.Failed+s.Errored, s.Total))},
		{"Skipped", fmt.Sprintf("%d (%s)", s.Skipped, pct(s.Skipped, s.Total))},
		{"Flaky in this run", fmt.Sprint(s.Flaky)},
		{"Quarantined", fmt.Sprint(s.Quarantined)},
		{"Duration", fmtMs(s.DurationMs)},
		{"Pass rate of the tests that ran", fmtPct(s.PassRate)},
	})
	p.Ln(4)
	d.resultChart()
	if c := r.Change; c != nil {
		p.Ln(3)
		d.text(11, "B", ink, fmt.Sprintf("Against %s, run #%d (%s)", branchName(c.Base.Branch), c.Base.Number, shortSHA(c.Base.SHA)))
		d.text(10, "", ink, fmt.Sprintf("%d newly failing, %d newly passing; %+d tests; duration %s.", c.NewlyFailing, c.NewlyPassing, c.TotalDelta, signedMs(c.DurationDelta)))
		for i, sl := range c.Slower {
			if i == 5 {
				d.text(10, "", muted, fmt.Sprintf("and %d more tests that are slower", len(c.Slower)-5))
				break
			}
			d.text(10, "", ink, fmt.Sprintf("Slower: %s, %s to %s", sl.Name, fmtMs(sl.BaseMs), fmtMs(sl.Ms)))
		}
	}
}

func pct(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmtPct(float64(n) * 100 / float64(total))
}

// resultChart draws the stacked bar with a fill pattern per state and a
// label on each part, followed by what it shows in words.
func (d *pdfDoc) resultChart() {
	p, s := d.pdf, d.r.Summary
	if s.Total == 0 {
		return
	}
	d.text(11, "B", ink, "Results by state")
	y := p.GetY() + 1
	x := margin
	w := bodyW
	h := 11.0
	type seg struct {
		label string
		n     int
		kind  int
		c     rgb
	}
	for _, sg := range []seg{{"Passed", s.Passed, 0, passCol}, {"Failed", s.Failed + s.Errored, 1, failCol}, {"Skipped", s.Skipped, 2, muted}} {
		if sg.n == 0 {
			continue
		}
		sw := max(w*float64(sg.n)/float64(s.Total), 1.2)
		p.SetFillColor(255, 255, 255)
		p.SetDrawColor(ink.r, ink.g, ink.b)
		p.Rect(x, y, sw, h, "DF")
		p.SetDrawColor(sg.c.r, sg.c.g, sg.c.b)
		p.SetLineWidth(0.25)
		switch sg.kind {
		case 0: // solid
			p.SetFillColor(sg.c.r, sg.c.g, sg.c.b)
			p.Rect(x, y, sw, h, "F")
		case 1: // diagonal stripes
			p.ClipRect(x, y, sw, h, false)
			for off := -h; off < sw; off += 2.2 {
				p.Line(x+off, y+h, x+off+h, y)
			}
			p.ClipEnd()
		case 2: // dots
			p.SetFillColor(sg.c.r, sg.c.g, sg.c.b)
			for dx := 1.0; dx < sw-0.5; dx += 2.6 {
				for dy := 1.2; dy < h-0.5; dy += 2.6 {
					p.Circle(x+dx, y+dy, 0.35, "F")
				}
			}
		}
		p.SetLineWidth(0.2)
		x += sw
	}
	p.SetY(y + h + 2)
	d.text(9, "", muted, fmt.Sprintf("Solid: passed, %d.  Diagonal stripes: failed, %d.  Dots: skipped, %d.  The table above has the same numbers.", s.Passed, s.Failed+s.Errored, s.Skipped))
}

func (d *pdfDoc) failures() {
	p, r := d.pdf, d.r
	p.AddPage()
	d.h1("Failures")
	if len(r.Failures) == 0 {
		d.text(10, "I", muted, "No test failed.")
		return
	}
	d.text(10, "", ink, fmt.Sprintf("%s.", plural(len(r.Failures), "failing test", "failing tests")))
	for i, f := range r.Failures {
		if i >= maxFail {
			d.text(10, "I", muted, fmt.Sprintf("%d more failures are in the HTML and JSON exports and in the live Debrief.", len(r.Failures)-maxFail))
			break
		}
		if p.GetY() > 245 {
			p.AddPage()
		}
		p.Ln(2)
		p.SetFont("Go", "B", 11)
		d.color(failCol)
		word := "FAILED"
		if f.Status == Errored {
			word = "ERRORED"
		}
		p.MultiCell(bodyW, 5.5, pdfText(word+"  "+f.Name), "", "L", false)
		if f.Suite != "" {
			d.text(9, "", muted, "in "+f.Suite)
		}
		var facts []string
		if f.File != "" {
			loc := f.File
			if f.Line > 0 {
				loc += fmt.Sprintf(":%d", f.Line)
			}
			facts = append(facts, "file "+loc)
		}
		facts = append(facts, "took "+fmtMs(f.DurationMs))
		if f.Variant != "" {
			facts = append(facts, "variant "+f.Variant)
		}
		if f.Owner != "" {
			facts = append(facts, "owner "+f.Owner)
		}
		if f.FirstFailedIn != nil {
			facts = append(facts, fmt.Sprintf("first failed in run #%d (%s)", f.FirstFailedIn.Number, shortSHA(f.FirstFailedIn.SHA)))
		}
		switch f.FailingOnBase {
		case "yes":
			facts = append(facts, "also failing on the default branch: yes")
		case "no":
			facts = append(facts, "also failing on the default branch: no, this change broke it")
		}
		if f.FlakyScore > 0 {
			facts = append(facts, fmt.Sprintf("flaky history (score %d): this failure may not be caused by the change", f.FlakyScore))
		}
		d.text(9, "", ink, strings.Join(facts, "  |  "))
		if f.Message != "" {
			d.mono(f.Message, 14, 1500, codeBg)
		}
		if f.Diff != nil {
			d.diff(f.Diff)
		}
		if f.Details != "" {
			d.text(9, "B", muted, "Stack or trace")
			d.mono(f.Details, 24, 4000, codeBg)
		}
		if f.Output != "" {
			d.text(9, "B", muted, "Captured output")
			d.mono(f.Output, 12, 2000, codeBg)
		}
		for _, a := range f.Attachments {
			d.text(9, "", muted, fmt.Sprintf("Attachment: %s (%s), in the live Debrief and the HTML export.", a.Name, a.Kind))
		}
	}
}

// mono prints a block of text in the fixed-width font with a light
// background, cut to a number of lines and bytes with the cut said.
func (d *pdfDoc) mono(s string, maxLines, maxBytes int, bg rgb) {
	p := d.pdf
	cut := false
	if len(s) > maxBytes {
		s, cut = s[:maxBytes], true
	}
	lines := strings.Split(strings.TrimRight(pdfText(s), "\n"), "\n")
	if len(lines) > maxLines {
		lines, cut = lines[:maxLines], true
	}
	if cut {
		lines = append(lines, "[cut; the full text is in the HTML and JSON exports]")
	}
	p.SetFont("Mono", "", 8)
	d.color(ink)
	p.SetFillColor(bg.r, bg.g, bg.b)
	for _, l := range lines {
		l = strings.ReplaceAll(l, "\t", "    ")
		p.MultiCell(bodyW, monoLH, l, "", "L", true)
	}
	p.Ln(1)
}

func (d *pdfDoc) diff(df *Diff) {
	p := d.pdf
	d.text(9, "B", muted, "Expected against actual")
	p.SetFont("Mono", "", 8)
	d.color(ink)
	n := 0
	if !strings.Contains(df.Expected, "\n") && !strings.Contains(df.Actual, "\n") {
		p.SetFillColor(251, 227, 224)
		p.MultiCell(bodyW, monoLH, "- expected: "+pdfText(cutTo(df.Expected, 600)), "", "L", true)
		p.SetFillColor(223, 243, 228)
		p.MultiCell(bodyW, monoLH, "+ actual:   "+pdfText(cutTo(df.Actual, 600)), "", "L", true)
		p.Ln(1)
		return
	}
	for _, l := range df.Lines {
		if n++; n > 40 {
			d.text(8, "I", muted, "[diff cut; the full diff is in the HTML and JSON exports]")
			break
		}
		switch l.Op {
		case "-":
			p.SetFillColor(251, 227, 224)
		case "+":
			p.SetFillColor(223, 243, 228)
		default:
			p.SetFillColor(255, 255, 255)
		}
		op := l.Op
		if op == " " {
			op = " "
		}
		p.MultiCell(bodyW, monoLH, op+" "+pdfText(cutTo(l.Text, 300)), "", "L", true)
	}
	p.Ln(1)
}

func cutTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r) + " [cut]"
}

func (d *pdfDoc) flaky() {
	r := d.r
	d.h1("Flaky tests")
	if len(r.Flaky) == 0 {
		d.text(10, "I", muted, "No test in this run has a flaky history.")
		return
	}
	d.text(9, "", muted, "Tests that passed and failed without a change that explains it. A test that passed on retry is listed here, never counted as a plain pass.")
	rows := make([][]string, len(r.Flaky))
	for i, f := range r.Flaky {
		rows[i] = []string{f.Name + "\n" + f.Suite, f.ThisRun, fmt.Sprint(f.Score), f.Reason, fmtTime(f.FirstSeen)}
	}
	d.tableOf([]col{{"Test", 62, "L"}, {"This run", 24, "L"}, {"Score", 14, "R"}, {"Why", 46, "L"}, {"First seen", 28, "L"}}, rows)
}

func (d *pdfDoc) quarantined() {
	r := d.r
	if len(r.Quarantined) == 0 {
		return
	}
	d.h1("Quarantined tests")
	d.text(9, "", muted, "Listed in the repository's quarantine file. Their failures do not fail the run, and are counted until the entry expires.")
	rows := make([][]string, len(r.Quarantined))
	for i, q := range r.Quarantined {
		exp := q.Expires
		if q.Expired {
			exp += " (expired)"
		}
		rows[i] = []string{q.Name + "\n" + q.Suite, strings.ToUpper(q.Status), q.Owner, exp, q.Reason}
	}
	d.tableOf([]col{{"Test", 58, "L"}, {"This run", 20, "L"}, {"Owner", 26, "L"}, {"Expires", 30, "L"}, {"Why", 40, "L"}}, rows)
}

func (d *pdfDoc) slowest() {
	r := d.r
	d.h1("Slowest tests")
	if len(r.Slowest) == 0 {
		d.text(10, "I", muted, "No durations were reported.")
		return
	}
	rows := make([][]string, len(r.Slowest))
	for i, s := range r.Slowest {
		usual := "-"
		if s.AvgMs > 0 {
			usual = fmtMs(s.AvgMs)
		}
		rows[i] = []string{s.Name + "\n" + s.Suite, fmtMs(s.DurationMs), usual}
	}
	d.tableOf([]col{{"Test", 118, "L"}, {"Took", 28, "R"}, {"Usually", 28, "R"}}, rows)
}

func (d *pdfDoc) suites() {
	r := d.r
	d.h1("Suites")
	if len(r.Suites) == 0 {
		d.text(10, "I", muted, "No suites.")
		return
	}
	rows := make([][]string, len(r.Suites))
	for i, s := range r.Suites {
		rows[i] = []string{strings.Repeat("  ", min(s.Depth, 6)) + s.Name, fmt.Sprint(s.Total), fmt.Sprint(s.Passed), fmt.Sprint(s.Failed), fmt.Sprint(s.Skipped), fmtMs(s.DurationMs)}
	}
	if len(rows) > 400 {
		rows = rows[:400]
		d.text(9, "I", muted, "The first 400 suites are listed; the rest are in the HTML and JSON exports.")
	}
	d.tableOf([]col{{"Suite", 82, "L"}, {"Tests", 18, "R"}, {"Passed", 18, "R"}, {"Failed", 18, "R"}, {"Skipped", 18, "R"}, {"Time", 20, "R"}}, rows)
}

func (d *pdfDoc) coverage() {
	r := d.r
	c := r.Coverage
	d.h1("Coverage")
	if c == nil {
		d.text(10, "I", muted, "No coverage report was collected for this run.")
		return
	}
	rows := [][]string{{"Lines", fmt.Sprintf("%d of %d", c.Lines.Covered, c.Lines.Total), fmtPct(c.Lines.Percent)}}
	if c.Branches.Total > 0 {
		rows = append(rows, []string{"Branches", fmt.Sprintf("%d of %d", c.Branches.Covered, c.Branches.Total), fmtPct(c.Branches.Percent)})
	}
	if c.Functions.Total > 0 {
		rows = append(rows, []string{"Functions", fmt.Sprintf("%d of %d", c.Functions.Covered, c.Functions.Total), fmtPct(c.Functions.Percent)})
	}
	if cc := c.Change; cc != nil {
		rows = append(rows, []string{"Changed lines", fmt.Sprintf("%d of %d", cc.Covered, cc.Total), fmtPct(cc.Percent)})
	}
	d.tableOf([]col{{"Measure", 60, "L"}, {"Covered", 50, "R"}, {"Percent", 30, "R"}}, rows)
	if c.BaseLines != nil {
		d.text(10, "", ink, fmt.Sprintf("Lines covered on the default branch: %s.", fmtPct(*c.BaseLines)))
	}
	if cc := c.Change; cc != nil && cc.Advisory != "" {
		d.text(10, "B", warnCol, "Advice: "+cc.Advisory)
	}
	files := func(title string, fs []FileCoverage) {
		if len(fs) == 0 {
			return
		}
		d.pdf.Ln(3)
		d.text(11, "B", ink, title)
		rows := make([][]string, len(fs))
		for i, f := range fs {
			rows[i] = []string{f.Path, fmt.Sprintf("%d/%d", f.Covered, f.Total), fmtPct(f.Percent), f.Missed}
		}
		d.tableOf([]col{{"File", 80, "L"}, {"Lines", 22, "R"}, {"Covered", 20, "R"}, {"Not covered", 52, "L"}}, rows)
	}
	if c.Change != nil {
		files("Changed files", c.Change.Files)
	}
	files(fmt.Sprintf("Least covered files (of %d)", c.Files), c.Least)
}

func (d *pdfDoc) trend() {
	r, p := d.r, d.pdf
	d.h1("Trend")
	t := r.Trend
	if len(t) < 2 {
		d.text(10, "I", muted, "A trend needs at least two runs with tests on this branch.")
		return
	}
	if p.GetY() > 190 {
		p.AddPage()
	}
	d.text(11, "B", ink, "Pass rate by run (vertical axis starts at "+fmtPct(trendFloor(t))+")")
	x0, y0, w, h := margin+10, p.GetY()+3, bodyW-14, 52.0
	p.SetDrawColor(lineCol.r, lineCol.g, lineCol.b)
	p.SetFont("Go", "", 8)
	d.color(muted)
	lo := trendFloor(t)
	for _, g := range []float64{lo, (lo + 100) / 2, 100} {
		y := y0 + h*(1-(g-lo)/(100-lo))
		p.Line(x0, y, x0+w, y)
		p.SetXY(margin, y-2)
		p.CellFormat(9, 4, fmt.Sprintf("%.0f%%", g), "", 0, "R", false, 0, "")
	}
	p.SetDrawColor(ink.r, ink.g, ink.b)
	p.SetLineWidth(0.5)
	var px, py float64
	xs, ys := make([]float64, len(t)), make([]float64, len(t))
	for i, pt := range t {
		xs[i] = x0 + w*float64(i)/float64(len(t)-1)
		ys[i] = y0 + h*(1-(pt.PassRate-lo)/(100-lo))
		if i > 0 {
			p.Line(px, py, xs[i], ys[i])
		}
		px, py = xs[i], ys[i]
	}
	p.SetLineWidth(0.2)
	step := max(1, (len(t)+11)/12)
	for i, pt := range t {
		if pt.PassRate < 100 { // a triangle where something failed
			p.SetFillColor(ink.r, ink.g, ink.b)
			p.Polygon([]fpdf.PointType{{X: xs[i], Y: ys[i] - 1.8}, {X: xs[i] + 1.7, Y: ys[i] + 1.4}, {X: xs[i] - 1.7, Y: ys[i] + 1.4}}, "F")
		} else {
			p.SetFillColor(255, 255, 255)
			p.Circle(xs[i], ys[i], 1.4, "DF")
		}
		if i%step == 0 || i == len(t)-1 {
			p.SetXY(xs[i]-6, y0+h+1)
			p.SetFont("Go", "", 7)
			d.color(muted)
			p.CellFormat(12, 4, fmt.Sprintf("#%d", pt.Number), "", 0, "C", false, 0, "")
		}
	}
	p.SetY(y0 + h + 8)
	d.text(9, "", muted, "A circle is a run where everything that ran passed; a triangle is a run with failures. The table has every value.")
	rows := make([][]string, len(t))
	for i, pt := range t {
		rows[i] = []string{fmt.Sprintf("#%d", pt.Number), shortSHA(pt.SHA), fmt.Sprint(pt.Total), fmtPct(pt.PassRate), fmtMs(pt.DurationMs), fmt.Sprint(pt.Flaky)}
	}
	d.tableOf([]col{{"Run", 22, "L"}, {"Commit", 30, "L"}, {"Tests", 24, "R"}, {"Pass rate", 30, "R"}, {"Duration", 36, "R"}, {"Flaky", 24, "R"}}, rows)
}

func (d *pdfDoc) notes() {
	if len(d.r.Notes) == 0 {
		return
	}
	d.h1("What this report leaves out")
	for _, n := range d.r.Notes {
		d.text(10, "", ink, "- "+n)
	}
}

// contents writes the table of contents onto page 2, now that the pages
// of the headings are known.
func (d *pdfDoc) contents() {
	p := d.pdf
	last := p.PageNo()
	p.SetPage(2)
	p.SetXY(margin, margin)
	p.SetFont("Go", "B", 18)
	d.color(ink)
	p.CellFormat(bodyW, 10, "Contents", "", 1, "L", false, 0, "")
	p.Ln(4)
	for _, e := range d.toc {
		p.SetX(margin)
		p.SetFont("Go", "", 12)
		d.color(accent)
		title := pdfText(e.title)
		tw := p.GetStringWidth(title)
		p.CellFormat(tw+1, 7, title, "", 0, "L", false, e.link, "")
		d.color(muted)
		dots := bodyW - tw - 14
		p.SetFont("Go", "", 12)
		dotW := p.GetStringWidth(". ")
		n := int(dots / dotW)
		p.CellFormat(dots, 7, strings.Repeat(". ", max(n, 0)), "", 0, "L", false, 0, "")
		d.color(ink)
		p.CellFormat(12, 7, fmt.Sprint(e.page), "", 1, "R", false, e.link, "")
	}
	p.SetPage(last)
}

type col struct {
	title string
	w     float64
	align string
}

// tableOf draws a table whose rows break across pages, repeating its
// header, wrapping every cell so that no text is clipped.
func (d *pdfDoc) tableOf(cols []col, rows [][]string) {
	p := d.pdf
	total := 0.0
	for _, c := range cols {
		total += c.w
	}
	scale := bodyW / total
	header := func() {
		p.SetFont("Go", "B", 9)
		d.color(ink)
		p.SetFillColor(headBg.r, headBg.g, headBg.b)
		p.SetDrawColor(lineCol.r, lineCol.g, lineCol.b)
		x := margin
		y := p.GetY()
		for _, c := range cols {
			p.SetXY(x, y)
			p.CellFormat(c.w*scale, 6.5, pdfText(c.title), "B", 0, c.align, true, 0, "")
			x += c.w * scale
		}
		p.SetXY(margin, y+6.5)
	}
	if p.GetY() > 255 {
		p.AddPage()
	}
	header()
	for _, row := range rows {
		p.SetFont("Go", "", 9)
		maxLines := 1
		cells := make([][]string, len(cols))
		for i, c := range cols {
			txt := ""
			if i < len(row) {
				txt = strings.ReplaceAll(pdfText(row[i]), "\t", " ")
			}
			cells[i] = splitCell(p, txt, c.w*scale-2)
			maxLines = max(maxLines, len(cells[i]))
		}
		h := float64(maxLines)*4.4 + 2
		if p.GetY()+h > 297-20 {
			p.AddPage()
			header()
		}
		x, y := margin, p.GetY()
		for i, c := range cols {
			p.SetXY(x, y+1)
			for j, ln := range cells[i] {
				p.SetFont("Go", "", 9)
				if j > 0 && i == 0 {
					d.color(muted)
					p.SetFont("Go", "", 8)
				} else {
					d.color(ink)
				}
				p.SetXY(x+1, y+1+float64(j)*4.4)
				p.CellFormat(c.w*scale-2, 4.4, ln, "", 0, c.align, false, 0, "")
			}
			x += c.w * scale
		}
		p.SetDrawColor(lineCol.r, lineCol.g, lineCol.b)
		p.Line(margin, y+h, pageW-margin, y+h)
		p.SetXY(margin, y+h)
	}
	p.Ln(2)
}

// splitCell wraps text to a width: at spaces, and inside a word that is
// itself wider than the cell.
func splitCell(p *fpdf.Fpdf, s string, w float64) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			if len(out) == 0 {
				out = append(out, "")
			}
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			for p.GetStringWidth(word) > w && len([]rune(word)) > 1 {
				// break a long word where it fits
				rs := []rune(word)
				n := len(rs)
				for n > 1 && p.GetStringWidth(string(rs[:n])) > w {
					n--
				}
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, string(rs[:n]))
				word = string(rs[n:])
			}
			try := word
			if line != "" {
				try = line + " " + word
			}
			if p.GetStringWidth(try) > w && line != "" {
				out = append(out, line)
				line = word
			} else {
				line = try
			}
		}
		out = append(out, line)
	}
	if len(out) > 12 {
		out = append(out[:11], "...")
	}
	return out
}
