package findings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// doc builds a one-run SARIF report.
func doc(tool, rules, results string) string {
	return `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":` + tool + `,"rules":[` + rules + `]}},"results":[` + results + `]}]}`
}

func mustParse(t testing.TB, s string, lim Limits) *Report {
	t.Helper()
	return parseBytes(t, []byte(s), lim)
}

// slow scales a time limit for the race detector and busy CI machines.
func slow(d time.Duration) time.Duration {
	if raceEnabled {
		return 10 * d
	}
	return d
}

func TestContentThatIsNotSARIF(t *testing.T) {
	random := make([]byte, 4096)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range random {
		random[i] = byte(rng.UintN(256))
	}
	random[0] = 0x8f
	for _, tc := range []struct{ name, in, want string }{
		{"empty file", "", "not a SARIF report: the file is empty"},
		{"only white space", " \n\t\r\n", "not a SARIF report: the file holds only white space"},
		{"a byte order mark and nothing else", "\xEF\xBB\xBF  ", "not a SARIF report: the file holds only white space"},
		{"plain text", "hello world, this is a log", `not a SARIF report: it is not JSON (it starts with "hello wor…")`},
		{"html page", "<!DOCTYPE html>\n<html><body>502</body></html>", `not a SARIF report: it is XML or HTML, not JSON (it starts with "<!DOCTYPE…")`},
		{"junit xml", `<?xml version="1.0"?><testsuite/>`, "not a SARIF report: it is XML or HTML, not JSON"},
		{"random binary", string(random), "not a SARIF report: it is not JSON (it starts with"},
		{"json list", `[{"runs":[]}]`, "not a SARIF report: the JSON is a list; a SARIF report is an object with a \"runs\" list"},
		{"json string", `"runs"`, "not a SARIF report: it is not JSON"},
		{"package.json", `{"name": "shop", "version": "1.0.0", "scripts": {"test": "jest"}}`, "not a SARIF report: version \"1.0.0\" is not supported; write SARIF 2.1.0"},
		{"an object without runs", `{"$schema": "x", "results": []}`, `not a SARIF report: the JSON object has no "runs" list`},
		{"runs is not a list", `{"version":"2.1.0","runs":{"tool":{}}}`, `not a SARIF report: its "runs" is not a list`},
		{"runs is null", `{"runs":null}`, `not a SARIF report: its "runs" is not a list`},
		{"sarif 2.0", `{"version":"2.0.0","runs":[]}`, `not a SARIF report: version "2.0.0" is not supported; write SARIF 2.1.0`},
		{"version after the runs", `{"runs":[],"version":"1.0"}`, `not a SARIF report: version "1.0" is not supported`},
		{"version as a number", `{"version":2.1,"runs":[]}`, `not a SARIF report: its "version" is not a string`},
		{"a dirty version", "{\"version\":\"2.1.0\\u001b[31m\\u0000\",\"runs\":[]}", `not a SARIF report: version "2.1.0" is not supported`},
		{"broken before the runs", "{\n\"version\": \"2.1.0\",\n\"runs\" oops", `not a SARIF report: the JSON could not be read (line 3: expected ':' after an object key, found 'o')`},
		{"ends before the runs", `{"version": "2.1.0"`, "not a SARIF report: the JSON could not be read (line 1: the data ends part-way through a value)"},
		{"a lone brace", `{`, "not a SARIF report: the JSON could not be read (line 1:"},
		{"bad literal", `{"version": tru}`, "not a SARIF report: the JSON could not be read (line 1: expected true"},
		{"cyclonedx", `{"bomFormat":"CycloneDX","specVersion":"1.5","components":[]}`, `not a SARIF report: the JSON object has no "runs" list`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(context.Background(), []byte(tc.in), Limits{})
			if !errors.Is(err, ErrNotSARIF) {
				t.Fatalf("err = %v, want ErrNotSARIF", err)
			}
			if !strings.HasPrefix(err.Error(), "not a SARIF report: ") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q\nwant it to contain %q", err, tc.want)
			}
			checkString(t, "error", err.Error(), 400, false)
		})
	}
}

