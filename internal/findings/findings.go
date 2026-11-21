// Package findings reads the SARIF 2.1.0 reports that code scanners write
// (gitleaks, trufflehog, semgrep, CodeQL, bandit, hadolint, trivy,
// govulncheck and others) and turns them into a list of findings.
//
// The files come from whoever can push code, so they are treated as hostile:
// memory and nesting are bounded, counts written in the file are never
// trusted (a rule index is checked, never followed blindly), and the whole
// parse has a time limit. When a file is only partly readable, the findings
// read so far are returned with a warning instead of an error.
//
// # Severity
//
// Each finding gets one of five severities, decided by the first of these
// that gives an answer (the same table is in SeverityTable):
//
//  1. A numeric "security-severity" property (a number or a string such as
//     "9.8") on the result, else on its rule, the way CodeQL and trivy write
//     CVSS scores: 9.0 and over is critical, 7.0 high, 4.0 medium, 0.1 low.
//     0, a negative number or anything unreadable falls through.
//  2. The tool's own severity word: the result's "issue_severity" or
//     "severity" property (bandit and others), else a rule tag that is
//     exactly CRITICAL, HIGH, MEDIUM or LOW (trivy), in any case. "moderate"
//     counts as medium; "info", "informational" and "negligible" as low.
//  3. The SARIF level: the result's level, else its rule's
//     defaultConfiguration.level, else "warning" (the SARIF default):
//     error is high, warning medium, note low, none unknown.
//  4. A secret finding is never below high.
//
// # Secrets
//
// A finding is a secret finding when its tool is a secret scanner (gitleaks,
// trufflehog, detect-secrets, ggshield, talisman, secretlint, kingfisher,
// noseyparker, whispers: the name only has to contain one of these words)
// or its rule's id, name, short description or tags mention a secret,
// password, passwd, credential, private key, API key, access key or token,
// or carry the CWE for hard-coded credentials (CWE-798, CWE-259, CWE-321).
// Over-classifying is the safe direction.
//
// Code snippets (region.snippet, contextRegion) are never read, for any
// finding: the source is in Git anyway, and a snippet from a secret rule is
// the secret. A secret finding's message is replaced by a fixed text built
// from the rule's own description, and every message is passed through
// Mask, which hides anything shaped like a key or token.
//
// # Fingerprints
//
// Each finding has a fingerprint that stays the same across runs, so a
// server can tell new findings from ones still there and ones fixed. It is
// the first 16 bytes, in hex, of a SHA-256 over the tool (lower-cased), the
// rule id and the file, followed either by the result's partial
// fingerprints and fingerprints (when it has real ones, such as CodeQL's
// primaryLocationLineHash) or by the start line and the masked message.
// Partial fingerprints do not change when code moves up or down a file;
// line-based fingerprints do, so a finding whose code moved shows as fixed
// and new again. Placeholder values ("requires login", which semgrep writes
// without a login, or a value every result in the file shares) are ignored.
// When two findings in one file would get the same fingerprint, the second
// gets "#2" added to its input, the third "#3", in file order.
package findings

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings/jsonscan"
)

// Severity says how bad a finding is.
type Severity string

// The five severities.
const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
	Unknown  Severity = "unknown"
)

// Severities lists them from most to least severe.
var Severities = []Severity{Critical, High, Medium, Low, Unknown}

// rank orders severities: a higher rank is more severe.
func (s Severity) rank() int {
	switch s {
	case Critical:
		return 4
	case High:
		return 3
	case Medium:
		return 2
	case Low:
		return 1
	}
	return 0
}

// SeverityTable is the severity mapping as a Markdown table, for the
// documentation.
const SeverityTable = `| Order | Taken from | Gives |
| --- | --- | --- |
| 1 | "security-severity" property (number or string) on the result, else on its rule | 9.0 and over: critical; 7.0: high; 4.0: medium; 0.1: low; 0 or unreadable: go on |
| 2 | result property "issue_severity" or "severity", else a rule tag that is exactly CRITICAL, HIGH, MEDIUM or LOW (any case) | critical, high, medium (also "moderate"), low (also "info", "informational", "negligible"); other words: go on |
| 3 | result level, else the rule's defaultConfiguration.level, else "warning" | error: high; warning: medium; note: low; none: unknown |
| 4 | the finding is a secret finding | raised to high if it was lower |
`

