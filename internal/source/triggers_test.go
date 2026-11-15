package source

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const triggerPipeline = `
pipeline: app
inputs:
  target: { type: choice, options: [staging, prod], default: staging }
  version: { pattern: "^v[0-9]+$", default: v1 }
  systems: { type: list, default: [debian] }
on:
  push: { branches: [main] }
  schedule:
    - { cron: "0 * * * *", inputs: { target: prod } }
    - { cron: "30 2 * * *", ref: no-such-branch }
  webhook:
    name: deploy
    inputs: { version: body.release.version, target: body.target }
steps:
  test:
    for_each: { os: { input: systems } }
    image: alpine
    run: echo test
  deploy:
    needs: test
    image: alpine
    when: { input: target, equals: prod }
    run: echo deploy
`

func TestManualRunsTakeInputs(t *testing.T) {
	dir, repo := prRepo(t, triggerPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	info, err := m.InputsAt(context.Background(), "app", "main")
	if err != nil || len(info.Inputs) != 3 || info.Inputs[0].Name != "target" || len(info.Problems) > 0 {
		t.Fatalf("inputs: %+v %v", info, err)
	}
	run, err := m.StartRunWith(context.Background(), StartRequest{Project: "app", Actor: "ann",
		Inputs: map[string]string{"target": "prod", "systems": "debian, alpine"}})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := eng.GetRun(run.ID)
	if d.TriggerDetail == nil || d.TriggerDetail.Inputs["target"] != "prod" || d.TriggerDetail.Inputs["version"] != "v1" {
		t.Fatalf("recorded: %+v", d.TriggerDetail)
	}
	if len(d.StepList) != 3 || d.StepList[1].ID != "test[os=alpine]" || len(d.Graph.Loops) != 1 {
		t.Fatalf("steps: %+v", d.StepList)
	}
	_, err = m.StartRunWith(context.Background(), StartRequest{Project: "app", Inputs: map[string]string{"target": "qa", "version": "1", "nope": "x"}})
	var ie pipeline.InputErrors
	if !errors.As(err, &ie) || len(ie) != 3 || !strings.Contains(ie["version"], "pattern") {
		t.Fatalf("bad values: %v", err)
	}
	// A run started without values takes the defaults.
	run2, err := m.StartRun(context.Background(), "app", "main", "bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr, _ := eng.RunTrigger(run2.ID); tr.Inputs["target"] != "staging" {
		t.Fatalf("defaults: %+v", tr.Inputs)
	}
}

func TestPushesUseDefaults(t *testing.T) {
	dir, repo := prRepo(t, triggerPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	pollOnce(t, m)
	commit(t, repo, "", "a push")
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 {
		t.Fatalf("runs: %d", len(runs))
	}
	if tr, _ := eng.RunTrigger(runs[0].ID); tr.Event != "push" || tr.Inputs["target"] != "staging" || tr.Inputs["systems"] != `["debian"]` {
		t.Fatalf("push trigger: %+v", tr)
	}
}

func TestSchedulesFire(t *testing.T) {
	dir, repo := prRepo(t, triggerPipeline)
	addr := testdb.New(t)
	m, eng := prManager(t, addr, dir, Project{Name: "app", URL: repo})
	ctx := context.Background()
	pollOnce(t, m)
	p := m.projects["app"]
	t0 := time.Date(2026, 11, 15, 10, 20, 0, 0, time.UTC)

	// First seen now: nothing fires for the past.
	m.fireDue(ctx, p, t0)
	if n := len(runsOf(t, eng)); n != 0 {
		t.Fatalf("%d runs at first sight", n)
	}
	// Three hours missed: one run, for the latest of them.
	m.fireDue(ctx, p, t0.Add(3*time.Hour+5*time.Minute))
	runs := runsOf(t, eng)
	if len(runs) != 1 {
		t.Fatalf("%d runs after catching up", len(runs))
	}
	tr, _ := eng.RunTrigger(runs[0].ID)
	if tr.Event != "schedule" || tr.ScheduledFor != time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC).UnixMilli() ||
		tr.Inputs["target"] != "prod" || tr.Schedule != "0 * * * * (UTC)" || tr.Branch != "main" {
		t.Fatalf("scheduled run: %+v", tr)
	}
	// Looking again in the same hour starts nothing more.
	m.fireDue(ctx, p, t0.Add(3*time.Hour+30*time.Minute))
	if n := len(runsOf(t, eng)); n != 1 {
		t.Fatalf("%d runs", n)
	}
	v, err := m.Schedules("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Schedules) != 2 || v.Schedules[0].Next == 0 || v.Schedules[0].Last == nil || v.Schedules[0].Last.RunID != runs[0].ID {
		t.Fatalf("view: %+v", v.Schedules)
	}
	if len(v.Webhooks) != 1 || v.Webhooks[0].Configured {
		t.Fatalf("webhooks: %+v", v.Webhooks)
	}
	// The nightly schedule at a branch that does not exist records why.
	m.fireDue(ctx, p, time.Date(2026, 11, 16, 2, 31, 0, 0, time.UTC))
	v, _ = m.Schedules("app")
	var nightly *ScheduleView
	for i := range v.Schedules {
		if v.Schedules[i].Cron == "30 2 * * *" {
			nightly = &v.Schedules[i]
		}
	}
	if nightly == nil || nightly.Last == nil || nightly.Last.Outcome != "error" || !strings.Contains(nightly.Last.Detail, "no-such-branch") {
		t.Fatalf("nightly: %+v", nightly)
	}
}

func TestSchedulesTurnedOffAreSkipped(t *testing.T) {
	dir, repo := prRepo(t, triggerPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo, SchedulesOff: true})
	ctx := context.Background()
	pollOnce(t, m)
	p := m.projects["app"]
	t0 := time.Date(2026, 11, 15, 10, 20, 0, 0, time.UTC)
	m.fireDue(ctx, p, t0)
	m.fireDue(ctx, p, t0.Add(2*time.Hour))
	if n := len(runsOf(t, eng)); n != 0 {
		t.Fatalf("%d runs while turned off", n)
	}
	v, _ := m.Schedules("app")
	if v.Enabled || len(v.Fires) == 0 || v.Fires[0].Outcome != "skipped" || v.Schedules[0].Next != 0 {
		t.Fatalf("view: %+v", v)
	}
	// Turned on again, it does not catch up on what it skipped.
	p.SchedulesOff = false
	m.fireDue(ctx, p, t0.Add(2*time.Hour+10*time.Minute))
	if n := len(runsOf(t, eng)); n != 0 {
		t.Fatalf("%d runs caught up after turning on", n)
	}
	m.fireDue(ctx, p, t0.Add(3*time.Hour))
	if n := len(runsOf(t, eng)); n != 1 {
		t.Fatalf("%d runs once on again", n)
	}
}

