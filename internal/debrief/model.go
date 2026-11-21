// Package debrief is the report of a run's tests: what the Debrief shows,
// how a test's history is read for flakiness, how a quarantine file is read,
// how tests are split among shards, and how a Debrief is written out as one
// HTML file, a PDF, JSON or merged JUnit XML.
//
// It has no dependencies on the rest of Launchway: the engine builds a
// Report from the database and this package only reads and writes it. The
// PDF is typeset in process (github.com/go-pdf/fpdf, MIT) with the Go fonts
// (golang.org/x/image/font/gofont, BSD-style) embedded, so nothing outside
// the server binary is needed, and no browser.
package debrief

// Version is the version of the JSON form of a Report. A reader of the JSON
// export can rely on fields it knows keeping their meaning within a version.
const Version = 1

// Result words used in a Report.
const (
	Passed  = "passed"
	Failed  = "failed"
	Errored = "errored"
	Skipped = "skipped"
)

// Report is one run's Debrief.
type Report struct {
	Version   int    `json:"version"`
	Generated int64  `json:"generated_at"` // milliseconds
	Install   Brand  `json:"install"`
	Project   string `json:"project"`
	Run       RunRef `json:"run"`
	// Result is the run's own result: succeeded, failed, canceled, running.
	Result  string  `json:"result"`
	Summary Summary `json:"summary"`
	// Change is how the tests moved against the last good run of the
	// project's default branch; nil on that branch or when there is none.
	Change      *Change       `json:"change,omitempty"`
	Failures    []Failure     `json:"failures"`
	Flaky       []FlakyCase   `json:"flaky"`
	Quarantined []Quarantined `json:"quarantined"`
	Slowest     []SlowCase    `json:"slowest"`
	Suites      []Suite       `json:"suites"`
	Matrix      *Matrix       `json:"matrix,omitempty"`
	Coverage    *Coverage     `json:"coverage,omitempty"`
	Trend       []TrendPoint  `json:"trend"`
	// Notes are honest statements about what the report leaves out: results
	// cut by limits, a list shown in part, a step with no results.
	Notes []string `json:"notes"`
}

// Brand is the installation's name and an optional logo (a PNG or JPEG as a
// data URL, or an https address that is only written as text), set in
// settings. The default is neutral.
type Brand struct {
	Name string `json:"name"`
	Logo string `json:"logo,omitempty"`
	// URL is the address people open the installation at, for links back.
	URL string `json:"url,omitempty"`
}

// RunRef names a run.
type RunRef struct {
	ID         string `json:"id"`
	Number     int    `json:"number"`
	Ref        string `json:"ref"`
	Branch     string `json:"branch"`
	SHA        string `json:"sha"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Link       string `json:"link,omitempty"`
}

// Summary counts a run's tests.
type Summary struct {
	Total       int     `json:"total"`
	Passed      int     `json:"passed"`
	Failed      int     `json:"failed"`
	Errored     int     `json:"errored"`
	Skipped     int     `json:"skipped"`
	Flaky       int     `json:"flaky"`
	Quarantined int     `json:"quarantined"`
	DurationMs  int64   `json:"duration_ms"`
	PassRate    float64 `json:"pass_rate"` // percent of the tests that ran, 0 to 100
	Truncated   bool    `json:"truncated,omitempty"`
}

// Change compares a run with the last good run of the default branch.
type Change struct {
	Base           RunRef     `json:"base"`
	NewlyFailing   int        `json:"newly_failing"`
	NewlyPassing   int        `json:"newly_passing"`
	TotalDelta     int        `json:"total_delta"`
	DurationDelta  int64      `json:"duration_delta_ms"`
	Slower         []Slower   `json:"slower"`
	NewlyFailKeys  []string   `json:"newly_failing_keys,omitempty"`
	NewlyPassNames []TestName `json:"newly_passing_tests,omitempty"`
}

// TestName is a test's identity as people read it.
type TestName struct {
	Key   string `json:"key"`
	Suite string `json:"suite"`
	Name  string `json:"name"`
}

// Slower is a test that took notably longer than on the base.
type Slower struct {
	TestName
	BaseMs int64 `json:"base_ms"`
	Ms     int64 `json:"ms"`
}

// Failure is one failing test with everything needed to understand it.
type Failure struct {
	TestName
	CaseID     int64  `json:"case_id,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Variant    string `json:"variant,omitempty"`
	Step       string `json:"step,omitempty"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	Details    string `json:"details,omitempty"` // the stack or trace
	Output     string `json:"output,omitempty"`  // captured output
	Diff       *Diff  `json:"diff,omitempty"`
	Owner      string `json:"owner,omitempty"`
	// NewHere: it passed in the base run (or the previous run on the
	// branch) and fails now.
	NewHere bool `json:"new_here,omitempty"`
	// FirstFailedIn is where this test began failing on this branch.
	FirstFailedIn *RunRef `json:"first_failed_in,omitempty"`
	// FailingOnBase says whether the default branch's last run with this
	// test also fails it: "yes", "no", "unknown" (it did not run there).
	FailingOnBase string       `json:"failing_on_base,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	Retried       bool         `json:"retried,omitempty"`
	Link          string       `json:"link,omitempty"`
	// FlakyScore is set when the test's history says it is flaky although
	// it failed here: the failure may not be this change's.
	FlakyScore int `json:"flaky_score,omitempty"`
}

