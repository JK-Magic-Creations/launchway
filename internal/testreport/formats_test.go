package testreport

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// Hand-written inputs for what the real samples do not show.

func TestDetectionNearMisses(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`<testng/>`, "the XML's root element is <testng>; known roots are"},
		{`<TestRun xmlns="urn:other"/>`, "the XML's root element is <TestRun>"},
		{`<results><test/></results>`, "the XML's root element is <results>"},
		{`{"config":{}}`, `the JSON object has keys "config"; known JSON reports are`},
		{`{"results":{"tool":{"name":"x"}}}`, `the JSON object has keys "results"`},
		{`{"stats":{},"results":{}}`, `the JSON object has keys "stats", "results"`},
		{`{}`, "the JSON object has no keys"},
		{`[1,2,3]`, "the JSON is an array, but not of Cucumber features"},
		{`[{"name":"x","id":1}]`, `the JSON is an array of objects with keys "name", "id", not Cucumber features`},
		{`[`, "the JSON array ends before its first element"},
		{`{"a":1} {"b":2}`, "no go test -json events were found"},
		{`{"a":1}xyz`, "no go test -json events were found"},
		{"okay then\nnot really", "the content is not XML, JSON or TAP"},
		{"# just a comment\nhello", "the content is not XML, JSON or TAP"},
		{`"a string"`, "the content is not XML, JSON or TAP"},
		{`{"a":}`, "the JSON could not be read"},
	} {
		_, err := parseString(t, tc.in, Limits{})
		if err == nil || !strings.HasPrefix(err.Error(), "not a test report: ") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.in, err, tc.want)
		}
		if err != nil {
			checkString(t, "error", err.Error(), 320)
		}
	}
	// Small valid documents of each kind are detected.
	for in, format := range map[string]string{
		`[]`:                        FormatCucumber,
		`{"config":{},"suites":[]}`: FormatPlaywright,
		`{"results":{"tool":{"name":"x"},"tests":[]}}`: FormatCTRF,
		`{"stats":{},"results":[]}`:                    FormatMochawesome,
		`{"name":"f","scenarioResults":[]}`:            FormatKarate,
		`<testng-results/>`:                            FormatTestNG,
		`<test-run/>`:                                  FormatNUnit,
		`<assembly/>`:                                  FormatXUnit,
		`<assemblies/>`:                                FormatXUnit,
		`<TestRun/>`:                                   FormatTRX,
		`<TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010"/>`: FormatTRX,
		`<robot/>`:                  FormatRobot,
		"TAP version 14\n1..0\n":    FormatTAP,
		"1..1\nok 1\n":              FormatTAP,
		"# comment\nnot ok 1 - x\n": FormatTAP,
		`{"type":"SUITE_ENTER","data":{}}` + "\n" + `{"type":"SUITE_EXIT","data":{}}`: FormatKarate,
	} {
		r, err := parseString(t, in, Limits{})
		if err != nil || r.Format != format {
			t.Errorf("%q: %v, %+v", in, err, r)
		}
	}
}

func TestJUnitPropertiesAndAttachments(t *testing.T) {
	r := mustParse(t, `<testsuites><testsuite name="s">
<properties><property name="build" value="42"/><property name="branch">main</property></properties>
<testcase name="a" classname="c">
  <properties><property name="owner" value="team-x"/><property name="screenshot" value="shots/a.png"/><property name="long">`+strings.Repeat("v", 3000)+`</property></properties>
  <failure message="boom"/>
  <system-out>log
[[ATTACHMENT|/abs/path/trace.zip]] and [[ATTACHMENT|../../etc/passwd]]
[[ATTACHMENT|`+strings.Repeat("p", 2000)+`]]</system-out>
</testcase></testsuite></testsuites>`, Limits{})
	c := r.Cases[0]
	want := []Property{{"owner", "team-x"}, {"screenshot", "shots/a.png"}, {"long", strings.Repeat("v", MaxPropertyText-3) + "…"}, {"build", "42"}, {"branch", "main"}}
	if fmt.Sprint(c.Properties) != fmt.Sprint(want) {
		t.Errorf("properties = %v", c.Properties)
	}
	var paths []string
	for _, a := range c.Attachments {
		paths = append(paths, a.Path)
	}
	if strings.Join(paths, " ") != "shots/a.png /abs/path/trace.zip ../../etc/passwd" {
		t.Errorf("attachments = %v", paths)
	}
	if !strings.Contains(r.Warning, "longer than 1024 bytes") {
		t.Errorf("warning = %q", r.Warning)
	}
}

