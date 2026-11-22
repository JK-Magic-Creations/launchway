// Package testreport reads the test result files that pipeline steps write
// and turns them into a list of test results: JUnit XML in its dialects
// (Maven Surefire and Failsafe, Gradle, pytest, Jest, Vitest, PHPUnit, RSpec
// and others), go test -json, Cucumber JSON (Karate writes it too) and
// Karate's own JSON, Playwright's JSON report, mochawesome (Cypress), TestNG,
// NUnit 3, xUnit.net v2, .NET TRX, TAP, Robot Framework's output.xml and CTRF.
//
// Every format is turned into the same model: cases, each with optional
// steps (Cucumber and Karate steps, Robot keywords, Playwright test steps),
// tags, a variant (the browser or project a case ran in), retries,
// properties and attachments (screenshots, videos, traces, text, HTTP
// exchanges). Attachments are either named by path, which the caller
// resolves against files it kept, or carried inline in the report, in which
// case their bytes are returned (bounded) for the caller to store.
//
// The files come from whoever can push code, so they are treated as hostile:
// memory and nesting are bounded, no XML entities are expanded or fetched,
// counts and sizes written inside the file are never trusted, and the whole
// parse has a time limit. When a file is only partly readable, the results
// read so far are returned with a warning instead of an error.
package testreport

import (
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
	FormatJUnit       = "junit"
	FormatGoTest      = "go-test-json"
	FormatCucumber    = "cucumber-json" // Cucumber JSON, as cucumber-js, Cucumber-JVM, Karate and others write it
	FormatKarate      = "karate-json"   // Karate's own JSON (*.karate-json.txt)
	FormatPlaywright  = "playwright-json"
	FormatMochawesome = "mochawesome" // Cypress and Mocha with the mochawesome reporter
	FormatCTRF        = "ctrf"
	FormatTestNG      = "testng"
	FormatNUnit       = "nunit3"
	FormatXUnit       = "xunit2" // xUnit.net v2 XML
	FormatTRX         = "trx"    // .NET Visual Studio test results
	FormatTAP         = "tap"
	FormatRobot       = "robot" // Robot Framework output.xml
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

	// The richer model, filled in by the formats that have it. Every slice
	// is bounded by Limits.

	// Tags are the case's tags or categories, without a leading @, in the
	// order given, each at most MaxTagBytes; at most MaxTags.
	Tags []string `json:"tags,omitempty"`
	// Variant is what the same test ran as, when a report runs it several
	// ways: a Playwright project (chromium, firefox), a Cypress browser. It
	// is part of the test's key, so each variant has its own history.
	Variant string `json:"variant,omitempty"`
	// Retries is how many times the test tool ran the case again before
	// its final result (Playwright retries, Surefire reruns, CTRF retries).
	Retries int `json:"retries,omitempty"`
	// Attempts are the earlier tries of a case the tool retried, oldest
	// first, at most MaxAttempts.
	Attempts []Attempt `json:"attempts,omitempty"`
	// Steps are the case's steps (Cucumber and Karate steps, Robot
	// keywords, Playwright test steps), flattened in order with their
	// depth; at most Limits.MaxSteps.
	Steps []Step `json:"steps,omitempty"`
	// Attachments are files the report attaches to the case; steps refer
	// to them by index. At most Limits.MaxAttachments.
	Attachments []Attachment `json:"attachments,omitempty"`
	// Properties are name/value facts about the case (JUnit properties,
	// TRX and NUnit properties, the browser, the OS, a shard); at most
	// MaxProperties.
	Properties []Property `json:"properties,omitempty"`
}

// Attempt is one earlier try of a retried case.
type Attempt struct {
	Status     Status `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
}

// Step is one step of a case.
type Step struct {
	Depth      int    `json:"depth,omitempty"`   // 0 for a top-level step; at most MaxStepDepth
	Keyword    string `json:"keyword,omitempty"` // Given, When, Then, And, *; a Robot keyword's library; else empty
	Name       string `json:"name"`
	Status     Status `json:"status"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"` // at most Limits.MaxMessageBytes
	// Text is a doc string, data table or log the step carries (Karate's
	// request and response log), at most Limits.MaxStepTextBytes.
	Text string `json:"text,omitempty"`
	// HTTP is the request the step made and the response it got, when the
	// report says (Karate).
	HTTP *HTTPExchange `json:"http,omitempty"`
	// Attachments are indexes into the case's Attachments.
	Attachments []int `json:"attachments,omitempty"`
}

// HTTPExchange is one HTTP request and its response, as an API test made it.
type HTTPExchange struct {
	Method          string   `json:"method"`
	URL             string   `json:"url"`
	Status          int      `json:"status,omitempty"`
	DurationMs      int64    `json:"duration_ms,omitempty"`
	RequestHeaders  []Header `json:"request_headers,omitempty"`
	RequestBody     string   `json:"request_body,omitempty"`
	ResponseHeaders []Header `json:"response_headers,omitempty"`
	ResponseBody    string   `json:"response_body,omitempty"`
}

