package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/archive"
)

func TestArchiveThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic, otto := e.signIn(t, "ada"), e.signIn(t, "vic"), e.signIn(t, "otto")
	repos := e.srv.URL + "/api/v1/repositories"
	ada.do("POST", repos, `{"owner":"acme","name":"shop","visibility":"private"}`)
	ada.do("POST", repos, `{"owner":"other","name":"secret","visibility":"private"}`)
	base := repos + "/acme/shop/archive/issues"
	doc := `{"format":"launchway-archive/1","source":{"name":"old"},"issues":[
		{"number":2,"title":"Crash","state":"closed","author":"bob","body":"<script>alert(1)</script> it crashes","labels":["bug"],"comments":[{"author":"ada","body":"fixed by the Zebra patch"}]},
		{"number":1,"title":"Receipt","state":"open","author":"ada","body":"please"}]}`

	// Importing is for the repository's administrators.
	if r := otto.do("POST", base, doc); r.status != 403 {
		t.Errorf("an operator imported: %d %s", r.status, r.body)
	}
	if r := vic.do("POST", base, doc); r.status != 403 {
		t.Errorf("a viewer imported: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", base, `{"format":"launchway-archive/1","issues":[{"number":1,"title":"x","state":"nope"}]}`); r.status != 400 || !strings.Contains(string(r.body), `"code"`) || !strings.Contains(string(r.body), `"next"`) {
		t.Errorf("a bad document: %d %s", r.status, r.body)
	}
	var res archive.Result
	r := ada.do("POST", base, doc)
	r.json(t, &res)
	if r.status != 200 || res.Issues != 2 || res.Comments != 1 || res.Total != 2 {
		t.Fatalf("import: %d %s", r.status, r.body)
	}

	// Readers read, search, page; nothing else about it changes.
	var pg archive.Page
	vic.get(base+"?q=zebra").json(t, &pg)
	if pg.Total != 1 || pg.Items[0].Number != 2 {
		t.Fatalf("search: %+v", pg)
	}
	vic.get(base+"?state=open&per_page=1").json(t, &pg)
	if pg.Total != 1 || pg.Items[0].Number != 1 || pg.Open != 1 || pg.Closed != 1 {
		t.Fatalf("filter: %+v", pg)
	}
	var is archive.Issue
	vic.get(base+"/2").json(t, &is)
	if is.Title != "Crash" || len(is.Comments) != 1 || !strings.Contains(is.Body, "<script>") {
		t.Fatalf("detail: %+v", is) // stored as text; the page shows it as text
	}
	if r := vic.get(base + "/99"); r.status != 404 {
		t.Errorf("missing issue: %d", r.status)
	}
	if r := vic.get(base + "/abc"); r.status != 400 {
		t.Errorf("bad number: %d", r.status)
	}
	if r := vic.get(base + "?state=weird"); r.status != 400 {
		t.Errorf("bad state: %d", r.status)
	}
	// A repository one may not read has no archive, and answers as one that does not exist.
	if r := vic.get(repos + "/other/secret/archive/issues"); r.status != 404 {
		t.Errorf("hidden repository: %d", r.status)
	}
	// Nothing creates, edits or deletes an issue here.
	for _, m := range []string{"PUT", "PATCH", "DELETE"} {
		if r := ada.do(m, base+"/2", `{"title":"x"}`); r.status != 404 && r.status != 405 {
			t.Errorf("%s answers %d", m, r.status)
		}
	}
	log, err := e.eng.AuditLog(engineAuditFilter())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range log {
		found = found || (a.Action == "archive.import" && a.Outcome == "ok" && a.Target == "acme/shop" && strings.Contains(a.Detail, "2 issues, 1 comments"))
	}
	if !found {
		t.Errorf("audit: %+v", log)
	}
}

func engineAuditFilter() engine.AuditFilter { return engine.AuditFilter{Limit: 100} }
