package testreport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func parseString(t *testing.T, s string, lim Limits) (*Report, error) {
	t.Helper()
	r, err := Parse(context.Background(), []byte(s), lim)
	if err == nil {
		checkReport(t, r, lim)
	}
	return r, err
}

func mustParse(t *testing.T, s string, lim Limits) *Report {
	t.Helper()
	r, err := parseString(t, s, lim)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func TestContentThatIsNotAReport(t *testing.T) {
	random := make([]byte, 4096)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range random {
		random[i] = byte(rng.UintN(256))
	}
	random[0] = 0x8f // make sure it does not start with < or {

	for _, tc := range []struct {
		name, in, want string
	}{
		{"empty file", "", "not a test report: the file is empty"},
		{"only white space", " \n\t\n", "not a test report: the file holds only white space"},
		{"plain text", "hello world, this is a log", `not a test report: the content is not XML, JSON or TAP (it starts with "hello wor…")`},
		{"html page", "<!DOCTYPE html>\n<html><head><title>502 Bad Gateway</title></head><body></body></html>", "not a test report: the XML's root element is <html>; known roots are <testsuites>, <testsuite>, <testng-results>, <test-run>, <assemblies>, <TestRun>, <robot>"},
		{"random binary", string(random), "not a test report: the content is not XML, JSON or TAP (it starts with"},
		{"package.json", `{"name": "shop", "version": "1.0.0", "scripts": {"test": "jest"}}`, `not a test report: the JSON object has keys "name", "version", "scripts"; known JSON reports are Cucumber`},
		{"json lines without actions", "{\"level\":\"info\",\"msg\":\"hi\"}\n{\"level\":\"warn\"}\n", "no go test -json events were found"},
		{"broken xml before the root", "<<<>>>", "not a test report: the XML could not be read (line 1"},
		{"xml with no root element", "<?xml version=\"1.0\"?>\n<!-- nothing -->\n", "not a test report: the XML has no root element"},
		{"utf-16 xml", `<?xml version="1.0" encoding="UTF-16"?><testsuite/>`, "not a test report: the XML is in encoding UTF-16, which is not supported; write it as UTF-8"},
		{"json array", `[{"Action":"pass"}]`, `not a test report: the JSON is an array of objects with keys "Action", not Cucumber features`},
		{"stderr then plain text", "go: downloading x\n{not json}\n", "not a test report: the content is not XML, JSON or TAP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseString(t, tc.in, Limits{})
			if !errors.Is(err, ErrNotReport) {
				t.Fatalf("err = %v, want ErrNotReport", err)
			}
			if !strings.HasPrefix(err.Error(), "not a test report: ") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
			checkString(t, "error", err.Error(), 400)
		})
	}
}

func TestEmptyReportIsValid(t *testing.T) {
	for _, in := range []string{
		`<testsuites/>`,
		"\xEF\xBB\xBF  <?xml version='1.0' encoding='utf-8'?>\n<testsuite name=\"x\" tests=\"0\"></testsuite>",
	} {
		r := mustParse(t, in, Limits{})
		if r.Counts.Total != 0 || len(r.Cases) != 0 || r.Cases == nil || r.Warning != "" {
			t.Errorf("%q: %+v", in, r)
		}
	}
}

func TestAttributeCountsAreIgnored(t *testing.T) {
	r := mustParse(t, `<testsuites tests="99999999999" failures="99999999999" errors="-1" time="1e300">
<testsuite name="s" tests="99999999999" failures="18446744073709551616" skipped="NaN">
<testcase name="a" classname="c"/>
</testsuite></testsuites>`, Limits{})
	wantCounts(t, r, Counts{Total: 1, Passed: 1})
	if r.DurationMs != 0 {
		t.Errorf("duration %d came from the suite attribute", r.DurationMs)
	}
}

