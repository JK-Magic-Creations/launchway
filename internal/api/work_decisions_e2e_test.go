package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/work"
)

// A decision is made, answered by a person and replaced; its record is written into the repository's docs folder
// through a pull request, and once that is merged a second export finds nothing to do.
func TestDecisionRecordsAndTheirExport(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	vic, otto := e.signIn(t, "vic"), e.signIn(t, "otto")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/main")
	base := e.srv.URL + "/api/v1/work"
	repoAPI := e.srv.URL + "/api/v1/repositories/acme/shop"

	var d, task work.Item
	otto.do("POST", base+"/items", `{"title":"Use PostgreSQL","type":"decision","repository":"acme/shop","body":"## Options\n\n- PostgreSQL"}`).json(t, &d)
	otto.do("POST", base+"/items", `{"title":"Migrate","repository":"acme/shop"}`).json(t, &task)
	if d.Type != "decision" || !strings.Contains(d.Body, "PostgreSQL") {
		t.Fatalf("%+v", d)
	}
	if r := otto.do("POST", base+"/items/"+d.Ref+"/links", `{"kind":"decides","to":"`+task.Ref+`"}`); r.status != 201 && r.status != 200 {
		t.Fatalf("decides link: %d %s", r.status, r.body)
	}
	// Proposed: not exported yet; a viewer may neither answer nor export.
	if r := vic.do("POST", base+"/items/"+d.Ref+"/decision/accept", `{"answer":"x"}`); r.status != 403 {
		t.Fatalf("a viewer answered: %d %s", r.status, r.body)
	}
	if r := vic.do("POST", repoAPI+"/decisions/export", `{}`); r.status != 403 {
		t.Fatalf("a viewer exported: %d", r.status)
	}
	var out map[string]any
	if r := otto.do("POST", repoAPI+"/decisions/export", `{}`); r.status != 200 {
		t.Fatalf("with nothing decided: %d %s", r.status, r.body)
	} else {
		r.json(t, &out)
	}
	if out["changed"].(float64) != 0 {
		t.Fatalf("a proposal was exported: %v", out)
	}

	var v work.DecisionView
	r := otto.do("POST", base+"/items/"+d.Ref+"/decision/accept", `{"answer":"PostgreSQL, one per environment."}`)
	r.json(t, &v)
	if r.status != 200 || v.Status != "accepted" || v.DecidedBy != "otto" || len(v.Decides) != 1 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	vic.get(base+"/items/"+d.Ref+"/decision").json(t, &v)
	if v.Answer == "" {
		t.Fatalf("a viewer reads the record: %+v", v)
	}
	var list struct{ Items []work.DecisionRow }
	vic.get(base+"/decisions?repository=acme/shop&status=accepted").json(t, &list)
	if len(list.Items) != 1 || list.Items[0].Item != d.Ref {
		t.Fatalf("list: %+v", list)
	}
	if r := vic.get(base + "/decisions?status=maybe"); r.status != 400 {
		t.Fatalf("a bad status: %d", r.status)
	}

	// The export: a branch, a file, a pull request.
	var ex struct {
		Changed int      `json:"changed"`
		Branch  string   `json:"branch"`
		Files   []string `json:"files"`
		Pull    int      `json:"pull"`
	}
	r = otto.do("POST", repoAPI+"/decisions/export", `{}`)
	r.json(t, &ex)
	if r.status != 201 || ex.Changed != 1 || ex.Pull != 1 || len(ex.Files) != 1 || !strings.HasPrefix(ex.Files[0], "docs/decisions/"+strings.ToLower(d.Ref)+"-use-postgresql") {
		t.Fatalf("export: %d %s", r.status, r.body)
	}
	var file struct {
		Content string `json:"content"`
	}
	vic.get(repoAPI+"/files?ref="+ex.Branch+"&path="+ex.Files[0]).json(t, &file)
	for _, want := range []string{"# " + d.Ref + ": Use PostgreSQL", "Status: Accepted", "by otto", "PostgreSQL, one per environment.", "- " + task.Ref + " Migrate"} {
		if !strings.Contains(file.Content, want) {
			t.Errorf("the file lacks %q:\n%s", want, file.Content)
		}
	}
	if r := otto.do("POST", repoAPI+"/decisions/export", `{"folder":"../outside"}`); r.status != 400 {
		t.Fatalf("a folder outside the repository: %d", r.status)
	}

	// Merge it: the same export is then up to date.
	prURL := repoAPI + "/pulls/1"
	waitMergeable(t, otto, prURL, pulls.MergeClean)
	if r := otto.do("POST", prURL+"/merge", `{}`); r.status != 200 {
		t.Fatalf("merge: %d %s", r.status, r.body)
	}
	r = otto.do("POST", repoAPI+"/decisions/export", `{}`)
	r.json(t, &out)
	if r.status != 200 || out["changed"].(float64) != 0 {
		t.Fatalf("a second export after the merge: %d %s", r.status, r.body)
	}
}
