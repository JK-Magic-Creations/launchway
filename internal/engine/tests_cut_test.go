package engine

import (
	"fmt"
	"strings"
	"testing"
)

// A report the runner had to cut at its size limit says so in plain words with the number of tests in the part that
// was kept, and a test that began in the part that was not kept is missing: never errored.
func TestACutReportSaysSoAndMarksNothingAsErrored(t *testing.T) {
	e, _ := newEngine(t)
	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, `{"Action":"run","Package":"ex/p","Test":"TestA%d"}`+"\n"+`{"Action":"pass","Package":"ex/p","Test":"TestA%d","Elapsed":0.01}`+"\n", i, i)
	}
	b.WriteString(`{"Action":"run","Package":"ex/p","Test":"TestInTheCutOffPart"}` + "\n")
	out := e.parseTests(&CollectedReports{Files: []ReportFile{{Path: "reports/go.json", Data: []byte(b.String()), Size: 80 << 20, Cut: true}}})
	s := out.summary
	if s.Total != 5 || s.Errored != 0 || s.Failed != 0 || !s.Truncated {
		t.Fatalf("totals: %+v", s.TestTotals)
	}
	const want = "the report was larger than the limit; 5 tests in the part that was kept"
	if len(s.Files) != 1 || !strings.Contains(s.Files[0].Warning, want) {
		t.Fatalf("the file's warning: %+v", s.Files)
	}
	found := false
	for _, n := range s.Notes {
		found = found || strings.Contains(n, want)
	}
	if !found {
		t.Fatalf("notes: %v", s.Notes)
	}
	for _, c := range out.cases {
		if c.Name == "TestInTheCutOffPart" || c.Status == "errored" {
			t.Errorf("a missing test was stored: %+v", c.Case)
		}
	}
}
