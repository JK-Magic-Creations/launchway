package engine

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A report of 50,000 test cases (a run keeps that many): how long it takes
// to ingest, to build the Debrief and to list the flaky tests, and how much
// memory is in use after. Run with LAUNCHWAY_PERF=1; the numbers are logged.
// On PostgreSQL too when LAUNCHWAY_TEST_POSTGRES is set.
func TestDebriefWith50000Cases(t *testing.T) {
	if os.Getenv("LAUNCHWAY_PERF") == "" {
		t.Skip("set LAUNCHWAY_PERF=1 to measure")
	}
	e, _ := newEngine(t)
	var b strings.Builder
	b.WriteString("<testsuites>")
	for s := 0; s < 500; s++ {
		fmt.Fprintf(&b, `<testsuite name="pkg/s%03d">`, s)
		for i := 0; i < 100; i++ {
			fmt.Fprintf(&b, `<testcase classname="pkg/s%03d" name="Test%03d" time="0.0%02d">`, s, i, i%97)
			if (s*100+i)%2500 == 7 {
				b.WriteString(`<failure message="expected:&lt;1&gt; but was:&lt;2&gt;">at x_test.go:3</failure>`)
			}
			b.WriteString("</testcase>")
		}
		b.WriteString("</testsuite>")
	}
	b.WriteString("</testsuites>")
	data := ReportFile{Path: "reports/r.xml", Data: []byte(b.String()), Size: int64(b.Len())}
	id := create(t, e, debriefOne, Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: "s1"})
	j := take(t, e)
	t0 := time.Now()
	finishTests(t, e, j, "failed", data)
	ingest := time.Since(t0)
	t0 = time.Now()
	r, err := e.Debrief(id, DebriefOptions{DefaultRef: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	page := time.Since(t0)
	t0 = time.Now()
	if _, err := e.ProjectFlaky("demo", nil, 0); err != nil {
		t.Fatal(err)
	}
	flaky := time.Since(t0)
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	t.Logf("50k cases: ingest %v, Debrief %v (%d failures, %d suites), flaky list %v, heap in use %d MiB", ingest, page, len(r.Failures), len(r.Suites), flaky, m.HeapInuse>>20)
}
