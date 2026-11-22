package stepkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Result kinds, the "t" of a line of the results file.
const (
	KindSummary     = "summary"
	KindAnnotation  = "annotation"
	KindFinding     = "finding"
	KindTest        = "test"
	KindMask        = "mask"
	KindNeedsPerson = "needs_person"
)

// Result is one line of the results file, checked.
type Result struct {
	Kind     string `json:"t"`
	Markdown string `json:"markdown,omitempty"` // summary
	File     string `json:"file,omitempty"`     // annotation, finding
	Line     int    `json:"line,omitempty"`
	EndLine  int    `json:"end_line,omitempty"`
	Level    string `json:"level,omitempty"`    // annotation: note, warning, error
	Message  string `json:"message,omitempty"`  // annotation, finding, test, needs_person
	Rule     string `json:"rule,omitempty"`     // finding
	Severity string `json:"severity,omitempty"` // finding: critical, high, medium, low
	Name     string `json:"name,omitempty"`     // test
	Suite    string `json:"suite,omitempty"`    // test
	Status   string `json:"status,omitempty"`   // test: passed, failed, skipped
	Millis   int64  `json:"duration_ms,omitempty"`
	Value    string `json:"value,omitempty"` // mask
}

// Results is what a step's results file said.
type Results struct {
	Items []Result
	// Notes are lines that were not used, each saying why (a line number and
	// a reason), so a broken kit is found rather than silently ignored.
	Notes []string
}

var (
	annotationLevels = []string{"note", "warning", "error"}
	severities       = []string{"critical", "high", "medium", "low"}
	testStatuses     = []string{"passed", "failed", "skipped"}
)

// ParseResults reads a results file. It never fails: a line that is too long,
// not JSON, of an unknown kind or with an unusable field is skipped and
// noted. The text is untrusted: its size, line length and count are bounded,
// and every string is checked to be text.
func ParseResults(r io.Reader) *Results {
	out := &Results{}
	br := bufio.NewReaderSize(io.LimitReader(r, MaxResultsBytes+1), 64<<10)
	total, lineNo := 0, 0
	for {
		line, tooLong, err := readLine(br, MaxResultLine)
		if err != nil && len(line) == 0 && !tooLong {
			break
		}
		lineNo++
		total += len(line)
		if total > MaxResultsBytes {
			out.Notes = append(out.Notes, fmt.Sprintf("the results are larger than %d MiB; the rest was ignored", MaxResultsBytes>>20))
			break
		}
		if len(out.Items) >= MaxResults {
			out.Notes = append(out.Notes, fmt.Sprintf("more than %d results; the rest were ignored", MaxResults))
			break
		}
		switch {
		case tooLong:
			out.Notes = append(out.Notes, fmt.Sprintf("result line %d is longer than %d KiB and was ignored", lineNo, MaxResultLine>>10))
		case len(bytes.TrimSpace(line)) == 0:
		default:
			res, why := parseResult(line)
			if why != "" {
				out.Notes = append(out.Notes, fmt.Sprintf("result line %d was ignored: %s", lineNo, why))
			} else {
				out.Items = append(out.Items, res)
			}
		}
		if err != nil {
			break
		}
	}
	return out
}

// readLine reads one line of at most max bytes; a longer one is skipped to
// its end and reported.
func readLine(br *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, e := br.ReadLine()
		if e != nil {
			return line, tooLong, e
		}
		if !tooLong && len(line)+len(chunk) > max {
			tooLong, line = true, nil
		}
		if !tooLong {
			line = append(line, chunk...)
		}
		if !isPrefix {
			return line, tooLong, nil
		}
	}
}

func parseResult(line []byte) (Result, string) {
	var res Result
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		return res, "it is not a result object: " + clip(strings.TrimPrefix(err.Error(), "json: "), 100)
	}
	if dec.More() {
		return res, "there is text after the JSON object"
	}
	for _, s := range []string{res.Markdown, res.File, res.Message, res.Rule, res.Name, res.Suite, res.Value} {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return res, "a value is not text"
		}
	}
	switch res.Kind {
	case KindSummary:
		if strings.TrimSpace(res.Markdown) == "" {
			return res, "a summary needs markdown"
		}
		if len(res.Markdown) > MaxSummaryBytes {
			return res, fmt.Sprintf("a summary is at most %d KiB", MaxSummaryBytes>>10)
		}
	case KindAnnotation, KindFinding:
		if res.File != "" {
			if msg := CheckWorkspacePath(res.File); msg != "" || res.File == "." {
				return res, "the file must be a path inside the workspace"
			}
		}
		if res.Line < 0 || res.EndLine < 0 || res.Line > 1e7 || res.EndLine > 1e7 || (res.Line == 0 && res.File != "" && res.EndLine != 0) {
			return res, "the line is not a line number"
		}
		if strings.TrimSpace(res.Message) == "" || len(res.Message) > 2000 {
			return res, "a message is 1 to 2000 bytes"
		}
		if res.Kind == KindAnnotation {
			if res.Level == "" {
				res.Level = "note"
			}
			if !contains(annotationLevels, res.Level) {
				return res, "the level is note, warning or error"
			}
			if res.File == "" {
				return res, "an annotation is anchored to a file; use a summary for a note about the whole step"
			}
		} else {
			if !contains(severities, res.Severity) {
				return res, "the severity is critical, high, medium or low"
			}
			if res.Rule == "" || len(res.Rule) > 120 {
				return res, "a finding needs a rule of 1 to 120 bytes"
			}
		}
	case KindTest:
		if res.Name == "" || len(res.Name) > 300 || len(res.Suite) > 300 {
			return res, "a test needs a name of 1 to 300 bytes"
		}
		if !contains(testStatuses, res.Status) {
			return res, "the status is passed, failed or skipped"
		}
		if res.Millis < 0 || res.Millis > 1e10 || len(res.Message) > 2000 {
			return res, "the duration or message is out of range"
		}
	case KindMask:
		if len(res.Value) < 4 || len(res.Value) > 4096 {
			return res, "a value to mask is 4 to 4096 bytes"
		}
	case KindNeedsPerson:
		if strings.TrimSpace(res.Message) == "" || len(res.Message) > 1000 {
			return res, "say what a person is needed for, in 1 to 1000 bytes"
		}
	default:
		return res, fmt.Sprintf("the kind %q is not known (the kinds are summary, annotation, finding, test, mask, needs_person)", clip(res.Kind, 30))
	}
	return res, ""
}

// Masks lists the values the step asked to have masked.
func (r *Results) Masks() []string {
	var out []string
	for _, it := range r.Items {
		if it.Kind == KindMask {
			out = append(out, it.Value)
		}
	}
	return out
}

// NeedsPerson returns what the step said it needs a person for.
func (r *Results) NeedsPerson() string {
	for _, it := range r.Items {
		if it.Kind == KindNeedsPerson {
			return it.Message
		}
	}
	return ""
}

// Of returns the items of a kind.
func (r *Results) Of(kind string) []Result {
	var out []Result
	for _, it := range r.Items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}
