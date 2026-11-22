package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func TestWorkTimeThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	vic, otto, ada := e.signIn(t, "vic"), e.signIn(t, "otto"), e.signIn(t, "ada")
	hostedRepo(t, e, "acme/shop")
	base := e.srv.URL + "/api/v1/work"
	var a, b work.Item
	otto.do("POST", base+"/items?scope=project:acme/shop", `{"title":"Crash","estimate":3}`).json(t, &a)
	otto.do("POST", base+"/items?scope=project:acme/shop", `{"title":"Slow"}`).json(t, &b)

	// A viewer reads and cannot log.
	if r := vic.do("POST", base+"/items/"+a.Ref+"/time", `{"minutes":10}`); r.status != 403 {
		t.Fatalf("a viewer logged time: %d %s", r.status, r.body)
	}
	if r := vic.do("POST", base+"/items/"+a.Ref+"/timer", ``); r.status != 403 {
		t.Fatalf("a viewer started a timer: %d", r.status)
	}
	var entry work.TimeEntry
	r := otto.do("POST", base+"/items/"+a.Ref+"/time", `{"minutes":90,"day":"2026-03-01","note":"first look"}`)
	r.json(t, &entry)
	if r.status != 201 || entry.Minutes != 90 || entry.Who != "otto" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := otto.do("POST", base+"/items/"+a.Ref+"/time", `{"minutes":0}`); r.status != 400 {
		t.Fatalf("zero minutes: %d", r.status)
	}

	// Timer: one at a time; stop gives the entry; nothing running afterwards.
	if r := otto.do("POST", base+"/items/"+a.Ref+"/timer", ``); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := otto.do("POST", base+"/items/"+b.Ref+"/timer", ``); r.status != 409 || !strings.Contains(string(r.body), a.Ref) {
		t.Fatalf("a second timer: %d %s", r.status, r.body)
	}
	var cur struct {
		Timer *work.RunningTimer `json:"timer"`
	}
	otto.get(base+"/timer").json(t, &cur)
	if cur.Timer == nil || cur.Timer.Item != a.Ref {
		t.Fatalf("the running timer: %+v", cur)
	}
	var tl work.TimeLog
	vic.get(base+"/items/"+a.Ref+"/time").json(t, &tl) // a viewer sees who is timing
	if len(tl.Running) != 1 || tl.Running[0].Who != "otto" || tl.TotalMinutes != 90 {
		t.Fatalf("time on the item: %+v", tl)
	}
	var stopped work.TimeEntry
	if r := otto.do("POST", base+"/timer/stop", `{"note":"fixed"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &stopped)
	}
	if stopped.Minutes != 1 || stopped.Note != "fixed" || stopped.Kind != "timer" {
		t.Fatalf("stopped: %+v", stopped)
	}
	if r := otto.do("POST", base+"/timer/stop", ``); r.status != 404 {
		t.Fatalf("stop with none running: %d", r.status)
	}
	otto.get(base+"/timer").json(t, &cur)
	if cur.Timer != nil {
		t.Fatalf("a timer after stop: %+v", cur)
	}

	// Edit and delete are your own.
	if r := ada.do("PATCH", base+"/time/"+entry.ID, `{"minutes":5}`); r.status != 403 {
		t.Fatalf("an administrator edited otto's entry: %d %s", r.status, r.body)
	}
	if r := otto.do("PATCH", base+"/time/"+entry.ID, `{}`); r.status != 400 {
		t.Fatalf("nothing to change: %d", r.status)
	}
	if r := otto.do("PATCH", base+"/time/"+entry.ID, `{"minutes":120}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := vic.do("DELETE", base+"/time/"+entry.ID, ``); r.status != 403 {
		t.Fatalf("a viewer deleted: %d", r.status)
	}

	// Totals and the export.
	var rep work.TimeReport
	vic.get(base+"/time?person=otto").json(t, &rep)
	if rep.TotalMinutes != 121 || len(rep.People) != 1 || rep.People[0].Items[0].Item != a.Ref {
		t.Fatalf("report: %+v", rep)
	}
	otto.get(base+"/time?person=me&from=2026-03-01&to=2026-03-01").json(t, &rep)
	if rep.TotalMinutes != 120 {
		t.Fatalf("a day: %+v", rep)
	}
	if r := vic.get(base + "/time?from=nope"); r.status != 400 {
		t.Fatalf("a bad day: %d", r.status)
	}
	csv := vic.get(base + "/time/export?person=otto")
	if csv.status != 200 || !strings.Contains(csv.header.Get("Content-Type"), "text/csv") || !strings.Contains(csv.header.Get("Content-Disposition"), "attachment") ||
		!strings.HasPrefix(string(csv.body), "day,person,item,title,minutes,hours,kind,note\r\n") || !strings.Contains(string(csv.body), a.Ref) {
		t.Fatalf("export: %d %v\n%s", csv.status, csv.header, csv.body)
	}
	if r := otto.do("DELETE", base+"/time/"+entry.ID, ``); r.status != 200 {
		t.Fatalf("%d", r.status)
	}
}