// FormatSARIF names the one format Parse reads.
const FormatSARIF = "sarif-2.1.0"

// ErrNotSARIF is wrapped by errors saying the content is not a SARIF report.
var ErrNotSARIF = errors.New("not a SARIF report")

// Finding is one problem a tool reported.
type Finding struct {
	Tool        string   `json:"tool"`            // the run's tool.driver.name, e.g. "gitleaks"
	RuleID      string   `json:"rule_id"`         // result.ruleId, else the id of the rule it points at, else "(no rule)"
	Severity    Severity `json:"severity"`        // see the package comment
	Level       string   `json:"level,omitempty"` // error, warning, note or none: as given or as defaulted
	Message     string   `json:"message"`         // cleaned and masked; fixed text for a secret finding
	File        string   `json:"file,omitempty"`  // the primary location's path, relative to the workspace where the file says so
	StartLine   int      `json:"start_line,omitempty"`
	EndLine     int      `json:"end_line,omitempty"`
	StartColumn int      `json:"start_column,omitempty"`
	Fingerprint string   `json:"fingerprint"`      // 32 hex characters, stable across runs
	Secret      bool     `json:"secret,omitempty"` // judged to be a secret finding
	// Fix is the description of the first fix the tool suggested, if it
	// suggested one (SARIF "fixes"): words only, never the replacement text.
	Fix string `json:"fix,omitempty"`
}

// Rule describes a rule some finding refers to.
type Rule struct {
	Tool    string   `json:"tool"`
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Short   string   `json:"short,omitempty"`    // shortDescription.text
	Full    string   `json:"full,omitempty"`     // fullDescription.text
	Help    string   `json:"help,omitempty"`     // help.text; help.markdown is never used
	HelpURI string   `json:"help_uri,omitempty"` // only absolute http:// or https:// URLs
	Tags    []string `json:"tags,omitempty"`
}

// Counts says how many findings there are of each severity. Suppressed
// results are not findings and are not in Total.
type Counts struct {
	Total, Critical, High, Medium, Low, Unknown int
	Suppressed                                  int
}

// MaxToolCounts bounds the tools a report counts findings for one by one,
// and OtherTools is where findings of any more tools are counted.
const (
	MaxToolCounts = 50
	OtherTools    = "(other tools)"
)

// add counts one finding of a severity.
func (c *Counts) add(sev Severity) {
	c.Total++
	switch sev {
	case Critical:
		c.Critical++
	case High:
		c.High++
	case Medium:
		c.Medium++
	case Low:
		c.Low++
	default:
		c.Unknown++
	}
}

// Report is the result of parsing one file.
type Report struct {
	Format   string    // FormatSARIF
	Tools    []string  // tool names in the order their runs appear, each once
	Findings []Finding // at most Limits.MaxFindings, in file order
	Rules    []Rule    // the rules kept findings refer to, sorted by Tool then ID
	Counts   Counts    // every finding found, including any beyond MaxFindings
	// ByTool counts them by the tool that reported them, for at most
	// MaxToolCounts tools; the rest are counted under OtherTools.
	ByTool    map[string]Counts
	Truncated bool   // fewer Findings than Counts.Total, or rules beyond MaxRules were not read
	Warning   string // a problem that did not stop parsing
}

// Limits bounds the work and memory one parse may use.
type Limits struct {
	MaxFindings     int           // default 20000
	MaxRules        int           // rules read per run; default 5000 (findings of other rules are still counted, but not described)
	MaxMessageBytes int           // default 2048
	MaxHelpBytes    int           // default 4096 for Rule.Help; Short and Full get at most 1024
	MaxPathBytes    int           // default 500 for File
	MaxNameBytes    int           // default 300 for Tool, RuleID, Rule.Name and Rule.ID
	MaxDepth        int           // default 64: JSON nesting accepted
	Timeout         time.Duration // default 10s for the whole parse
}

