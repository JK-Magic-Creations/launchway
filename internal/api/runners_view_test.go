package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
)

// Anyone who can read one project may list the runners, but what a runner
// holds is shown only for projects the viewer may read, and the details of
// an enrolled runner's identity (its host, who enrolled it, the join token)
// only to those who manage runners.
func TestRunnersListShowsOnlyWhatTheViewerMayRead(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ctx := context.Background()
	start := func(project, step string) *engine.RunSummary {
		p, err := pipeline.Parse([]byte("pipeline: p\nsteps:\n  " + step + ": {image: none, run: echo hi}\n"))
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.eng.CreateRun(engine.NewRun{Project: project, Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
			Source: engine.Source{Kind: "git", URL: "https://git.example/" + project + ".git"}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// An enrolled runner takes pay-api's run, a runner with the shared
	// token takes web's.
	token, _, err := e.eng.CreateJoinToken(engine.JoinTokenSpec{}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	id, err := runner.Join(ctx, http.DefaultClient, e.srv.URL, token, t.TempDir(), "build-host-7",
		runner.JoinFacts{Name: "enrolled", OS: "linux", Arch: "amd64", Version: "test", Executor: "host"})
	if err != nil {
		t.Fatal(err)
	}
	id.Server = e.srv.URL
	enrolled := runner.NewClient(e.srv.URL, "")
	enrolled.Auth = &runner.IdentityAuth{ID: id, HTTP: http.DefaultClient, Log: quiet()}
	pay := start("pay-api", "build-pay")
	if j, err := enrolled.Lease(ctx, engine.RunnerInfo{Name: "enrolled", Features: engine.FeatureArtifacts}, 0); err != nil || j == nil || j.RunID != pay.ID {
		t.Fatalf("the enrolled runner's lease: %+v %v", j, err)
	}
	web := start("web", "build-secret-thing")
	shared := runner.NewClient(e.srv.URL, "runner-secret")
	if j, err := shared.Lease(ctx, engine.RunnerInfo{ID: "shared-1", Name: "shared", Features: engine.FeatureArtifacts}, 0); err != nil || j == nil || j.RunID != web.ID {
		t.Fatalf("the shared runner's lease: %+v %v", j, err)
	}

	list := func(user string) map[string]engine.RunnerView {
		t.Helper()
		var rs []engine.RunnerView
		resp := e.signIn(t, user).get(e.srv.URL + "/api/v1/runners")
		if resp.status != 200 {
			t.Fatalf("%s: %d %s", user, resp.status, resp.body)
		}
		resp.json(t, &rs)
		out := map[string]engine.RunnerView{}
		for _, r := range rs {
			if r.Work == nil {
				t.Errorf("%s: runner %s has no work list (the UI expects one)", user, r.ID)
			}
			out[r.Name] = r
		}
		return out
	}
	projects := func(r engine.RunnerView) []string {
		var ps []string
		for _, w := range r.Work {
			ps = append(ps, w.Project)
		}
		return ps
	}
	for user, want := range map[string]struct{ enrolled, shared []string }{
		"vic":  {[]string{"pay-api"}, nil}, // a viewer of pay-*
		"dana": {nil, []string{"web"}},     // an operator of web
	} {
		got := list(user)
		if len(got) != 2 {
			t.Fatalf("%s sees %d runners, want both: %+v", user, len(got), got)
		}
		if !slices.Equal(projects(got["enrolled"]), want.enrolled) || !slices.Equal(projects(got["shared"]), want.shared) {
			t.Errorf("%s sees work %v on the enrolled runner and %v on the shared one, want %v and %v",
				user, projects(got["enrolled"]), projects(got["shared"]), want.enrolled, want.shared)
		}
		for _, r := range got {
			if r.Identity != nil {
				t.Errorf("%s sees the identity of runner %s: %+v", user, r.ID, r.Identity)
			}
			for _, w := range r.Work {
				if w.Project == "web" && user == "vic" || w.Project == "pay-api" && user == "dana" {
					t.Errorf("%s sees work of %s", user, w.Project)
				}
			}
		}
		if got["enrolled"].Auth != "identity" || !got["enrolled"].Online {
			t.Errorf("%s: the enrolled runner is still told as such: %+v", user, got["enrolled"])
		}
	}
	// Nothing else about the other project leaks: not in the body at all.
	resp := e.signIn(t, "vic").get(e.srv.URL + "/api/v1/runners")
	for _, secret := range []string{`"web"`, web.ID, "build-secret-thing", "build-host-7", "ada", "join_"} {
		if strings.Contains(string(resp.body), secret) {
			t.Errorf("vic's list mentions %q: %s", secret, resp.body)
		}
	}

	// A runner manager sees everything.
	ada := list("ada")
	if !slices.Equal(projects(ada["enrolled"]), []string{"pay-api"}) || !slices.Equal(projects(ada["shared"]), []string{"web"}) {
		t.Errorf("ada sees work %v and %v", projects(ada["enrolled"]), projects(ada["shared"]))
	}
	if i := ada["enrolled"].Identity; i == nil || i.Hostname != "build-host-7" || i.EnrolledBy != "ada" || i.JoinToken == "" {
		t.Errorf("ada does not see the identity: %+v", i)
	}
}

// A runner's accept: section names projects, so only those who manage
// runners see it, as with the identity.
func TestRunnersListHidesAcceptFromThoseWhoDoNotManageRunners(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ctx := context.Background()
	shared := runner.NewClient(e.srv.URL, "runner-secret")
	info := engine.RunnerInfo{ID: "shared-1", Name: "shared", Features: engine.FeatureArtifacts,
		Accept: &accept.Config{Projects: []string{"secret-project"}}}
	if _, err := shared.Lease(ctx, info, 0); err != nil {
		t.Fatal(err)
	}
	list := func(user string) (engine.RunnerView, string) {
		t.Helper()
		resp := e.signIn(t, user).get(e.srv.URL + "/api/v1/runners")
		if resp.status != 200 {
			t.Fatalf("%s: %d %s", user, resp.status, resp.body)
		}
		var rs []engine.RunnerView
		resp.json(t, &rs)
		for _, r := range rs {
			if r.Name == "shared" {
				return r, string(resp.body)
			}
		}
		t.Fatalf("%s does not see the runner: %s", user, resp.body)
		return engine.RunnerView{}, ""
	}
	for _, user := range []string{"vic", "dana"} {
		r, body := list(user)
		if r.Accept != nil || strings.Contains(body, "secret-project") {
			t.Errorf("%s sees the runner's accept section: %s", user, body)
		}
	}
	if r, _ := list("ada"); r.Accept == nil || !slices.Equal(r.Accept.Projects, []string{"secret-project"}) {
		t.Errorf("ada does not see the accept section: %+v", r.Accept)
	}
}

// Whether a runner is enrolled decides whether a shared-token caller may
// say it is that runner. If the database cannot answer, the answer is not
// "no": the lease and the leave are refused with a 500.
func TestEnrolmentCheckFailsClosed(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ctx := context.Background()
	token, _, err := e.eng.CreateJoinToken(engine.JoinTokenSpec{}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	id, err := runner.Join(ctx, http.DefaultClient, e.srv.URL, token, t.TempDir(), "host",
		runner.JoinFacts{Name: "enrolled", OS: "linux", Arch: "amd64", Version: "test", Executor: "host"})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) int {
		t.Helper()
		req, err := http.NewRequest("POST", e.srv.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer runner-secret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	lease := `{"runner":{"id":"` + id.RunnerID + `","name":"imposter"},"wait_seconds":0}`
	leave := `{"runner_id":"` + id.RunnerID + `"}`
	// With the database answering, the impersonation is refused outright.
	if got := post("/api/runner/v1/lease", lease); got != http.StatusForbidden {
		t.Fatalf("lease as an enrolled runner: %d, want 403", got)
	}
	if got := post("/api/runner/v1/leave", leave); got != http.StatusForbidden {
		t.Fatalf("leave as an enrolled runner: %d, want 403", got)
	}
	// Take the identities table away, as a database fault would.
	driver := "sqlite"
	if strings.HasPrefix(e.db, "postgres") {
		driver = "pgx"
	}
	db, err := sql.Open(driver, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE runner_identities RENAME TO runner_identities_gone`); err != nil {
		t.Fatal(err)
	}
	if got := post("/api/runner/v1/lease", lease); got != http.StatusInternalServerError {
		t.Errorf("lease with the identity check failing: %d, want 500", got)
	}
	if got := post("/api/runner/v1/leave", leave); got != http.StatusInternalServerError {
		t.Errorf("leave with the identity check failing: %d, want 500", got)
	}
}
