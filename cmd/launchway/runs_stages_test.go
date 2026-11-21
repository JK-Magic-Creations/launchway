package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runs show names the stages, and the steps of a stage that run together.
func TestCLIShowsStagesWithParallelSteps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"run_1","project":"app","number":4,"status":"running","event":"push","branch":"main","sha":"0123456789abcdef","actor":"ann","created_at":1,
			"levels":[["check-code"],["test","security-go","security-web"],["build"]],
			"step_list":[{"id":"check-code","status":"succeeded"},{"id":"test","status":"running"},{"id":"security-go","status":"running"},
			{"id":"security-web","status":"succeeded"},{"id":"build","status":"pending"}]}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out)
	if err := cl.show([]string{"run_1"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"stage 1: check-code (succeeded)",
		"stage 2 (runs together): test (running), security-go (running), security-web (succeeded)",
		"stage 3: build\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