func TestCucumberJVMShapes(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	in := `[{"uri":"classpath:f.feature","name":"F","tags":[{"name":"@feat"}],"elements":[
 {"type":"background","keyword":"Background","steps":[{"keyword":"Given ","name":"bg","result":{"status":"passed","duration":2000000}}]},
 {"type":"scenario","keyword":"Scenario","name":"S","line":5,"tags":[{"name":"@s"},{"name":"@feat"}],
  "before":[{"match":{"location":"Hooks.before()"},"result":{"status":"passed","duration":1}}],
  "steps":[
   {"keyword":"When ","name":"doc","result":{"status":"passed","duration":3000000},"doc_string":{"value":"line one\nline two"}},
   {"keyword":"And ","name":"table","result":{"status":"passed"},"rows":[{"cells":["a","b"]},{"cells":["1","2"]}],"output":["printed"]},
   {"keyword":"Then ","name":"fails","result":{"status":"failed","error_message":"java.lang.AssertionError: no\n\tat X.y(X.java:1)"}},
   {"keyword":"And ","name":"after","result":{"status":"skipped"}}],
  "after":[{"match":{"location":"Hooks.screenshot()"},"result":{"status":"passed"},"embeddings":[{"data":"` + png + `","media":{"type":"image/png"},"name":"shot"}]}]},
 {"type":"scenario","keyword":"Scenario","name":"S","line":9,"steps":[{"keyword":"Given ","name":"ambiguous","result":{"status":"ambiguous"}}]}
]}]`
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 2, Failed: 1, Errored: 1})
	c := r.Cases[0]
	if c.Name != "S" || c.Suite != "F" || c.File != "classpath:f.feature" || strings.Join(c.Tags, ",") != "feat,s" || c.DurationMs != 5 {
		t.Errorf("case = %+v", c)
	}
	var names []string
	for _, s := range c.Steps {
		names = append(names, s.Keyword+":"+s.Name)
	}
	// The passing before hook is left out; the after hook with a
	// screenshot is kept; the background comes first.
	if strings.Join(names, "|") != "Given:bg|When:doc|And:table|Then:fails|And:after|After:Hooks.screenshot()" {
		t.Errorf("steps = %v", names)
	}
	if c.Steps[1].Text != "line one\nline two" || c.Steps[2].Text != "| a | b |\n| 1 | 2 |" || c.Output != "printed" {
		t.Errorf("texts %q %q, output %q", c.Steps[1].Text, c.Steps[2].Text, c.Output)
	}
	if c.Message != "java.lang.AssertionError: no" || !strings.Contains(c.Details, "X.java:1") {
		t.Errorf("message %q details %q", c.Message, c.Details)
	}
	if len(c.Attachments) != 1 || c.Attachments[0].ContentType != "image/png" || string(c.Attachments[0].Data) != "\x89PNG fake" || c.Steps[5].Attachments[0] != 0 {
		t.Errorf("attachments = %+v", c.Attachments)
	}
	// A repeated name gets its line; ambiguous is an error.
	c = r.Cases[1]
	if c.Name != "S (line 9)" || c.Status != Errored || c.Message != "step is ambiguous: Given ambiguous" {
		t.Errorf("second = %+v", c)
	}
}

