package testreport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func readSample(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseSample(t *testing.T, name string) *Report {
	t.Helper()
	r, err := Parse(context.Background(), readSample(t, name), Limits{})
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	checkReport(t, r, DefaultLimits())
	return r
}

// checkReport checks the promises every report keeps, whatever the input.
func checkReport(t testing.TB, r *Report, lim Limits) {
	t.Helper()
	lim = lim.withDefaults()
	n := r.Counts
	if n.Total != n.Passed+n.Failed+n.Errored+n.Skipped {
		t.Errorf("counts do not add up: %+v", n)
	}
	if n.Total < len(r.Cases) || len(r.Cases) > lim.MaxCases {
		t.Errorf("%d cases stored, counts %+v, MaxCases %d", len(r.Cases), n, lim.MaxCases)
	}
	if r.Truncated != (len(r.Cases) < n.Total) {
		t.Errorf("Truncated = %v with %d cases of %d", r.Truncated, len(r.Cases), n.Total)
	}
	if n.Flaky > n.Passed || r.DurationMs < 0 {
		t.Errorf("bad counts or duration: %+v %d", n, r.DurationMs)
	}
	checkString(t, "Warning", r.Warning, 2000)
	for i, c := range r.Cases {
		checkString(t, "Suite", c.Suite, lim.MaxNameBytes)
		checkString(t, "Name", c.Name, lim.MaxNameBytes)
		checkString(t, "File", c.File, maxFileBytes)
		checkString(t, "Message", c.Message, lim.MaxMessageBytes)
		checkString(t, "Details", c.Details, lim.MaxDetailsBytes)
		checkString(t, "Output", c.Output, lim.MaxOutputBytes)
		switch c.Status {
		case Passed, Failed, Errored, Skipped:
		default:
			t.Errorf("case %d has status %q", i, c.Status)
		}
		if c.Output != "" && c.Status != Failed && c.Status != Errored {
			t.Errorf("case %d (%s) keeps output", i, c.Status)
		}
		if c.DurationMs < 0 || c.DurationMs > 1e10 {
			t.Errorf("case %d duration %d", i, c.DurationMs)
		}
		checkRichCase(t, i, c, lim)
	}
	var inline int64
	for _, c := range r.Cases {
		for _, a := range c.Attachments {
			inline += int64(len(a.Data))
		}
	}
	if inline > lim.MaxInlineTotal {
		t.Errorf("inline attachments hold %d bytes, over %d", inline, lim.MaxInlineTotal)
	}
}

// checkRichCase checks the bounds of the richer model on one case.
func checkRichCase(t testing.TB, i int, c Case, lim Limits) {
	t.Helper()
	if len(c.Tags) > MaxTags {
		t.Errorf("case %d has %d tags", i, len(c.Tags))
	}
	for _, tag := range c.Tags {
		checkString(t, "Tag", tag, MaxTagBytes)
		if tag == "" || strings.HasPrefix(tag, "@") || strings.ContainsAny(tag, "\n\t") {
			t.Errorf("case %d tag %q", i, tag)
		}
	}
	checkString(t, "Variant", c.Variant, MaxVariantBytes)
	if c.Retries < 0 || c.Retries > maxRetries {
		t.Errorf("case %d retries %d", i, c.Retries)
	}
	if len(c.Attempts) > MaxAttempts {
		t.Errorf("case %d has %d attempts", i, len(c.Attempts))
	}
	for _, a := range c.Attempts {
		checkStatus(t, a.Status)
		checkString(t, "Attempt.Message", a.Message, lim.MaxMessageBytes)
		if a.DurationMs < 0 || a.DurationMs > 1e10 {
			t.Errorf("case %d attempt duration %d", i, a.DurationMs)
		}
	}
	if len(c.Properties) > MaxProperties {
		t.Errorf("case %d has %d properties", i, len(c.Properties))
	}
	for _, p := range c.Properties {
		checkString(t, "Property.Name", p.Name, MaxPropertyName)
		checkString(t, "Property.Value", p.Value, MaxPropertyText)
		if p.Name == "" {
			t.Errorf("case %d has a property with no name", i)
		}
	}
	if len(c.Attachments) > lim.MaxAttachments {
		t.Errorf("case %d has %d attachments", i, len(c.Attachments))
	}
	for _, a := range c.Attachments {
		checkString(t, "Attachment.Name", a.Name, MaxAttachName)
		checkString(t, "Attachment.ContentType", a.ContentType, maxContentTypeBytes)
		checkString(t, "Attachment.Path", a.Path, MaxPathBytes)
		switch {
		case a.Path != "":
			if a.Data != nil || a.Problem != "" {
				t.Errorf("case %d attachment %q has a path and data or a problem", i, a.Name)
			}
		case a.Data != nil:
			if int64(len(a.Data)) > lim.MaxInlineBytes || a.Size != int64(len(a.Data)) || a.Problem != "" {
				t.Errorf("case %d attachment %q: %d bytes, size %d, problem %q", i, a.Name, len(a.Data), a.Size, a.Problem)
			}
		default:
			if a.Problem == "" {
				t.Errorf("case %d attachment %q has neither path, data nor problem", i, a.Name)
			}
		}
		checkString(t, "Attachment.Problem", a.Problem, 300)
	}
	if len(c.Steps) > lim.MaxSteps {
		t.Errorf("case %d has %d steps", i, len(c.Steps))
	}
	for _, s := range c.Steps {
		if s.Depth < 0 || s.Depth > MaxStepDepth {
			t.Errorf("case %d step depth %d", i, s.Depth)
		}
		checkStatus(t, s.Status)
		checkString(t, "Step.Keyword", s.Keyword, maxKeywordBytes)
		checkString(t, "Step.Name", s.Name, lim.MaxNameBytes)
		checkString(t, "Step.Error", s.Error, lim.MaxMessageBytes)
		checkString(t, "Step.Text", s.Text, lim.MaxStepTextBytes)
		if s.DurationMs < 0 || s.DurationMs > 1e10 {
			t.Errorf("case %d step duration %d", i, s.DurationMs)
		}
		for _, ai := range s.Attachments {
			if ai < 0 || ai >= len(c.Attachments) {
				t.Errorf("case %d step refers to attachment %d of %d", i, ai, len(c.Attachments))
			}
		}
		if h := s.HTTP; h != nil {
			checkString(t, "HTTP.Method", h.Method, maxMethodBytes)
			checkString(t, "HTTP.URL", h.URL, MaxURLBytes)
			checkString(t, "HTTP.RequestBody", h.RequestBody, MaxHTTPBody)
			checkString(t, "HTTP.ResponseBody", h.ResponseBody, MaxHTTPBody)
			if h.DurationMs < 0 || h.DurationMs > 1e10 {
				t.Errorf("case %d HTTP duration %d", i, h.DurationMs)
			}
			for _, hs := range [][]Header{h.RequestHeaders, h.ResponseHeaders} {
				if len(hs) > MaxHeaders {
					t.Errorf("case %d has %d headers", i, len(hs))
				}
				for _, hd := range hs {
					if len(hd.Name)+len(hd.Value) > MaxHeaderBytes {
						t.Errorf("header %q is %d bytes", hd.Name, len(hd.Name)+len(hd.Value))
					}
					checkString(t, "Header.Name", hd.Name, MaxHeaderBytes)
					checkString(t, "Header.Value", hd.Value, MaxHeaderBytes)
					if sensitiveHeader(hd.Name) && hd.Value != "" && hd.Value != hidden {
						t.Errorf("header %s keeps its value %q", hd.Name, hd.Value)
					}
				}
			}
		}
	}
}

func checkStatus(t testing.TB, s Status) {
	t.Helper()
	switch s {
	case Passed, Failed, Errored, Skipped:
	default:
		t.Errorf("status %q", s)
	}
}

func checkString(t testing.TB, field, s string, max int) {
	t.Helper()
	if len(s) > max {
		t.Errorf("%s is %d bytes, over %d", field, len(s), max)
	}
	if !utf8.ValidString(s) {
		t.Errorf("%s is not valid UTF-8: %q", field, s)
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' || r == 0x7f {
			t.Errorf("%s holds control character %U: %q", field, r, s)
			return
		}
	}
}

