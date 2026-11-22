package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pr checks prints each pipeline's sentence, tests, coverage, findings and policy, and the places in files.
func TestCLIPullChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repositories/acme/shop/pulls/4/checks" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"head":"abc","target_branch":"main","policy_verdict":"warn","checks":[
		 {"number":7,"project":"shop","pipeline":"app","sentence":"Tests passed; waiting for review.","detail":true,
		  "tests":{"totals":{"Total":10,"Passed":8,"Failed":1,"Skipped":1},"new_failures":["demo.Suite t1"],"new_failure_count":1,"fixed":0,"compared":true},
		  "coverage":{"lines_percent":81.5,"change":-1.2},
		  "findings":{"counts":{"Total":3,"High":1,"Low":2},"new_total":1},"policy_verdict":"warn"}],
		 "deployments":[{"environment":"preview","version":"v1","phase":"live"}],
		 "annotations":[{"Kind":"finding","File":"a.go","Line":12,"Severity":"high","Message":"bad call"}]}`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "t"}, &out)
	if err := cl.pr("checks", []string{"4", "-R", "acme/shop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app #7 (shop): Tests passed", "8 passed, 1 failed, 1 skipped of 10", "1 newly failing, 0 fixed against main",
		"newly failing: demo.Suite t1", "coverage: 81.5% (-1.2 points)", "1 new in this change", "policy verdict: warn", "a.go:12", "launched to preview"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
