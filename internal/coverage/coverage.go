// Package coverage reads the code coverage files that pipeline steps write
// and turns them into per-file and total counters of covered lines,
// branches and functions: Cobertura XML (coverage.py/pytest-cov, Jest and
// Vitest's cobertura reporter, coverlet, gcovr), JaCoCo XML, Clover XML
// (Istanbul, OpenClover, PHPUnit), LCOV tracefiles, Go coverprofiles and
// Istanbul's json-summary.
//
// The files come from whoever can push code, pull-request authors
// included, so they are treated as hostile: memory and nesting are
// bounded, no XML entities are expanded or fetched, percentages and totals
// written inside a file are never trusted when the line or counter data
// they summarise is there (they are computed from it; a file's own totals
// are used only where that is all the format has, such as Istanbul's
// json-summary), every count is clamped (never negative, covered never
// above total, sums stop at math.MaxInt64), strings are cleaned and cut,
// and the whole parse has a time limit. When a file is only partly
// readable, what was read is returned with a Warning instead of an error.
//
// Memory use is proportional to the size of the input: line data is kept
// (compactly) for every file so totals stay right past Limits.MaxFiles and
// so Merge can combine line data.
package coverage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The formats Parse understands.
const (
	FormatCobertura = "cobertura"
	FormatJaCoCo    = "jacoco"
	FormatLCOV      = "lcov"
	FormatGo        = "go-coverprofile"
	FormatClover    = "clover"
	FormatIstanbul  = "istanbul-json-summary"
	// FormatMixed is the Format of a Merge of reports in different formats.
	FormatMixed = "mixed"
)

// The units Report.Unit names: what the Lines counters count.
const (
	UnitLines = "lines"
	// UnitStatements is the unit of Go coverprofiles: Go records
	// statements per block, not lines, so for Go the Lines counters count
	// statements (as go tool cover -func does).
	UnitStatements = "statements"
	// UnitMixed is the Unit of a Merge of reports with different units.
	UnitMixed = "mixed"
)

// Counter is how many of one kind of thing were covered, of how many.
type Counter struct {
	Covered int64 `json:"covered"`
	Total   int64 `json:"total"` // 0 means the format or file has none of that kind
}

// Percent is Covered as a percentage of Total; ok is false when Total is 0.
func (c Counter) Percent() (float64, bool) {
	if c.Total <= 0 {
		return 0, false
	}
	return float64(c.Covered) / float64(c.Total) * 100, true
}

func (c Counter) norm() Counter {
	c.Total = max(c.Total, 0)
	c.Covered = min(max(c.Covered, 0), c.Total)
	return c
}

func (c Counter) add(o Counter) Counter {
	return Counter{Covered: addSat(c.Covered, o.Covered), Total: addSat(c.Total, o.Total)}.norm()
}

// File is the coverage of one source file.
type File struct {
	// Path is the file as the report names it, cleaned, at most
	// Limits.MaxPathBytes. For JaCoCo it is the package and the source
	// file joined with /; for Cobertura it is the filename attribute,
	// which may be relative to one of Report.Sources.
	Path string `json:"path"`
	// Package is the Cobertura, JaCoCo or Clover package; for the other
	// formats the directory of Path (for Go, the import path).
	Package   string  `json:"package,omitempty"`
	Lines     Counter `json:"lines"` // statements for Go (see Report.Unit)
	Branches  Counter `json:"branches"`
	Functions Counter `json:"functions"`
}

// Totals are the counters of a whole report.
type Totals struct {
	Lines     Counter `json:"lines"`
	Branches  Counter `json:"branches"`
	Functions Counter `json:"functions"`
}

func (t Totals) add(o Totals) Totals {
	return Totals{Lines: t.Lines.add(o.Lines), Branches: t.Branches.add(o.Branches), Functions: t.Functions.add(o.Functions)}
}

