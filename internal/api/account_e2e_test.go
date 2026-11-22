package api

import (
	"strings"
	"testing"
)

// The account summary names the real areas only, lists tokens with their limits and the browsers signed in, and a
// browser can be ended by its id or all the others at once.
func TestAccountSummaryAndSessions(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	vic := e.signIn(t, "vic")
	second := e.signIn(t, "vic") // another browser of the same person
	other := e.signIn(t, "otto")
	tok := e.tokenFor(t, "vic", "ci")
	_ = tok
	u := e.srv.URL + "/api/v1/account"

	var sum struct {
		Profile  map[string]any  `json:"profile"`
		Features map[string]bool `json:"features"`
		Tokens   struct {
			Items   []struct{ Name string } `json:"items"`
			MaxDays int                     `json:"max_days"`
		} `json:"tokens"`
		SSHKeys  []any                    `json:"ssh_keys"`
		Second   struct{ Available bool } `json:"second_factor"`
		Sessions []sessionOut             `json:"sessions"`
	}
	vic.get(u+"/summary").json(t, &sum)
	if sum.Profile["user"] != "vic" || !sum.Features["hosting"] || !sum.Features["ssh_keys"] || !sum.Features["tokens"] || sum.Features["registry"] || sum.Features["local_accounts"] {
		t.Fatalf("profile and features: %+v", sum)
	}
	if len(sum.Tokens.Items) != 1 || sum.Tokens.Items[0].Name != "ci" || sum.Tokens.MaxDays == 0 || sum.SSHKeys == nil || sum.Second.Available {
		t.Fatalf("tokens, keys, second factor: %+v", sum)
	}
	// Three: this browser, the second one, and the one the token was made through.
	if len(sum.Sessions) != 3 {
		t.Fatalf("sessions: %+v", sum.Sessions)
	}
	cur := 0
	for _, s := range sum.Sessions {
		if s.Current {
			cur++
		}
		if len(s.ID) != 12 {
			t.Fatalf("a session id: %q", s.ID)
		}
	}
	if cur != 1 {
		t.Fatalf("which session is this one: %+v", sum.Sessions)
	}
	if strings.Contains(string(vic.get(u+"/summary").body), "id_hash") {
		t.Fatal("a session hash was sent")
	}

	// Ending one by id: another person cannot, a short prefix does nothing, the right id ends exactly that one.
	var mine struct {
		Items []sessionOut `json:"items"`
	}
	vic.get(u+"/sessions").json(t, &mine)
	var notMine sessionOut
	for _, s := range mine.Items {
		if !s.Current {
			notMine = s
		}
	}
	if r := other.do("DELETE", u+"/sessions/"+notMine.ID, ``); r.status != 404 {
		t.Fatalf("another person ended a session: %d", r.status)
	}
	if r := vic.do("DELETE", u+"/sessions/"+notMine.ID[:4], ``); r.status != 404 {
		t.Fatalf("a short prefix: %d", r.status)
	}
	if r := vic.do("DELETE", u+"/sessions/"+notMine.ID, ``); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	_ = second
	vic.get(u+"/sessions").json(t, &mine)
	if len(mine.Items) != 2 {
		t.Fatalf("after ending one: %+v", mine.Items)
	}

	// End all the others: a third browser goes, this one stays.
	third := e.signIn(t, "vic")
	vic.get(u+"/sessions").json(t, &mine)
	if len(mine.Items) != 3 {
		t.Fatalf("a new browser: %+v", mine.Items)
	}
	var ended struct{ Ended int }
	vic.do("POST", u+"/sessions/end-others", ``).json(t, &ended)
	if ended.Ended != 2 {
		t.Fatalf("ended %d", ended.Ended)
	}
	if r := third.get(e.srv.URL + "/api/v1/me"); r.status != 401 {
		t.Fatalf("the other browser still signed in: %d", r.status)
	}
	if r := vic.get(e.srv.URL + "/api/v1/me"); r.status != 200 {
		t.Fatalf("this browser was signed out: %d", r.status)
	}
	// Somebody else's sessions were not touched.
	if r := other.get(e.srv.URL + "/api/v1/me"); r.status != 200 {
		t.Fatalf("another person's browser was signed out: %d", r.status)
	}
}