func TestPlaywrightShapes(t *testing.T) {
	in := `{"config":{"projects":[{"name":"webkit"}]},"suites":[{"title":"a.spec.ts","file":"a.spec.ts","specs":[],"suites":[
 {"title":"outer","file":"a.spec.ts","suites":[{"title":"inner","file":"a.spec.ts","specs":[
  {"title":"t1","file":"a.spec.ts","tags":["@slow"],"tests":[{"projectName":"webkit","status":"unexpected","annotations":[{"type":"tag","description":"@extra"}],"results":[
    {"status":"interrupted","duration":-5,"retry":0,"workerIndex":3,"shardIndex":2,"error":{"message":"\u001b[31mTarget closed\u001b[39m","stack":"Error: Target closed\n    at a.spec.ts:3"},
     "stdout":[{"text":"out line\n"},{"buffer":"` + base64.StdEncoding.EncodeToString([]byte("buffered\n")) + `"}],
     "steps":[{"title":"Before Hooks","duration":1},{"title":"page.goto","duration":12,"steps":[{"title":"inner step","duration":3}]},
              {"title":"fixture: page","category":"fixture","duration":1,"error":{"message":"fixture broke"}}],
     "attachments":[{"name":"trace","contentType":"application/zip","path":"/tmp/../etc/trace.zip"},{"name":"big","contentType":"text/plain","body":"` + strings.Repeat("QUFB", 400) + `"}]}]}]}]}]}]}],
 "stats":{"duration":"NaN"}}`
	r := mustParse(t, in, Limits{MaxInlineBytes: 1000})
	wantCounts(t, r, Counts{Total: 1, Errored: 1})
	c := r.Cases[0]
	if c.Suite != "a.spec.ts › outer › inner" || c.Name != "t1" || c.Variant != "webkit" || strings.Join(c.Tags, ",") != "slow,extra" || c.DurationMs != 0 || r.DurationMs != 0 {
		t.Errorf("case = %+v", c)
	}
	if c.Message != "Target closed" || c.Output != "out line\nbuffered" {
		t.Errorf("message %q output %q", c.Message, c.Output)
	}
	var steps []string
	for _, s := range c.Steps {
		steps = append(steps, fmt.Sprintf("%d:%s:%s", s.Depth, s.Name, s.Status))
	}
	if strings.Join(steps, "|") != "0:page.goto:passed|1:inner step:passed|0:fixture: page:failed" {
		t.Errorf("steps = %v", steps)
	}
	if fmt.Sprint(c.Properties) != "[{project webkit} {worker 3} {shard 2}]" {
		t.Errorf("properties = %v", c.Properties)
	}
	if len(c.Attachments) != 2 || c.Attachments[0].Path != "/tmp/../etc/trace.zip" || c.Attachments[1].Data != nil || c.Attachments[1].Size != 1200 || !strings.Contains(c.Attachments[1].Problem, "1200 bytes") {
		t.Errorf("attachments = %+v", c.Attachments)
	}
}