// Report is the result of parsing one file (or of merging several).
type Report struct {
	Format string `json:"format"`
	// Unit is what the Lines counters count: UnitLines, or
	// UnitStatements for Go coverprofiles.
	Unit string `json:"unit"`
	// Totals add up every file seen, including those beyond MaxFiles.
	// They are computed from the files; a file's own totals are used only
	// when it has no file entries at all (an Istanbul json-summary with
	// just "total").
	Totals Totals `json:"totals"`
	// Files holds at most Limits.MaxFiles files, sorted by Path: the
	// first ones in that order when there are more.
	Files     []File `json:"files"`
	FileCount int    `json:"file_count"`          // every distinct file seen
	Truncated bool   `json:"truncated,omitempty"` // Files holds fewer than FileCount
	Warning   string `json:"warning,omitempty"`   // a problem that did not stop parsing
	// Sources are the Cobertura <source> directories that Paths may be
	// relative to, cleaned; at most MaxSources.
	Sources []string `json:"sources,omitempty"`

	// all is every file seen with its line data, sorted by path, for Merge.
	all []*fileAcc
}

// MaxSources bounds Report.Sources.
const MaxSources = 20

// Limits bounds the work and memory one parse may use. Zero fields are
// taken as the defaults.
type Limits struct {
	MaxFiles     int           // default 20000 files in Report.Files
	MaxDepth     int           // default 64: XML element nesting and JSON nesting
	Timeout      time.Duration // default 10s for the whole parse
	MaxPathBytes int           // default 1000
}

// DefaultLimits returns the default limits.
func DefaultLimits() Limits {
	return Limits{MaxFiles: 20000, MaxDepth: 64, Timeout: 10 * time.Second, MaxPathBytes: 1000}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFiles <= 0 {
		l.MaxFiles = d.MaxFiles
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = d.MaxPathBytes
	}
	return l
}

// ErrNotReport is wrapped by errors saying the content is not a coverage
// report. Their text starts "not a coverage report: ".
var ErrNotReport = errors.New("not a coverage report")

