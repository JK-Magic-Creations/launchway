// Package testreport reads the test result files that pipeline steps write
// (JUnit XML and go test -json output) and turns them into a list of test
// results.
//
// The files come from whoever can push code, so they are treated as hostile:
// memory and nesting are bounded, no XML entities are expanded or fetched,
// counts and sizes written inside the file are never trusted, and the whole
// parse has a time limit. When a file is only partly readable, the results
// read so far are returned with a warning instead of an error.
package testreport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Status is the outcome of one test.
type Status string

// The four outcomes a test can have.
const (
	Passed  Status = "passed"
	Failed  Status = "failed"  // an assertion failed
	Errored Status = "errored" // the test could not run properly (JUnit <error>, a Go test that never finished, a package that failed to build)
	Skipped Status = "skipped"
)

// The formats Parse understands.
const (
	FormatJUnit  = "junit"
	FormatGoTest = "go-test-json"
)

// Case is one test result.
type Case struct {
	Suite      string `json:"suite"`          // JUnit: testcase classname, else the enclosing testsuite name(s); Go: the package import path
	Name       string `json:"name"`           // JUnit: testcase name; Go: the test name, subtests included (TestX/sub)
	File       string `json:"file,omitempty"` // JUnit file attribute when given; Go: empty
	Status     Status `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"` // one line or a few: failure/error/skip message
	Details    string `json:"details,omitempty"` // stack trace / failure body
	Output     string `json:"output,omitempty"`  // captured output; only kept for failed and errored cases
	Flaky      bool   `json:"flaky,omitempty"`   // passed after reruns inside the test tool
}

// Key identifies a test across runs: suite, name and file.
func (c Case) Key() string {
	h := sha256.New()
	h.Write([]byte(c.Suite))
	h.Write([]byte{0})
	h.Write([]byte(c.Name))
	h.Write([]byte{0})
	h.Write([]byte(c.File))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

// Counts says how many tests ended each way. Flaky tests are also counted
// as Passed.
type Counts struct{ Total, Passed, Failed, Errored, Skipped, Flaky int }

// Report is the result of parsing one file.
type Report struct {
	Format     string // FormatJUnit or FormatGoTest
	Cases      []Case // at most Limits.MaxCases, in file order
	Counts     Counts // every case found, including any beyond MaxCases
	DurationMs int64
	Truncated  bool   // Cases holds fewer cases than Counts.Total
	Warning    string // a problem that did not stop parsing
}

// Limits bounds the work and memory one parse may use.
type Limits struct {
	MaxCases        int           // default 50000
	MaxMessageBytes int           // default 2048
	MaxDetailsBytes int           // default 8192
	MaxOutputBytes  int           // default 4096 (the end of the output is kept)
	MaxNameBytes    int           // default 1000, for Suite and Name; File gets at most 500
	MaxDepth        int           // default 64: XML element nesting
	Timeout         time.Duration // default 10s for the whole parse
}

// maxFileBytes is the most a Case.File may hold.
const maxFileBytes = 500

// DefaultLimits returns the default limits. Zero fields in a Limits passed
// to Parse are taken as the defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxCases:        50000,
		MaxMessageBytes: 2048,
		MaxDetailsBytes: 8192,
		MaxOutputBytes:  4096,
		MaxNameBytes:    1000,
		MaxDepth:        64,
		Timeout:         10 * time.Second,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxCases <= 0 {
		l.MaxCases = d.MaxCases
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = d.MaxMessageBytes
	}
	if l.MaxDetailsBytes <= 0 {
		l.MaxDetailsBytes = d.MaxDetailsBytes
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = d.MaxOutputBytes
	}
	if l.MaxNameBytes <= 0 {
		l.MaxNameBytes = d.MaxNameBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	return l
}

// ErrNotReport is wrapped by errors saying the content is not a test report.
var ErrNotReport = errors.New("not a test report")

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parse detects the format from the content (never the file name) and
// parses it.
func Parse(ctx context.Context, data []byte, lim Limits) (*Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lim = lim.withDefaults()
	dl := newDeadline(ctx, lim.Timeout)

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
		return parseJUnit(dl, body, lim)
	case '{':
		return parseGoTest(dl, body, lim)
	}
	// go test -json output often starts with a few lines from standard
	// error ("go: downloading ..."), so look for an event line near the
	// start before giving up.
	if jsonLineNearStart(body) {
		r, err := parseGoTest(dl, body, lim)
		if !errors.Is(err, ErrNotReport) {
			return r, err
		}
	}
	return nil, fmt.Errorf("%w: neither JUnit XML nor go test -json output (it starts with %s)", ErrNotReport, snippet(body))
}

// jsonLineNearStart says whether a line in the first 64 KiB starts with
// {" the way every go test -json event does.
func jsonLineNearStart(b []byte) bool {
	b = b[:min(len(b), 64<<10)]
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return false
		}
		b = b[i+1:]
		if bytes.HasPrefix(bytes.TrimLeft(b, " \t\r"), []byte(`{"`)) {
			return true
		}
	}
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

// stopError is returned when the time ran out before any result was read.
func (d *deadline) stopError() error {
	if err := d.ctx.Err(); err != nil {
		return fmt.Errorf("parsing %s before any test results were read: %w", d.what(), err)
	}
	return fmt.Errorf("parsing %s before any test results were read", d.what())
}

// collector gathers cases and counts for one report.
type collector struct {
	lim      Limits
	rep      Report
	warnings []string
}

func newCollector(format string, lim Limits) *collector {
	return &collector{lim: lim, rep: Report{Format: format, Cases: []Case{}}}
}

// add counts a case and stores it while there is room. build is only called
// when the case is stored, so cases past the limit cost almost nothing.
func (c *collector) add(st Status, flaky bool, build func() Case) {
	n := &c.rep.Counts
	n.Total++
	switch st {
	case Passed:
		n.Passed++
	case Failed:
		n.Failed++
	case Errored:
		n.Errored++
	default:
		n.Skipped++
	}
	if flaky {
		n.Flaky++
	}
	if len(c.rep.Cases) >= c.lim.MaxCases {
		c.rep.Truncated = true
		return
	}
	cs := build()
	cs.Status = st
	cs.Flaky = flaky
	c.rep.Cases = append(c.rep.Cases, cs)
}

func (c *collector) warn(format string, args ...any) {
	if len(c.warnings) < 5 {
		c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
	}
}

func (c *collector) finish() *Report {
	r := c.rep
	if len(c.warnings) > 0 {
		r.Warning = cutHead(cleanText(strings.Join(c.warnings, "; ")), 2000, false)
	}
	return &r
}

// secondsToMs turns a duration in seconds into whole milliseconds. Bad
// values (negative, NaN, infinite) become 0; anything over 10^7 seconds is
// cut to 10^7 seconds.

func secondsToMs(f float64) int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0
	}
	if f > 1e7 {
		f = 1e7
	}
	return int64(math.Round(f * 1000))
}

// addMs adds two non-negative durations without overflowing.
func addMs(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}