func TestMochawesomeContext(t *testing.T) {
	ctx := func(v string) string { b := fmt.Sprintf("%q", v); return b }
	in := `{"stats":{},"results":[{"title":"","fullFile":"cypress/e2e/a.cy.js","suites":[{"title":"A","tests":[
 {"title":"one","state":"failed","duration":5,"context":` + ctx(`["screens/one.png",{"title":"video","value":"videos/a.mp4"},{"title":"build","value":42},{"title":"note","value":"plain text"}]`) + `,"err":{"message":"boom","estack":"Error: boom\n at x"}},
 {"title":"\"before each\" hook for \"two\"","state":"failed","isHook":true,"err":{"message":"hook broke"}},
 {"title":"three","state":null,"pass":false,"fail":false,"pending":true},
 {"title":"four","state":"passed","context":` + ctx(`not json at all`) + `}],"suites":[{"title":"B","tests":[{"title":"five","state":"passed"}]}]}]}]}`
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 5, Passed: 2, Failed: 1, Errored: 1, Skipped: 1})
	c := r.Cases[0]
	if len(c.Attachments) != 2 || c.Attachments[0].Path != "screens/one.png" || c.Attachments[1].Name != "video" || c.Attachments[1].Path != "videos/a.mp4" {
		t.Errorf("attachments = %+v", c.Attachments)
	}
	if fmt.Sprint(c.Properties) != "[{build 42} {note plain text}]" || c.Details != "Error: boom\n at x" {
		t.Errorf("case = %+v", c)
	}
	if c := findCase(t, r, "five"); c.Suite != "A › B" || c.File != "cypress/e2e/a.cy.js" {
		t.Errorf("nested = %+v", c)
	}
	if c := findCase(t, r, "four"); fmt.Sprint(c.Properties) != "[{context not json at all}]" {
		t.Errorf("plain context = %+v", c)
	}
}

func TestCTRFShapes(t *testing.T) {
	shot := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	in := `{"results":{"tool":{"name":"x"},"summary":{"tests":999},"tests":[
 {"name":"a","status":"other","message":"todo: not written","suite":"s1 > s2","browser":"chromium 121.0","screenshot":"` + shot + `",
  "tags":["@t1","t2"],"parameters":{"n":1},"extra":{"owner":"me","nested":{"x":[1,2]}},"retries":5000,"steps":[{"name":"st","status":"failed"}],
  "attachments":[{"name":"log","contentType":"text/plain","path":"logs/a.txt"}],"stdout":["o1"],"duration":"12.6"},
 {"name":"b","status":"other"},
 {"name":"c","status":"pending","duration":-1}]}}`
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 3, Errored: 1, Skipped: 2})
	c := r.Cases[0]
	if c.Suite != "s1 > s2" || c.Variant != "chromium" || c.Retries != maxRetries || strings.Join(c.Tags, ",") != "t1,t2" || c.DurationMs != 13 || c.Output != "o1" {
		t.Errorf("case = %+v", c)
	}
	if fmt.Sprint(c.Properties) != `[{browser chromium 121.0} {n 1} {nested {"x":[1,2]}} {owner me}]` {
		t.Errorf("properties = %v", c.Properties)
	}
	if len(c.Attachments) != 2 || c.Attachments[0].Path != "logs/a.txt" || string(c.Attachments[1].Data) != "PNGDATA" || len(c.Steps) != 1 || c.Steps[0].Status != Failed {
		t.Errorf("attachments %+v steps %+v", c.Attachments, c.Steps)
	}
}

func TestTestNGShapes(t *testing.T) {
	in := `<testng-results><suite name="S"><test name="T"><class name="pkg.C">
<test-method is-config="true" status="FAIL" name="setUp" duration-ms="3"><exception class="java.lang.IllegalStateException"><message>db down</message></exception></test-method>
<test-method status="FAIL" name="flaky" retried="true" duration-ms="5"><exception class="E"><message>first</message></exception></test-method>
<test-method status="FAIL" name="flaky" retried="true" duration-ms="6"><exception class="E"><message>second</message></exception></test-method>
<test-method status="PASS" name="flaky" duration-ms="NaN"/>
<test-method status="PASS" name="ok" duration-ms="-4"/>
</class></test></suite></testng-results>`
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 3, Passed: 2, Errored: 1})
	c := r.Cases[0]
	if c.Name != "setUp" || c.Status != Errored || c.Message != "configuration method failed: db down" {
		t.Errorf("config = %+v", c)
	}
	c = findCase(t, r, "flaky")
	if c.Retries != 2 || len(c.Attempts) != 2 || c.Attempts[1].Message != "second" || c.Attempts[0].DurationMs != 5 || c.DurationMs != 0 {
		t.Errorf("retried = %+v", c)
	}
}

