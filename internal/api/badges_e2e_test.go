package api

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// A badge is shown to anyone only when the Flight's badges are on (or its repository is public): until then it is the
// same plain not found as for a Flight that does not exist. It says the status of the last Sortie on the branch asked
// for, or the version of the latest release, and nothing else; an administrator of the Flight turns it on.
func TestBadgesShowOnlyStatusAndOnlyWhenSwitchedOn(t *testing.T) {
	e := setupShip(t)
	anon := func(path string) response { return newBrowser(t).get(e.srv.URL + "/public/badges/" + path) }
	// Off by default: a Flight that is there and one that is not answer the same.
	here, gone := anon("pay-ship/status.svg"), anon("ghost/status.svg")
	if here.status != 404 || gone.status != 404 || string(here.body) != string(gone.body) {
		t.Fatalf("off by default: %d %q and %d %q", here.status, here.body, gone.status, gone.body)
	}
	if r := anon("pay-ship/release.svg"); r.status != 404 {
		t.Fatalf("release badge off by default: %d", r.status)
	}
	// An operator may read the settings but not change them; an administrator does, and it is in the Logbook.
	var view struct {
		Enabled, Shown bool
		Links          map[string]string
		Markdown       map[string]string
	}
	e.vic.get(e.url("/badges")).json(t, &view)
	if view.Enabled || view.Shown || !strings.Contains(view.Links["status"], "/public/badges/pay-ship/status.svg") || !strings.HasPrefix(view.Markdown["status"], "![pay-ship status](") {
		t.Fatalf("view: %+v", view)
	}
	if r := e.otto.do("PUT", e.url("/badges"), `{"enabled":true}`); r.status != 403 {
		t.Fatalf("an operator switched badges on: %d %s", r.status, r.body)
	}
	if r := e.ada.do("PUT", e.url("/badges"), `{"enabled":true}`); r.status != 200 {
		t.Fatalf("switch on: %d %s", r.status, r.body)
	}
	// No runs yet, then the last run on the branch asked for.
	if r := anon("pay-ship/status.svg"); r.status != 200 || !strings.Contains(string(r.body), "no Sorties") || r.header.Get("Content-Type") != "image/svg+xml; charset=utf-8" {
		t.Fatalf("no Sorties: %d %s %v", r.status, r.body, r.header)
	}
	mk := func(branch, status string) {
		t.Helper()
		p, err := pipeline.Parse([]byte("pipeline: pay-ship\nsteps:\n  hello:\n    image: alpine\n    run: echo hi\n"))
		if err != nil {
			t.Fatal(err)
		}
		run, err := e.eng.CreateRun(engine.NewRun{Project: "pay-ship", Pipeline: p, Trigger: engine.Trigger{Event: "push", Branch: branch, Ref: "refs/heads/" + branch, Actor: "ada-secret", Message: "a secret commit message"}, Source: engine.Source{Kind: "dir", Path: "/tmp"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.eng.Database().Exec(t.Context(), `UPDATE runs SET status = ? WHERE id = ?`, status, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	mk("main", "failed")
	mk("main", "succeeded")
	mk("dev", "failed")
	for path, want := range map[string]string{"pay-ship/status.svg": "passing", "pay-ship/status.svg?branch=main": "passing", "pay-ship/status.svg?branch=dev": "failing", "pay-ship/status.svg?branch=other": "no Sorties"} {
		if r := anon(path); r.status != 200 || !strings.Contains(string(r.body), ": "+want+`"`) {
			t.Errorf("%s: %d %s, want %s", path, r.status, r.body, want)
		}
	}
	// Only the word: nothing a private Flight knows.
	r := anon("pay-ship/status.svg?branch=dev")
	for _, leak := range []string{"ada-secret", "secret commit", "pay-ship", "sha", "run_"} {
		if strings.Contains(string(r.body), leak) {
			t.Errorf("the badge holds %q: %s", leak, r.body)
		}
	}
	if r := anon("pay-ship/status.svg?branch=" + "x\"><script>"); r.status != 404 {
		t.Errorf("a branch that is not a name: %d", r.status)
	}
	// Cache headers: it can be cached for a minute and asks again with its tag.
	if cc, et := r.header.Get("Cache-Control"), r.header.Get("ETag"); !strings.Contains(cc, "max-age=60") || et == "" || r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", r.header)
	}
	if again := newBrowser(t).get(e.srv.URL+"/public/badges/pay-ship/status.svg?branch=dev", "If-None-Match", r.header.Get("ETag")); again.status != 304 {
		t.Errorf("a repeated request with the tag: %d", again.status)
	}
	// The release badge: none, then the version of the latest release that is not a pre-release.
	if r := anon("pay-ship/release.svg"); !strings.Contains(string(r.body), ": none") {
		t.Errorf("release badge before a release: %s", r.body)
	}
	e.publishAs(t, "pay-ship", "7.0.0")
	if r := anon("pay-ship/release.svg"); !strings.Contains(string(r.body), ": 7.0.0") {
		t.Errorf("release badge: %s", r.body)
	}
	// Switching off hides them again at once (apart from what a cache keeps for a minute).
	if r := e.ada.do("PUT", e.url("/badges"), `{"enabled":false}`); r.status != 200 {
		t.Fatalf("switch off: %d", r.status)
	}
	if r := anon("pay-ship/status.svg"); r.status != 404 {
		t.Errorf("after switching off: %d", r.status)
	}
	var on, off int
	for _, a := range e.auditEntries(t) {
		if a.Action == "flight.badges" && a.Outcome == "ok" && strings.Contains(a.Detail, "badges on") {
			on++
		}
		if a.Action == "flight.badges" && a.Outcome == "ok" && strings.Contains(a.Detail, "badges off") {
			off++
		}
	}
	if on != 1 || off != 1 {
		t.Errorf("audit lines: %d on, %d off", on, off)
	}
}

// A Flight that reads a public repository hosted here shows its badges without the switch; making the repository
// private hides them again.
func TestBadgesOfAPublicRepositoryNeedNoSwitch(t *testing.T) {
	e, _ := hostedEnvWith(t, envOpts{projectsAt: func(base string) []source.Project {
		return []source.Project{{Name: "hub", URL: base + "/git/acme/hub.git", Poll: time.Hour}}
	}}, hosting.Options{}, githttp.Options{})
	e.api.Projects.SetHostedLocal(e.api)
	ada := e.signIn(t, "ada")
	api := e.srv.URL + "/api/v1"
	if r := ada.do("POST", api+"/repositories", `{"owner":"acme","name":"hub","visibility":"private"}`); r.status != 201 {
		t.Fatalf("repository: %d %s", r.status, r.body)
	}
	get := func() response { return newBrowser(t).get(e.srv.URL + "/public/badges/hub/status.svg") }
	if r := get(); r.status != 404 {
		t.Fatalf("a private repository's badge: %d", r.status)
	}
	if r := ada.do("PATCH", api+"/repositories/acme/hub", `{"visibility":"public"}`); r.status != 200 {
		t.Fatalf("make public: %d %s", r.status, r.body)
	}
	if r := get(); r.status != 200 || !strings.Contains(string(r.body), "no Sorties") {
		t.Fatalf("a public repository's badge: %d %s", r.status, r.body)
	}
	if r := ada.do("PATCH", api+"/repositories/acme/hub", `{"visibility":"private"}`); r.status != 200 {
		t.Fatalf("make private: %d %s", r.status, r.body)
	}
	if r := get(); r.status != 404 {
		t.Fatalf("after making it private: %d", r.status)
	}
}