func findCase(t *testing.T, r *Report, name string) Case {
	t.Helper()
	for _, c := range r.Cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no case named %q", name)
	return Case{}
}

func wantCounts(t *testing.T, r *Report, want Counts) {
	t.Helper()
	if r.Counts != want {
		t.Errorf("counts = %+v, want %+v", r.Counts, want)
	}
}

func TestPytestReport(t *testing.T) {
	r := parseSample(t, "pytest.xml")
	if r.Format != FormatJUnit || r.Warning != "" || r.Truncated {
		t.Errorf("format %q, warning %q, truncated %v", r.Format, r.Warning, r.Truncated)
	}
	wantCounts(t, r, Counts{Total: 6, Passed: 2, Failed: 1, Errored: 1, Skipped: 2})
	if r.DurationMs != 17 {
		t.Errorf("duration = %d, want 17", r.DurationMs)
	}

	c := findCase(t, r, "test_create_user")
	if c.Suite != "tests.test_api" || c.File != "tests/test_api.py" || c.Status != Failed || c.DurationMs != 11 {
		t.Errorf("test_create_user = %+v", c)
	}
	if c.Message != "AssertionError: assert 500 == 201\n +  where 500 = <Response [500]>.status_code" {
		t.Errorf("message = %q", c.Message)
	}
	if !strings.Contains(c.Details, "tests/test_api.py:23: AssertionError") || !strings.HasPrefix(c.Details, "client = <FlaskClient") {
		t.Errorf("details = %q", c.Details)
	}
	if !strings.Contains(c.Output, "db error: duplicate key") {
		t.Errorf("output = %q", c.Output)
	}

	c = findCase(t, r, "test_migrate")
	if c.Status != Errored || !strings.Contains(c.Message, "connection failed: Connection refused") || !strings.Contains(c.Details, "OperationalError") {
		t.Errorf("test_migrate = %+v", c)
	}
	c = findCase(t, r, "test_windows_paths")
	if c.Status != Skipped || c.Message != "only runs on Windows" || c.Output != "" {
		t.Errorf("test_windows_paths = %+v", c)
	}
	c = findCase(t, r, "test_unicode_slug")
	if c.Status != Skipped || c.Message != "slugify drops accents, see #42" || c.Details != "" {
		t.Errorf("test_unicode_slug = %+v", c)
	}
	c = findCase(t, r, "test_parse[a-b]")
	if c.Status != Passed || c.Message != "" || c.DurationMs != 1 {
		t.Errorf("test_parse[a-b] = %+v", c)
	}
	if got := r.Cases[0].Name; got != "test_health" {
		t.Errorf("first case = %q, want file order", got)
	}
}