func TestOddTimes(t *testing.T) {
	for in, want := range map[string]int64{
		"0.001":    1,
		"1,234.5":  1234500,
		"":         0,
		"abc":      0,
		"-5":       0,
		"NaN":      0,
		"Inf":      0,
		"-Inf":     0,
		"1e300":    1e10,
		"1e400":    1e10,
		"10000001": 1e10,
		" 2.5 ":    2500,
		"0.0004":   0,
		"0.0005":   1,
	} {
		r := mustParse(t, fmt.Sprintf(`<testsuite><testcase name="a" time=%q/></testsuite>`, in), Limits{})
		if got := r.Cases[0].DurationMs; got != want {
			t.Errorf("time=%q: %d ms, want %d", in, got, want)
		}
	}
	var sb strings.Builder
	sb.WriteString("<testsuite>")
	for range 1000 {
		sb.WriteString(`<testcase name="a" time="1e300"/>`)
	}
	sb.WriteString("</testsuite>")
	if r := mustParse(t, sb.String(), Limits{}); r.DurationMs != 1000*1e10 {
		t.Errorf("total duration %d", r.DurationMs)
	}
}

func TestDeepNestingStopsPromptly(t *testing.T) {
	const depth = 100000
	var sb strings.Builder
	sb.WriteString(`<testsuites><testsuite name="top"><testcase name="first"/>`)
	for range depth {
		sb.WriteString("<testsuite>")
	}
	sb.WriteString(`<testcase name="deep"/>`)
	for range depth {
		sb.WriteString("</testsuite>")
	}
	sb.WriteString("</testsuite></testsuites>")
	start := time.Now()
	r := mustParse(t, sb.String(), Limits{})
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %s", d)
	}
	if r.Counts.Total != 1 || !strings.Contains(r.Warning, "more than 64 deep") {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}

	// Deep nesting inside a test case, and before any test at all.
	_, err := parseString(t, strings.Repeat("<a>", depth), Limits{})
	if !errors.Is(err, ErrNotReport) {
		t.Errorf("err = %v", err)
	}
	r = mustParse(t, "<testsuite><testcase name='x'><failure>"+strings.Repeat("<b>", depth), Limits{MaxDepth: 10})
	if r.Counts.Total != 0 || !strings.Contains(r.Warning, "more than 10 deep") {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}

	// Nested suites within the limit name the case after every suite.
	r = mustParse(t, `<testsuites name="all"><testsuite name="outer"><testsuite name=""><testsuite name="inner"><testcase name="t"/></testsuite></testsuite></testsuite></testsuites>`, Limits{})
	if r.Cases[0].Suite != "outer / inner" {
		t.Errorf("suite = %q", r.Cases[0].Suite)
	}
}

func TestBillionLaughsIsNotExpanded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\"?>\n<!DOCTYPE lolz [\n <!ENTITY lol \"lol\">\n")
	sb.WriteString(" <!ENTITY lol1 \"&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;\">\n")
	for i := 2; i <= 9; i++ {
		fmt.Fprintf(&sb, " <!ENTITY lol%d \"%s\">\n", i, strings.Repeat(fmt.Sprintf("&lol%d;", i-1), 10))
	}
	sb.WriteString("]>\n<testsuite name=\"&lol9;\"><testcase name=\"boom &lol9;\"><failure message=\"&lol9;\">&lol9;</failure></testcase></testsuite>\n")
	start := time.Now()
	r := mustParse(t, sb.String(), Limits{})
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s", d)
	}
	c := r.Cases[0]
	if c.Name != "boom &lol9;" || c.Message != "&lol9;" || c.Status != Failed {
		t.Errorf("case = %+v", c)
	}
}