func TestNUnitAndXUnitShapes(t *testing.T) {
	r := mustParse(t, `<test-run><test-suite type="TestFixture" fullname="N.C" classname="N.C">
<test-case name="E" fullname="N.C.E" result="Failed" label="Error" duration="0.5"><failure><message>boom</message><stack-trace>at x</stack-trace></failure><output>out</output>
<attachments><attachment><filePath>/tmp/shot.png</filePath><description>screen</description></attachment><attachment><filePath>C:\results\a.txt</filePath></attachment></attachments></test-case>
<test-case name="I" fullname="N.C.I" result="Skipped" label="Ignored"><reason><message>not now</message></reason></test-case>
<test-case name="W" fullname="N.C.W" result="Warning"/>
</test-suite></test-run>`, Limits{})
	wantCounts(t, r, Counts{Total: 3, Passed: 1, Errored: 1, Skipped: 1})
	c := r.Cases[0]
	if c.Suite != "N.C" || c.Message != "boom" || c.Output != "out" || len(c.Attachments) != 2 || c.Attachments[0].Name != "screen" || c.Attachments[1].Path != `C:\results\a.txt` || c.Attachments[1].Name != "a.txt" {
		t.Errorf("nunit = %+v", c)
	}
	if c := r.Cases[1]; c.Message != "not now" {
		t.Errorf("ignored = %+v", c)
	}

	r = mustParse(t, `<assemblies><assembly name="/x/A.dll"><errors><error type="fixture-cleanup" name="A.Fix"><failure exception-type="E"><message>cleanup broke</message></failure></error></errors>
<collection><test name="A.T.Skip" type="A.T" method="Skip" result="Skip" time="0"><reason><![CDATA[later]]></reason></test>
<test name="A.T.NR" type="A.T" method="NR" result="NotRun"/></collection></assembly></assemblies>`, Limits{})
	wantCounts(t, r, Counts{Total: 3, Errored: 1, Skipped: 2})
	if c := r.Cases[0]; c.Suite != "A.dll" || c.Name != "[fixture-cleanup] A.Fix" || c.Message != "cleanup broke" {
		t.Errorf("xunit error = %+v", c)
	}
	if c := r.Cases[1]; c.Name != "Skip" || c.Message != "later" {
		t.Errorf("xunit skip = %+v", c)
	}
}

func TestTRXShapes(t *testing.T) {
	in := `<TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010"><TestSettings><Deployment runDeploymentRoot="user_HOST 2026"/></TestSettings>
<Results>
 <UnitTestResult executionId="e1" testId="t1" testName="N.C.Data" outcome="Failed" duration="00:01:02.5000000" relativeResultsDirectory="e1">
  <InnerResults>
   <UnitTestResult executionId="e2" testName="Data (1)" outcome="Passed" duration="00:00:00.0010000"/>
   <UnitTestResult executionId="e3" testName="Data (2)" outcome="Timeout" duration="bad" relativeResultsDirectory="e3">
    <Output><StdOut>so</StdOut><ErrorInfo><Message>too slow</Message></ErrorInfo></Output>
    <ResultFiles><ResultFile path="HOST\shot.png"/><ResultFile path="/abs/x.log"/></ResultFiles></UnitTestResult>
  </InnerResults></UnitTestResult>
 <UnitTestResult executionId="e4" testId="t2" testName="N.C.Abort" outcome="Aborted"/>
 <UnitTestResult executionId="e5" testId="t3" testName="Plain" outcome="NotExecuted"><Output><TextMessages><Message>skipped because</Message></TextMessages></Output></UnitTestResult>
</Results>
<TestDefinitions>
 <UnitTest id="t1"><TestCategory><TestCategoryItem TestCategory="slow"/></TestCategory><Properties><Property><Key>k</Key><Value>v</Value></Property></Properties><TestMethod className="N.C" name="Data"/></UnitTest>
 <UnitTest id="t2"><TestMethod className="N.C" name="Abort"/></UnitTest>
</TestDefinitions></TestRun>`
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 4, Passed: 1, Errored: 2, Skipped: 1})
	c := r.Cases[1]
	if c.Name != "Data (2)" || c.Suite != "N.C" || c.Message != "too slow" || c.Output != "so" || strings.Join(c.Tags, ",") != "slow" || fmt.Sprint(c.Properties) != "[{k v}]" {
		t.Errorf("inner = %+v", c)
	}
	if len(c.Attachments) != 2 || c.Attachments[0].Path != "user_HOST 2026/In/e3/HOST/shot.png" || c.Attachments[1].Path != "/abs/x.log" {
		t.Errorf("result files = %+v", c.Attachments)
	}
	if r.Cases[0].DurationMs != 1 || r.Cases[2].Name != "Abort" || r.Cases[3].Message != "skipped because" || r.Cases[3].Suite != "" {
		t.Errorf("cases = %+v", r.Cases)
	}
}

