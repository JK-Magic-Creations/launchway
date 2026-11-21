package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The event log through the API: a run started by hand is an event with a
// "started" decision; people see only events of projects they may read; an
// operator replays one, which is a new event pointing back and is audited; a
// viewer may not.
func TestEventsAPIListShowReplay(t *testing.T) {
	e := setupAuth(t, envOpts{mcp: &MCPOptions{}})
	pay := e.startRun(t, "pay-api")
	e.startRun(t, "web")
	vic, otto, dana := e.signIn(t, "vic"), e.signIn(t, "otto"), e.signIn(t, "dana")

	var evs []engine.EventRecord
	r := otto.get(e.srv.URL + "/api/v1/events?project=pay-api")
	if r.status != 200 || json.Unmarshal(r.body, &evs) != nil || len(evs) != 1 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	ev := evs[0]
	if ev.Source != engine.SourceManual || !ev.Verified || len(ev.Decisions) != 1 || ev.Decisions[0].Decision != engine.DecStarted || ev.Decisions[0].RunID != pay {
		t.Fatalf("%+v", ev)
	}
	// dana operates web only: she sees no event of pay-api, in the list or by ID.
	if r := dana.get(e.srv.URL + "/api/v1/events?project=pay-api"); r.status != 200 || strings.TrimSpace(string(r.body)) != "[]" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := dana.get(e.srv.URL + "/api/v1/events/" + ev.ID); r.status != 404 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := otto.get(e.srv.URL + "/api/v1/events?decision=bogus"); r.status != 400 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := otto.get(e.srv.URL + "/api/v1/events?decision=filtered"); r.status != 200 || strings.TrimSpace(string(r.body)) != "[]" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A viewer may read it, not replay it.
	if r := vic.get(e.srv.URL + "/api/v1/events/" + ev.ID); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := vic.do("POST", e.srv.URL+"/api/v1/events/"+ev.ID+"/replay", "{}"); r.status != 403 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// An operator replays it.
	r = otto.do("POST", e.srv.URL+"/api/v1/events/"+ev.ID+"/replay", "{}")
	var out struct {
		Run      engine.RunSummary `json:"run"`
		ReplayOf string            `json:"replay_of"`
	}
	if r.status != 201 || json.Unmarshal(r.body, &out) != nil || out.ReplayOf != ev.ID || out.Run.Number != 2 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = otto.get(e.srv.URL + "/api/v1/events?project=pay-api")
	evs = nil
	if json.Unmarshal(r.body, &evs) != nil || len(evs) != 2 || evs[0].ReplayOf != ev.ID || !strings.Contains(evs[0].Summary, "Replay of event "+ev.ID) ||
		evs[0].Decisions[0].RunNumber != 2 {
		t.Fatalf("%s", r.body)
	}
	// It is in the Logbook, with the original's ID as the target.
	entries, err := e.eng.AuditLog(engine.AuditFilter{Action: "event.replay", Limit: 10})
	if err != nil || len(entries) != 2 || entries[0].Target != ev.ID || entries[0].User != "otto" || entries[0].Outcome != "ok" || !strings.Contains(entries[0].Detail, "run #2") ||
		entries[1].User != "vic" || entries[1].Outcome != "denied" {
		t.Fatalf("%v %+v", err, entries)
	}
	// A refused event cannot be replayed.
	id, err := e.eng.RecordEvent(engine.EventRecord{Project: "pay-api", Source: engine.SourceWebhook, SourceName: "x", Summary: "bad signature"},
		engine.EventDecision{Trigger: "webhook:x", Decision: engine.DecRefused, Reason: "the signature did not match"})
	if err != nil {
		t.Fatal(err)
	}
	if r := otto.do("POST", e.srv.URL+"/api/v1/events/"+id+"/replay", "{}"); r.status != 409 || !strings.Contains(string(r.body), "not_verified") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}