// Attachment is a file a test produced: a screenshot, a trace, a video.
type Attachment struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // image, video, trace, log, other
	ContentType string `json:"content_type,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	// Data is an image, inline as a data URL, for the HTML and PDF; only
	// small images are embedded. JSON carries Link, never Data.
	Data string `json:"-"`
	Link string `json:"link,omitempty"`
}

// Diff is what a test expected and what it got.
type Diff struct {
	Expected string     `json:"expected"`
	Actual   string     `json:"actual"`
	Lines    []DiffLine `json:"lines,omitempty"`
}

// DiffLine is one line of a diff: Op is " ", "-" (only in expected) or "+"
// (only in actual).
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// FlakyCase is a test that passed and failed without a change that explains it.
type FlakyCase struct {
	TestName
	Score      int      `json:"score"` // 0 to 100
	Reason     string   `json:"reason"`
	ThisRun    string   `json:"this_run"` // "passed on retry", "failed", "passed"
	Branches   []string `json:"branches,omitempty"`
	FirstSeen  int64    `json:"first_seen,omitempty"`
	Commits    int      `json:"commits"`
	SameCommit int      `json:"same_commit"`
}

// Quarantined is a test whose failure the repository's quarantine file
// excuses: it does not fail the run, and it is counted and shown.
type Quarantined struct {
	TestName
	Owner   string `json:"owner,omitempty"`
	Expires string `json:"expires,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Status  string `json:"status"`
	Expired bool   `json:"expired,omitempty"`
}

// SlowCase is one of the slowest tests.
type SlowCase struct {
	TestName
	DurationMs int64 `json:"duration_ms"`
	AvgMs      int64 `json:"avg_ms,omitempty"`
}

// Suite is one node of the tree of suites, packages or files, with the
// totals of everything below it.
type Suite struct {
	Name       string `json:"name"`
	Depth      int    `json:"depth"`
	Total      int    `json:"total"`
	Passed     int    `json:"passed"`
	Failed     int    `json:"failed"`
	Skipped    int    `json:"skipped"`
	DurationMs int64  `json:"duration_ms"`
}

// Matrix is a grid of tests by combination (a browser, a version), for runs
// that expand one step over several combinations.
type Matrix struct {
	Columns []string    `json:"columns"`
	Rows    []MatrixRow `json:"rows"`
	Cut     bool        `json:"cut,omitempty"`
}

// MatrixRow is one test across the combinations; Cells has one status per
// column, "" where it did not run.
type MatrixRow struct {
	TestName
	Cells []string `json:"cells"`
}

// Coverage is a run's coverage.
type Coverage struct {
	Lines     Ratio `json:"lines"`
	Branches  Ratio `json:"branches"`
	Functions Ratio `json:"functions"`
	// Base is the default branch's lines percentage, when known.
	BaseLines *float64 `json:"base_lines,omitempty"`
	Floor     *float64 `json:"floor,omitempty"` // the advised lowest percentage of the change
	// Change is the coverage of the lines the change added or modified.
	Change *ChangeCoverage `json:"change,omitempty"`
	// Least are the least covered files.
	Least []FileCoverage `json:"least"`
	Files int            `json:"files"`
}

// Ratio is a covered/total pair with its percentage.
type Ratio struct {
	Covered int     `json:"covered"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
}

// FileCoverage is one file's line coverage.
type FileCoverage struct {
	Path    string  `json:"path"`
	Covered int     `json:"covered"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
	// Missed lists the uncovered line ranges as "12-15, 20".
	Missed string `json:"missed,omitempty"`
}

// ChangeCoverage is the coverage of a change: the lines it added or
// modified that a test ran.
type ChangeCoverage struct {
	Covered  int            `json:"covered"`
	Total    int            `json:"total"` // changed lines that could be covered (that have code)
	Percent  float64        `json:"percent"`
	Files    []FileCoverage `json:"files"`
	Advisory string         `json:"advisory,omitempty"` // set when under the floor
	BaseRef  string         `json:"base_ref,omitempty"`
}

// TrendPoint is one run in the project's trend.
type TrendPoint struct {
	Number     int     `json:"number"`
	SHA        string  `json:"sha"`
	At         int64   `json:"at"`
	Total      int     `json:"total"`
	PassRate   float64 `json:"pass_rate"`
	DurationMs int64   `json:"duration_ms"`
	Flaky      int     `json:"flaky"`
	Coverage   float64 `json:"coverage,omitempty"`
}
