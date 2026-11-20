package testreport

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reportFile says whether a file under testdata is a report (and not a
// screenshot or trace the tools wrote next to it).
func reportFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".xml", ".json", ".jsonl", ".ndjson", ".txt", ".tap", ".trx":
		return true
	}
	return false
}

func FuzzParse(f *testing.F) {
	filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !reportFile(path) {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && len(b) < 1<<20 {
			f.Add(b)
		}
		return nil
	})
	for _, gen := range generators {
		f.Add([]byte(gen(3)))
	}
	for _, s := range []string{
		"",
		"<testsuite/>",
		"<testsuite><testcase name=\"a\x00\"><failure>\x1b[31mx</failure><system-out>&#27;[0m</system-out></testcase>",
		"<!DOCTYPE x [<!ENTITY a \"&b;\">]><testsuites><testsuite><testsuite><testcase/>",
		`{"Action":"run","Package":"p","Test":"T"}` + "\n" + `{"Action":"output","Package":"p","Test":"T","Output":"panic: x\n"}`,
		"go: x\n{\"Action\":\"fail\",\"Package\":\"p\"}\n",
		"<?xml version='1.0' encoding='latin1'?><testsuite name='\xff'><testcase name='\x80'/></testsuite>",
		`<testsuite><testcase name="a"><properties><property name="screenshot" value="../a.png"/></properties><system-out>[[ATTACHMENT|/x]]</system-out><flakyFailure message="m"/></testcase></testsuite>`,
		`[{"name":"F","elements":[{"type":"background","steps":[{"result":{"status":"failed"}}]},{"keyword":"Scenario Outline","name":"o","line":3,"steps":[{"result":{"status":"undefined"},"embeddings":[{"mime_type":"text/plain","data":"1 > GET http://u:p@h/?token=x\n1 > Cookie: a\n1 < 200"}]}]}]}]`,
		`{"name":"F","scenarioResults":[{"name":"s","exampleIndex":0,"line":4,"stepResults":[{"step":{"text":"x"},"result":{"status":"failed"},"stepLog":"1 > GET /x\n1 > Authorization: y\nbody\n1 < 500 GET /x\n1 < Set-Cookie: z\n\nGET /x\n500 (3 ms)","callResults":[{"scenarioResults":[{"stepResults":[{"step":{"text":"n"},"result":{"status":"passed"}}]}]}]}]}]}`,
		`{"config":{},"suites":[{"title":"f","suites":[{"title":"d","specs":[{"title":"t","tests":[{"status":"flaky","results":[{"status":"failed","attachments":[{"path":"a"}]},{"status":"passed","steps":[{"title":"s","steps":[{"title":"n"}]}]}]}]}]}]}]}`,
		`{"stats":{},"results":[{"suites":[{"title":"s","tests":[{"title":"t","state":"failed","context":"[\"a.png\",{\"title\":\"x\",\"value\":{}}]"}]}]}]}`,
		`{"results":{"tool":{},"tests":[{"name":"t","status":"other","retryAttempts":[{"status":"failed"}],"screenshot":"AAAA","extra":{"a":[1]}}]}}`,
		`<testng-results><suite><groups><group name="g"><method name="m" class="C"/></group></groups><test><class name="C"><test-method name="m" status="FAIL" retried="true"/><test-method name="m" status="PASS"><params><param><value>1</value></param></params></test-method></class></test></suite></testng-results>`,
		`<test-run><test-suite classname="C" fullname="N.C"><test-case name="t" result="Failed" label="Error"><properties><property name="Category" value="c"/></properties><attachments><attachment><filePath>a</filePath></attachment></attachments></test-case></test-suite></test-run>`,
		`<assemblies><assembly><errors><error type="x"/></errors><test name="C.t" type="C" result="Fail"><traits><trait name="Category" value="c"/></traits></test></assembly></assemblies>`,
		`<TestRun><TestSettings><Deployment runDeploymentRoot="r"/></TestSettings><Results><UnitTestResult testId="1" testName="C.t" outcome="Error" duration="1:2:3"><InnerResults><UnitTestResult testName="i" outcome="Failed"><ResultFiles><ResultFile path="a\b"/></ResultFiles></UnitTestResult></InnerResults></UnitTestResult></Results><TestDefinitions><UnitTest id="1"><TestMethod className="C"/></UnitTest></TestDefinitions></TestRun>`,
		"TAP version 14\n1..3\nok 1 - a # SKIP\n    not ok 1 - c\n      ---\n      message: |\n        x\n      ...\nnot ok 2 - b # TODO\n# note\nBail out!\n",
		`<robot><suite name="S" source="/s"><test name="t"><for flavor="IN"><iter><kw name="k"><msg level="FAIL" html="true">&lt;img src="a.png"&gt;</msg><status status="FAIL" starttime="20260101 00:00:00.000" endtime="20260101 00:00:01.000"/></kw><status status="FAIL"/></iter><status status="FAIL"/></for><tags><tag>@x</tag></tags><status status="FAIL">m</status></test></suite></robot>`,
		`{"type":"FEATURE_EXIT","data":{"name":"F","scenarioResults":[{"name":"s","failed":true}]}}` + "\n" + `{"type":"SUITE_EXIT","data":{}}`,
	} {
		f.Add([]byte(s))
	}
	lim := Limits{MaxCases: 20, MaxSteps: 10, MaxAttachments: 3, MaxInlineBytes: 64, MaxInlineTotal: 128}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Parse(context.Background(), data, lim)
		if err != nil {
			if errors.Is(err, ErrNotReport) && !strings.HasPrefix(err.Error(), "not a test report: ") {
				t.Errorf("error = %q", err)
			}
			checkString(t, "error", err.Error(), 1000)
			return
		}
		checkReport(t, r, lim)
	})
}
