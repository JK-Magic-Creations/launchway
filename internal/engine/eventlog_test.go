package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func decisionsOf(t *testing.T, e *Engine, runID string) []EventDecision {
	t.Helper()
	evs, err := e.EventLog(EventFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var out []EventDecision
	for _, ev := range evs {
		for _, d := range ev.Decisions {
			if d.RunID == runID {
				out = append(out, d)
			}
		}
	}
	return out
}

// A manual start leaves an event with a "started" decision naming the run.
func TestEventLogRecordsAManualStart(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, lanePipeline("{when_busy: wait}"), Trigger{Event: "manual", Actor: "ann"})
	evs, err := e.EventLog(EventFilter{Project: "demo"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("%v %+v", err, evs)
	}
	ev := evs[0]
	if ev.Source != SourceManual || !ev.Verified || !strings.Contains(ev.Summary, "ann") || ev.Replay == nil {
		t.Fatalf("%+v", ev)
	}
	if len(ev.Decisions) != 1 || ev.Decisions[0].Decision != DecStarted || ev.Decisions[0].RunID != id || ev.Decisions[0].RunNumber != 1 ||
		!strings.Contains(ev.Decisions[0].Reason, "started run #1") {
		t.Fatalf("%+v", ev.Decisions)
	}
	one, err := e.EventLogEntry(ev.ID)
	if err != nil || one.ID != ev.ID || len(one.Decisions) != 1 {
		t.Fatalf("%v %+v", err, one)
	}
	if _, err := e.EventLogEntry("evt_missing"); err == nil {
		t.Fatal("a missing event was found")
	}
}

// A run that waits for a busy lane says "held" with the lane's reason, and a
// run that a newer one replaces says "superseded" on its own event.
func TestEventLogExplainsHeldAndSuperseded(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{when_busy: wait, queue: newest}")
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	r3 := create(t, e, src, mainPush("s3"))
	d2 := decisionsOf(t, e, r2)
	if len(d2) != 2 || d2[0].Decision != DecHeld || !strings.Contains(d2[0].Reason, "waiting for lane demo") || d2[1].Decision != DecSuperseded ||
		!strings.Contains(d2[1].Reason, "replaced in lane demo by run #3") {
		t.Fatalf("%+v", d2)
	}
	if d1 := decisionsOf(t, e, r1); len(d1) != 1 || d1[0].Decision != DecStarted {
		t.Fatalf("%+v", d1)
	}
	if d3 := decisionsOf(t, e, r3); len(d3) != 1 || d3[0].Decision != DecHeld {
		t.Fatalf("%+v", d3)
	}
	held, err := e.EventLog(EventFilter{Decision: DecSuperseded})
	if err != nil || len(held) != 1 {
		t.Fatalf("%v %+v", err, held)
	}
}

// A push the trigger ignores is an event with the reason; a push that starts a
// run is one event, not two.
func TestEventLogRecordsFilteredPushes(t *testing.T) {
	e, clk := newEngine(t)
	ch := RefChange{Ref: "refs/heads/main", New: "abcdef1234567", SeenAt: 1}
	if _, err := e.RecordPush("demo", ch, PushDecision{Ref: ch.Ref, SHA: ch.New, Outcome: "ignored", Detail: "paths: only docs/** changed"}); err != nil {
		t.Fatal(err)
	}
	evs, _ := e.EventLog(EventFilter{Source: SourcePush})
	if len(evs) != 1 || evs[0].Summary != "Push to main at abcdef1" || len(evs[0].Decisions) != 1 ||
		evs[0].Decisions[0].Decision != DecFiltered || evs[0].Decisions[0].Reason != "paths: only docs/** changed" {
		t.Fatalf("%+v", evs)
	}
	// A push that starts a run, a moment later (events are listed newest first, and two at one instant have no order).
	clk.Add(time.Second)
	p, _ := bound(t, lanePipeline("{when_busy: wait}"), nil)
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: mainPush("s9"), Source: Source{Kind: "dir", Path: "/tmp"},
		Push: &RefChange{Ref: "refs/heads/main", Old: "abcdef1234567", New: "s9", SeenAt: 2}}); err != nil {
		t.Fatal(err)
	}
	evs, _ = e.EventLog(EventFilter{Source: SourcePush})
	if len(evs) != 2 || len(evs[0].Decisions) != 1 || evs[0].Decisions[0].Decision != DecStarted {
		t.Fatalf("%+v", evs)
	}
	// Filters.
	if got, _ := e.EventLog(EventFilter{Decision: DecFiltered}); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if got, _ := e.EventLog(EventFilter{Verified: "no"}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if got, _ := e.EventLog(EventFilter{Projects: []string{}}); len(got) != 0 {
		t.Fatalf("an empty list of visible projects returned %+v", got)
	}
	if got, _ := e.EventLog(EventFilter{Projects: []string{"other"}}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Payloads are kept small and without the values of credential-looking keys;
// an event that was refused is kept with verified false, and the unverified
// ones have a smaller allowance than the rest.
func TestEventLogScrubsPayloadsAndBoundsUnverified(t *testing.T) {
	raw := []byte(`{"release":{"version":"v1"},"token":"abc","nested":{"Authorization":"Bearer x","ok":1}}`)
	var got map[string]any
	if err := json.Unmarshal(ScrubPayload(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["token"] != "[removed]" || got["nested"].(map[string]any)["Authorization"] != "[removed]" || got["release"].(map[string]any)["version"] != "v1" {
		t.Fatalf("%v", got)
	}
	if ScrubPayload([]byte("not json")) != nil {
		t.Fatal("a body that is not JSON was kept")
	}
	big, _ := json.Marshal(map[string]string{"a": strings.Repeat("x", maxPayload)})
	if b := ScrubPayload(big); !strings.Contains(string(b), "truncated") {
		t.Fatalf("%s", b)
	}
	e, _ := newEngine(t)
	for i := 0; i < keepUnverified+5; i++ {
		if _, err := e.RecordEvent(EventRecord{Project: "demo", Source: SourceWebhook, SourceName: "deploy", Summary: "bad signature"},
			EventDecision{Trigger: "webhook:deploy", Decision: DecRefused, Reason: "the signature did not match"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := e.EventLog(EventFilter{Verified: "no", Limit: 500})
	if err != nil || len(all) != keepUnverified {
		t.Fatalf("%v %d unverified events kept, want %d", err, len(all), keepUnverified)
	}
	if all[0].Verified || len(all[0].Decisions) != 1 || all[0].Decisions[0].Decision != DecRefused {
		t.Fatalf("%+v", all[0])
	}
}
