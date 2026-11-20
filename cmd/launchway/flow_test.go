package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// runs show says a step that failed and was allowed to is failed (allowed).
func TestCLIShowsAnAllowedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"run_1","project":"app","number":4,"status":"succeeded","event":"manual","branch":"main","sha":"0123456789abcdef","actor":"ann","created_at":1,
			"step_list":[{"id":"lint","status":"failed","attempt":1,"error":"exit status 1","allowed_failure":true},{"id":"build","status":"succeeded","attempt":1}]}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out)
	if err := cl.show([]string{"run_1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "failed (allowed) lint") || !strings.Contains(out.String(), "succeeded  build") {
		t.Fatalf("%s", out.String())
	}
}

// runs show says when a retry will be tried and why a run was canceled.
func TestCLIShowsRetryWaitsAndCancelReasons(t *testing.T) {
	due := time.Now().Add(20 * time.Second).UnixMilli()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":"run_1","project":"app","number":4,"status":"running","event":"manual","branch":"main","sha":"0123456789abcdef","actor":"ann","created_at":1,
			"cancel_reason":"run timeout of 2h reached","deadline_at":%d,
			"step_list":[{"id":"flaky","status":"queued","attempt":2,"retry":3,"not_before":%d,"failure_cause":"timeout"}]}`, due, due)
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out).show([]string{"run_1"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"canceled: run timeout of 2h reached", "times out at", "retrying in", "(attempt 2 of 4)", "[cause: timeout]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}
