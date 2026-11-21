package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func approvalServer(t *testing.T, status int, reply string, got *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = append(*got, r.Method+" "+r.URL.Path+" "+string(b))
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCLIApprovalsList(t *testing.T) {
	var got []string
	srv := approvalServer(t, 200, `[{"run_id":"run_1","project":"app","run_number":4,"step_id":"approve","message":"Deploy?","started_by":"ann","waiting_since":1,"deadline":0,
		"approvals_needed":2,"approvals":[{"actor":"bob"}],"approvals_needed_from":"the min_approvals of the environment prod"},
		{"run_id":"run_2","project":"other","run_number":1,"step_id":"ok","message":"Go?","approvals_needed":1,"approvals":[]}]`, &got)
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out)
	if err := cl.approvals([]string{"list", "--project", "app"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app #4  approve  run run_1", "Deploy?", "started by ann", "1 of 2 approved (bob)", "min_approvals of the environment prod"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "other") {
		t.Fatal("the project filter")
	}
	out.Reset()
	if err := cl.approvals([]string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal(out.Bytes(), &list); err != nil || len(list) != 2 {
		t.Fatalf("%v %s", err, out.String())
	}
	if got[0] != "GET /api/v1/approvals " {
		t.Fatalf("%v", got)
	}
}

func TestCLIApprovalsDecide(t *testing.T) {
	var got []string
	srv := approvalServer(t, 200, `{"ok":true,"approvals":[{"actor":"ann"}],"approvals_needed":2,"done":false}`, &got)
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out)
	if err := cl.approvals([]string{"approve", "run_1", "approve prod", "--comment", "fine"}); err != nil {
		t.Fatal(err)
	}
	if got[0] != `POST /api/v1/runs/run_1/steps/approve prod/approve {"comment":"fine"}` || !strings.Contains(out.String(), "1 of 2 approved") {
		t.Fatalf("%v %s", got, out.String())
	}
	out.Reset()
	if err := cl.approvals([]string{"reject", "run_1", "s"}); err != nil || !strings.Contains(out.String(), "Rejected s of run run_1") {
		t.Fatalf("%v %s", err, out.String())
	}
	if err := cl.approvals([]string{"approve", "run_1"}); err == nil {
		t.Fatal("a step is needed")
	}
	if err := cl.approvals([]string{"bogus"}); err == nil {
		t.Fatal("unknown command")
	}
}

// The server's refusals are printed as they are, with a non-zero exit.
func TestCLIApprovalsRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"self":        {403, `{"error":"ann started this run, so someone else must approve it (you may still reject it)"}`},
		"expired":     {409, `{"error":"approve is no longer waiting: nobody decided within 24h"}`},
		"not waiting": {409, `{"error":"approve is not waiting for a decision (it is succeeded)"}`},
		"signed out":  {401, `{"error":"sign in"}`},
	} {
		var got []string
		srv := approvalServer(t, tc.status, tc.body, &got)
		var out bytes.Buffer
		err := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out).approvals([]string{"approve", "run_1", "approve"})
		var ce cliErr
		if err == nil || !asCliErr(err, &ce) || ce.code != exitAPI {
			t.Fatalf("%s: %v", name, err)
		}
		want := strings.Trim(strings.SplitN(strings.SplitN(tc.body, `":"`, 2)[1], `"`, 2)[0], `"`)
		if !strings.Contains(ce.msg, want) {
			t.Errorf("%s: %q does not say %q", name, ce.msg, want)
		}
	}
}

func asCliErr(err error, ce *cliErr) bool {
	c, ok := err.(cliErr)
	*ce = c
	return ok
}
