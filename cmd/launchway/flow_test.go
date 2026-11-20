package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