const knownFormats = "known coverage formats are Cobertura, JaCoCo and Clover XML, LCOV, Go coverprofiles and Istanbul json-summary"

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parse detects the format from the content (never a file name) and
// parses it. A context that is already done, or that ends or passes
// Limits.Timeout before any coverage was read, gives an error (wrapping
// the context's error when there is one); one that ends later gives the
// files read so far and a Warning.
func Parse(ctx context.Context, data []byte, lim Limits) (*Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lim = lim.withDefaults()
	dl := newDeadline(ctx, lim.Timeout)
	if ctx.Err() != nil {
		return nil, dl.stopError()
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: the file is empty", ErrNotReport)
	}
	body := bytes.TrimPrefix(data, utf8BOM)
	i := 0
	for i < len(body) && isSpace(body[i]) {
		i++
	}
	if i == len(body) {
		return nil, fmt.Errorf("%w: the file holds only white space", ErrNotReport)
	}
	body = body[i:]
	switch body[0] {
	case '<':
		return parseXML(dl, body, lim)
	case '{':
		return parseIstanbul(dl, body, lim)
	case '[':
		return nil, fmt.Errorf("%w: the file is a JSON array; %s", ErrNotReport, knownFormats)
	}
	switch {
	case bytes.HasPrefix(body, []byte("mode:")):
		return parseGo(dl, body, lim)
	case bytes.HasPrefix(body, []byte("TN:")), bytes.HasPrefix(body, []byte("SF:")):
		return parseLCOV(dl, body, lim)
	}
	return nil, fmt.Errorf("%w: the format is not recognised (it starts with %s); %s", ErrNotReport, snippet(body), knownFormats)
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// deadline tells a parser when to stop: the context ends or the time limit
// passes. It only looks at the clock every so often.
type deadline struct {
	ctx   context.Context
	start time.Time
	at    time.Time
	limit time.Duration
	n     int
}

func newDeadline(ctx context.Context, limit time.Duration) *deadline {
	now := time.Now()
	return &deadline{ctx: ctx, start: now, at: now.Add(limit), limit: limit}
}

// tick is called once per token or line; every 1000 calls it checks the
// clock and the context.
func (d *deadline) tick() bool {
	d.n++
	if d.n%1000 != 0 {
		return false
	}
	return d.expired()
}

func (d *deadline) expired() bool {
	return d.ctx.Err() != nil || !time.Now().Before(d.at)
}

// what says why parsing stopped, as words that fit after "parsing".
func (d *deadline) what() string {
	if errors.Is(d.ctx.Err(), context.Canceled) {
		return "was cancelled"
	}
	if d.ctx.Err() != nil {
		return "stopped after " + time.Since(d.start).Round(time.Millisecond).String()
	}
	return "stopped after " + d.limit.String()
}

// stopError is returned when the time ran out before any coverage was read.
func (d *deadline) stopError() error {
	if err := d.ctx.Err(); err != nil {
		return fmt.Errorf("parsing %s before any coverage was read: %w", d.what(), err)
	}
	return fmt.Errorf("parsing %s before any coverage was read", d.what())
}

// The kinds of line data a file can hold.
const (
	hasLines    = 1 << iota // lines: line number and hits
	hasBlocks               // Go blocks: statements and a count
	hasBranches             // branches
	hasFuncs                // functions
)

type lineRec struct{ n, hits int64 }

// brRec is a branch, or (with block and branch -1) the branches of a line
// given as counts (Cobertura condition-coverage, JaCoCo mb/cb, Istanbul's
// clover cond lines).
type brRec struct{ line, block, branch, cov, tot int64 }

type fnRec struct {
	name string
	hits int64
}

type blockRec struct{ sl, sc, el, ec, stmts, count int64 }

// fileAcc gathers what a report says about one file. After finalize its
// slices are sorted and free of duplicates, and it is not changed again.
type fileAcc struct {
	path, pkg string
	has       uint8
	lines     []lineRec
	blocks    []blockRec
	branches  []brRec
	funcs     []fnRec
	fb        Totals // the file's own counters, used for a kind it has no data for
	out       File
}

// finalize sorts and merges the line data and computes the counters.
// Duplicates merge as "covered if covered in any": the highest hit count,
// and for counted branches the highest covered and total.
func (f *fileAcc) finalize() {
	f.lines = dedupe(f.lines, func(a, b lineRec) int { return cmp64(a.n, b.n) }, func(a *lineRec, b lineRec) {
		a.hits = max(a.hits, b.hits)
	})
	f.blocks = dedupe(f.blocks, func(a, b blockRec) int {
		if c := cmp64(a.sl, b.sl); c != 0 {
			return c
		}
		if c := cmp64(a.sc, b.sc); c != 0 {
			return c
		}
		if c := cmp64(a.el, b.el); c != 0 {
			return c
		}
		return cmp64(a.ec, b.ec)
	}, func(a *blockRec, b blockRec) {
		a.stmts, a.count = max(a.stmts, b.stmts), max(a.count, b.count)
	})
	f.branches = dedupe(f.branches, func(a, b brRec) int {
		if c := cmp64(a.line, b.line); c != 0 {
			return c
		}
		if c := cmp64(a.block, b.block); c != 0 {
			return c
		}
		return cmp64(a.branch, b.branch)
	}, func(a *brRec, b brRec) {
		a.cov, a.tot = max(a.cov, b.cov), max(a.tot, b.tot)
	})
	f.funcs = dedupe(f.funcs, func(a, b fnRec) int { return strings.Compare(a.name, b.name) }, func(a *fnRec, b fnRec) {
		a.hits = max(a.hits, b.hits)
	})

	out := File{Path: f.path, Package: f.pkg}
	switch {
	case f.has&hasLines != 0:
		for _, l := range f.lines {
			out.Lines.Total++
			if l.hits > 0 {
				out.Lines.Covered++
			}
		}
	case f.has&hasBlocks != 0:
		for _, b := range f.blocks {
			out.Lines.Total = addSat(out.Lines.Total, b.stmts)
			if b.count > 0 {
				out.Lines.Covered = addSat(out.Lines.Covered, b.stmts)
			}
		}
	default:
		out.Lines = f.fb.Lines.norm()
	}
	if f.has&hasBranches != 0 {
		for _, b := range f.branches {
			out.Branches = out.Branches.add(Counter{Covered: b.cov, Total: b.tot}.norm())
		}
	} else {
		out.Branches = f.fb.Branches.norm()
	}
	if f.has&hasFuncs != 0 {
		for _, fn := range f.funcs {
			out.Functions.Total++
			if fn.hits > 0 {
				out.Functions.Covered++
			}
		}
	} else {
		out.Functions = f.fb.Functions.norm()
	}
	out.Lines = out.Lines.norm()
	f.out = out
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// dedupe sorts s and merges neighbours that compare equal.
func dedupe[T any](s []T, cmp func(a, b T) int, merge func(a *T, b T)) []T {
	if len(s) < 2 {
		return s
	}
	slices.SortFunc(s, cmp)
	out := s[:1]
	for _, v := range s[1:] {
		last := &out[len(out)-1]
		if cmp(*last, v) == 0 {
			merge(last, v)
			continue
		}
		out = append(out, v)
	}
	return out
}

// collector gathers the files of one report.
type collector struct {
	lim      Limits
	format   string
	unit     string
	files    map[string]*fileAcc
	lastRaw  string
	lastAcc  *fileAcc
	warnings []string
	sources  []string
	// fallback is a report's own totals, used only when it lists no files.
	fallback *Totals
}

func newCollector(format string, lim Limits) *collector {
	return &collector{lim: lim, format: format, unit: UnitLines, files: map[string]*fileAcc{}}
}

// file returns the accumulator for a path as the report writes it. pkg is
// the package the report names; when empty, the directory of the path is
// used.
func (c *collector) file(raw, pkg string) *fileAcc {
	if c.lastAcc != nil && raw == c.lastRaw {
		return c.lastAcc
	}
	p := cleanName(rawCut(raw, 4*c.lim.MaxPathBytes), c.lim.MaxPathBytes)
	if p == "" {
		p = "(unnamed)"
	}
	f := c.files[p]
	if f == nil {
		pk := cleanName(rawCut(pkg, 4*c.lim.MaxPathBytes), c.lim.MaxPathBytes)
		if pk == "" && pkg == "" {
			pk = dirOf(p)
		}
		f = &fileAcc{path: p, pkg: pk}
		c.files[p] = f
	}
	c.lastRaw, c.lastAcc = strings.Clone(raw), f
	return f
}

func (c *collector) any() bool { return len(c.files) > 0 }

func (c *collector) warn(format string, args ...any) {
	if len(c.warnings) < 5 {
		c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
	}
}

func (c *collector) addSource(s string) {
	s = cleanName(s, c.lim.MaxPathBytes)
	if s == "" || len(c.sources) >= MaxSources {
		return
	}
	c.sources = append(c.sources, s)
}

func (c *collector) finish() *Report {
	all := make([]*fileAcc, 0, len(c.files))
	for _, f := range c.files {
		f.finalize()
		all = append(all, f)
	}
	r := buildReport(c.format, c.unit, all, c.lim)
	r.Sources = c.sources
	if len(all) == 0 && c.fallback != nil {
		r.Totals = Totals{Lines: c.fallback.Lines.norm(), Branches: c.fallback.Branches.norm(), Functions: c.fallback.Functions.norm()}
	}
	r.Warning = joinWarnings(c.warnings)
	return r
}

func joinWarnings(w []string) string {
	if len(w) == 0 {
		return ""
	}
	return cutHead(cleanName(strings.Join(w, "; "), 2000), 2000, false)
}

// buildReport sorts finalized files by path and fills in a report.
func buildReport(format, unit string, all []*fileAcc, lim Limits) *Report {
	sort.Slice(all, func(i, j int) bool { return all[i].path < all[j].path })
	r := &Report{Format: format, Unit: unit, Files: make([]File, 0, min(len(all), lim.MaxFiles)), FileCount: len(all), all: all}
	for _, f := range all {
		r.Totals = r.Totals.add(Totals{Lines: f.out.Lines, Branches: f.out.Branches, Functions: f.out.Functions})
		if len(r.Files) < lim.MaxFiles {
			r.Files = append(r.Files, f.out)
		} else {
			r.Truncated = true
		}
	}
	return r
}