func TestSurefireReport(t *testing.T) {
	r := parseSample(t, "TEST-com.example.FooTest.xml")
	wantCounts(t, r, Counts{Total: 6, Passed: 2, Failed: 2, Errored: 1, Skipped: 1, Flaky: 1})

	c := findCase(t, r, "dividesByZero")
	if c.Suite != "com.example.FooTest" || c.Status != Failed || c.Message != "expected: <4> but was: <5>" {
		t.Errorf("dividesByZero = %+v", c)
	}
	if !strings.Contains(c.Details, "FooTest.java:31") {
		t.Errorf("details = %q", c.Details)
	}
	if c.Output != "computing 10 / 2\nWARN rounding mode not set, using HALF_UP" {
		t.Errorf("output = %q", c.Output)
	}

	c = findCase(t, r, "readsConfig")
	if c.Status != Errored || c.Message != "config.yaml (No such file or directory)" || !strings.Contains(c.Details, "FooTest.java:44") {
		t.Errorf("readsConfig = %+v", c)
	}
	c = findCase(t, r, "slowNetwork")
	if c.Status != Skipped || c.Message != "Disabled until the mock server is fixed" {
		t.Errorf("slowNetwork = %+v", c)
	}
	c = findCase(t, r, "retriesConnection")
	if c.Status != Passed || !c.Flaky || c.Message != "Connection reset" || !strings.Contains(c.Details, "FooTest.java:60") || c.Output != "" {
		t.Errorf("retriesConnection = %+v", c)
	}
	c = findCase(t, r, "parsesDates")
	if c.Status != Failed || c.Flaky || !strings.Contains(c.Details, "rerun: expected") {
		t.Errorf("parsesDates = %+v", c)
	}
}

func TestJestReport(t *testing.T) {
	r := parseSample(t, "jest-junit.xml")
	wantCounts(t, r, Counts{Total: 5, Passed: 3, Failed: 1, Skipped: 1})
	c := findCase(t, r, "Cart applies a discount")
	if c.Suite != "Cart applies a discount" || c.Status != Failed {
		t.Errorf("case = %+v", c)
	}
	if c.Message != "Error: expect(received).toBe(expected) // Object.is equality" {
		t.Errorf("message = %q", c.Message)
	}
	if strings.Contains(c.Details, "[3") || strings.Contains(c.Details, "\x1b") || !strings.Contains(c.Details, "Received: 100") {
		t.Errorf("details still hold colour codes or lost text: %q", c.Details)
	}
	if r.DurationMs != 5+12+1420+2 {
		t.Errorf("duration = %d", r.DurationMs)
	}
}