func TestExternalEntityIsNeverRead(t *testing.T) {
	secret := "TOP-SECRET-" + fmt.Sprint(time.Now().UnixNano())
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	in := fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE testsuite [ <!ENTITY xxe SYSTEM "file://%s"> <!ENTITY %% remote SYSTEM "file://%s"> %%remote; ]>
<testsuite name="&xxe;"><testcase name="&xxe;" classname="c &xxe;"><failure message="&xxe;">&xxe;</failure><system-out>&xxe;</system-out></testcase></testsuite>`, path, path)
	r := mustParse(t, in, Limits{})
	if dump := fmt.Sprintf("%+v", r); strings.Contains(dump, "TOP-SECRET") {
		t.Fatalf("the file's content leaked: %s", dump)
	}
	if r.Cases[0].Name != "&xxe;" {
		t.Errorf("name = %q", r.Cases[0].Name)
	}
}

func TestDirtyBytesAreCleaned(t *testing.T) {
	in := "<testsuite name=\"s\xff\"><testcase classname=\"pkg\x00.Class\" name=\"  bad\xc3\x28name\x01 \"><failure message=\"boom\x1b[31m red\x1b[0m\">body\x00 \xfe</failure>" +
		"<system-out>line one\x1b[32m ok\x1b[0m\r\nline\x07 two \xe2\x82</system-out><system-err>\x0bspent\x00</system-err></testcase>" +
		"<testcase name=\"after\"/></testsuite>"
	r := mustParse(t, in, Limits{})
	if r.Counts.Total != 2 || r.Warning != "" {
		t.Fatalf("counts %+v, warning %q", r.Counts, r.Warning)
	}
	c := r.Cases[0]
	if c.Suite != "pkg.Class" || c.Name != "bad�(name" || c.Message != "boom red" {
		t.Errorf("case = %+v", c)
	}
	if c.Details != "body �" {
		t.Errorf("details = %q", c.Details)
	}
	if c.Output != "line one ok\nline two \uFFFD\uFFFD\nspent" {
		t.Errorf("output = %q", c.Output)
	}
}

func TestForbiddenCharacterReferences(t *testing.T) {
	in := `<testsuite><testcase name="a&#0;b&#x1;"><failure message="&#27;[31mred&#x1b;[0m &#xFFFE; &#99999999999999;">x &amp; y &lt; z &#65; &#x1F600; &nbsp; & lone</failure><system-out>&#x1B;[1mbold&#27;[0m &#8;</system-out></testcase></testsuite>`
	r := mustParse(t, in, Limits{})
	c := r.Cases[0]
	if c.Name != "ab" || c.Message != "red � �" {
		t.Errorf("name %q, message %q", c.Name, c.Message)
	}
	if c.Details != "x & y < z A 😀 &nbsp; & lone" {
		t.Errorf("details = %q", c.Details)
	}
	if c.Output != "bold" {
		t.Errorf("output = %q", c.Output)
	}
}

func TestCDATAIsLeftAlone(t *testing.T) {
	r := mustParse(t, "<testsuite><testcase name=\"a\"><failure><![CDATA[a & b &lt; <c> \x1b[31mred\x1b[0m]]></failure></testcase></testsuite>", Limits{})
	if got := r.Cases[0].Details; got != "" || r.Cases[0].Message != "a & b &lt; <c> red" {
		t.Errorf("message %q details %q", r.Cases[0].Message, got)
	}
}

func TestLatin1Report(t *testing.T) {
	in := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<testsuite name=\"caf\xe9\"><testcase name=\"na\xefve\" classname=\"\"><failure message=\"\xfcber\x1b[31m\">x</failure></testcase></testsuite>"
	r := mustParse(t, in, Limits{})
	c := r.Cases[0]
	if c.Suite != "café" || c.Name != "naïve" || c.Message != "über" {
		t.Errorf("case = %+v", c)
	}
}

func TestTruncatedJUnit(t *testing.T) {
	full := string(readSample(t, "TEST-com.example.FooTest.xml"))
	cut := full[:strings.Index(full, `<testcase name="slowNetwork"`)+40]
	r := mustParse(t, cut, Limits{})
	if r.Counts.Total != 3 {
		t.Errorf("counts = %+v", r.Counts)
	}
	if !strings.HasPrefix(r.Warning, "the file ends part-way through (XML error at line 29: unexpected EOF); results after that are missing") {
		t.Errorf("warning = %q", r.Warning)
	}

	r = mustParse(t, `<testsuite><testcase name="a"/><testcase name="b"></oops></testsuite>`, Limits{})
	if r.Counts.Total != 1 || !strings.HasPrefix(r.Warning, "the XML is broken at line 1 (") {
		t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
	}
}

