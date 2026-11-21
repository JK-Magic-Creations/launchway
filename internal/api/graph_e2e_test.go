package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/graph"
)

// Several interdependent repositories are pushed to a hosted server; the
// graph follows each push by itself; and what each person may ask of it is
// what that person may read.

const e2ePipeline = "pipeline: svc\non: { push: { branches: [main] } }\nsteps:\n  image:\n    kind: image\n    context: .\n    file: Containerfile\n    platforms: [linux/amd64, linux/arm64]\n    tags:\n      - { registry: harbor, repository: apps/svc, tag: latest }\n"

var graphFixture = map[string]map[string]string{
	"acme/libgo": {"go.mod": "module example.test/libgo\n\ngo 1.23\n"},
	"acme/base":  {"Containerfile": "FROM debian:12\n", ".launchway/pipeline.yaml": strings.Replace(strings.Replace(e2ePipeline, "apps/svc", "base/runtime", 1), "pipeline: svc", "pipeline: base", 1)},
	"acme/svc": {"go.mod": "module example.test/svc\n\ngo 1.23\n\nrequire example.test/libgo v1.2.0\n", "Containerfile": "FROM harbor.test/base/runtime:1.0\nEXPOSE 8080\n",
		".launchway/pipeline.yaml": e2ePipeline},
	"acme/ui":  {"package.json": `{"name":"@acme/ui"}`},
	"acme/web": {"package.json": `{"name":"web","dependencies":{"@acme/ui":"^1.0.0"}}`},
}

func pushFiles(t *testing.T, e *authEnv, tok, path string, files map[string]string, msg string) {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	for p, c := range files {
		fp := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(fp), 0o755)
		if err := os.WriteFile(fp, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", msg)
	mustGit(t, dir, "push", "-q", "-f", withToken(e.srv.URL, "/git/"+path+".git", tok), "HEAD:refs/heads/main")
}

func gcall(t *testing.T, e *authEnv, tok, method, path, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Launchway-CSRF", "1")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, data)
		}
	}
	if out != nil && resp.StatusCode >= 300 {
		_ = json.Unmarshal(data, out)
	}
	return resp.StatusCode
}