// A public profile shows only the name and the activity on what the viewer may read: never a private repository's.
func TestPublicProfileShowsOnlyWhatTheViewerMayRead(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	repos := e.srv.URL + "/api/v1/repositories"
	ada.do("POST", repos, `{"owner":"acme","name":"shop","visibility":"private"}`)
	ada.do("POST", repos, `{"owner":"other","name":"secret","visibility":"private"}`)
	tok := e.tokenFor(t, "ada", "t")
	for _, path := range []string{"acme/shop", "other/secret"} {
		src := workingCopy(t)
		mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/"+path+".git", tok), "HEAD:refs/heads/main")
	}
	var prof struct {
		Name     string       `json:"name"`
		Activity activityPage `json:"activity"`
	}
	raw := vic.get(e.srv.URL + "/api/v1/people/ada")
	raw.json(t, &prof)
	if raw.status != 200 || prof.Name != "ada" || len(prof.Activity.Items) != 1 || prof.Activity.Items[0].Repository != "acme/shop" {
		t.Fatalf("what vic sees of ada: %d %s", raw.status, raw.body)
	}
	if strings.Contains(string(raw.body), "secret") || strings.Contains(string(raw.body), "email") || strings.Contains(string(raw.body), "groups") {
		t.Fatalf("a private field or repository in the profile: %s", raw.body)
	}
	ada.get(e.srv.URL+"/api/v1/people/ada").json(t, &prof)
	if len(prof.Activity.Items) != 2 {
		t.Fatalf("what ada sees of ada: %+v", prof.Activity.Items)
	}
	// Another person's pushes are not in ada's profile, and an unknown person is not found.
	if r := vic.get(e.srv.URL + "/api/v1/people/zz-unknown"); r.status != 404 {
		t.Fatalf("an unknown person: %d", r.status)
	}
	vic.get(e.srv.URL+"/api/v1/people/vic").json(t, &prof)
	if len(prof.Activity.Items) != 0 {
		t.Fatalf("vic did nothing: %+v", prof.Activity.Items)
	}
}

// A Squadron's team: roles, repositories and Flights, to those who may see its Flights; to nobody else.
func TestSquadronTeamFollowsThePermissions(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto, dana := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "dana")
	u := e.srv.URL
	if _, err := e.api.Settings.ImportBootstrap(); err != nil {
		t.Fatal(err)
	}
	ada.do("POST", u+"/api/v1/units", `{"name":"payments","display_name":"Payments"}`)
	for path, body := range map[string]string{
		"/api/v1/config/projects/pay-api":       `{"unit":"payments","spec":{"repo":"/tmp/x/pay"}}`,
		"/api/v1/config/role-bindings/otto-ops": `{"unit":"payments","spec":{"user":"otto","role":"operator"}}`,
		"/api/v1/config/role-bindings/viewers":  `{"unit":"payments","spec":{"group":"pay-view","role":"viewer"}}`,
	} {
		if r := ada.do("PUT", u+path, body); r.status != 200 && r.status != 201 {
			t.Fatalf("%s: %d %s", path, r.status, r.body)
		}
	}
	var team struct {
		Display string                  `json:"display_name"`
		Members map[string][]personRole `json:"members"`
		Flights []flightRow             `json:"flights"`
		Repos   []map[string]string     `json:"repositories"`
	}
	r := ada.get(u + "/api/v1/units/payments/team")
	r.json(t, &team)
	if r.status != 200 || team.Display != "Payments" || len(team.Flights) != 1 || len(team.Members["operator"]) != 1 || team.Members["operator"][0].Who != "otto" ||
		len(team.Members["viewer"]) != 1 || team.Members["viewer"][0].Who != "group pay-view" || team.Repos == nil {
		t.Fatalf("the team: %d %s", r.status, r.body)
	}
	if r := otto.get(u + "/api/v1/units/payments/team"); r.status != 200 {
		t.Fatalf("a person who sees the Flights: %d", r.status)
	}
	if r := dana.get(u + "/api/v1/units/payments/team"); r.status != 404 {
		t.Fatalf("a person who sees none of its Flights learned of it: %d %s", r.status, r.body)
	}
	if r := ada.get(u + "/api/v1/units/nothing/team"); r.status != 404 {
		t.Fatalf("a Squadron that is not there: %d", r.status)
	}
}