func TestTAPShapes(t *testing.T) {
	in := "TAP version 14\n1..6\n" +
		"ok 1 - plain # time=12.5ms\n" +
		"not ok 2 - todo failing # TODO later\n" +
		"ok 3 escaped \\# hash\n" +
		"not ok 4 - with diag\n  ---\n  message: \"quoted: \\\"x\\\"\"\n  severity: fail\n  at:\n    file: a.js\n    line: 3\n  duration_ms: 1e400\n  ...\n" +
		"    ok 1 - sub a\n    not ok 2 - sub b\n" +
		"not ok 5 - parent\n" +
		"    ok 1 - only child\n" +
		"not ok 6 - parent failing alone\n" +
		strings.Repeat("x", 2<<20) + "\n" +
		"Bail out! database gone\n" +
		"ok 7 - never read\n"
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 8, Passed: 4, Failed: 2, Errored: 1, Skipped: 1})
	if c := findCase(t, r, "plain"); c.DurationMs != 13 {
		t.Errorf("plain = %+v", c)
	}
	if c := findCase(t, r, "todo failing"); c.Status != Skipped || c.Message != "TODO later" {
		t.Errorf("todo = %+v", c)
	}
	if c := findCase(t, r, "escaped # hash"); c.Status != Passed {
		t.Errorf("escaped = %+v", c)
	}
	if c := findCase(t, r, "with diag"); c.Message != `quoted: "x"` || c.DurationMs != 1e10 || !strings.Contains(c.Details, "file: a.js") {
		t.Errorf("diag = %+v", c)
	}
	if c := findCase(t, r, "sub b"); c.Suite != "parent" || c.Status != Failed {
		t.Errorf("sub = %+v", c)
	}
	if c := findCase(t, r, "parent failing alone"); c.Status != Errored {
		t.Errorf("parent with passing children = %+v", c)
	}
	if !strings.Contains(r.Warning, "bailed out: database gone") || !strings.Contains(r.Warning, "longer than 1 MiB") {
		t.Errorf("warning = %q", r.Warning)
	}
}