func TestGraphFollowsPushesAndFiltersByWhatThePersonMayRead(t *testing.T) {
	pol, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{Group: "pay-view", Role: auth.Viewer, Projects: []string{"acme/libgo", "acme/ui", "acme/web", "acme/base"}},
		{Group: "pay-ops", Role: auth.Operator, Projects: []string{"acme/*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e, h := hostedEnv(t, envOpts{policy: pol})
	store := graph.NewStore(e.eng.Database())
	cfg := graph.Config{RegistryHosts: map[string]string{"harbor": "harbor.test"}}
	repos := graph.HostedRepos{Lister: func(ctx context.Context) ([]graph.Repo, error) {
		pg, err := h.Provider.Repositories().List(ctx, "")
		var out []graph.Repo
		for _, r := range pg.Items {
			out = append(out, graph.Repo{ID: r.ID, Path: r.Path, DefaultBranch: r.DefaultBranch})
		}
		return out, err
	}, Git: h.Store}
	runner := &graph.Runner{Store: store, Repos: repos, Sources: []graph.Source{graph.ManifestSource{Repos: repos}, graph.ContainerSource{Repos: repos, Cfg: cfg}, graph.RequirementsSource{Repos: repos, Cfg: cfg}}}
	e.api.Graph = &GraphService{Store: store, Runner: runner, Watch: &graph.Watcher{Runner: runner, DB: e.eng.Database()}}

	ada, vic, otto := e.tokenFor(t, "ada", "a"), e.tokenFor(t, "vic", "v"), e.tokenFor(t, "otto", "o")
	for path := range graphFixture {
		hostedRepo(t, e, path)
	}
	for path, files := range graphFixture {
		pushFiles(t, e, ada, path, files, "first")
	}
	type order struct {
		Order  []string `json:"order"`
		Levels [][]string
		Edges  []graph.RepoEdge `json:"edges"`
	}
	var full order
	hostedWait(t, "the graph to follow the pushes", func() bool {
		full = order{}
		gcall(t, e, ada, "GET", "/api/v1/graph/order", "", &full)
		return len(full.Edges) == 3
	})
	pos := map[string]int{}
	for i, n := range full.Order {
		pos[n] = i
	}
	if pos["acme/libgo"] >= pos["acme/svc"] || pos["acme/base"] >= pos["acme/svc"] || pos["acme/ui"] >= pos["acme/web"] {
		t.Fatalf("order %v", full.Order)
	}

	// vic may read libgo, ui, web and base but not svc: nothing of svc is in what vic is told.
	var part order
	gcall(t, e, vic, "GET", "/api/v1/graph/order", "", &part)
	for _, ed := range part.Edges {
		if ed.Consumer == "acme/svc" || ed.Provider == "acme/svc" {
			t.Fatalf("vic was told of svc: %+v", ed)
		}
	}
	if len(part.Edges) != 1 {
		t.Fatalf("vic's order: %+v", part)
	}
	var im graph.ImpactResult
	gcall(t, e, ada, "GET", "/api/v1/graph/impact?ref=package:go:example.test/libgo", "", &im)
	var imVic graph.ImpactResult
	gcall(t, e, vic, "GET", "/api/v1/graph/impact?ref=package:go:example.test/libgo", "", &imVic)
	if im.Total == 0 || imVic.Total != 0 || len(imVic.Repositories) != 0 {
		t.Fatalf("impact for ada %d, for vic %d %v", im.Total, imVic.Total, imVic.Repositories)
	}
	var sumA, sumV graph.Summary
	gcall(t, e, ada, "GET", "/api/v1/graph", "", &sumA)
	gcall(t, e, vic, "GET", "/api/v1/graph", "", &sumV)
	if sumV.Nodes["repository"] != 4 || sumA.Nodes["repository"] != 5 || sumV.Edges >= sumA.Edges {
		t.Fatalf("counts leaked: ada %+v vic %+v", sumA, sumV)
	}
	// A hidden node and a node that does not exist answer alike.
	var e1, e2 map[string]string
	c1 := gcall(t, e, vic, "GET", "/api/v1/graph/node?ref=repository:acme/svc", "", &e1)
	c2 := gcall(t, e, vic, "GET", "/api/v1/graph/node?ref=repository:acme/nothing", "", &e2)
	if c1 != 404 || c2 != 404 || strings.Replace(e1["error"], "svc", "X", 1) != strings.Replace(e2["error"], "nothing", "X", 1) {
		t.Fatalf("%d %v / %d %v", c1, e1, c2, e2)
	}
	if code := gcall(t, e, "", "GET", "/api/v1/graph", "", nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}

	// A push that drops the dependency closes the edge, seen without asking.
	pushFiles(t, e, ada, "acme/svc", map[string]string{"go.mod": "module example.test/svc\n", "Containerfile": "FROM harbor.test/base/runtime:1.0\n", ".launchway/pipeline.yaml": e2ePipeline}, "drop libgo")
	hostedWait(t, "the graph to see the dependency go", func() bool {
		var o order
		gcall(t, e, ada, "GET", "/api/v1/graph/order", "", &o)
		return len(o.Edges) == 2
	})
	var then order
	gcall(t, e, ada, "GET", "/api/v1/graph/order?as_of="+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "", &then)
	if len(then.Edges) != 0 {
		t.Fatalf("before the graph existed: %+v", then)
	}

	// Overrides: a reason is required; the operator role is needed on both repositories.
	if code := gcall(t, e, vic, "POST", "/api/v1/graph/overrides", `{"kind":"ignore","from":"acme/web","to":"acme/ui","reason":"x"}`, nil); code != 403 {
		t.Fatalf("a viewer changed the order: %d", code)
	}
	var ov map[string]any
	if code := gcall(t, e, otto, "POST", "/api/v1/graph/overrides", `{"kind":"ignore","from":"acme/web","to":"acme/ui"}`, &ov); code != 400 || !strings.Contains(ov["error"].(string), "reason") {
		t.Fatalf("%d %v", code, ov)
	}
	if code := gcall(t, e, otto, "POST", "/api/v1/graph/overrides", `{"kind":"ignore","from":"acme/web","to":"acme/ui","reason":"web only uses ui's types"}`, &ov); code != 201 {
		t.Fatalf("%d %v", code, ov)
	}
	var o2 order
	gcall(t, e, ada, "GET", "/api/v1/graph/order", "", &o2)
	var marked bool
	for _, ed := range o2.Edges {
		marked = marked || (ed.Consumer == "acme/web" && ed.Override == "ignored")
	}
	if !marked {
		t.Fatalf("%+v", o2.Edges)
	}

	// Requirements: a file with a mistake is refused; a good one opens a pull request nobody merged.
	var bad map[string]string
	if code := gcall(t, e, otto, "POST", "/api/v1/repositories/acme/svc/requirements/proposal", `{"content":"version: 1\nmemory: lots\n"}`, &bad); code != 422 {
		t.Fatalf("%d %v", code, bad)
	}
	var pr struct {
		Number int    `json:"number"`
		Branch string `json:"branch"`
	}
	if code := gcall(t, e, otto, "POST", "/api/v1/repositories/acme/svc/requirements/proposal", `{"content":"version: 1\narchitectures: [arm64]\nmemory: 1Gi\n"}`, &pr); code != 201 || pr.Number == 0 || !strings.HasPrefix(pr.Branch, "launchway/requirements-") {
		t.Fatalf("%d %+v", code, pr)
	}
	// Until it is merged the graph does not know: the file is read from the default branch.
	var needs struct {
		Items []graph.ComponentNeeds `json:"items"`
	}
	gcall(t, e, ada, "GET", "/api/v1/graph/needs?ref=repository:acme/svc", "", &needs)
	for _, n := range needs.Items {
		for _, f := range n.Facts {
			if f.Source == graph.Declared {
				t.Fatalf("an unmerged proposal counted: %+v", f)
			}
		}
	}
	if code := gcall(t, e, vic, "POST", "/api/v1/repositories/acme/svc/requirements/proposal", `{"content":"version: 1\n"}`, nil); code != 404 && code != 403 {
		t.Fatalf("a viewer proposed: %d", code)
	}
	// Rebuild from scratch equals what the pushes built.
	before, _ := store.Fingerprint(t.Context())
	if code := gcall(t, e, ada, "POST", "/api/v1/graph/rebuild", "{}", nil); code != 200 {
		t.Fatalf("rebuild %d", code)
	}
	if after, _ := store.Fingerprint(t.Context()); after != before {
		t.Fatalf("a rebuild differs from the pushes:\n%s\n---\n%s", before, after)
	}
}
