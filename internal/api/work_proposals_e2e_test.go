package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// proposalFindings builds n findings of one group, numbered from first.
func proposalFindings(group string, first, n int) []map[string]any {
	var out []map[string]any
	for i := first; i < first+n; i++ {
		out = append(out, map[string]any{"dedupe_key": fmt.Sprintf("%s-%d", group, i), "group_key": group, "group_title": "Fix {n} " + group,
			"title": fmt.Sprintf("%s %d", group, i), "severity": 2})
	}
	return out
}

func batchJSON(t *testing.T, source string, complete bool, fs ...[]map[string]any) string {
	t.Helper()
	var all []map[string]any
	for _, f := range fs {
		all = append(all, f...)
	}
	b, err := json.Marshal(map[string]any{"scope": "project:acme/shop", "source": source, "run_id": "r", "complete": complete, "proposals": all})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An analyzer proposes, a person reviews, a re-run keeps the item in line, and
// an agent's token has no part in it.
func TestProposalsThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	base := e.srv.URL + "/api/v1/work"
	batch := batchJSON(t, "lint", true, proposalFindings("deps", 0, 8), proposalFindings("docker", 0, 4))

	if r := vic.do("POST", base+"/proposals", batch); r.status != 403 {
		t.Fatalf("a viewer proposed: %d %s", r.status, r.body)
	}
	var res work.ProposeResult
	r := otto.do("POST", base+"/proposals", batch)
	r.json(t, &res)
	if r.status != 200 || res.New != 12 || res.Waiting != 2 || res.Opened != 0 {
		t.Fatalf("propose: %d %s", r.status, r.body)
	}
	var pg work.Page
	otto.get(base+"/items?scope=project:acme/shop&q=label:machine-proposed").json(t, &pg)
	if pg.Total != 0 {
		t.Fatalf("items were made without a review: %d", pg.Total)
	}
	var gs struct{ Groups []work.ProposalGroup }
	otto.get(base+"/proposals?scope=project:acme/shop&state=pending").json(t, &gs)
	if len(gs.Groups) != 2 {
		t.Fatalf("%+v", gs)
	}

	// Accept one group: an item with a checklist, marked as machine-proposed.
	r = otto.do("POST", base+"/proposals/accept", `{"scope":"project:acme/shop","source":"lint","groups":["deps"]}`)
	r.json(t, &res)
	if r.status != 200 || res.Opened != 1 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	otto.get(base+"/items?scope=project:acme/shop&q=label:machine-proposed").json(t, &pg)
	if pg.Total != 1 {
		t.Fatalf("%+v", pg)
	}
	var det workDetail
	otto.get(base+"/items/"+pg.Items[0].Ref).json(t, &det)
	if len(det.Criteria) != 8 || det.Reporter != "source:lint" || !hasLabel(det.Labels, "source:lint") {
		t.Fatalf("%+v", det.Item)
	}

	// A re-run with three of the findings gone ticks them off.
	r = otto.do("POST", base+"/proposals", batchJSON(t, "lint", true, proposalFindings("deps", 3, 5), proposalFindings("docker", 0, 4)))
	r.json(t, &res)
	if r.status != 200 || res.Fixed != 3 {
		t.Fatalf("re-run: %d %s", r.status, r.body)
	}
	otto.get(base+"/items/"+pg.Items[0].Ref).json(t, &det)
	done := 0
	for _, c := range det.Criteria {
		if c.Done {
			done++
		}
	}
	if done != 3 {
		t.Fatalf("%d ticked: %+v", done, det.Criteria)
	}

	// An agent's token neither proposes nor accepts.
	var reg struct {
		Token string
	}
	body := `{"name":"docs","scope":{"kind":"installation"},"projects":["acme/shop"],"labels":["agent:docs"],"types":["task","issue"]}`
	ada.do("POST", base+"/agents", body).json(t, &reg)
	for path, b := range map[string]string{"/proposals": batch, "/proposals/accept": `{"scope":"project:acme/shop","source":"lint","groups":["docker"]}`} {
		if r := vic.do("POST", base+path, b, "Authorization", "Bearer "+reg.Token); r.status != 403 {
			t.Errorf("an agent called %s: %d %s", path, r.status, r.body)
		}
	}
	// Dismissing needs a reason.
	if r := otto.do("POST", base+"/proposals/dismiss", `{"scope":"project:acme/shop","source":"lint","groups":["docker"]}`); r.status != 400 {
		t.Fatalf("dismissed without a reason: %d %s", r.status, r.body)
	}
}

func hasLabel(ls []string, l string) bool {
	for _, x := range ls {
		if x == l {
			return true
		}
	}
	return false
}

// The repository health check on a repository with one file proposes the basics
// as one group, and running it twice does not duplicate.
func TestRepositoryHealthThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	base := e.srv.URL + "/api/v1/work"

	src := t.TempDir()
	date := "2026-06-01T10:00:00Z"
	gitAt(t, src, date, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "main.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")

	if r := vic.do("POST", base+"/health", `{"repository":"acme/shop"}`); r.status != 403 {
		t.Fatalf("a viewer ran the check: %d %s", r.status, r.body)
	}
	var res work.ProposeResult
	r := ada.do("POST", base+"/health", `{"repository":"acme/shop"}`)
	r.json(t, &res)
	if r.status != 200 || res.New != 6 || res.Waiting != 1 {
		t.Fatalf("health: %d %s", r.status, r.body)
	}
	var gs struct{ Groups []work.ProposalGroup }
	ada.get(base+"/proposals?scope=project:acme/shop").json(t, &gs)
	if len(gs.Groups) != 1 || gs.Groups[0].Group != "repository-hygiene" || gs.Groups[0].Source != "repository-health" || gs.Groups[0].Count != 6 {
		t.Fatalf("%s", ada.get(base+"/proposals?scope=project:acme/shop").body)
	}
	titles := ""
	for _, o := range gs.Groups[0].Items {
		titles += o.Title + "\n"
	}
	for _, want := range []string{"README", "license", "owners file", "pipeline file", "Protect the default branch", "Add tests"} {
		if !strings.Contains(titles, want) {
			t.Errorf("no finding about %q in:\n%s", want, titles)
		}
	}
	r = ada.do("POST", base+"/health", `{"repository":"acme/shop"}`)
	r.json(t, &res)
	if r.status != 200 || res.New != 0 || res.Updated != 6 {
		t.Fatalf("second run: %d %s", r.status, r.body)
	}
	ada.get(base+"/proposals?scope=project:acme/shop").json(t, &gs)
	if len(gs.Groups) != 1 || gs.Groups[0].Count != 6 {
		t.Fatalf("a second run duplicated: %+v", gs)
	}
}