func TestRobotShapes(t *testing.T) {
	// Robot Framework 6: starttime/endtime, <tags>, library=, setup type,
	// a screenshot in an HTML message, FOR loops and IF branches.
	in := `<?xml version="1.0" encoding="UTF-8"?>
<robot generator="Robot 6.1.1"><suite name="Top" source="/t"><suite name="Login" source="/t/login.robot">
<test name="Shot" id="s1-s1-t1">
<kw name="Open" library="SeleniumLibrary" type="setup"><status status="PASS" starttime="20261119 10:00:00.000" endtime="20261119 10:00:01.500"/></kw>
<for flavor="IN"><var>${x}</var><value>1</value><value>2</value>
 <iter><var name="${x}">1</var><kw name="Log" library="BuiltIn"><msg level="INFO">x=1</msg><status status="PASS" starttime="20261119 10:00:01.500" endtime="20261119 10:00:01.500"/></kw><status status="PASS" starttime="20261119 10:00:01.500" endtime="20261119 10:00:01.500"/></iter>
 <status status="PASS" starttime="20261119 10:00:01.500" endtime="20261119 10:00:01.600"/></for>
<if><branch type="IF" condition="$x"><kw name="Fail" library="BuiltIn"><msg level="INFO" html="true">&lt;a href="selenium-screenshot-1.png"&gt;&lt;img src="selenium-screenshot-1.png" width="800px"&gt;&lt;/a&gt;</msg><msg level="FAIL">bad thing</msg><status status="FAIL" starttime="20261119 10:00:01.600" endtime="20261119 10:00:01.700">bad thing</status></kw><status status="FAIL" starttime="20261119 10:00:01.600" endtime="20261119 10:00:01.700"/></branch>
<branch type="ELSE"><kw name="No Operation" library="BuiltIn"><status status="NOT RUN" starttime="20261119 10:00:01.700" endtime="20261119 10:00:01.700"/></kw><status status="NOT RUN" starttime="20261119 10:00:01.700" endtime="20261119 10:00:01.700"/></branch>
<status status="FAIL" starttime="20261119 10:00:01.600" endtime="20261119 10:00:01.700"/></if>
<tags><tag>ui</tag><tag>@login</tag></tags>
<status status="FAIL" starttime="20261119 10:00:00.000" endtime="20261119 10:00:02.000">bad thing
second line</status></test>
<test name="NotRun"><status status="NOT RUN" starttime="20261119 10:00:02.000" endtime="20261119 10:00:01.000"/></test>
<status status="FAIL"/></suite><status status="FAIL"/></suite>
<statistics><suite><stat name="x">x</stat></suite></statistics></robot>`
	r := mustParse(t, in, Limits{})
	wantCounts(t, r, Counts{Total: 2, Failed: 1, Skipped: 1})
	c := r.Cases[0]
	if c.Suite != "Top.Login" || c.File != "/t/login.robot" || c.DurationMs != 2000 || strings.Join(c.Tags, ",") != "ui,login" || c.Message != "bad thing" || c.Details != "bad thing\nsecond line" {
		t.Errorf("case = %+v", c)
	}
	var steps []string
	for _, s := range c.Steps {
		steps = append(steps, fmt.Sprintf("%d:%s:%s:%d", s.Depth, s.Name, s.Status, s.DurationMs))
	}
	if strings.Join(steps, "|") != "0:SETUP Open:passed:1500|0:FOR ${x} IN 1 2:passed:100|1:ITERATION ${x}=1:passed:0|2:Log:passed:0|0:IF $x:failed:100|1:Fail:failed:100" {
		t.Errorf("steps = %v", steps)
	}
	s := c.Steps[5]
	if s.Error != "bad thing" || len(s.Attachments) != 1 || c.Attachments[0].Path != "selenium-screenshot-1.png" || s.Keyword != "BuiltIn" {
		t.Errorf("fail step = %+v, attachments %+v", s, c.Attachments)
	}
	if c := r.Cases[1]; c.Status != Skipped || c.DurationMs != 0 {
		t.Errorf("not run = %+v", c)
	}
}

