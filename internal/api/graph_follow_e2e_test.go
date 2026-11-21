package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/graph"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// A push to a library repository can start the projects that depend on it, in order, when each allows it:
// the software graph says who depends on whom, and the same result triggers as any other project's start
// the dependents. Nothing is made until asked, a project that has not allowed it is left out with the reason,
// and asking again makes nothing twice.
func TestGraphDependenciesBecomeResultTriggers(t *testing.T) {
	dir := t.TempDir()
	e := setupAuth(t, envOpts{projects: []source.Project{
		{Name: "pay-inputs", URL: gitRepo(t, filepath.Join(dir, "inputs"), inputsPipeline), Poll: time.Hour},
	}})
	ada := e.signIn(t, "ada")
	ctx := t.Context()
	e.api.Projects.ProcessResults(ctx)

	mem := graph.NewMemRepos()
	mem.Push("acme/api", "c1", map[string]string{"go.mod": "module example.test/api\n\ngo 1.23\n"})
	mem.Push("acme/inputs", "c1", map[string]string{"go.mod": "module example.test/inputs\n\ngo 1.23\n\nrequire example.test/api v1.0.0\n"})
	store := graph.NewStore(e.eng.Database())
	runner := &graph.Runner{Store: store, Repos: mem, Sources: []graph.Source{graph.ManifestSource{Repos: mem}}}
	e.api.Graph = &GraphService{Store: store, Runner: runner, ProjectRepos: func(context.Context) (map[string]string, error) {
		return map[string]string{"pay-api": "acme/api", "pay-inputs": "acme/inputs"}, nil
	}}
	if _, err := runner.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	down := e.srv.URL + "/api/v1/projects/pay-inputs"
	up := e.srv.URL + "/api/v1/projects/pay-api"

	var plan FollowPlan
	if r := ada.do("GET", down+"/run-triggers/graph", ""); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &plan)
	}
	if plan.Repository != "acme/inputs" || len(plan.Items) != 1 || plan.Items[0].UpstreamProject != "pay-api" || plan.Items[0].Allowed || !strings.Contains(plan.Items[0].Note, "has not allowed") {
		t.Fatalf("the plan before an allow: %+v", plan)
	}
	if trs, _ := e.eng.RunTriggers("pay-inputs"); len(trs) != 0 {
		t.Fatalf("looking made triggers: %+v", trs)
	}
	// Asking to make them before pay-api allows it makes none, and says why.
	if r := ada.do("POST", down+"/run-triggers/graph", `{}`); r.status != 200 || strings.Contains(string(r.body), `"created":true`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if trs, _ := e.eng.RunTriggers("pay-inputs"); len(trs) != 0 {
		t.Fatalf("a trigger was made without the upstream's permission: %+v", trs)
	}
	if r := ada.do("POST", up+"/run-trigger-allows", `{"downstream_project":"pay-inputs"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do("POST", down+"/run-triggers/graph", `{}`); r.status != 200 || !strings.Contains(string(r.body), `"created":true`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	trs, _ := e.eng.RunTriggers("pay-inputs")
	if len(trs) != 1 || trs[0].Name != "graph-pay-api" || trs[0].UpstreamProject != "pay-api" || trs[0].When != "succeeded" {
		t.Fatalf("%+v", trs)
	}
	// Again: nothing twice.
	if r := ada.do("POST", down+"/run-triggers/graph", `{}`); r.status != 200 || strings.Contains(string(r.body), `"created":true`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if trs, _ := e.eng.RunTriggers("pay-inputs"); len(trs) != 1 {
		t.Fatalf("%+v", trs)
	}
	// The trigger is the ordinary one: a result of pay-api is judged for it, and one that did not end well starts nothing.
	e.eng.Cancel(e.startRun(t, "pay-api"), "ada")
	e.api.Projects.ProcessResults(ctx)
	evs, _ := e.eng.EventLog(engine.EventFilter{Project: "pay-inputs", Decision: engine.DecFiltered})
	if len(evs) != 1 || !strings.Contains(evs[0].Decisions[0].Reason, "waits for succeeded") {
		t.Fatalf("%+v", evs)
	}
}

// A formation can take its order from the software graph: what the others depend on goes first, whatever order the
// releases are written in; the order's source is recorded; and without a graph that knows the projects it is refused.
func TestFormationOrderedByDependencies(t *testing.T) {
	e := setupShip(t, "pay-api2", "pay-web2")
	e.publishAs(t, "pay-ship", "5.0.0")
	e.publishAs(t, "pay-api2", "2.0.0")
	e.publishAs(t, "pay-web2", "3.0.0")
	body := `{"name":"deps","order":"dependencies","releases":["pay-web2@3.0.0","pay-ship@5.0.0","pay-api2@2.0.0"]}`
	if r := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations", body); r.status != 409 || !strings.Contains(string(r.body), "software graph") {
		t.Fatalf("without a graph: %d %s", r.status, r.body)
	}
	mem := graph.NewMemRepos()
	mem.Push("acme/api2", "c1", map[string]string{"go.mod": "module example.test/api2\n\ngo 1.23\n"})
	mem.Push("acme/ship", "c1", map[string]string{"go.mod": "module example.test/ship\n\ngo 1.23\n\nrequire example.test/api2 v1.0.0\n"})
	mem.Push("acme/web2", "c1", map[string]string{"go.mod": "module example.test/web2\n\ngo 1.23\n\nrequire example.test/ship v1.0.0\n"})
	store := graph.NewStore(e.eng.Database())
	runner := &graph.Runner{Store: store, Repos: mem, Sources: []graph.Source{graph.ManifestSource{Repos: mem}}}
	repos := map[string]string{"pay-api2": "acme/api2", "pay-ship": "acme/ship", "pay-web2": "acme/web2"}
	e.api.Graph = &GraphService{Store: store, Runner: runner, ProjectRepos: func(context.Context) (map[string]string, error) { return repos, nil }}
	if _, err := runner.Rescan(t.Context()); err != nil {
		t.Fatal(err)
	}
	var f engine.Formation
	r := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations", body)
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r.json(t, &f)
	if f.OrderSource != "dependencies" || len(f.Members) != 3 || f.Members[0].Project != "pay-api2" || f.Members[1].Project != "pay-ship" || f.Members[2].Project != "pay-web2" {
		t.Fatalf("%+v", f)
	}
	var got engine.Formation
	e.otto.get(e.srv.URL+"/api/v1/release-formations/deps").json(t, &got)
	if got.OrderSource != "dependencies" {
		t.Fatalf("the source was not kept: %+v", got)
	}
	if r := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations", `{"name":"odd","order":"alphabet","releases":["pay-web2@3.0.0","pay-ship@5.0.0"]}`); r.status != 400 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A project the graph cannot place is named, and nothing is guessed.
	delete(repos, "pay-ship")
	if r := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations", `{"name":"lost","order":"dependencies","releases":["pay-web2@3.0.0","pay-ship@5.0.0"]}`); r.status != 409 || !strings.Contains(string(r.body), "pay-ship") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}
