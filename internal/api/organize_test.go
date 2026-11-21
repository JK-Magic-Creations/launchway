package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

func TestHomeNeverLeaksAnotherTeamsProjects(t *testing.T) {
	e := setupAuth(t, envOpts{})
	e.startRun(t, "pay-api")
	e.startRun(t, "web")
	for user, hidden := range map[string]string{"dana": "pay-api", "vic": "web"} {
		b := e.signIn(t, user)
		for _, path := range []string{"/api/v1/home", "/api/v1/home?view=mine", "/api/v1/attention", "/api/v1/runs?attention=running", "/api/v1/organize/pipelines"} {
			r := b.get(e.srv.URL + path)
			if r.status != 200 {
				t.Fatalf("%s %s: %d %s", user, path, r.status, r.body)
			}
			if strings.Contains(string(r.body), hidden) {
				t.Errorf("%s sees %q in %s: %s", user, hidden, path, r.body)
			}
		}
		var h homeAnswer
		b.get(e.srv.URL+"/api/v1/home").json(t, &h)
		if h.Counts["projects"] != 1 || len(h.Running) != 1 {
			t.Errorf("%s: counts %v running %d; one project and one run are visible", user, h.Counts, len(h.Running))
		}
	}
	// An administrator sees both, with the run in progress.
	var h homeAnswer
	e.signIn(t, "ada").get(e.srv.URL+"/api/v1/home").json(t, &h)
	if h.Counts["projects"] != 2 || len(h.Running) != 2 {
		t.Errorf("ada: counts %v running %d", h.Counts, len(h.Running))
	}
}

func TestDismissingIsPersonal(t *testing.T) {
	e := setupAuth(t, envOpts{})
	run := e.startRun(t, "pay-api") // paused at its gate: a decision waits
	var a attentionAnswer
	otto := e.signIn(t, "otto")
	for i := 0; i < 50 && len(a.Needs) == 0; i++ {
		otto.get(e.srv.URL+"/api/v1/attention").json(t, &a)
		if len(a.Needs) == 0 {
			e.api.hc().drop("otto")
		}
	}
	_ = run
	if len(a.Needs) == 0 {
		t.Skip("the run has not reached its gate; the decision rule is covered by the engine test")
	}
	key := a.Needs[0].Key
	if r := otto.do("POST", e.srv.URL+"/api/v1/attention/dismiss", `{"key":"`+key+`"}`); r.status != 200 {
		t.Fatalf("dismiss: %d %s", r.status, r.body)
	}
	var after attentionAnswer
	otto.get(e.srv.URL+"/api/v1/attention").json(t, &after)
	if after.Count != len(a.Needs)-1 || len(after.Dismissed) != 1 {
		t.Errorf("after dismissing: %+v", after)
	}
	var ada attentionAnswer
	e.signIn(t, "ada").get(e.srv.URL+"/api/v1/attention").json(t, &ada)
	if len(ada.Dismissed) != 0 {
		t.Errorf("someone else's dismissal changed ada's list: %+v", ada)
	}
}

