package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `runs tests` says when a report was cut at the Station's size limit, with the number of tests in the part that
// was kept, and shows no error for the tests that were in the part that was not.
func TestRunsTestsSaysAReportWasCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/tests/cases"):
			_, _ = w.Write([]byte(`{"cases":[],"matching":0}`))
		default:
			_, _ = w.Write([]byte(`{"totals":{"total":5,"passed":5,"failed":0,"errored":0,"skipped":0,"flaky":0,"duration_ms":50,"truncated":true},
"steps":[{"step":"test","attempt":1,"files":[{"path":"reports/go.json","cases":5,"warning":"the report was larger than the limit; 5 tests in the part that was kept (the first 20 MiB of its 80 MiB)"}]}],"declared":["test"],"flaky":0,"new_failures":0}`))
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out)
	if err := cl.tests([]string{"run_1"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "5 tests: 5 passed, 0 failed, 0 errors") || !strings.Contains(got, "test: reports/go.json: the report was larger than the limit; 5 tests in the part that was kept") {
		t.Fatalf("output:\n%s", got)
	}
}
