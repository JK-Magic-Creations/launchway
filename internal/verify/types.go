// Package verify is Launchway's deployment verification: it judges, from
// metrics and logs read during a time window, whether a deployment is
// healthy. Everything in this package is pure: no network, no clock, no
// randomness. The deploy runner reads the data (package verify/source) and
// reports it interval by interval; every server folds the same reports with
// Evaluate and reaches the same verdict.
//
// The analysis is statistics built into Launchway. No AI service is needed;
// a model may be asked afterwards to explain a verdict (and, only if the
// pipeline says so, to turn a pass into inconclusive or a fail; never a fail
// into a pass).
package verify

// Outcomes of a verification and of each check.
const (
	Running      = "running"      // not decided yet: more intervals to come
	Passed       = "passed"       // every check passed with enough data
	Failed       = "failed"       // a check failed
	Inconclusive = "inconclusive" // no check failed, but some had too little data to say
)

// Sensitivity says how large a deviation must be to count. See Thresholds.
const (
	Low    = "low"
	Medium = "medium"
	High   = "high"
)

// Directions of a metric: which way is worse.
const (
	HigherIsWorse = "higher-is-worse"
	LowerIsWorse  = "lower-is-worse"
	Either        = "either"
)

// Log cluster classes.
const (
	Known                = "known"                // seen in the reference (baseline or stable) at a similar rate
	Unknown              = "unknown"              // not seen in the reference: new
	UnexpectedFrequency  = "unexpected_frequency" // seen in the reference, but much more often now
	NoReference          = "no_reference"         // there is no reference to compare with
)

// Spec is what a verification judges: the resolved form of a verify step,
// as the server hands it to the deploy runner and folds it again. Queries
// are not part of it (the runner needs them, the analysis does not).
type Spec struct {
	Sensitivity string       `json:"sensitivity"` // low, medium or high
	Intervals   int          `json:"intervals"`   // how many intervals the window has
	IntervalMS  int64        `json:"interval_ms"` // length of one interval
	Metrics     []MetricSpec `json:"metrics,omitempty"`
	Logs        []LogSpec    `json:"logs,omitempty"`
}

// MetricSpec is how one metric is judged.
type MetricSpec struct {
	Name      string `json:"name"`
	Direction string `json:"direction"` // higher-is-worse (default), lower-is-worse or either
	// Fixed bounds: a reading above Max or below Min fails the interval.
	Max *float64 `json:"max,omitempty"`
	Min *float64 `json:"min,omitempty"`
	// FailFastMax and FailFastMin are bounds that fail the whole
	// verification at the first interval that breaches them.
	FailFastMax *float64 `json:"fail_fast_max,omitempty"`
	FailFastMin *float64 `json:"fail_fast_min,omitempty"`
	// Stable: compare with the stable version's series in the same interval
	// (the step has a stable query). Baseline: compare with the series of
	// earlier passed verifications (when a baseline exists).
	Stable   bool `json:"stable,omitempty"`
	Baseline bool `json:"baseline,omitempty"`
	// Failures is how many consecutive failing intervals fail the metric;
	// default 2 (1 when the window has a single interval).
	Failures int `json:"failures,omitempty"`
	// MinIntervals is how many intervals must have data for the metric to
	// pass; fewer is inconclusive, never a pass. Default: half the window's
	// intervals, at least 1.
	MinIntervals int `json:"min_intervals,omitempty"`
	// MinPoints is how many points an interval needs to count as having
	// data; default 1.
	MinPoints int `json:"min_points,omitempty"`
}

// LogSpec is how one log query is judged.
type LogSpec struct {
	Name string `json:"name"`
	// Errors says which clusters count as errors: auto (the default: those
	// whose template has an error word such as error, exception, fatal or
	// panic) or all (the query already selects only errors).
	Errors string `json:"errors,omitempty"`
	Stable bool   `json:"stable,omitempty"` // the step has a stable query
	// Baseline: compare with the clusters of earlier passed verifications.
	Baseline bool `json:"baseline,omitempty"`
}

// Point is one reading of a metric.
type Point struct {
	T int64   `json:"t"` // Unix ms
	V float64 `json:"v"`
}

// Cluster is a group of log lines with one template, as the deploy runner's
// clusterer made it. Samples are redacted before they leave the runner.
type Cluster struct {
	Template string   `json:"template"` // tokens separated by single spaces; <*> marks a variable token
	Count    int      `json:"count"`
	Samples  []string `json:"samples,omitempty"` // at most MaxSamples, each at most MaxSampleBytes
}

// Limits on what an interval report may carry, enforced by the runner and
// again by the server.
const (
	MaxPointsPerSeries = 500
	MaxClusters        = 200
	MaxSamples         = 3
	MaxSampleBytes     = 500
	MaxTemplateBytes   = 1000
	MaxErrorBytes      = 1000
)