func TestVitestReport(t *testing.T) {
	r := parseSample(t, "vitest.xml")
	wantCounts(t, r, Counts{Total: 4, Passed: 2, Failed: 1, Skipped: 1})
	c := findCase(t, r, "math > divides")
	if c.Suite != "src/math.test.ts" || c.Message != "expected 3 to be 2 // Object.is equality" {
		t.Errorf("case = %+v", c)
	}
	if !strings.HasPrefix(c.Details, "AssertionError: expected 3 to be 2") || !strings.Contains(c.Details, "❯ src/math.test.ts:9:21") {
		t.Errorf("details = %q", c.Details)
	}
	if c.Output != "dividing 6 by 2" {
		t.Errorf("output = %q", c.Output)
	}
	if c := findCase(t, r, "math > subtracts"); c.DurationMs != 1 {
		t.Errorf("0.0005s rounds to %d ms, want 1", c.DurationMs)
	}
}

func TestGotestsumReport(t *testing.T) {
	r := parseSample(t, "gotestsum.xml")
	wantCounts(t, r, Counts{Total: 4, Passed: 1, Failed: 2, Skipped: 1})
	c := findCase(t, r, "TestGet")
	if c.Suite != "example.com/app/store" || c.Message != "Failed" || !strings.Contains(c.Details, `store_test.go:41: got "b", want "a"`) {
		t.Errorf("TestGet = %+v", c)
	}
	c = findCase(t, r, "TestMain")
	if c.Suite != "example.com/app/db" || c.Status != Failed || !strings.Contains(c.Details, "FAIL\texample.com/app/db\t0.004s") {
		t.Errorf("TestMain = %+v", c)
	}
	c = findCase(t, r, "TestSlow")
	if c.Status != Skipped || !strings.Contains(c.Message, "skipping in short mode") {
		t.Errorf("TestSlow = %+v", c)
	}
}

func TestGoTestJSONReport(t *testing.T) {
	r := parseSample(t, "go-test.json")
	if r.Format != FormatGoTest || r.Warning != "" {
		t.Errorf("format %q, warning %q", r.Format, r.Warning)
	}
	wantCounts(t, r, Counts{Total: 11, Passed: 5, Failed: 3, Errored: 2, Skipped: 1})
	if r.DurationMs != 12+7+0+41 {
		t.Errorf("duration = %d", r.DurationMs)
	}
	var names []string
	for _, c := range r.Cases {
		names = append(names, c.Suite+" "+c.Name)
	}
	want := []string{
		"example.com/app/calc TestAdd",
		"example.com/app/calc TestDiv",
		"example.com/app/calc TestTable/one",
		"example.com/app/calc TestTable/two",
		"example.com/app/calc TestTable/three",
		"example.com/app/calc TestTable",
		"example.com/app/calc TestNetwork",
		"example.com/app/crash TestOK",
		"example.com/app/crash TestWorkerStop",
		"example.com/app/broken [build failed]",
		"example.com/app/ok TestA",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("cases:\n%s", strings.Join(names, "\n"))
	}

	c := findCase(t, r, "TestDiv")
	if c.Status != Failed || c.Message != "calc_test.go:22: Div(1, 0) = 0, want an error" || c.DurationMs != 1 {
		t.Errorf("TestDiv = %+v", c)
	}
	if c.Output != "    calc_test.go:22: Div(1, 0) = 0, want an error" {
		t.Errorf("TestDiv output = %q", c.Output)
	}
	c = findCase(t, r, "TestTable/three")
	if c.Status != Failed || c.Message != "calc_test.go:40: Sum(2, 5) = 7, want 9" {
		t.Errorf("TestTable/three = %+v", c)
	}
	if c := findCase(t, r, "TestTable/one"); c.Status != Passed || c.Output != "" {
		t.Errorf("TestTable/one = %+v", c)
	}
	c = findCase(t, r, "TestNetwork")
	if c.Status != Skipped || c.Message != "calc_test.go:55: needs network access; set CALC_NET=1" {
		t.Errorf("TestNetwork = %+v", c)
	}
	c = findCase(t, r, "TestWorkerStop")
	if c.Status != Errored || !strings.HasPrefix(c.Message, "the test did not finish") || !strings.HasSuffix(c.Message, "\npanic: close of closed channel") {
		t.Errorf("TestWorkerStop message = %q", c.Message)
	}
	if !strings.Contains(c.Details, "pool.go:61") || !strings.Contains(c.Output, "starting 4 workers") {
		t.Errorf("TestWorkerStop = %+v", c)
	}
	c = findCase(t, r, "[build failed]")
	if c.Status != Errored || c.Suite != "example.com/app/broken" || c.Message != "broken/parse.go:9:6: undefined: strings.Cutt" {
		t.Errorf("build failure = %+v", c)
	}
	if !strings.Contains(c.Output, "declared and not used") || !strings.Contains(c.Output, "[build failed]") {
		t.Errorf("build output = %q", c.Output)
	}
}