func TestEmptyReportsAreValid(t *testing.T) {
	for _, in := range []string{
		`{"runs":[]}`,
		"\xEF\xBB\xBF \n {\"$schema\":\"https://json.schemastore.org/sarif-2.1.0.json\",\"version\":\"2.1.0\",\"runs\":[]} \n",
		`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x"}},"results":[]}]}`,
		`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x"}}}]}`,
	} {
		r := mustParse(t, in, Limits{})
		if r.Counts.Total != 0 || len(r.Findings) != 0 || r.Warning != "" {
			t.Errorf("%q: %+v", in, r)
		}
	}
	r := mustParse(t, `{"runs":[{}, 5, {"tool":{"driver":{"name":"x"}}}, {"tool":"x"}]}`, Limits{})
	if strings.Join(r.Tools, ",") != "(unknown tool),x" || r.Warning != "1 run was not an object and skipped" {
		t.Errorf("tools %q, warning %q", r.Tools, r.Warning)
	}
}

func TestSuppressionsAndKinds(t *testing.T) {
	res := func(extra string) string {
		return `{"ruleId":"r","message":{"text":"m"},` + extra + `}`
	}
	in := doc(`"x"`, `{"id":"r"}`, strings.Join([]string{
		res(`"suppressions":[{"kind":"inSource"}]`),
		res(`"suppressions":[{"status":"Accepted"}]`),
		res(`"suppressions":[{"status":"rejected"},{"status":"accepted"}]`),
		res(`"suppressions":[{"status":"rejected"}]`),
		res(`"suppressions":[{"status":"underReview"}]`),
		res(`"suppressions":[]`),
		res(`"suppressions":"yes"`),
		res(`"kind":"pass"`),
		res(`"kind":"notApplicable"`),
		res(`"kind":"informational"`),
		res(`"kind":"review"`),
		res(`"kind":"open"`),
		res(`"baselineState":"absent"`),
		res(`"baselineState":"updated"`),
	}, ","))
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 7, Medium: 7, Suppressed: 3})
}