func TestHugeClaims(t *testing.T) {
	big := strings.Repeat("9", 5000)
	in := `<?xml version="1.0" encoding="UTF-8"?>
<!-- size="104857600" -->
<testsuites tests="` + big + `" size="104857600" time="` + big + `">
<testsuite name="` + strings.Repeat("n", 200000) + `" tests="` + big + `">
<testcase name="` + strings.Repeat("x", 200000) + `" classname="c" file="` + strings.Repeat("f/", 100000) + `" time="` + big + `"><failure message="` + strings.Repeat("m", 300000) + `" type="` + big + `">` + strings.Repeat("d", 300000) + `</failure></testcase>
</testsuite></testsuites>`
	r := mustParse(t, in, Limits{})
	c := r.Cases[0]
	if len(c.Name) != 1000 || !strings.HasSuffix(c.Name, "…") || len(c.File) > 500 || !strings.HasSuffix(c.File, "…") {
		t.Errorf("name %d bytes, file %d bytes", len(c.Name), len(c.File))
	}
	if len(c.Message) != 2048 || len(c.Details) != 8192 || !strings.HasSuffix(c.Details, "…") {
		t.Errorf("message %d bytes, details %d bytes", len(c.Message), len(c.Details))
	}
	if c.DurationMs != 1e10 {
		t.Errorf("duration %d", c.DurationMs)
	}
}