// Several servers looking at the same schedule start one run per firing.
func TestTwoServersFireAScheduleOnce(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	dir, repo := prRepo(t, triggerPipeline)
	addr := testdb.New(t)
	var managers []*Manager
	var eng *engine.Engine
	for _, name := range []string{"a", "b", "c"} {
		e, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		mm, err := NewManager(e, filepath.Join(dir, name), []Project{{Name: "app", URL: repo}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		pollOnce(t, mm)
		managers, eng = append(managers, mm), e
	}
	t0 := time.Date(2026, 11, 15, 10, 20, 0, 0, time.UTC)
	all := func(at time.Time) {
		var wg sync.WaitGroup
		for _, mm := range managers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				mm.fireDue(context.Background(), mm.projects["app"], at)
			}()
		}
		wg.Wait()
	}
	all(t0)
	for i := 1; i <= 4; i++ {
		all(t0.Add(time.Duration(i) * time.Hour))
	}
	runs := runsOf(t, eng)
	if len(runs) != 4 {
		t.Fatalf("%d runs for 4 firings", len(runs))
	}
	seen := map[int64]bool{}
	for _, r := range runs {
		tr, _ := eng.RunTrigger(r.ID)
		if seen[tr.ScheduledFor] {
			t.Fatalf("two runs for %d", tr.ScheduledFor)
		}
		seen[tr.ScheduledFor] = true
	}
}

func TestWebhookTriggers(t *testing.T) {
	dir, repo := prRepo(t, triggerPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	ctx := context.Background()
	body := []byte(`{"release":{"version":"v42"},"target":"prod"}`)
	run, repeat, err := m.StartWebhook(ctx, WebhookRequest{Project: "app", Name: "deploy", Body: body, Key: "k1", BodyHash: "h1"})
	if err != nil || repeat {
		t.Fatalf("first call: %v %v", err, repeat)
	}
	tr, _ := eng.RunTrigger(run.ID)
	if tr.Event != "webhook" || tr.Webhook != "deploy" || tr.Inputs["version"] != "v42" || tr.Inputs["target"] != "prod" || tr.Actor != "webhook:deploy" {
		t.Fatalf("trigger: %+v", tr)
	}
	again, repeat, err := m.StartWebhook(ctx, WebhookRequest{Project: "app", Name: "deploy", Body: body, Key: "k1", BodyHash: "h1"})
	if err != nil || !repeat || again.ID != run.ID {
		t.Fatalf("repeat: %v %v %v", again, repeat, err)
	}
	if _, _, err := m.StartWebhook(ctx, WebhookRequest{Project: "app", Name: "deploy", Body: []byte(`{}`), Key: "k1", BodyHash: "h2"}); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("key reused with another body: %v", err)
	}
	_, _, err = m.StartWebhook(ctx, WebhookRequest{Project: "app", Name: "deploy", Body: []byte(`{"release":{"version":"latest"}}`)})
	var ie pipeline.InputErrors
	if !errors.As(err, &ie) || !strings.Contains(ie["version"], "pattern") {
		t.Fatalf("a value that does not fit: %v", err)
	}
	if _, _, err := m.StartWebhook(ctx, WebhookRequest{Project: "app", Name: "nope", Body: body}); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown trigger: %v", err)
	}
	if _, _, err := m.StartWebhook(ctx, WebhookRequest{Project: "app", Name: "deploy", Body: []byte(`[`)}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("not JSON: %v", err)
	}
	if n := len(runsOf(t, eng)); n != 1 {
		t.Fatalf("%d runs", n)
	}
}

func TestPollNow(t *testing.T) {
	dir, repo := prRepo(t, triggerPipeline)
	m, _ := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	if m.PollNow("nope") {
		t.Fatal("woke a project that does not exist")
	}
	for range 3 {
		if !m.PollNow("app") {
			t.Fatal("PollNow")
		}
	}
	select {
	case <-m.wakeChan("app"):
	default:
		t.Fatal("no wake-up")
	}
	select {
	case <-m.wakeChan("app"):
		t.Fatal("requests while one is pending are one request")
	default:
	}
}