// Fixed limits for the smaller fields.
const (
	maxDescBytes   = 1024 // Rule.Short and Rule.Full
	maxTags        = 20   // Rule.Tags
	maxTagBytes    = 100  // each tag
	maxURIBytes    = 2000 // Rule.HelpURI; longer ones are dropped
	maxWarnBytes   = 2000 // Report.Warning
	maxExtensions  = 1000 // tool.extensions read per run
	maxBases       = 100  // originalUriBaseIds read per run
	maxPartialFPs  = 16   // partialFingerprints entries used per result
	maxFPStatsKeys = 64   // partialFingerprints keys watched for placeholders
)

// DefaultLimits returns the default limits. Zero fields in a Limits passed
// to Parse are taken as the defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxFindings:     20000,
		MaxRules:        5000,
		MaxMessageBytes: 2048,
		MaxHelpBytes:    4096,
		MaxPathBytes:    500,
		MaxNameBytes:    300,
		MaxDepth:        64,
		Timeout:         10 * time.Second,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFindings <= 0 {
		l.MaxFindings = d.MaxFindings
	}
	if l.MaxRules <= 0 {
		l.MaxRules = d.MaxRules
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = d.MaxMessageBytes
	}
	if l.MaxHelpBytes <= 0 {
		l.MaxHelpBytes = d.MaxHelpBytes
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = d.MaxPathBytes
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

// SeverityFromScore maps a CVSS-style score, as found in "security-severity"
// properties, to a severity: 9.0 and over is critical, 7.0 high, 4.0
// medium, 0.1 low. Anything below 0.1, and NaN, is Unknown.
func SeverityFromScore(score float64) Severity {
	switch {
	case math.IsNaN(score):
		return Unknown
	case score >= 9.0:
		return Critical
	case score >= 7.0:
		return High
	case score >= 4.0:
		return Medium
	case score >= 0.1:
		return Low
	}
	return Unknown
}

// severityWord maps a tool's own severity word. ok is false for words it
// does not know.
func severityWord(w string) (Severity, bool) {
	switch toLowerTrim(w) {
	case "critical":
		return Critical, true
	case "high":
		return High, true
	case "medium", "moderate":
		return Medium, true
	case "low", "info", "informational", "negligible":
		return Low, true
	}
	return Unknown, false
}

// tagSeverity maps a rule tag that is exactly a severity name.
func tagSeverity(tag string) (Severity, bool) {
	switch toLowerTrim(tag) {
	case "critical":
		return Critical, true
	case "high":
		return High, true
	case "medium":
		return Medium, true
	case "low":
		return Low, true
	}
	return Unknown, false
}

// levelSeverity maps a SARIF level.
func levelSeverity(level string) Severity {
	switch level {
	case "error":
		return High
	case "warning":
		return Medium
	case "note":
		return Low
	}
	return Unknown
}

// sarifLevel returns the SARIF level named by s, or "" if s names none.
func sarifLevel(s string) string {
	switch l := toLowerTrim(s); l {
	case "error", "warning", "note", "none":
		return l
	}
	return ""
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parse reads a SARIF 2.1.0 report.
func Parse(ctx context.Context, data []byte, lim Limits) (*Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lim = lim.withDefaults()
	dl := jsonscan.NewDeadline(ctx, lim.Timeout)
	body, err := startOfJSON(data)
	if err != nil {
		return nil, err
	}
	if dl.Expired() {
		return nil, dl.StopError("results")
	}
	p := newParser(dl, lim)
	return p.parse(body)
}

func notSARIF(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrNotSARIF}, args...)...)
}

// startOfJSON checks the first bytes and returns the data from the
// opening brace on.
func startOfJSON(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, notSARIF("the file is empty")
	}
	body := data
	if len(body) >= 3 && body[0] == utf8BOM[0] && body[1] == utf8BOM[1] && body[2] == utf8BOM[2] {
		body = body[3:]
	}
	i := 0
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if i == len(body) {
		return nil, notSARIF("the file holds only white space")
	}
	body = body[i:]
	switch body[0] {
	case '{':
		return body, nil
	case '<':
		return nil, notSARIF("it is XML or HTML, not JSON (it starts with %s)", jsonscan.Snippet(body))
	case '[':
		return nil, notSARIF("the JSON is a list; a SARIF report is an object with a \"runs\" list")
	}
	return nil, notSARIF("it is not JSON (it starts with %s)", jsonscan.Snippet(body))
}