func generateJUnit(cases int) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<testsuites name="big" tests="1">` + "\n")
	for s := 0; s*1000 < cases; s++ {
		fmt.Fprintf(&b, "<testsuite name=\"com.example.Suite%d\" tests=\"1000\">\n", s)
		for i := s * 1000; i < min(cases, (s+1)*1000); i++ {
			switch i % 10 {
			case 3:
				fmt.Fprintf(&b, "  <testcase name=\"test%d\" classname=\"com.example.Suite%d\" time=\"0.%03d\"><failure message=\"expected 1 but was 2\" type=\"AssertionError\">java.lang.AssertionError: expected 1 but was 2\n\tat com.example.Suite.test(Suite.java:%d)\n</failure><system-out>some output for test %d\n</system-out></testcase>\n", i, s, i%1000, i, i)
			case 7:
				fmt.Fprintf(&b, "  <testcase name=\"test%d\" classname=\"com.example.Suite%d\" time=\"0\"><skipped message=\"not today\"/></testcase>\n", i, s)
			default:
				fmt.Fprintf(&b, "  <testcase name=\"test%d\" classname=\"com.example.Suite%d\" time=\"0.%03d\"/>\n", i, s, i%1000)
			}
		}
		b.WriteString("</testsuite>\n")
	}
	b.WriteString("</testsuites>\n")
	return b.Bytes()
}

func TestManyTestcasesRespectMaxCases(t *testing.T) {
	data := generateJUnit(200000)
	start := time.Now()
	// The parser's own time limit is not what this test is about: on a busy
	// machine under the race detector the default would cut the file short.
	r, err := Parse(context.Background(), data, Limits{MaxCases: 1000, Timeout: 5 * time.Minute})
	d := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	checkReport(t, r, Limits{MaxCases: 1000})
	t.Logf("%d testcases, %.1f MB, parsed in %s", r.Counts.Total, float64(len(data))/1e6, d)
	wantCounts(t, r, Counts{Total: 200000, Passed: 160000, Failed: 20000, Skipped: 20000})
	if len(r.Cases) != 1000 || !r.Truncated || r.Cases[999].Name != "test999" {
		t.Errorf("%d cases stored, truncated %v", len(r.Cases), r.Truncated)
	}
	// A guard against quadratic work, not a benchmark: generous, because
	// the race detector and a busy two-CPU machine slow this many times.
	limit := 15 * time.Second
	if raceEnabled {
		limit = 60 * time.Second
	}
	if d > limit {
		t.Errorf("took %s", d)
	}
}

func TestHugeSystemOut(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`<testsuite><testcase name="noisy"><failure message="boom"/><system-out>`)
	for i := 0; b.Len() < 10<<20; i++ {
		fmt.Fprintf(&b, "log line %d with some padding to make it longer than it needs to be\n", i)
	}
	b.WriteString("the last line before the failure</system-out></testcase><testcase name=\"quiet\"><system-out>")
	b.WriteString(strings.Repeat("x", 5<<20))
	b.WriteString("</system-out></testcase></testsuite>")
	r, err := Parse(context.Background(), b.Bytes(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	checkReport(t, r, Limits{})
	c := r.Cases[0]
	if len(c.Output) > 4096 || !strings.HasPrefix(c.Output, "…\n") || !strings.HasSuffix(c.Output, "the last line before the failure") {
		t.Errorf("output is %d bytes: %.80q…%.80q", len(c.Output), c.Output, c.Output[max(0, len(c.Output)-80):])
	}
	if r.Cases[1].Output != "" {
		t.Errorf("a passing test kept its output")
	}
}

func generateGoJSON(tests int) []byte {
	var b bytes.Buffer
	pkg := ""
	for i := range tests {
		if i%500 == 0 {
			if pkg != "" {
				fmt.Fprintf(&b, "{\"Action\":\"output\",\"Package\":%q,\"Output\":\"FAIL\\n\"}\n{\"Action\":\"fail\",\"Package\":%q,\"Elapsed\":1.5}\n", pkg, pkg)
			}
			pkg = fmt.Sprintf("example.com/big/pkg%d", i/500)
			fmt.Fprintf(&b, "{\"Time\":\"2026-11-14T10:25:00.1Z\",\"Action\":\"start\",\"Package\":%q}\n", pkg)
		}
		name := fmt.Sprintf("TestThing%d", i)
		fmt.Fprintf(&b, "{\"Time\":\"2026-11-14T10:25:00.1Z\",\"Action\":\"run\",\"Package\":%q,\"Test\":%q}\n", pkg, name)
		fmt.Fprintf(&b, "{\"Time\":\"2026-11-14T10:25:00.1Z\",\"Action\":\"output\",\"Package\":%q,\"Test\":%q,\"Output\":\"=== RUN   %s\\n\"}\n", pkg, name, name)
		action := "pass"
		if i%10 == 3 {
			action = "fail"
			fmt.Fprintf(&b, "{\"Time\":\"2026-11-14T10:25:00.1Z\",\"Action\":\"output\",\"Package\":%q,\"Test\":%q,\"Output\":\"    thing_test.go:%d: got 1, want 2\\n\"}\n", pkg, name, i)
		}
		fmt.Fprintf(&b, "{\"Time\":\"2026-11-14T10:25:00.1Z\",\"Action\":\"output\",\"Package\":%q,\"Test\":%q,\"Output\":\"--- %s: %s (0.00s)\\n\"}\n", pkg, name, strings.ToUpper(action), name)
		fmt.Fprintf(&b, "{\"Time\":\"2026-11-14T10:25:00.1Z\",\"Action\":%q,\"Package\":%q,\"Test\":%q,\"Elapsed\":0.01}\n", action, pkg, name)
	}
	fmt.Fprintf(&b, "{\"Action\":\"fail\",\"Package\":%q,\"Elapsed\":1.5}\n", pkg)
	return b.Bytes()
}

func TestGoTestJSONManyTests(t *testing.T) {
	r, err := Parse(context.Background(), generateGoJSON(20000), Limits{MaxCases: 100})
	if err != nil {
		t.Fatal(err)
	}
	checkReport(t, r, Limits{MaxCases: 100})
	wantCounts(t, r, Counts{Total: 20000, Passed: 18000, Failed: 2000})
	if !r.Truncated || len(r.Cases) != 100 || r.DurationMs != 40*1500 {
		t.Errorf("truncated %v, %d cases, duration %d", r.Truncated, len(r.Cases), r.DurationMs)
	}
}

func TestGoTestJSONLongLine(t *testing.T) {
	long := fmt.Sprintf(`{"Action":"output","Package":"p","Test":"TestBig","Output":"%s\n"}`, strings.Repeat("y", 5<<20))
	in := `{"Action":"run","Package":"p","Test":"TestBig"}` + "\n" + long + "\n" +
		`{"Action":"output","Package":"p","Test":"TestBig","Output":"    big_test.go:9: too big\n"}` + "\n" +
		`{"Action":"fail","Package":"p","Test":"TestBig","Elapsed":0.25}` + "\n"
	r := mustParse(t, in, Limits{})
	if r.Warning != "line 2 is longer than 1 MiB and was skipped" {
		t.Errorf("warning = %q", r.Warning)
	}
	c := r.Cases[0]
	if c.Status != Failed || c.Message != "big_test.go:9: too big" || c.DurationMs != 250 || r.DurationMs != 250 {
		t.Errorf("case = %+v, report duration %d", c, r.DurationMs)
	}
}

func TestGoTestJSONCutOffMidLine(t *testing.T) {
	data := string(readSample(t, "go-test.json"))
	i := strings.Index(data, `"Test":"TestDiv","Output":"    calc_test.go`)
	r := mustParse(t, data[:i], Limits{})
	if !strings.HasPrefix(r.Warning, "the file ends part-way through line ") {
		t.Errorf("warning = %q", r.Warning)
	}
	wantCounts(t, r, Counts{Total: 3, Passed: 1, Errored: 2})
	if c := findCase(t, r, "TestDiv"); c.Status != Errored || c.Message != notFinished {
		t.Errorf("TestDiv = %+v", c)
	}
	// The build failure was announced before the cut.
	if c := findCase(t, r, "[build failed]"); c.Suite != "example.com/app/broken" {
		t.Errorf("build case = %+v", c)
	}
}

