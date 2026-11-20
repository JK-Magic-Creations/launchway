package testreport

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// generators write a report of n tests in each format, every tenth one
// failing.
var generators = map[string]func(n int) string{
	FormatJUnit: func(n int) string {
		return string(generateJUnit(n))
	},
	FormatCucumber: func(n int) string {
		var b strings.Builder
		b.WriteString(`[{"uri":"f.feature","name":"F","elements":[`)
		for i := range n {
			st := "passed"
			if i%10 == 3 {
				st = "failed"
			}
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"type":"scenario","name":"s%d","steps":[{"keyword":"Given ","name":"x","result":{"status":%q,"duration":1000000}}]}`, i, st)
		}
		b.WriteString(`]}]`)
		return b.String()
	},
	FormatKarate: func(n int) string {
		var b strings.Builder
		b.WriteString(`{"name":"F","relativePath":"f.feature","scenarioResults":[`)
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"name":"s%d","failed":%v,"durationMillis":1.5,"stepResults":[{"step":{"prefix":"*","text":"x"},"result":{"status":"passed","nanos":1}}]}`, i, i%10 == 3)
		}
		b.WriteString(`]}`)
		return b.String()
	},
	FormatPlaywright: func(n int) string {
		var b strings.Builder
		b.WriteString(`{"config":{},"suites":[{"title":"a.spec.ts","file":"a.spec.ts","specs":[`)
		for i := range n {
			st, ts := "passed", "expected"
			if i%10 == 3 {
				st, ts = "failed", "unexpected"
			}
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"title":"t%d","tests":[{"projectName":"p","status":%q,"results":[{"status":%q,"duration":2}]}]}`, i, ts, st)
		}
		b.WriteString(`]}]}`)
		return b.String()
	},
	FormatMochawesome: func(n int) string {
		var b strings.Builder
		b.WriteString(`{"stats":{},"results":[{"title":"","file":"a.js","suites":[{"title":"S","tests":[`)
		for i := range n {
			st := "passed"
			if i%10 == 3 {
				st = "failed"
			}
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"title":"t%d","state":%q,"duration":2,"err":{}}`, i, st)
		}
		b.WriteString(`]}]}]}`)
		return b.String()
	},
	FormatCTRF: func(n int) string {
		var b strings.Builder
		b.WriteString(`{"results":{"tool":{"name":"x"},"tests":[`)
		for i := range n {
			st := "passed"
			if i%10 == 3 {
				st = "failed"
			}
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"name":"t%d","status":%q,"duration":2}`, i, st)
		}
		b.WriteString(`]}}`)
		return b.String()
	},
	FormatTestNG: func(n int) string {
		var b strings.Builder
		b.WriteString(`<testng-results><suite name="s"><test name="t"><class name="C">`)
		for i := range n {
			st := "PASS"
			if i%10 == 3 {
				st = "FAIL"
			}
			fmt.Fprintf(&b, `<test-method status=%q name="m%d" duration-ms="2"/>`, st, i)
		}
		b.WriteString(`</class></test></suite></testng-results>`)
		return b.String()
	},
	FormatNUnit: func(n int) string {
		var b strings.Builder
		b.WriteString(`<test-run><test-suite type="TestFixture" fullname="N.C" classname="N.C">`)
		for i := range n {
			st := "Passed"
			if i%10 == 3 {
				st = "Failed"
			}
			fmt.Fprintf(&b, `<test-case name="t%d" fullname="N.C.t%d" result=%q duration="0.002"/>`, i, i, st)
		}
		b.WriteString(`</test-suite></test-run>`)
		return b.String()
	},
	FormatXUnit: func(n int) string {
		var b strings.Builder
		b.WriteString(`<assemblies><assembly name="a.dll"><collection>`)
		for i := range n {
			st := "Pass"
			if i%10 == 3 {
				st = "Fail"
			}
			fmt.Fprintf(&b, `<test name="C.t%d" type="C" method="t%d" result=%q time="0.002"/>`, i, i, st)
		}
		b.WriteString(`</collection></assembly></assemblies>`)
		return b.String()
	},
	FormatTRX: func(n int) string {
		var b strings.Builder
		b.WriteString(`<TestRun><Results>`)
		for i := range n {
			st := "Passed"
			if i%10 == 3 {
				st = "Failed"
			}
			fmt.Fprintf(&b, `<UnitTestResult testId="id%d" testName="C.t%d" outcome=%q duration="00:00:00.002"/>`, i, i, st)
		}
		b.WriteString(`</Results><TestDefinitions>`)
		for i := range n {
			fmt.Fprintf(&b, `<UnitTest id="id%d"><TestMethod className="C" name="t%d"/></UnitTest>`, i, i)
		}
		b.WriteString(`</TestDefinitions></TestRun>`)
		return b.String()
	},
	FormatTAP: func(n int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "TAP version 13\n1..%d\n", n)
		for i := range n {
			if i%10 == 3 {
				fmt.Fprintf(&b, "not ok %d - t%d\n  ---\n  message: bad\n  duration_ms: 2\n  ...\n", i+1, i)
			} else {
				fmt.Fprintf(&b, "ok %d - t%d\n", i+1, i)
			}
		}
		return b.String()
	},
	FormatRobot: func(n int) string {
		var b strings.Builder
		b.WriteString(`<robot><suite name="S">`)
		for i := range n {
			st := "PASS"
			if i%10 == 3 {
				st = "FAIL"
			}
			fmt.Fprintf(&b, `<test name="t%d"><kw name="k"><status status=%q elapsed="0.001"/></kw><tag>x</tag><status status=%q elapsed="0.002"/></test>`, i, st, st)
		}
		b.WriteString(`<status status="PASS"/></suite></robot>`)
		return b.String()
	},
}

func TestEveryFormatRespectsMaxCases(t *testing.T) {
	for format, gen := range generators {
		t.Run(format, func(t *testing.T) {
			r := mustParse(t, gen(3000), Limits{MaxCases: 10, Timeout: time.Minute})
			if r.Format != format {
				t.Errorf("format %q", r.Format)
			}
			want := Counts{Total: 3000, Passed: 2700, Failed: 300}
			if format == FormatJUnit {
				want = Counts{Total: 3000, Passed: 2400, Failed: 300, Skipped: 300}
			}
			wantCounts(t, r, want)
			if len(r.Cases) != 10 || !r.Truncated || r.Cases[9].Name == "" || r.Cases[3].Status != Failed {
				t.Errorf("%d cases, truncated %v: %+v", len(r.Cases), r.Truncated, r.Cases)
			}
		})
	}
}

func TestEveryFormatParsesLargeInputsQuickly(t *testing.T) {
	// A guard against quadratic work, not a benchmark. The parse itself has
	// a time limit far above what it needs, and must finish without
	// hitting it; the wall-clock check is only made when asked for
	// (LAUNCHWAY_TEST_SPEED=1), so a slow or busy machine cannot fail it.
	limit := time.Duration(1<<63 - 1)
	if os.Getenv("LAUNCHWAY_TEST_SPEED") != "" {
		limit = 5 * time.Second
		if raceEnabled {
			limit = 60 * time.Second
		}
	}
	for format, gen := range generators {
		data := []byte(gen(60000))
		start := time.Now()
		r, err := Parse(context.Background(), data, Limits{Timeout: 5 * time.Minute})
		d := time.Since(start)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		t.Logf("%s: %.1f MB, %d cases, parsed in %s", format, float64(len(data))/1e6, r.Counts.Total, d)
		if d > limit || r.Counts.Total != 60000 || r.Warning != "" {
			t.Errorf("%s took %s, counts %+v, warning %q", format, d, r.Counts, r.Warning)
		}
	}
}

func TestEveryFormatStopsAtTheTimeLimit(t *testing.T) {
	// Not a race against the clock: a limit of 1ns has always passed by
	// the time parsing starts, and a cancelled context is cancelled.
	for format, gen := range generators {
		t.Run(format, func(t *testing.T) {
			data := []byte(gen(50))
			_, err := Parse(context.Background(), data, Limits{Timeout: time.Nanosecond})
			if err == nil || err.Error() != "parsing stopped after 1ns before any test results were read" {
				t.Errorf("err = %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = Parse(ctx, data, Limits{})
			if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "parsing was cancelled before any test results were read") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestDeepJSONIsRefused(t *testing.T) {
	deep := strings.Repeat("[", 100000)
	for _, in := range []string{deep, "{\"a\":" + deep, `[{"elements":` + deep} {
		_, err := parseString(t, in, Limits{})
		if !errors.Is(err, ErrNotReport) || !strings.Contains(err.Error(), "nests more than 64 deep") {
			t.Errorf("%.20q: err = %v", in, err)
		}
	}
	// Deep nesting after a good case: the good case is kept.
	nest := strings.Repeat(`{"a":`, 200)
	for name, in := range map[string]string{
		"cucumber":    `[{"name":"F","elements":[{"name":"ok","steps":[]},{"name":"deep","steps":[` + nest + `]}]}]`,
		"karate":      `{"name":"F","scenarioResults":[{"name":"ok"},{"name":"deep","stepResults":[` + nest + `]}]}`,
		"playwright":  `{"config":{},"suites":[{"title":"f","specs":[{"title":"ok","tests":[{"results":[{"status":"passed"}]}]},{"title":"deep","tests":[` + nest + `]}]}]}`,
		"mochawesome": `{"stats":{},"results":[{"title":"","tests":[{"title":"ok","state":"passed"},{"title":"deep","err":` + nest + `}]}]}`,
		"ctrf":        `{"results":{"tool":{},"tests":[{"name":"ok","status":"passed"},{"name":"deep","extra":` + nest + `}]}}`,
		"suites":      `{"config":{},"suites":[{"title":"f","specs":[{"title":"ok","tests":[{"results":[{"status":"passed"}]}]}],"suites":[` + strings.Repeat(`{"title":"x","suites":[`, 100) + `]}]}`,
	} {
		r := mustParse(t, in, Limits{})
		if r.Counts.Total != 1 || r.Cases[0].Name != "ok" || !strings.Contains(r.Warning, "nests more than 64 deep") {
			t.Errorf("%s: counts %+v, warning %q", name, r.Counts, r.Warning)
		}
	}
}

func TestDeepXMLStopsEveryFormat(t *testing.T) {
	deep := strings.Repeat("<x>", 100000)
	for format, in := range map[string]string{
		FormatTestNG: `<testng-results><suite><test><class name="C"><test-method status="PASS" name="ok"/>` + deep,
		FormatNUnit:  `<test-run><test-case name="ok" result="Passed"/>` + deep,
		FormatXUnit:  `<assemblies><assembly><test name="ok" result="Pass"/>` + deep,
		FormatTRX:    `<TestRun><Results><UnitTestResult testName="ok" outcome="Passed"/>` + deep,
		FormatRobot:  `<robot><suite name="S"><test name="ok"><status status="PASS"/></test>` + deep,
	} {
		r := mustParse(t, in, Limits{})
		if r.Format != format || r.Counts.Total != 1 || !strings.Contains(r.Warning, "more than 64 deep") {
			t.Errorf("%s: counts %+v, warning %q", format, r.Counts, r.Warning)
		}
	}
	// Deep keywords inside a Robot test: steps stop at MaxStepDepth.
	var b strings.Builder
	b.WriteString(`<robot><suite name="S"><test name="t">`)
	for range 40 {
		b.WriteString(`<kw name="k">`)
	}
	for range 40 {
		b.WriteString(`<status status="PASS"/></kw>`)
	}
	b.WriteString(`<status status="PASS"/></test></suite></robot>`)
	r := mustParse(t, b.String(), Limits{})
	if n := len(r.Cases[0].Steps); n != MaxStepDepth+1 || !strings.Contains(r.Warning, "nested more than 8 deep") {
		t.Errorf("%d steps, warning %q", n, r.Warning)
	}
}

func TestInlineAttachmentBounds(t *testing.T) {
	emb := func(n int) string {
		return `{"mime_type":"application/octet-stream","data":"` + base64.StdEncoding.EncodeToString(make([]byte, n)) + `"}`
	}
	in := `[{"name":"F","elements":[{"name":"s","steps":[{"keyword":"Given ","name":"x","result":{"status":"passed"},"embeddings":[` +
		emb(1500) + `,` + emb(900) + `,` + emb(900) + `,` + emb(900) + `,{"mime_type":"image/png","data":"!!!not base64!!!"}]}]}]}]`
	r := mustParse(t, in, Limits{MaxInlineBytes: 1000, MaxInlineTotal: 2000})
	a := r.Cases[0].Attachments
	if len(a) != 5 {
		t.Fatalf("attachments = %+v", a)
	}
	if a[0].Data != nil || a[0].Size != 1500 || !strings.Contains(a[0].Problem, "over the 1000 bytes kept for one") {
		t.Errorf("big one = %+v", a[0])
	}
	if len(a[1].Data) != 900 || len(a[2].Data) != 900 || a[3].Data != nil || !strings.Contains(a[3].Problem, "passed the 2000 bytes kept for one report") {
		t.Errorf("total: %+v", a[1:4])
	}
	if a[4].Data != nil || a[4].Problem != "not kept: the data is not valid base64" {
		t.Errorf("bad base64 = %+v", a[4])
	}
	if !strings.Contains(r.Warning, "some inline attachments were not kept") {
		t.Errorf("warning = %q", r.Warning)
	}

	// A huge attachment is measured, not decoded.
	huge := strings.Repeat("A", 12<<20)
	r = mustParse(t, `{"config":{},"suites":[{"title":"f","specs":[{"title":"t","tests":[{"results":[{"status":"passed","attachments":[{"name":"v","body":"`+huge+`"}]}]}]}]}]}`, Limits{})
	if a := r.Cases[0].Attachments[0]; a.Data != nil || a.Size != 9<<20 || a.Problem == "" {
		t.Errorf("huge = %+v", a)
	}
}

func TestRichModelIsCutToLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString(`[{"name":"F","tags":[`)
	for i := range 30 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"@tag%d"},{"name":"@tag%d"}`, i, i)
	}
	fmt.Fprintf(&b, `,{"name":"@%s"}],"elements":[{"name":"s","steps":[`, strings.Repeat("t", 500))
	for i := range 300 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"keyword":"Given ","name":"step %d","result":{"status":"passed","duration":"NaN"},"embeddings":[{"mime_type":"text/plain","data":"aGk="}],"doc_string":{"value":%q}}`, i, strings.Repeat("d", 10000))
	}
	b.WriteString(`]}]}]`)
	r := mustParse(t, b.String(), Limits{})
	c := r.Cases[0]
	if len(c.Tags) != MaxTags || c.Tags[0] != "tag0" || c.Tags[1] != "tag1" || len(c.Steps) != 200 || len(c.Attachments) != 20 || len(c.Steps[0].Text) != 8192 {
		t.Errorf("%d tags, %d steps, %d attachments, text %d", len(c.Tags), len(c.Steps), len(c.Attachments), len(c.Steps[0].Text))
	}
	for _, w := range []string{"more than 20 tags", "more than 200 steps", "more than 20 attachments"} {
		if !strings.Contains(r.Warning, w) {
			t.Errorf("warning %q lacks %q", r.Warning, w)
		}
	}

	// CTRF: many properties; NUnit: many properties and tags.
	var p strings.Builder
	for i := range 50 {
		fmt.Fprintf(&p, `,"k%02d":"%s"`, i, strings.Repeat("v", 1000))
	}
	r = mustParse(t, `{"results":{"tool":{},"tests":[{"name":"t","status":"passed","extra":{"a":1`+p.String()+`}}]}}`, Limits{})
	if c := r.Cases[0]; len(c.Properties) != MaxProperties || len(c.Properties[1].Value) != MaxPropertyText || !strings.Contains(r.Warning, "more than 20 properties") {
		t.Errorf("properties = %d, warning %q", len(c.Properties), r.Warning)
	}
}

func TestDirtyStringsInEveryJSONFormat(t *testing.T) {
	dirty := "\\u0000bad\\u001b[31mred\\u001b[0m \xff\xfe\\ud800 \\t tab"
	for format, in := range map[string]string{
		FormatCucumber:    `[{"name":"` + dirty + `","uri":"` + dirty + `","elements":[{"name":"` + dirty + `","tags":[{"name":"@` + dirty + `"}],"steps":[{"keyword":"` + dirty + `","name":"` + dirty + `","result":{"status":"failed","error_message":"` + dirty + `","duration":-5}}]}]}]`,
		FormatKarate:      `{"name":"` + dirty + `","scenarioResults":[{"name":"` + dirty + `","tags":["` + dirty + `"],"failed":true,"error":"` + dirty + `","durationMillis":"1e999","stepResults":[{"step":{"text":"` + dirty + `"},"result":{"status":"failed","errorMessage":"` + dirty + `"},"stepLog":"` + dirty + `"}]}]}`,
		FormatPlaywright:  `{"config":{},"suites":[{"title":"` + dirty + `","specs":[{"title":"` + dirty + `","tags":["` + dirty + `"],"tests":[{"projectName":"` + dirty + `","results":[{"status":"failed","duration":1e308,"error":{"message":"` + dirty + `"},"attachments":[{"name":"` + dirty + `","path":"a\u0000b/../` + dirty + `.png"}]}]}]}]}]}`,
		FormatMochawesome: `{"stats":{},"results":[{"title":"` + dirty + `","tests":[{"title":"` + dirty + `","state":"failed","duration":-1,"err":{"message":"` + dirty + `"}}]}]}`,
		FormatCTRF:        `{"results":{"tool":{},"tests":[{"name":"` + dirty + `","status":"failed","message":"` + dirty + `","browser":"` + dirty + `","tags":["` + dirty + `"],"duration":"NaN"}]}}`,
	} {
		r := mustParse(t, in, Limits{})
		c := r.Cases[0]
		if r.Format != format || !strings.Contains(c.Name, "badred") || strings.Contains(c.Name, "[31m") || c.DurationMs < 0 {
			t.Errorf("%s: %+v", format, c)
		}
	}
	// Attachment paths are cleaned and kept, not resolved; one too long
	// is dropped.
	in := `{"config":{},"suites":[{"title":"f","specs":[{"title":"t","tests":[{"results":[{"status":"passed","attachments":[` +
		`{"name":"a","path":"../../etc/passwd"},{"name":"b","path":"/abs/x.png"},{"name":"c","path":"x\u0000y.png"},{"name":"d","path":"` + strings.Repeat("p/", 600) + `"}]}]}]}]}]}`
	r := mustParse(t, in, Limits{})
	var paths []string
	for _, a := range r.Cases[0].Attachments {
		paths = append(paths, a.Path)
	}
	if strings.Join(paths, " ") != "../../etc/passwd /abs/x.png xy.png" {
		t.Errorf("paths = %q", paths)
	}
}

func TestHugeStringsInEveryFormat(t *testing.T) {
	big := strings.Repeat("m", 3<<20)
	for _, in := range []string{
		`[{"name":"F","elements":[{"name":"` + big + `","steps":[{"keyword":"Given ","name":"` + big + `","result":{"status":"failed","error_message":"` + big + `"}}]}]}]`,
		`{"results":{"tool":{},"tests":[{"name":"` + big + `","status":"failed","message":"` + big + `","trace":"` + big + `","stdout":["` + big + `"]}]}}`,
		`<testng-results><suite><test><class name="` + big + `"><test-method status="FAIL" name="` + big + `"><exception><message>` + big + `</message><full-stacktrace>` + big + `</full-stacktrace></exception></test-method></class></test></suite></testng-results>`,
		`<robot><suite name="S"><test name="` + big + `"><kw name="` + big + `"><msg level="INFO">` + big + `</msg><status status="FAIL">` + big + `</status></kw><status status="FAIL">` + big + `</status></test></suite></robot>`,
		"not ok 1 - " + strings.Repeat("n", 900<<10) + "\n  ---\n  message: " + strings.Repeat("m", 900<<10) + "\n  ...\n",
		`<TestRun><Results><UnitTestResult testName="` + big + `" outcome="Failed"><Output><StdOut>` + big + `</StdOut><ErrorInfo><Message>` + big + `</Message></ErrorInfo></Output></UnitTestResult></Results></TestRun>`,
	} {
		r := mustParse(t, in, Limits{})
		if r.Counts.Total != 1 || len(r.Cases[0].Name) != 1000 {
			t.Errorf("%.30q: counts %+v, name %d bytes", in, r.Counts, len(r.Cases[0].Name))
		}
	}
}

func TestBrokenFilesKeepWhatWasRead(t *testing.T) {
	for format, gen := range generators {
		full := gen(20)
		cut := full[:len(full)*2/3]
		r, err := parseString(t, cut, Limits{})
		if err != nil {
			t.Errorf("%s: %v", format, err)
			continue
		}
		// TRX lists every result before the definitions, which is
		// where this cut falls.
		if r.Counts.Total == 0 || (r.Counts.Total >= 20 && format != FormatTRX) || (r.Warning == "" && format != FormatTAP) {
			t.Errorf("%s: counts %+v, warning %q", format, r.Counts, r.Warning)
		}
	}
}