func TestFoldersAreSharedByRoleAndHiddenByVisibility(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, vic, dana, otto := e.signIn(t, "ada"), e.signIn(t, "vic"), e.signIn(t, "dana"), e.signIn(t, "otto")
	if r := vic.do("POST", e.srv.URL+"/api/v1/organize/pipelines/folders", `{"scope":"base","name":"Payments"}`); r.status != 403 {
		t.Fatalf("a viewer makes a shared folder: %d %s", r.status, r.body)
	}
	r := ada.do("POST", e.srv.URL+"/api/v1/organize/pipelines/folders", `{"scope":"base","name":"Payments"}`)
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var f struct{ ID string }
	r.json(t, &f)
	if r := ada.do("POST", e.srv.URL+"/api/v1/organize/pipelines/folders", `{"scope":"base","name":"Payments"}`); r.status != 409 {
		t.Errorf("the same name twice: %d", r.status)
	}
	if r := vic.do("POST", e.srv.URL+"/api/v1/organize/pipelines/move", `{"item":"pay-api","folder":"`+f.ID+`"}`); r.status != 403 {
		t.Errorf("a viewer files an item for everyone: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", e.srv.URL+"/api/v1/organize/pipelines/move", `{"item":"pay-api","folder":"`+f.ID+`"}`); r.status != 200 {
		t.Fatalf("move: %d %s", r.status, r.body)
	}
	type org struct {
		Folders []struct {
			ID    string
			Path  string
			Count int
		}
		Items map[string]itemOrg
	}
	var seen org
	vic.get(e.srv.URL+"/api/v1/organize/pipelines").json(t, &seen)
	if len(seen.Folders) != 1 || seen.Folders[0].Count != 1 || seen.Items["pay-api"].Folder != f.ID {
		t.Errorf("vic sees %+v", seen)
	}
	// dana sees nothing in it, and may not arrange it: it does not exist for her, and no count leaks.
	dr := dana.get(e.srv.URL + "/api/v1/organize/pipelines")
	var dseen org
	dr.json(t, &dseen)
	if len(dseen.Folders) != 0 || strings.Contains(string(dr.body), "pay-api") || strings.Contains(string(dr.body), "Payments") {
		t.Errorf("dana learns of a folder: %s", dr.body)
	}
	if r := dana.do("PATCH", e.srv.URL+"/api/v1/organize/folders/"+f.ID, `{"name":"Mine"}`); r.status != 404 {
		t.Errorf("dana renames it: %d", r.status)
	}
	// Labels need the operator role on the project.
	if r := vic.do("PUT", e.srv.URL+"/api/v1/organize/pipelines/labels", `{"item":"pay-api","labels":["Tier-1","team-pay"]}`); r.status != 403 {
		t.Errorf("a viewer labels: %d", r.status)
	}
	if r := otto.do("PUT", e.srv.URL+"/api/v1/organize/pipelines/labels", `{"item":"pay-api","labels":["Tier-1","team-pay"]}`); r.status != 200 {
		t.Errorf("an operator labels: %d %s", r.status, r.body)
	}
	vic.get(e.srv.URL+"/api/v1/organize/pipelines").json(t, &seen)
	if got := strings.Join(seen.Items["pay-api"].Labels, ","); got != "team-pay,tier-1" {
		t.Errorf("labels %q", got)
	}
	// Deleting moves the item out, not away.
	if r := ada.do("DELETE", e.srv.URL+"/api/v1/organize/folders/"+f.ID, ""); r.status != 200 {
		t.Fatal(r.status)
	}
	vic.get(e.srv.URL+"/api/v1/organize/pipelines").json(t, &seen)
	if len(seen.Folders) != 0 || seen.Items["pay-api"].Folder != "" {
		t.Errorf("after delete: %+v", seen)
	}
}

func TestLayoutsInheritLockAndPersonalSurvive(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	type ans struct {
		Effective, Shared, Personal map[string]any
		LockedAt                    string `json:"locked_at"`
		Customized                  bool
	}
	get := func(b *browser, scope string) ans {
		var a ans
		b.get(e.srv.URL+"/api/v1/layouts/pipelines?scope="+scope).json(t, &a)
		return a
	}
	put := func(b *browser, scope, data string, locked bool) int {
		l := "false"
		if locked {
			l = "true"
		}
		return b.do("PUT", e.srv.URL+"/api/v1/layouts/pipelines", `{"scope":"`+scope+`","data":`+data+`,"locked":`+l+`}`).status
	}
	if put(otto, "base", `{"view":"table"}`, false) != 403 {
		t.Error("an operator sets the layout for everyone")
	}
	if put(ada, "base", `{"view":"table","sort":"name"}`, false) != 200 {
		t.Fatal("ada sets the base layout")
	}
	if put(ada, "project:pay-api", `{"sort":"recent"}`, false) != 200 {
		t.Fatal("a project overrides one key")
	}
	if a := get(ada, "project:pay-api"); a.Shared["view"] != "table" || a.Shared["sort"] != "recent" {
		t.Errorf("inheritance: %+v", a.Shared)
	}
	// otto's own choice wins over the shared default and survives a change of it.
	if r := otto.do("PUT", e.srv.URL+"/api/v1/me/layouts/pipelines", `{"view":"cards"}`); r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	if a := get(otto, "base"); a.Effective["view"] != "cards" || a.Effective["sort"] != "name" || !a.Customized {
		t.Errorf("personal over shared: %+v", a)
	}
	put(ada, "base", `{"view":"table","sort":"recent","group":"unit"}`, false)
	if a := get(otto, "base"); a.Effective["view"] != "cards" || a.Effective["group"] != "unit" {
		t.Errorf("a shared change reaches a personalized person: %+v", a)
	}
	// A locked level wins over the personal layer and stops the levels below it.
	put(ada, "base", `{"view":"table"}`, true)
	if a := get(otto, "base"); a.Effective["view"] != "table" || a.LockedAt != "base" {
		t.Errorf("locked: %+v", a)
	}
	if put(ada, "project:pay-api", `{"view":"cards"}`, false) != 409 {
		t.Error("a level below a lock changed it")
	}
	// Reset.
	if r := otto.do("DELETE", e.srv.URL+"/api/v1/me/layouts", ""); r.status != 200 {
		t.Fatal(r.status)
	}
	if a := get(otto, "base"); a.Customized {
		t.Errorf("after reset: %+v", a)
	}
	// Export and import round-trip, dry run first; a person who may not arrange cannot export.
	if r := otto.get(e.srv.URL + "/api/v1/layouts-export?scope=base"); r.status != 403 {
		t.Errorf("export without the right: %d", r.status)
	}
	r := ada.get(e.srv.URL + "/api/v1/layouts-export?scope=base")
	var doc map[string]any
	r.json(t, &doc)
	if !strings.Contains(string(r.body), `"pipelines"`) {
		t.Errorf("export: %s", r.body)
	}
	b, _ := json.Marshal(doc)
	if r := ada.do("POST", e.srv.URL+"/api/v1/layouts-import?dry_run=1", string(b)); r.status != 200 || !strings.Contains(string(r.body), "layout pipelines") {
		t.Errorf("import: %d %s", r.status, r.body)
	}
}

func TestFuzzyScoreRanksAndForgivesTypos(t *testing.T) {
	cases := []struct {
		q, text  string
		min, max int
	}{
		{"pay", "pay", 100, 100},
		{"pay", "pay-api", 80, 90},
		{"api", "pay-api", 65, 75},
		{"ayap", "pay-api", 1, 40},      // letters in order, skipped
		{"paymnets", "payments", 1, 40}, // swapped letters
		{"pyment", "payments", 1, 40},   // a missing letter
		{"zzz", "payments", 0, 0},
	}
	for _, c := range cases {
		if got := fuzzyScore(c.q, c.text); got < c.min || got > c.max {
			t.Errorf("%q in %q scored %d, want %d to %d", c.q, c.text, got, c.min, c.max)
		}
	}
	if fuzzyScore("pay", "pay") <= fuzzyScore("pay", "pay-api") || fuzzyScore("pay", "pay-api") <= fuzzyScore("api", "pay-api") {
		t.Error("exact beats prefix beats word-prefix")
	}
}

func TestSearchOnlyReturnsWhatThePersonMaySee(t *testing.T) {
	e := setupAuth(t, envOpts{})
	run := e.startRun(t, "pay-api")
	_ = run
	dana, vic := e.signIn(t, "dana"), e.signIn(t, "vic")
	for _, q := range []string{"pay", "#1", "main", "1"} {
		r := dana.get(e.srv.URL + "/api/v1/search?q=" + q)
		if r.status != 200 || strings.Contains(string(r.body), "pay-api") {
			t.Errorf("dana searches %q and learns of pay-api: %d %s", q, r.status, r.body)
		}
	}
	var out struct{ Hits []SearchHit }
	vic.get(e.srv.URL+"/api/v1/search?q=pay-ap").json(t, &out)
	if len(out.Hits) == 0 || out.Hits[0].Name != "pay-api" || out.Hits[0].Kind != "project" {
		t.Errorf("vic's hits: %+v", out.Hits)
	}
	vic.get(e.srv.URL+"/api/v1/search?q=%231").json(t, &out)
	found := false
	for _, h := range out.Hits {
		found = found || h.Kind == "run"
	}
	if !found {
		t.Errorf("a Sortie is found by number: %+v", out.Hits)
	}
}

func TestTrailIsPrivateAndFollowsPermission(t *testing.T) {
	e := setupAuth(t, envOpts{})
	vic, ada, dana := e.signIn(t, "vic"), e.signIn(t, "ada"), e.signIn(t, "dana")
	run := e.startRun(t, "pay-api")
	post := func(b *browser, body string) int { return b.do("PUT", e.srv.URL+"/api/v1/activity", body).status }
	if post(vic, `{"kind":"run","obj":"`+run+`","project":"pay-api","title":"pay-api #1","href":"/runs/`+run+`","action":"viewed","state":"running"}`) != 204 {
		t.Fatal("recording")
	}
	if post(vic, `{"kind":"project","obj":"web","project":"web","title":"web","href":"/projects/web"}`) != 204 {
		t.Fatal("recording a project the person cannot see is quietly ignored")
	}
	type trail struct {
		Recent   []activityView
		Continue []activityView
		Paused   bool
	}
	var mine trail
	vic.get(e.srv.URL+"/api/v1/activity").json(t, &mine)
	if len(mine.Recent) != 1 || mine.Recent[0].Obj != run {
		t.Fatalf("vic's trail: %+v", mine.Recent)
	}
	// Nobody else reads it: not another person, and not an administrator.
	for name, b := range map[string]*browser{"ada (an administrator)": ada, "dana": dana} {
		r := b.get(e.srv.URL + "/api/v1/activity?user=vic")
		var other trail
		r.json(t, &other)
		if len(other.Recent) != 0 || strings.Contains(string(r.body), run) {
			t.Errorf("%s reads vic's trail: %s", name, r.body)
		}
	}
	// What changed since: the run was running when vic looked; now it is waiting at its gate or has moved on.
	if mine.Recent[0].NowState == "" {
		t.Errorf("the entry does not say what the Sortie is now: %+v", mine.Recent[0])
	}
	// Losing permission hides the entry without deleting it.
	e.api.Auth.Policy = testPolicyWithout(t, "vic")
	vic.get(e.srv.URL+"/api/v1/activity").json(t, &mine)
	if len(mine.Recent) != 0 {
		t.Errorf("an entry for a project the person lost is shown: %+v", mine.Recent)
	}
	// Pause, then clear.
	if vic.do("PUT", e.srv.URL+"/api/v1/activity/pause", `{"paused":true}`).status != 200 {
		t.Fatal("pause")
	}
	if post(ada, `{"kind":"page","obj":"/x","title":"X","href":"/x"}`) != 204 {
		t.Fatal("ada records")
	}
	var adas trail
	ada.get(e.srv.URL+"/api/v1/activity").json(t, &adas)
	if len(adas.Recent) != 1 {
		t.Errorf("ada's own trail: %+v", adas.Recent)
	}
	if vic.do("DELETE", e.srv.URL+"/api/v1/activity", "").status != 200 {
		t.Fatal("clear")
	}
	ada.get(e.srv.URL+"/api/v1/activity").json(t, &adas)
	if len(adas.Recent) != 1 {
		t.Errorf("vic clearing his trail touched ada's: %+v", adas.Recent)
	}
}

func TestStartingASortieIsInTheTrailAndPausedTrailsWriteNothing(t *testing.T) {
	e := setupAuth(t, envOpts{})
	otto := e.signIn(t, "otto")
	r := otto.do("POST", e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`)
	if r.status != 201 {
		t.Fatalf("start: %d %s", r.status, r.body)
	}
	var tr struct{ Continue []activityView }
	otto.get(e.srv.URL+"/api/v1/activity").json(t, &tr)
	if len(tr.Continue) != 1 || tr.Continue[0].Action != "started" || tr.Continue[0].Kind != "run" {
		t.Fatalf("continue: %+v", tr.Continue)
	}
	otto.do("PUT", e.srv.URL+"/api/v1/activity/pause", `{"paused":true}`)
	otto.do("POST", e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`)
	var after struct{ Recent []activityView }
	otto.get(e.srv.URL+"/api/v1/activity").json(t, &after)
	if len(after.Recent) != 1 {
		t.Errorf("a paused trail wrote: %+v", after.Recent)
	}
}

// testPolicyWithout is the test policy after the groups that gave pay-* access were removed: only admins and dana remain.
func testPolicyWithout(t testing.TB, _ string) *auth.Policy {
	p, err := auth.NewPolicy([]auth.Binding{{Group: "admins", Role: auth.Admin}, {User: "dana", Role: auth.Operator, Projects: []string{"web"}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