func TestGoTestJSONEdgeCases(t *testing.T) {
	t.Run("timeout panic", func(t *testing.T) {
		in := `{"Action":"run","Package":"p","Test":"TestSlow"}
{"Action":"output","Package":"p","Test":"TestSlow","Output":"=== RUN   TestSlow\n"}
{"Action":"output","Package":"p","Test":"TestSlow","Output":"panic: test timed out after 10m0s\n"}
{"Action":"output","Package":"p","Test":"TestSlow","Output":"\trunning tests:\n"}
{"Action":"output","Package":"p","Test":"TestSlow","Output":"\t\tTestSlow (10m0s)\n"}
{"Action":"output","Package":"p","Output":"FAIL\tp\t600.012s\n"}
{"Action":"fail","Package":"p","Elapsed":600.012}
`
		r := mustParse(t, in, Limits{})
		wantCounts(t, r, Counts{Total: 1, Errored: 1})
		c := r.Cases[0]
		if !strings.HasSuffix(c.Message, "\npanic: test timed out after 10m0s") || !strings.Contains(c.Details, "TestSlow (10m0s)") || r.DurationMs != 600012 {
			t.Errorf("case = %+v", c)
		}
	})
	t.Run("TestMain failure", func(t *testing.T) {
		in := `{"Action":"start","Package":"p"}
{"Action":"output","Package":"p","Output":"setup: cannot reach the database\n"}
{"Action":"output","Package":"p","Output":"FAIL\tp\t0.004s\n"}
{"Action":"fail","Package":"p","Elapsed":0.004}
`
		r := mustParse(t, in, Limits{})
		c := r.Cases[0]
		if c.Name != "[package failed]" || c.Status != Errored || c.Message != "setup: cannot reach the database" || !strings.Contains(c.Output, "FAIL\tp") {
			t.Errorf("case = %+v", c)
		}
	})
	t.Run("setup failed", func(t *testing.T) {
		in := `{"Action":"start","Package":"p"}
{"Action":"output","Package":"p","Output":"# p\nsetup failed: go.mod not found\n"}
{"Action":"output","Package":"p","Output":"FAIL\tp [setup failed]\n"}
{"Action":"fail","Package":"p"}
`
		r := mustParse(t, in, Limits{})
		if c := r.Cases[0]; c.Name != "[build failed]" || c.Message != "setup failed: go.mod not found" {
			t.Errorf("case = %+v", c)
		}
	})
	t.Run("a failing test means no package case", func(t *testing.T) {
		in := `{"Action":"fail","Package":"p","Test":"TestA","Elapsed":0.1}
{"Action":"fail","Package":"p","Elapsed":0.2}
`
		r := mustParse(t, in, Limits{})
		wantCounts(t, r, Counts{Total: 1, Failed: 1})
		if r.Cases[0].Message != "" || r.DurationMs != 200 {
			t.Errorf("report = %+v", r)
		}
	})
	t.Run("no package timing uses the top-level tests", func(t *testing.T) {
		in := `{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.1}
{"Action":"pass","Package":"p","Test":"TestA/sub","Elapsed":0.1}
{"Action":"pass","Package":"p","Test":"TestB","Elapsed":0.25}
`
		if r := mustParse(t, in, Limits{}); r.DurationMs != 350 {
			t.Errorf("duration = %d", r.DurationMs)
		}
	})
	t.Run("a build failure with no package events", func(t *testing.T) {
		in := `{"ImportPath":"example.com/dep","Action":"build-output","Output":"# example.com/dep\n"}
{"ImportPath":"example.com/dep","Action":"build-output","Output":"dep/dep.go:3:1: syntax error: unexpected }\n"}
{"ImportPath":"example.com/dep","Action":"build-fail"}
`
		r := mustParse(t, in, Limits{})
		if c := r.Cases[0]; c.Suite != "example.com/dep" || c.Name != "[build failed]" || c.Message != "dep/dep.go:3:1: syntax error: unexpected }" {
			t.Errorf("case = %+v", c)
		}
	})
	t.Run("dirty strings", func(t *testing.T) {
		in := "{\"Action\":\"output\",\"Package\":\"p\\u0000\",\"Test\":\"Test\\u001b[31mX\",\"Output\":\"\\u001b[31m    x_test.go:1: bad \\u0000\\ud800 byte\\u001b[0m\\n\"}\n" +
			"{\"Action\":\"fail\",\"Package\":\"p\\u0000\",\"Test\":\"Test\\u001b[31mX\"}\n"
		r := mustParse(t, in, Limits{})
		c := r.Cases[0]
		if c.Suite != "p" || c.Name != "TestX" || c.Message != "x_test.go:1: bad � byte" {
			t.Errorf("case = %+v", c)
		}
	})
	t.Run("too many open tests", func(t *testing.T) {
		var sb strings.Builder
		for i := range 100 {
			fmt.Fprintf(&sb, "{\"Action\":\"run\",\"Package\":\"p\",\"Test\":\"T%d\"}\n", i)
		}
		r := mustParse(t, sb.String(), Limits{MaxCases: 5})
		if r.Counts.Total != 19 || !strings.Contains(r.Warning, "were not tracked") {
			t.Errorf("counts %+v, warning %q", r.Counts, r.Warning)
		}
	})
}