// Interval is what the deploy runner read in one interval. It is recorded
// in a verify.interval event exactly as accepted.
type Interval struct {
	Index   int          `json:"index"` // 0-based
	Start   int64        `json:"start"` // Unix ms
	End     int64        `json:"end"`
	Metrics []MetricData `json:"metrics,omitempty"`
	Logs    []LogData    `json:"logs,omitempty"`
}

// MetricData is one metric's readings in one interval: the new version's
// and, when the step has a stable query, the stable version's. Error says
// why it could not be read (the source did not answer, refused, or returned
// something unusable); the interval then has no data for the metric.
type MetricData struct {
	Name   string  `json:"name"`
	Canary []Point `json:"canary,omitempty"`
	Stable []Point `json:"stable,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// LogData is one log query's clusters in one interval.
type LogData struct {
	Name      string    `json:"name"`
	Lines     int       `json:"lines"`                // lines read
	Truncated bool      `json:"truncated,omitempty"`  // the source had more than the limit
	Clusters  []Cluster `json:"clusters,omitempty"`   // the new version's
	Stable    []Cluster `json:"stable,omitempty"`     // the stable version's, with a stable query
	StableN   int       `json:"stable_lines,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Baseline is what earlier passed verifications of the same environment,
// project and service saw, by metric and log name. The server records it
// in the verify.started event, so every server judges against the same one.
type Baseline struct {
	Runs    []BaselineRun          `json:"runs,omitempty"` // where it came from, newest first
	Metrics map[string][]float64   `json:"metrics,omitempty"`
	Logs    map[string]BaselineLog `json:"logs,omitempty"`
}

// BaselineRun names one verification a baseline comes from.
type BaselineRun struct {
	Run     string `json:"run"`
	Number  int    `json:"number"`
	Step    string `json:"step"`
	Version string `json:"version,omitempty"`
	At      int64  `json:"at"`
}

// BaselineLog is a log query's clusters in earlier verifications, with the
// number of lines and minutes they were read over (for rates).
type BaselineLog struct {
	Clusters []Cluster `json:"clusters"`
	Lines    int       `json:"lines"`
	Minutes  float64   `json:"minutes"`
}

// Verdict is what Evaluate decides from a spec, a baseline and the
// intervals so far.
type Verdict struct {
	Outcome string `json:"outcome"` // running, passed, failed or inconclusive
	// Final says no more intervals are needed: the window is over, or a
	// check failed early (fail fast or enough consecutive failures).
	Final bool `json:"final"`
	// Summary says, in one or two plain sentences, why: "error-rate: 4.2%
	// versus baseline 0.3%, 6.1 standard deviations above (medium needs 3)".
	Summary string          `json:"summary"`
	Metrics []MetricVerdict `json:"metrics,omitempty"`
	Logs    []LogVerdict    `json:"logs,omitempty"`
	// Intervals is how many intervals were judged.
	Intervals int `json:"intervals"`
}

// MetricVerdict is one metric's result.
type MetricVerdict struct {
	Name      string          `json:"name"`
	Outcome   string          `json:"outcome"` // running, passed, failed or inconclusive
	Reason    string          `json:"reason"`  // in words
	Intervals []IntervalCheck `json:"intervals,omitempty"`
	// Summaries of what was compared, for charts: mean and standard
	// deviation of the new version's, stable's and the baseline's readings
	// (zero counts when absent).
	Canary   Stats `json:"canary"`
	Stable   Stats `json:"stable"`
	Baseline Stats `json:"baseline"`
}

// Stats summarise a set of readings.
type Stats struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean"`
	SD   float64 `json:"sd"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

// IntervalCheck is one metric in one interval.
type IntervalCheck struct {
	Index   int    `json:"index"`
	Outcome string `json:"outcome"` // passed, failed, or no_data
	// Value is the new version's mean in the interval.
	Value float64 `json:"value"`
	// Deviations in standard deviations from the stable version and from
	// the baseline, when compared (nil when not).
	StableZ   *float64 `json:"stable_z,omitempty"`
	BaselineZ *float64 `json:"baseline_z,omitempty"`
	Reason    string   `json:"reason,omitempty"` // why it failed or had no data
}

// LogVerdict is one log query's result.
type LogVerdict struct {
	Name     string           `json:"name"`
	Outcome  string           `json:"outcome"`
	Reason   string           `json:"reason"`
	Lines    int              `json:"lines"`
	Clusters []ClusterVerdict `json:"clusters,omitempty"` // new and unexpected first, then by count
}

// ClusterVerdict is one cluster of the new version's logs, classified.
type ClusterVerdict struct {
	Template  string   `json:"template"`
	Count     int      `json:"count"`
	Class     string   `json:"class"` // known, unknown, unexpected_frequency or no_reference
	Error     bool     `json:"error"` // counts as an error (see LogSpec.Errors)
	Reference int      `json:"reference,omitempty"`
	RateRatio float64  `json:"rate_ratio,omitempty"` // now versus the reference, per minute (or per line for stable)
	Samples   []string `json:"samples,omitempty"`
	// Failing says this cluster is why the log query failed.
	Failing bool `json:"failing,omitempty"`
}
