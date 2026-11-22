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

// readLine reads one line of at most limit bytes; a longer one is skipped to
// its end and reported.
func readLine(br *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, e := br.ReadLine()
		if e != nil {
			return line, tooLong, e
		}
		if !tooLong && len(line)+len(chunk) > limit {
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
	if why := checkResult(&res); why != "" {
		return res, why
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

// checkResult checks the fields of a result of its kind, and fills in the defaults; it returns why the line is unusable, or "".
func checkResult(res *Result) string {
	switch res.Kind {
	case KindSummary:
		return checkSummary(res)
	case KindAnnotation, KindFinding:
		return checkLocated(res)
	case KindTest:
		return checkTest(res)
	case KindMask:
		if len(res.Value) < 4 || len(res.Value) > 4096 {
			return "a value to mask is 4 to 4096 bytes"
		}
	case KindNeedsPerson:
		if strings.TrimSpace(res.Message) == "" || len(res.Message) > 1000 {
			return "say what a person is needed for, in 1 to 1000 bytes"
		}
	default:
		return fmt.Sprintf("the kind %q is not known (the kinds are summary, annotation, finding, test, mask, needs_person)", clip(res.Kind, 30))
	}
	return ""
}

func checkSummary(res *Result) string {
	switch {
	case strings.TrimSpace(res.Markdown) == "":
		return "a summary needs markdown"
	case len(res.Markdown) > MaxSummaryBytes:
		return fmt.Sprintf("a summary is at most %d KiB", MaxSummaryBytes>>10)
	}
	return ""
}

// checkLocated checks an annotation or a finding: where it is, what it says, and its level or severity.
func checkLocated(res *Result) string {
	if res.File != "" {
		if msg := CheckWorkspacePath(res.File); msg != "" || res.File == "." {
			return "the file must be a path inside the workspace"
		}
	}
	if res.Line < 0 || res.EndLine < 0 || res.Line > 1e7 || res.EndLine > 1e7 || (res.Line == 0 && res.File != "" && res.EndLine != 0) {
		return "the line is not a line number"
	}
	if strings.TrimSpace(res.Message) == "" || len(res.Message) > 2000 {
		return "a message is 1 to 2000 bytes"
	}
	if res.Kind == KindFinding {
		switch {
		case !contains(severities, res.Severity):
			return "the severity is critical, high, medium or low"
		case res.Rule == "" || len(res.Rule) > 120:
			return "a finding needs a rule of 1 to 120 bytes"
		}
		return ""
	}
	if res.Level == "" {
		res.Level = "note"
	}
	switch {
	case !contains(annotationLevels, res.Level):
		return "the level is note, warning or error"
	case res.File == "":
		return "an annotation is anchored to a file; use a summary for a note about the whole step"
	}
	return ""
}

func checkTest(res *Result) string {
	switch {
	case res.Name == "" || len(res.Name) > 300 || len(res.Suite) > 300:
		return "a test needs a name of 1 to 300 bytes"
	case !contains(testStatuses, res.Status):
		return "the status is passed, failed or skipped"
	case res.Millis < 0 || res.Millis > 1e10 || len(res.Message) > 2000:
		return "the duration or message is out of range"
	}
	return ""
}