// Header is one HTTP header. Values of headers that carry credentials
// (Authorization, Cookie, Set-Cookie, Proxy-Authorization, X-Api-Key and
// the like) are replaced by "[hidden]" (see HideCredentials).
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Property is a name and a value.
type Property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Attachment is a file a report attaches to a case: named by Path (as the
// report wrote it: relative to the report file, or absolute), or carried
// inline in Data. Exactly one of Path and Data is set, except for an inline
// attachment over the limits, which has neither and says so in Problem.
type Attachment struct {
	Name        string `json:"name"`                   // as the report names it, or the file's base name
	ContentType string `json:"content_type,omitempty"` // as the report declares it; never trusted for serving
	Path        string `json:"path,omitempty"`
	Data        []byte `json:"-"`
	// Size is len(Data) for inline content (or the decoded size of one
	// left out); 0 for a path (the caller knows the file).
	Size int64 `json:"size,omitempty"`
	// Problem says why an inline attachment's bytes were not kept.
	Problem string `json:"problem,omitempty"`
}

// Bounds of the richer model that are not configurable.
const (
	MaxTags         = 20
	MaxTagBytes     = 100
	MaxProperties   = 20
	MaxPropertyName = 100
	MaxPropertyText = 500
	MaxAttempts     = 5
	MaxStepDepth    = 8
	MaxHeaders      = 50
	MaxHeaderBytes  = 1000 // a header's name and value together
	MaxHTTPBody     = 16 << 10
	MaxURLBytes     = 2000
	MaxVariantBytes = 100
	MaxAttachName   = 200
	MaxPathBytes    = 1024 // an attachment's path
)

// Key identifies a test across runs: suite, name and file, and the variant
// when there is one (so a test that runs in chromium and in firefox has two
// histories). Results without a variant keep the keys they always had.
func (c Case) Key() string {
	h := sha256.New()
	h.Write([]byte(c.Suite))
	h.Write([]byte{0})
	h.Write([]byte(c.Name))
	h.Write([]byte{0})
	h.Write([]byte(c.File))
	if c.Variant != "" {
		h.Write([]byte{0})
		h.Write([]byte(c.Variant))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

// Counts says how many tests ended each way. Flaky tests are also counted
// as Passed.
type Counts struct{ Total, Passed, Failed, Errored, Skipped, Flaky int }

// Report is the result of parsing one file.
type Report struct {
	Format     string // one of the Format constants
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
	MaxDepth        int           // default 64: XML element nesting, and JSON nesting
	Timeout         time.Duration // default 10s for the whole parse
	// InputCut says the data is only the start of a larger file, cut at a size limit: a test that began and shows
	// no result is missing, not errored.
	InputCut bool

	MaxSteps         int   // default 200 steps per case, nested ones included
	MaxStepTextBytes int   // default 8192: a step's doc string or log
	MaxAttachments   int   // default 20 per case
	MaxInlineBytes   int64 // default 5 MiB: one inline attachment, decoded; a larger one is listed without its bytes
	MaxInlineTotal   int64 // default 20 MiB: all inline attachments of one report, decoded
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

		MaxSteps:         200,
		MaxStepTextBytes: 8192,
		MaxAttachments:   20,
		MaxInlineBytes:   5 << 20,
		MaxInlineTotal:   20 << 20,
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
	if l.MaxSteps <= 0 {
		l.MaxSteps = d.MaxSteps
	}
	if l.MaxStepTextBytes <= 0 {
		l.MaxStepTextBytes = d.MaxStepTextBytes
	}
	if l.MaxAttachments <= 0 {
		l.MaxAttachments = d.MaxAttachments
	}
	if l.MaxInlineBytes <= 0 {
		l.MaxInlineBytes = d.MaxInlineBytes
	}
	if l.MaxInlineTotal <= 0 {
		l.MaxInlineTotal = d.MaxInlineTotal
	}
	return l
}

// ErrNotReport is wrapped by errors saying the content is not a test report.
var ErrNotReport = errors.New("not a test report")

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

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
	lim        Limits
	rep        Report
	warnings   []string
	once       map[string]bool // kinds of cut already noted (see warnOnce)
	inlineUsed int64           // inline attachment bytes kept so far
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

// full says whether the report has no room for more cases.
func (c *collector) full() bool { return len(c.rep.Cases) >= c.lim.MaxCases }

func (c *collector) warn(format string, args ...any) {
	if len(c.warnings) < 5 {
		w := fmt.Sprintf(format, args...)
		for _, have := range c.warnings {
			if have == w {
				return
			}
		}
		c.warnings = append(c.warnings, w)
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