func TestKarateCredentialsAreHidden(t *testing.T) {
	// A token-shaped value, built at run time so no credential-like
	// string sits in the source.
	tok := strings.Join([]string{"ey" + "J0eXAi", "c2VjcmV0", "c2lnbmF0dXJl"}, ".")
	log := "12:00:00.000 request:\n" +
		"1 > POST https://user:" + "pa" + "ss@api.example.com/v1/items?token=" + tok + "&page=2&X-Amz-Signature=abc\n" +
		"1 > Authorization: Bearer " + tok + "\n" +
		"1 > Cookie: session=" + tok + "\n" +
		"1 > X-Api-Key: " + tok + "\n" +
		"1 > X-Custom-Token: " + tok + "\n" +
		"1 > Content-Type: application/json\n" +
		`{"name":"x"}` + "\n\n" +
		"12:00:00.100 response time in milliseconds: 100\n" +
		"1 < 201\n1 < Set-Cookie: id=" + tok + "\n1 < Location: /v1/items/7\n" +
		`{"id":7}` + "\n" +
		"2 > GET https://api.example.com/second\n2 < 200\n"
	enc := func(s string) string { b, _ := jsonMarshalString(s); return b }
	in := `{"name":"F","relativePath":"f.feature","scenarioResults":[{"name":"s","stepResults":[{"step":{"prefix":"When","text":"method post"},"result":{"status":"passed","nanos":1},"stepLog":` + enc(log) + `}]}]}`
	r := mustParse(t, in, Limits{})
	c := r.Cases[0]
	s := c.Steps[0]
	h := s.HTTP
	if h == nil {
		t.Fatal("no exchange")
	}
	if h.URL != "https://user:[hidden]@api.example.com/v1/items?token=[hidden]&page=2&X-Amz-Signature=[hidden]" || h.Status != 201 || h.DurationMs != 100 {
		t.Errorf("exchange = %+v", h)
	}
	want := map[string]string{"Authorization": hidden, "Cookie": hidden, "X-Api-Key": hidden, "X-Custom-Token": hidden, "Content-Type": "application/json", "Set-Cookie": hidden, "Location": "/v1/items/7"}
	for _, hd := range append(h.RequestHeaders, h.ResponseHeaders...) {
		if w, ok := want[hd.Name]; !ok || hd.Value != w {
			t.Errorf("header %s = %q", hd.Name, hd.Value)
		}
	}
	if h.RequestBody != `{"name":"x"}` || h.ResponseBody != `{"id":7}` {
		t.Errorf("bodies %q %q", h.RequestBody, h.ResponseBody)
	}
	dump := fmt.Sprintf("%+v", r)
	if strings.Contains(dump, tok) || strings.Contains(dump, "pa"+"ss@") {
		t.Errorf("a credential leaked: %s", s.Text)
	}
	if !strings.Contains(s.Text, "2 > GET https://api.example.com/second") {
		t.Errorf("text = %q", s.Text)
	}
}

func jsonMarshalString(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

func TestHideCredentials(t *testing.T) {
	for _, tc := range [][3]string{
		{"Authorization", "Basic x", hidden},
		{"proxy-authorization", "x", hidden},
		{"X-API-KEY", "x", hidden},
		{"x-auth-token", "x", hidden},
		{"X-Vault-Token", "x", hidden},
		{"Client-Secret", "x", hidden},
		{"X-Password", "x", hidden},
		{"Api-Key", "x", hidden},
		{"Accept", "text/html", "text/html"},
		{"Authorization", "", ""},
	} {
		if got := HideCredentials(tc[0], tc[1]); got != tc[2] {
			t.Errorf("%s: %q", tc[0], got)
		}
	}
	for in, want := range map[string]string{
		"https://a.example/x?q=1":                       "https://a.example/x?q=1",
		"https://tok@github.com/o/r.git":                "https://[hidden]@github.com/o/r.git",
		"http://u:p@h:8080/p?key=1&sig=2&monkey=3#frag": "http://u:[hidden]@h:8080/p?key=[hidden]&sig=[hidden]&monkey=3#frag",
		"/rel?access_token=a&api_key=b&client_secret=c": "/rel?access_token=[hidden]&api_key=[hidden]&client_secret=[hidden]",
		"https://h/p?password=&x":                       "https://h/p?password=[hidden]&x",
		"https://h/a@b/c":                               "https://h/a@b/c",
	} {
		if got := HideURLCredentials(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