func TestDeepNestingIsSkipped(t *testing.T) {
	const depth = 100000
	deep := strings.Repeat("[", depth) + strings.Repeat("]", depth)
	good := `{"ruleId":"r","message":{"text":"ok"}}`
	in := doc(`"x"`, `{"id":"r","properties":{"x":`+deep+`}}`, good+`,{"ruleId":"r","codeFlows":`+deep+`},`+good)
	start := time.Now()
	r := mustParse(t, in, Limits{})
	if d := time.Since(start); d > slow(2*time.Second) {
		t.Errorf("took %s", d)
	}
	if r.Counts.Total != 2 || r.Warning != "1 result was nested more than 64 deep and skipped; 1 rule could not be read; findings of those rules are not described" {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
	// Deep nesting in a part that is never decoded is only skipped over.
	r = mustParse(t, `{"version":"2.1.0","x":`+deep+`,"runs":[{"invocations":`+deep+`,"results":[`+good+`]}]}`, Limits{})
	if r.Counts.Total != 1 || r.Warning != "" {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
	// Deep nesting that never closes is a file cut short.
	r = mustParse(t, `{"version":"2.1.0","runs":[{"results":[`+good+`,`+strings.Repeat("[", depth), Limits{})
	if r.Counts.Total != 1 || !strings.HasPrefix(r.Warning, "the file ends part-way through") {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
	// A low MaxDepth applies to results too.
	r = mustParse(t, doc(`"x"`, "", `{"message":{"text":"a"},"locations":[{"physicalLocation":{"region":{"startLine":1}}}]}`), Limits{MaxDepth: 6})
	if r.Counts.Total != 0 || !strings.Contains(r.Warning, "more than 6 deep") {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
}

func TestHugeStringsAreCut(t *testing.T) {
	big := strings.Repeat("ab€ ", 5<<20/4)
	in := doc(`"`+big+`"`,
		`{"id":"`+big+`","name":"`+big+`","shortDescription":{"text":"`+big+`"},"help":{"text":"`+big+`"},"helpUri":"https://x.example/`+strings.Repeat("a", 1<<20)+`","properties":{"tags":["`+big+`"]}}`,
		`{"ruleIndex":0,"message":{"text":"`+big+`"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"`+big+`"}}}],"partialFingerprints":{"`+big+`":"`+big+`"}}`)
	start := time.Now()
	r := mustParse(t, in, Limits{})
	if d := time.Since(start); d > slow(3*time.Second) {
		t.Errorf("took %s", d)
	}
	f := r.Findings[0]
	for name, s := range map[string]string{"tool": f.Tool, "rule": f.RuleID, "message": f.Message, "file": f.File} {
		if !strings.HasSuffix(s, "…") || len(s) < 250 {
			t.Errorf("%s is %d bytes and ends %q", name, len(s), s[max(0, len(s)-10):])
		}
	}
	ru := r.Rules[0]
	if len(ru.Help) != 4096-1 && len(ru.Help) != 4096 || ru.HelpURI != "" || len(ru.Tags[0]) > 100 || len(ru.Short) > 1024 {
		t.Errorf("rule: help %d bytes, uri %d bytes, tag %d bytes", len(ru.Help), len(ru.HelpURI), len(ru.Tags[0]))
	}
}

// manyResults writes a report with n results of a handful of rules.
func manyResults(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"bulk","rules":[`)
	for i := range 10 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"R%d","shortDescription":{"text":"rule %d"},"defaultConfiguration":{"level":%q}}`, i, i, []string{"error", "warning", "note"}[i%3])
	}
	b.WriteString(`]}},"results":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"ruleId":"R%d","ruleIndex":%d,"message":{"text":"problem %d"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/f%d.go"},"region":{"startLine":%d}}}]}`, i%10, i%10, i, i%100, i%5000+1)
	}
	b.WriteString("]}]}")
	return b.Bytes()
}

func TestManyResultsRespectMaxFindings(t *testing.T) {
	n := 1_000_000
	if raceEnabled || testing.Short() {
		n = 100_000
	}
	data := manyResults(n)
	lim := Limits{MaxFindings: 1000, Timeout: 10 * time.Minute}
	start := time.Now()
	r := parseBytes(t, data, lim)
	d := time.Since(start)
	t.Logf("%d results, %.1f MB, parsed in %s", r.Counts.Total, float64(len(data))/1e6, d)
	if r.Counts.Total != n || len(r.Findings) != 1000 || !r.Truncated || r.Warning != "" {
		t.Errorf("counts %+v, %d findings, truncated %v, warning %q", r.Counts, len(r.Findings), r.Truncated, r.Warning)
	}
	wantHigh := (n + 2) / 3 // rules 0, 3, 6, 9 are errors: 4 of every 10
	_ = wantHigh
	if r.Counts.High != n*4/10 || r.Counts.Medium != n*3/10 || r.Counts.Low != n*3/10 {
		t.Errorf("counts %+v", r.Counts)
	}
	if len(r.Rules) != 10 || r.Findings[999].Message != "problem 999" {
		t.Errorf("%d rules, last finding %+v", len(r.Rules), r.Findings[999])
	}
	// A guard against quadratic work, not a benchmark.
	if d > slow(30*time.Second) {
		t.Errorf("took %s", d)
	}
}

func TestManyRules(t *testing.T) {
	n := 1_000_000
	if raceEnabled || testing.Short() {
		n = 100_000
	}
	var b bytes.Buffer
	b.WriteString(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x","rules":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"R%d","defaultConfiguration":{"level":"error"}}`, i)
	}
	b.WriteString(`]}},"results":[`)
	fmt.Fprintf(&b, `{"ruleIndex":4999,"message":{"text":"a"}},{"ruleIndex":%d,"message":{"text":"b"}},{"ruleId":"R%d","message":{"text":"c"}}`, n-1, n-2)
	b.WriteString("]}]}")
	start := time.Now()
	r := parseBytes(t, b.Bytes(), Limits{Timeout: 10 * time.Minute})
	if d := time.Since(start); d > slow(20*time.Second) {
		t.Errorf("took %s", d)
	}
	if r.Counts.Total != 3 || !r.Truncated || r.Warning != "a run describes more than 5000 rules; findings of the others are counted but not described" {
		t.Errorf("counts %+v, truncated %v, warning %q", r.Counts, r.Truncated, r.Warning)
	}
	got := []string{}
	for _, f := range r.Findings {
		got = append(got, f.RuleID+" "+string(f.Severity))
	}
	if want := fmt.Sprintf("R4999 high,(no rule) medium,R%d medium", n-2); strings.Join(got, ",") != want {
		t.Errorf("findings %q, want %q", strings.Join(got, ","), want)
	}
}

func TestRuleReferences(t *testing.T) {
	rules := `{"id":"A","defaultConfiguration":{"level":"error"}},{"id":"B","defaultConfiguration":{"level":"note"}}`
	run := func(results string) string {
		return `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"main","rules":[` + rules + `]},"extensions":[{"name":"pack","rules":[{"id":"E","defaultConfiguration":{"level":"note"}}]}]},"results":[` + results + `]}]}`
	}
	for _, tc := range []struct{ name, result, rule, sev string }{
		{"index", `"ruleIndex":1`, "B", "low"},
		{"index as a string", `"ruleIndex":"1"`, "B", "low"},
		{"index as a whole float", `"ruleIndex":1.0`, "B", "low"},
		{"index with a fraction", `"ruleIndex":1.5`, "(no rule)", "medium"},
		{"negative index", `"ruleIndex":-1`, "(no rule)", "medium"},
		{"index out of range", `"ruleIndex":2`, "(no rule)", "medium"},
		{"huge index", `"ruleIndex":1e300`, "(no rule)", "medium"},
		{"enormous index", `"ruleIndex":99999999999999999999999999`, "(no rule)", "medium"},
		{"id wins over a wrong index", `"ruleId":"A","ruleIndex":1`, "A", "high"},
		{"unknown id with a valid index", `"ruleId":"Z","ruleIndex":1`, "Z", "medium"},
		{"id as a number", `"ruleId":7`, "7", "medium"},
		{"rule.id", `"rule":{"id":"B"}`, "B", "low"},
		{"rule.index", `"rule":{"index":0}`, "A", "high"},
		{"extension by index", `"rule":{"index":0,"toolComponent":{"index":0}}`, "E", "low"},
		{"extension by name", `"rule":{"index":0,"toolComponent":{"name":"pack"}}`, "E", "low"},
		{"driver by name", `"rule":{"index":0,"toolComponent":{"name":"main"}}`, "A", "high"},
		{"extension out of range", `"rule":{"index":0,"toolComponent":{"index":5}}`, "(no rule)", "medium"},
		{"unknown component name", `"rule":{"index":0,"toolComponent":{"name":"nope"}}`, "(no rule)", "medium"},
		{"extension rule by id", `"ruleId":"E"`, "E", "low"},
		{"hierarchical id", `"ruleId":"A/1"`, "A/1", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := mustParse(t, run(`{`+tc.result+`,"message":{"text":"m"}}`), Limits{})
			if len(r.Findings) != 1 {
				t.Fatalf("%d findings, warning %q", len(r.Findings), r.Warning)
			}
			if f := r.Findings[0]; f.RuleID != tc.rule || string(f.Severity) != tc.sev {
				t.Errorf("rule %q severity %s, want %q %s", f.RuleID, f.Severity, tc.rule, tc.sev)
			}
		})
	}
}

func TestScalarsOfEitherKind(t *testing.T) {
	in := doc(`"x"`, `{"id":"R","properties":{"security-severity":7}}`, strings.Join([]string{
		`{"ruleId":"R","message":{"text":12},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":"12","endLine":13.0,"startColumn":"x"}}}]}`,
		`{"ruleId":"R","level":5,"message":"plain string","properties":{"security-severity":"9.9","severity":{"x":1}},"locations":[{"physicalLocation":{"region":{"startLine":-5,"endLine":1e9,"startColumn":10000000}}}]}`,
		`{"ruleId":"R","message":{"text":null},"properties":"not a bag","partialFingerprints":{"a":5,"b":null,"c":{"d":1}},"suppressions":[1,"x",null],"locations":"nowhere"}`,
		`{"ruleId":"R","message":{"text":"t"},"properties":{"security-severity":true,"tags":"HIGH"}}`,
	}, ","))
	r := mustParse(t, in, Limits{})
	if r.Counts.Total != 4 || r.Warning != "" {
		t.Fatalf("counts %+v, warning %q", r.Counts, r.Warning)
	}
	f := r.Findings
	if f[0].Message != "12" || f[0].StartLine != 12 || f[0].EndLine != 13 || f[0].StartColumn != 0 || f[0].Severity != High {
		t.Errorf("finding 0 = %+v", f[0])
	}
	if f[1].Message != "plain string" || f[1].StartLine != 0 || f[1].EndLine != 0 || f[1].StartColumn != 0 || f[1].Severity != Critical || f[1].Level != "warning" {
		t.Errorf("finding 1 = %+v", f[1])
	}
	if f[2].Message != "R" || f[2].File != "" || f[3].Severity != High {
		t.Errorf("findings 2 and 3 = %+v %+v", f[2], f[3])
	}
}

func TestBadResultsAreSkippedOneByOne(t *testing.T) {
	good := `{"ruleId":"r","message":{"text":"good"}}`
	in := doc(`"x"`, `{"id":"r"},{"id":"r","id":"s"},"just text",{"id":{"no":1}}`, strings.Join([]string{
		good,
		`{"ruleId":"r","message":{"text":"a"},"locations":[{"physicalLocation":{"region":{"startLine":{}}}}]}`,
		`{"ruleId":"r","message":["a"]}`,
		`{"ruleId":"r","message":{"text":"a"},"locations":[5]}`,
		`"a string"`,
		`null`,
		`{"ruleId":"r","ruleId":"s","message":{"text":"two ids"}}`,
		`{"ruleId":"r","message":{"text":"a","text":"b"}}`,
		`{"ruleId":true}`,
		`{"ruleId":"r","rule":"r"}`,
		good,
	}, ","))
	r := mustParse(t, in, Limits{})
	if r.Counts.Total != 2 || r.Warning != "9 results could not be read and skipped; 3 rules could not be read; findings of those rules are not described" {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
}

func TestDirtyTextIsCleaned(t *testing.T) {
	in := "{\"version\":\"2.1.0\",\"runs\":[{\"tool\":{\"driver\":{\"name\":\"to\\u0000ol\\u001b[1m\\n x\\u202e\",\"rules\":[{\"id\":\"r\\u0007\",\"shortDescription\":{\"text\":\"\\u001b]8;;http://evil\\u0007link\\u001b]8;;\\u0007 text\"},\"help\":{\"text\":\"line1\\r\\nline2\\rline3\\tTab\\u0085\"}}]}}," +
		"\"results\":[{\"ruleId\":\"r\\u0007\",\"message\":{\"text\":\"  \\n\\nbad \\u0000\\ud800 \xff\xfe byte\\u001b[31m red\\u001b[0m\\n  \"},\"locations\":[{\"physicalLocation\":{\"artifactLocation\":{\"uri\":\"a\\u0000b/\\u001b[2Jc.go\\n\"}}}]}]}]}"
	r := mustParse(t, in, Limits{})
	f := r.Findings[0]
	if f.Tool != "tool  x" {
		t.Errorf("tool = %q", f.Tool)
	}
	if f.RuleID != "r" || f.Message != "bad \uFFFD \uFFFD\uFFFD byte red" || f.File != "ab/c.go" {
		t.Errorf("finding = %+v", f)
	}
	ru := r.Rules[0]
	if ru.Short != "link text" || ru.Help != "line1\nline2\nline3 Tab" {
		t.Errorf("rule = %+v", ru)
	}
}

func TestSmallLimits(t *testing.T) {
	in := doc(`"a-long-tool-name"`, `{"id":"a-long-rule-id","help":{"text":"help text that is long"}}`, `{"ruleId":"a-long-rule-id","message":{"text":"a message that is long"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"some/long/path.go"}}}]}`)
	lim := Limits{MaxMessageBytes: 10, MaxHelpBytes: 10, MaxPathBytes: 10, MaxNameBytes: 10}
	r := mustParse(t, in, lim)
	f := r.Findings[0]
	if f.Tool != "a-long-…" || f.RuleID != "a-long-…" || f.Message != "a messa…" || f.File != "some/lo…" || r.Rules[0].Help != "help te…" {
		t.Errorf("finding %+v, rule %+v", f, r.Rules[0])
	}
}

func TestFileCutShort(t *testing.T) {
	full := readSample(t, "trivy-image.sarif")
	whole := parseSample(t, "trivy-image.sarif")
	start := bytes.Index(full, []byte(`"results"`))
	prev := 0
	for cut := start + 12; cut < len(full)-1; cut += 997 {
		r, err := Parse(context.Background(), full[:cut], Limits{})
		if err != nil {
			t.Fatalf("cut at %d: %v", cut, err)
		}
		checkReport(t, r, Limits{})
		if r.Warning != "the file ends part-way through; results after that are missing" {
			t.Fatalf("cut at %d: warning %q", cut, r.Warning)
		}
		if r.Counts.Total < prev || r.Counts.Total >= whole.Counts.Total {
			t.Fatalf("cut at %d: %d findings after %d", cut, r.Counts.Total, prev)
		}
		prev = r.Counts.Total
		// What was read matches the whole file, fingerprints included.
		for i, f := range r.Findings {
			if f != whole.Findings[i] {
				t.Fatalf("cut at %d: finding %d differs: %+v", cut, i, f)
			}
		}
	}
	if prev < 40 {
		t.Errorf("only %d findings read before the last cut", prev)
	}

	// Results before the tool: a cut inside the tool keeps every result
	// but loses the tool's name and rules.
	h := readSample(t, "hadolint.sarif")
	i := bytes.Index(h, []byte(`"tool"`))
	r := parseBytes(t, h[:i+20], Limits{})
	if r.Counts.Total != 7 || r.Tools[0] != "(unknown tool)" || !strings.HasPrefix(r.Warning, "the file ends part-way through") {
		t.Errorf("counts %+v, tools %v, warning %q", r.Counts, r.Tools, r.Warning)
	}

	// Broken in the middle, rather than cut.
	broken := bytes.Replace(full, []byte(`"ruleId": "CVE-2023-5363"`), []byte(`"ruleId" "CVE-2023-5363"`), 1)
	r = parseBytes(t, broken, Limits{})
	if r.Counts.Total == 0 || r.Counts.Total >= 49 || !regexp.MustCompile(`^the JSON is broken at line \d+ \(expected ':' after an object key, found '"'\); results after that are missing$`).MatchString(r.Warning) {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
	// Junk after the end.
	r = parseBytes(t, append(append([]byte{}, full...), "\n\ngarbage"...), Limits{})
	if r.Counts.Total != 49 || !regexp.MustCompile(`^there is more data after the end of the report \(line \d+\); it was ignored$`).MatchString(r.Warning) {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
}

func TestDuplicateLists(t *testing.T) {
	r := mustParse(t, `{"runs":[{"results":[{"message":{"text":"a"}}],"results":[{"message":{"text":"b"}}]}],"runs":[{"results":[{"message":{"text":"c"}}]}]}`, Limits{})
	if r.Counts.Total != 1 || r.Findings[0].Message != "a" || r.Warning != `the report has a second "runs" list, which was ignored; a run has a second "results" list, which was ignored` {
		t.Errorf("findings %+v, warning %q", r.Findings, r.Warning)
	}
}

func TestTimeoutStopsParsing(t *testing.T) {
	data := manyResults(300_000)
	start := time.Now()
	r, err := Parse(context.Background(), data, Limits{Timeout: 20 * time.Millisecond})
	if d := time.Since(start); d > slow(time.Second) {
		t.Errorf("took %s", d)
	}
	if err != nil {
		if !strings.HasPrefix(err.Error(), "parsing stopped after 20ms before any results were read") {
			t.Errorf("err = %v", err)
		}
	} else {
		checkReport(t, r, Limits{})
		if !regexp.MustCompile(`^parsing stopped after 20ms; results after line \d+ are missing$`).MatchString(r.Warning) || r.Counts.Total == 300_000 {
			t.Errorf("warning %q, counts %+v", r.Warning, r.Counts)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Parse(ctx, data, Limits{})
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "parsing was cancelled before any results were read") {
		t.Errorf("err = %v", err)
	}
}

func TestFingerprintOccurrences(t *testing.T) {
	one := `{"ruleId":"r","message":{"text":"same"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":3}}}]}`
	r := mustParse(t, doc(`"x"`, "", one+","+one+","+one), Limits{})
	single := mustParse(t, doc(`"x"`, "", one), Limits{})
	if r.Findings[0].Fingerprint != single.Findings[0].Fingerprint {
		t.Error("the first of several equal findings changed its fingerprint")
	}
	if r.Findings[1].Fingerprint != fingerprint([]string{"x", "r", "a.go", "line", "3", "same", "#2"}) {
		t.Errorf("second fingerprint = %s", r.Findings[1].Fingerprint)
	}
	if r.Findings[0].Fingerprint != fingerprint([]string{"x", "r", "a.go", "line", "3", "same"}) {
		t.Errorf("first fingerprint = %s", r.Findings[0].Fingerprint)
	}

	// Partial fingerprints: used when real, ignored when every result
	// shares one value, used again when only one result has it.
	pf := func(v string) string {
		return `{"ruleId":"r","message":{"text":"m"},"partialFingerprints":{"h":"` + v + `"},"locations":[{"physicalLocation":{"region":{"startLine":1}}}]}`
	}
	a := mustParse(t, doc(`"x"`, "", pf("v1")+","+pf("v2")), Limits{})
	if a.Findings[0].Fingerprint != fingerprint([]string{"x", "r", "", "partial", "h=v1"}) {
		t.Error("real partial fingerprints were not used")
	}
	b := mustParse(t, doc(`"x"`, "", pf("same")+","+pf("same")), Limits{})
	if b.Findings[0].Fingerprint != fingerprint([]string{"x", "r", "", "line", "1", "m"}) {
		t.Error("a value shared by every result was used")
	}
	c := mustParse(t, doc(`"x"`, "", pf("only")), Limits{})
	if c.Findings[0].Fingerprint != fingerprint([]string{"x", "r", "", "partial", "h=only"}) {
		t.Error("the partial fingerprint of a lone result was not used")
	}
	d := mustParse(t, doc(`"x"`, "", pf("Requires Login ")), Limits{})
	if d.Findings[0].Fingerprint != fingerprint([]string{"x", "r", "", "line", "1", "m"}) {
		t.Error("the requires login placeholder was used")
	}
}

// memoryUsed reports the bytes allocated while fn runs.
func memoryUsed(fn func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestMemoryIsBounded(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation")
	}
	const size = 20 << 20
	var huge, many, deep bytes.Buffer
	// One result whose message is the whole file.
	huge.WriteString(`{"version":"2.1.0","runs":[{"results":[{"ruleId":"r","message":{"text":"`)
	for huge.Len() < size {
		huge.WriteString("a1b2c3d4e5f6g7h8 ")
	}
	huge.WriteString(`"}}]}]}`)
	// Many results, each with a long message, a long path and code flows
	// that are never read.
	many.WriteString(`{"version":"2.1.0","runs":[{"results":[`)
	for i := 0; many.Len() < size; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		fmt.Fprintf(&many, `{"ruleId":"r%d","message":{"text":"%s"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"%s"}}}],"codeFlows":[{"threadFlows":[{"locations":[%s]}]}]}`,
			i, strings.Repeat("m", 1500), strings.Repeat("d/", 400), strings.Repeat(`{"location":{"message":{"text":"step"}}},`, 50)+`{}`)
	}
	many.WriteString("]}]}")
	// Results with a million locations each.
	deep.WriteString(`{"version":"2.1.0","runs":[{"results":[{"ruleId":"r","message":{"text":"x"},"locations":[`)
	for i := 0; deep.Len() < size; i++ {
		deep.WriteString(`{"physicalLocation":{"artifactLocation":{"uri":"a"}}},`)
	}
	deep.WriteString(`{}]}]}]}`)

	for name, data := range map[string][]byte{"one huge message": huge.Bytes(), "many results": many.Bytes(), "many locations": deep.Bytes()} {
		var r *Report
		used := memoryUsed(func() {
			var err error
			r, err = Parse(context.Background(), data, Limits{MaxFindings: 100, Timeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
		})
		checkReport(t, r, Limits{MaxFindings: 100})
		t.Logf("%s: %.1f MB of input, %.1f MB allocated, %d findings", name, float64(len(data))/1e6, float64(used)/1e6, r.Counts.Total)
		if used > 3*uint64(len(data)) {
			t.Errorf("%s: %d bytes allocated for %d bytes of input", name, used, len(data))
		}
		if r.Counts.Total == 0 {
			t.Errorf("%s: nothing read", name)
		}
	}
}

func TestFieldNamesMatchExactly(t *testing.T) {
	// encoding/json's older Unmarshal would take RULEID, ruleIndeX and
	// ſeverity (with a long s) as ruleId, ruleIndex and severity.
	in := doc(`"x"`, `{"id":"A","defaultConfiguration":{"level":"error"}},{"id":"B","defaultConfiguration":{"level":"note"}}`,
		`{"ruleId":"A","RULEID":"B","message":{"text":"m"}},`+
			`{"ruleIndeX":1,"message":{"TEXT":"hidden","text":"shown"}},`+
			`{"ruleId":"A","properties":{"ſeverity":"low"},"message":{"text":"m"}}`)
	r := mustParse(t, in, Limits{})
	got := []string{}
	for _, f := range r.Findings {
		got = append(got, f.RuleID+" "+string(f.Severity)+" "+f.Message)
	}
	if strings.Join(got, ",") != "A high m,(no rule) medium shown,A high m" || r.Warning != "" {
		t.Errorf("findings %q, warning %q", got, r.Warning)
	}
}