func TestTimeoutStopsParsing(t *testing.T) {
	data := generateJUnit(200000)
	start := time.Now()
	r, err := Parse(context.Background(), data, Limits{Timeout: 20 * time.Millisecond})
	d := time.Since(start)
	if d > time.Second {
		t.Errorf("took %s", d)
	}
	if err != nil {
		if !strings.HasPrefix(err.Error(), "parsing stopped after 20ms") {
			t.Errorf("err = %v", err)
		}
	} else if !regexp.MustCompile(`^parsing stopped after 20ms; results after line \d+ are missing$`).MatchString(r.Warning) || r.Counts.Total == 0 || r.Counts.Total == 200000 {
		t.Errorf("warning %q, counts %+v", r.Warning, r.Counts)
	}

	gj := generateGoJSON(100000)
	start = time.Now()
	r, err = Parse(context.Background(), gj, Limits{Timeout: 20 * time.Millisecond})
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s", d)
	}
	if err == nil && !strings.HasPrefix(r.Warning, "parsing stopped after 20ms; results after line ") {
		t.Errorf("warning %q", r.Warning)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Parse(ctx, data, Limits{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestCutsStayWithinLimits(t *testing.T) {
	s := strings.Repeat("ab€", 100) // 5 bytes per repeat
	for max := 0; max < 40; max++ {
		h := cutHead(s, max, false)
		tl := cutTail(s, max, false)
		checkString(t, "head", h, max)
		checkString(t, "tail", tl, max)
		if max > 4 && (!strings.HasSuffix(h, "…") || !strings.HasPrefix(tl, "…\n")) {
			t.Errorf("max %d: %q %q", max, h, tl)
		}
	}
	if got := cutHead("short", 100, true); got != "short…" {
		t.Errorf("forced cut = %q", got)
	}
	if got := cutTail("short", 100, true); got != "…\nshort" {
		t.Errorf("forced tail cut = %q", got)
	}
}

func TestKey(t *testing.T) {
	a := Case{Suite: "pkg", Name: "TestA", File: "a_test.go"}
	k := a.Key()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(k) {
		t.Fatalf("key = %q", k)
	}
	b := a
	b.Status, b.Message, b.DurationMs = Failed, "boom", 12
	if b.Key() != k || a.Key() != k {
		t.Error("key changed with the result")
	}
	b.File = "b_test.go"
	if b.Key() == k {
		t.Error("key ignores the file")
	}
	// The separator keeps "ab"+"c" and "a"+"bc" apart.
	if (Case{Suite: "ab", Name: "c"}).Key() == (Case{Suite: "a", Name: "bc"}).Key() {
		t.Error("key mixes suite and name")
	}
}

// largeInputs are two inputs of about 10 MB each, and how many cases they
// hold.
var largeInputs = []struct {
	name  string
	gen   func(n int) []byte
	cases int
}{
	{"JUnit", generateJUnit, 100000},
	{"go test -json", generateGoJSON, 19000},
}

// parseMallocs parses data with a budget that cannot run out, and counts the
// allocations it took: a measure of work that does not depend on how busy the
// machine is.
func parseMallocs(t *testing.T, data []byte) (*Report, uint64) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r, err := Parse(context.Background(), data, Limits{Timeout: 2 * time.Minute})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	return r, after.Mallocs - before.Mallocs
}

// Large inputs parse completely, without a warning, whatever load the machine
// is under: the parse has a budget of its own that is far more than it needs.
func TestLargeInputsParseCompletely(t *testing.T) {
	for _, in := range largeInputs {
		t.Run(in.name, func(t *testing.T) {
			data := in.gen(in.cases)
			start := time.Now()
			r, _ := parseMallocs(t, data)
			t.Logf("%s: %.1f MB, %d cases, parsed in %s", in.name, float64(len(data))/1e6, r.Counts.Total, time.Since(start))
			if r.Counts.Total != in.cases || r.Warning != "" {
				t.Fatalf("%d cases, want %d (warning %q)", r.Counts.Total, in.cases, r.Warning)
			}
			if r.Counts.Failed != (in.cases+6)/10 {
				t.Fatalf("%d failed", r.Counts.Failed)
			}
		})
	}
}

// Parsing is linear: twice the input is about twice the work. Allocations
// count the work without a clock, so this holds under the race detector and
// on a busy machine.
func TestLargeInputsParseInLinearWork(t *testing.T) {
	for _, in := range largeInputs {
		t.Run(in.name, func(t *testing.T) {
			_, one := parseMallocs(t, in.gen(in.cases/2))
			_, two := parseMallocs(t, in.gen(in.cases))
			t.Logf("%s: %d allocations for half the input, %d for all of it", in.name, one, two)
			if ratio := float64(two) / float64(one); ratio > 2.5 {
				t.Fatalf("twice the input took %.1f times the allocations: not linear", ratio)
			}
		})
	}
}

// With nothing else to slow it down, 10 MB parses in a few seconds. A clock
// is only fair then: not under the race detector, which is several times
// slower, and not in a short run on a shared machine.
func TestLargeInputsParseQuickly(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("the wall-clock budget is checked only without the race detector and without -short")
	}
	for _, in := range largeInputs {
		data := in.gen(in.cases)
		start := time.Now()
		r, _ := parseMallocs(t, data)
		if d := time.Since(start); d > 5*time.Second || r.Warning != "" {
			t.Errorf("%s took %s (warning %q)", in.name, d, r.Warning)
		}
	}
}
