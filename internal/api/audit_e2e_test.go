package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/fakeidp"
)

func TestAuditTrail(t *testing.T) {
	e := setupAuth(t, envOpts{})
	var secrets []string // every session id and token handed out

	otto := e.signIn(t, "otto")
	secrets = append(secrets, otto.cookieValue(e.srv.URL, sessionCookie))
	ada := e.signIn(t, "ada")
	secrets = append(secrets, ada.cookieValue(e.srv.URL, sessionCookie))

	// Start, cancel, resume, approve, reject.
	r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`)
	var run engine.RunSummary
	r.json(t, &run)
	runURL := e.srv.URL + "/api/v1/runs/" + run.ID
	for _, c := range []struct {
		b    *browser
		path string
		want int
	}{
		{otto, "/cancel", 200},
		{otto, "/resume", 200},
		{otto, "/steps/gate/approve", 403}, // otto started it
		{ada, "/steps/gate/reject", 200},
		{ada, "/steps/gate/approve", 409}, // no longer waiting
	} {
		if r := c.b.do(http.MethodPost, runURL+c.path, `{"comment":"why not"}`); r.status != c.want {
			t.Fatalf("%s: %d %s", c.path, r.status, r.body)
		}
	}
	// A viewer is refused.
	vic := e.signIn(t, "vic")
	secrets = append(secrets, vic.cookieValue(e.srv.URL, sessionCookie))
	vic.do(http.MethodPost, runURL+"/cancel", "")

	// Tokens.
	id, tok := ada.newToken(e.srv.URL, "nightly")
	secrets = append(secrets, tok)
	newBrowser(t).do(http.MethodPost, e.srv.URL+"/api/v1/projects/web/runs", `{"ref":"main"}`, bearer(tok)...)
	ada.do(http.MethodDelete, e.srv.URL+"/api/v1/tokens/"+id, "")

	// A failed sign-in and a sign-out.
	e.idp.Fail(fakeidp.Expired)
	newBrowser(t).login(e.srv.URL, "/")
	e.idp.Fail(fakeidp.None)
	otto.do(http.MethodPost, e.srv.URL+"/auth/logout", "")

	// Reads leave nothing.
	otto2 := e.signIn(t, "otto")
	secrets = append(secrets, otto2.cookieValue(e.srv.URL, sessionCookie))
	otto2.get(runURL)
	otto2.get(e.srv.URL + "/api/v1/runs")

	var got []string
	for _, a := range e.auditEntries(t) {
		if a.At == 0 || a.Source != "127.0.0.1" {
			t.Errorf("entry without a time or source: %+v", a)
		}
		got = append(got, fmt.Sprintf("%s %s %s %s %s", a.User, a.Action, a.Project, a.Target, a.Outcome))
	}
	webRun := ""
	if runs, _ := e.eng.ListRuns(engine.RunFilter{Project: "web"}); len(runs) == 1 {
		webRun = runs[0].ID
	}
	want := []string{
		"otto auth.sign_in   ok",
		"ada auth.sign_in   ok",
		"otto run.start pay-api " + run.ID + " ok",
		"otto run.cancel pay-api " + run.ID + " ok",
		"otto run.resume pay-api " + run.ID + " ok",
		"otto approval.approve pay-api " + run.ID + "/gate denied", // the engine refused: otto started it
		"ada approval.reject pay-api " + run.ID + "/gate ok",
		"ada approval.approve pay-api " + run.ID + "/gate failed",
		"vic auth.sign_in   ok",
		"vic run.cancel pay-api " + run.ID + " denied",
		"ada token.create  nightly ok",
		"ada run.start web " + webRun + " ok",
		"ada token.revoke  nightly ok",
		" auth.sign_in   failed",
		"otto auth.sign_out   ok",
		"otto auth.sign_in   ok",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit trail:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Details say what happened, in a few words.
	for _, a := range e.auditEntries(t) {
		switch {
		case a.Action == "run.start" && a.Project == "pay-api":
			if !strings.HasPrefix(a.Detail, "run #1 of main at ") {
				t.Errorf("start detail %q", a.Detail)
			}
		case a.Action == "approval.approve" && a.User == "otto":
			if !strings.Contains(a.Detail, "someone else must approve it") {
				t.Errorf("refused approval detail %q", a.Detail)
			}
		case a.Action == "auth.sign_in" && a.Outcome == "failed":
			if !strings.Contains(a.Detail, "expired") {
				t.Errorf("failed sign-in detail %q", a.Detail)
			}
		}
	}

	// No session id or token appears anywhere in the database, the audit
	// trail included.
	dump := dumpDatabase(t, e.db)
	for _, s := range secrets {
		if s == "" || strings.Contains(dump, s) {
			t.Fatalf("a session id or token (%d chars) is in the database", len(s))
		}
	}

	// Admins can read it over the API, filtered and a page at a time.
	read := func(q string) (entries []engine.AuditEntry, next int64) {
		r := ada.get(e.srv.URL + "/api/v1/audit?" + q)
		if r.status != http.StatusOK {
			t.Fatalf("audit?%s: %d %s", q, r.status, r.body)
		}
		var out struct {
			Entries []engine.AuditEntry `json:"entries"`
			Next    int64               `json:"next_before"`
		}
		r.json(t, &out)
		return out.Entries, out.Next
	}
	if list, _ := read("user=vic"); len(list) != 2 || list[0].Action != "run.cancel" {
		t.Errorf("user=vic: %+v", list)
	}
	if list, _ := read("project=web"); len(list) != 1 || list[0].Target != webRun {
		t.Errorf("project=web: %+v", list)
	}
	if list, _ := read("action=auth.sign_in"); len(list) != 5 {
		t.Errorf("action=auth.sign_in: %d entries", len(list))
	}
	all := e.auditEntries(t)
	mid := all[5].At
	if list, _ := read(url.Values{"from": {fmt.Sprint(mid)}, "to": {fmt.Sprint(mid + 1)}}.Encode()); len(list) == 0 {
		t.Errorf("a time range found nothing")
	}
	var paged []engine.AuditEntry
	q := "limit=3"
	for pages := 0; ; pages++ {
		list, next := read(q)
		paged = append(paged, list...)
		if next == 0 || pages > 10 {
			break
		}
		q = fmt.Sprintf("limit=3&before=%d", next)
	}
	if len(paged) != len(all) || paged[0].ID != all[len(all)-1].ID || paged[len(paged)-1].ID != all[0].ID {
		t.Errorf("paging gave %d entries, want %d, newest first", len(paged), len(all))
	}
}
