package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// validate --project has the server check the file for that project too,
// and prints its warnings and problems.
func TestCLIValidateAgainstAProject(t *testing.T) {
	var gotPath, gotBody string
	reply := `{"ok":true,"warnings":["the pipeline filters pull requests by the branch they target (on.pull_request.into), and project app has no forge connection"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.String(), string(b)
		fmt.Fprint(w, reply)
	}))
	defer srv.Close()
	t.Setenv("LAUNCHWAY_SERVER", srv.URL)
	t.Setenv("LAUNCHWAY_TOKEN", "lw_x")
	file := filepath.Join(t.TempDir(), "pipeline.yaml")
	text := "pipeline: demo\non: { pull_request: { into: [main] } }\nsteps:\n  a: {image: x, run: a}\n"
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdValidate([]string{file, "--project", "app"}); err != nil {
		t.Fatalf("%v", err)
	}
	if gotPath != "/api/v1/validate?project=app" || gotBody != text {
		t.Fatalf("%s %q", gotPath, gotBody)
	}
	// into is accepted by the local check alone, with no server involved.
	gotPath = ""
	if err := cmdValidate([]string{file}); err != nil || gotPath != "" {
		t.Fatalf("%v %q", err, gotPath)
	}
	reply = `{"ok":false,"problems":[{"line":2,"path":"timeout","message":"too long"}]}`
	if err := cmdValidate([]string{file, "--project", "app"}); err != exitErr(1) {
		t.Fatalf("%v", err)
	}
}

func TestCLIShowsThePullRequestTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"run_1","project":"app","number":4,"status":"succeeded","event":"pull_request","pull_request":12,"pull_request_into":"main","sha":"0123456789abcdef","actor":"ann","created_at":1,"step_list":[]}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out).show([]string{"run_1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pull request #12 into main") {
		t.Fatalf("%s", out.String())
	}
}

// validate warns, at the line of the test, about an output nothing writes.
func TestCLIValidateWarnsAboutAnOutputNothingWrites(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pipeline.yaml")
	text := "pipeline: demo\nsteps:\n  scan: {image: x, run: 'echo changed=1 >> $LAUNCHWAY_OUTPUTS'}\n  fix: {image: x, run: f, needs: scan, when: {output: scan.chnaged, equals: '1'}}\n"
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	err := cmdValidate([]string{file})
	w.Close()
	os.Stderr = old
	got, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := file + ":4: warning: steps.fix.when.output: "; !strings.Contains(string(got), want) || !strings.Contains(string(got), `"chnaged" of step scan`) {
		t.Fatalf("%q", got)
	}
}

// A run that went on past tolerated failures says so in runs list and runs
// show, naming the steps.
func TestCLIShowsAllowedFailureCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		one := `{"id":"run_1","project":"app","number":4,"status":"succeeded","event":"manual","branch":"main","sha":"0123456789abcdef","actor":"ann","created_at":1,
			"allowed_failures":2,"allowed_failure_steps":["lint","scan"],"step_list":[]}`
		if r.URL.Path == "/api/v1/runs" {
			fmt.Fprint(w, "["+one+`,{"id":"run_2","project":"app","number":5,"status":"succeeded","event":"manual","branch":"main","created_at":1,"allowed_failures":0}]`)
			return
		}
		fmt.Fprint(w, one)
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "lw_x"}, &out)
	if err := cl.show([]string{"run_1"}); err != nil || !strings.Contains(out.String(), "  2 allowed failures: lint, scan\n") {
		t.Fatalf("%v %s", err, out.String())
	}
	out.Reset()
	if err := cl.list(nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "(2 allowed failures: lint, scan)") || strings.Contains(lines[1], "allowed") {
		t.Fatalf("%q", lines)
	}
}

func TestCLIShowsThePullRequestAsTheHostDescribedIt(t *testing.T) {
	var r runView
	r.PullRequest = 12
	if pullRequestLine(&r) != "" {
		t.Fatal("a pull request of a hosted repository has no host line")
	}
	r.TriggerDetail = &struct {
		Inputs            map[string]string `json:"inputs"`
		Schedule          string            `json:"schedule"`
		Webhook           string            `json:"webhook"`
		PullRequestTitle  string            `json:"pull_request_title"`
		PullRequestAuthor string            `json:"pull_request_author"`
		PullRequestURL    string            `json:"pull_request_url"`
		PullRequestDraft  bool              `json:"pull_request_draft"`
		PullRequestFork   bool              `json:"pull_request_fork"`
	}{PullRequestTitle: "Fix the retry", PullRequestAuthor: "dana", PullRequestURL: "https://git.example.com/acme/app/pull/12", PullRequestFork: true}
	want := "Pull request #12: Fix the retry, by dana (from a fork: restricted)\n  https://git.example.com/acme/app/pull/12"
	if got := pullRequestLine(&r); got != want {
		t.Fatalf("got %q", got)
	}
}
