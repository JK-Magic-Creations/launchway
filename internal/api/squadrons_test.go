package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// The story of a Squadron over HTTP: made with a name only, Flights filed in it, moved with a preview, a secret
// store that would answer from another place blocking a move, organized from name prefixes, and undone.
func TestSquadronsOverHTTP(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	u := e.srv.URL
	if _, err := e.api.Settings.ImportBootstrap(); err != nil {
		t.Fatal(err)
	}
	post := func(b *browser, path, body string) (int, string) {
		t.Helper()
		r := b.do("POST", u+path, body)
		return r.status, string(r.body)
	}
	put := func(path, body string) {
		t.Helper()
		if r := ada.do("PUT", u+path, body); r.status != 200 && r.status != 201 {
			t.Fatalf("%s: %d %s", path, r.status, r.body)
		}
	}

	// A name only; plain messages for a bad one; a second of the same name; a person who may not.
	if code, body := post(ada, "/api/v1/units", `{"name":"payments"}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := post(ada, "/api/v1/units", `{"name":"two words"}`); code != 400 || !strings.Contains(body, "letters, digits") {
		t.Fatalf("a bad name: %d %s", code, body)
	}
	if code, body := post(ada, "/api/v1/units", `{"name":"Payments"}`); code != 409 || !strings.Contains(body, "already exists") {
		t.Fatalf("a repeated name: %d %s", code, body)
	}
	if code, _ := post(otto, "/api/v1/units", `{"name":"other"}`); code != 403 {
		t.Fatalf("an operator made a Squadron: %d", code)
	}
	post(ada, "/api/v1/units", `{"name":"platform","display_name":"Platform","description":"shared tooling"}`)

	var list struct {
		Squadrons []squadronSummary `json:"squadrons"`
		Unfiled   []flightRow       `json:"unfiled"`
		May       bool              `json:"may_manage"`
	}
	otto.get(u+"/api/v1/units").json(t, &list)
	if len(list.Squadrons) != 2 || list.May {
		t.Fatalf("%+v", list)
	}
	var one struct {
		Flights []flightRow `json:"flights"`
		Display string      `json:"display_name"`
	}
	ada.get(u+"/api/v1/units/platform").json(t, &one)
	if one.Display != "Platform" || len(one.Flights) != 0 {
		t.Fatalf("%+v", one)
	}
	if r := ada.get(u + "/api/v1/units/nothing"); r.status != 404 {
		t.Fatalf("a missing Squadron: %d", r.status)
	}

	// Flights in the Squadron, and one outside.
	put("/api/v1/config/projects/pay-api", `{"unit":"payments","spec":{"repo":"/tmp/x/pay"}}`)
	// (pay-api is also a Flight the test server watches, so its pipeline file can be read.)
	put("/api/v1/config/projects/pay-web", `{"unit":"payments","spec":{"repo":"/tmp/x/payweb"}}`)
	put("/api/v1/config/projects/ledger-core", `{"spec":{"repo":"/tmp/x/ledger"}}`)
	put("/api/v1/config/projects/ledger-ui", `{"spec":{"repo":"/tmp/x/ledgerui"}}`)
	// A secret store defined for the Squadron payments only, and a binding that lets otto run Flights there.
	put("/api/v1/config/secret-stores/vault", `{"unit":"payments","spec":{"type":"vault"}}`)
	put("/api/v1/config/role-bindings/otto-ops", `{"unit":"payments","spec":{"user":"otto","role":"operator"}}`)

	// The preview changes nothing and names what changes.
	code, body := post(ada, "/api/v1/unit-moves/preview", `{"flights":["pay-api"],"to":"platform"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var pv struct {
		Plans []struct {
			Flight  string
			Blocked bool
			Access  []struct{ Who, Effect string }
			Stops   []struct{ Kind, Name string }
			Secrets []struct{ Store, Result string }
		}
		Blocked int
	}
	if err := json.Unmarshal([]byte(body), &pv); err != nil {
		t.Fatal(err)
	}
	p := pv.Plans[0]
	if p.Blocked || len(p.Access) != 1 || p.Access[0].Who != "user otto" || p.Access[0].Effect != "loses" || len(p.Stops) != 2 {
		t.Fatalf("%s", body)
	}
	ada.get(u+"/api/v1/units/payments").json(t, &one)
	if len(one.Flights) != 1 || one.Flights[0].Name != "pay-api" {
		t.Fatalf("%+v", one.Flights)
	}

	// A pipeline that names the store the Squadron defines: moving it away would leave the reference nowhere.
	text := func(string) (string, bool) {
		return "steps:\n  a:\n    image: x\n    secrets:\n      T: { store: vault, key: k }\n", true
	}
	plans, err := e.api.Settings.PlanMoves(t.Context(), []string{"pay-web"}, "platform", text)
	if err != nil || !plans[0].Blocked || plans[0].Secrets[0].Result != "none" {
		t.Fatalf("%v %+v", err, plans)
	}
	res, err := e.api.Settings.MoveFlights(t.Context(), "ada", []string{"pay-web"}, "platform", nil, "", text)
	if err != nil || len(res.Moved) != 0 || len(res.Blocked) != 1 {
		t.Fatalf("a blocked move went through: %v %+v", err, res)
	}

	// Without such a reference the move goes, in one change, and says how to undo it.
	code, body = post(ada, "/api/v1/unit-moves", `{"flights":["pay-api","ledger-core"],"to":"platform"}`)
	var mv struct {
		Moved []string
		Undo  map[string]string
	}
	if err := json.Unmarshal([]byte(body), &mv); err != nil || code != 200 || len(mv.Moved) != 2 || mv.Undo["pay-api"] != "payments" || mv.Undo["ledger-core"] != "" {
		t.Fatalf("%d %s", code, body)
	}
	lay, _ := e.api.Settings.Levels()
	if lay.ProjectUnit["pay-api"] != "platform" || lay.ProjectUnit["ledger-core"] != "platform" {
		t.Fatalf("%v", lay.ProjectUnit)
	}
	undo, _ := json.Marshal(map[string]any{"flights": []string{"pay-api", "ledger-core"}, "homes": mv.Undo})
	if code, body = post(ada, "/api/v1/unit-moves", string(undo)); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	lay, _ = e.api.Settings.Levels()
	if lay.ProjectUnit["pay-api"] != "payments" || lay.ProjectUnit["ledger-core"] != "" {
		t.Fatalf("undo: %v", lay.ProjectUnit)
	}

	// Organize: ledger-core and ledger-ui share a prefix.
	if r := otto.get(u + "/api/v1/unit-organize"); r.status != 403 {
		t.Fatalf("an operator read the proposal: %d", r.status)
	}
	var prop struct {
		Groups []organizeGroupView
	}
	ada.get(u+"/api/v1/unit-organize").json(t, &prop)
	if len(prop.Groups) != 1 || prop.Groups[0].Name != "ledger" || len(prop.Groups[0].Flights) != 2 || prop.Groups[0].Source != "prefix" {
		t.Fatalf("%+v", prop)
	}
	code, body = post(ada, "/api/v1/unit-organize", `{"groups":[{"name":"ledger","flights":["ledger-core","ledger-ui"]}]}`)
	var org struct {
		Created    []string          `json:"created"`
		Moved      []string          `json:"moved"`
		Undo       map[string]string `json:"undo"`
		UndoDelete []string          `json:"undo_delete"`
	}
	if err := json.Unmarshal([]byte(body), &org); err != nil || code != 200 || len(org.Created) != 1 || len(org.Moved) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	lay, _ = e.api.Settings.Levels()
	if lay.ProjectUnit["ledger-ui"] != "ledger" {
		t.Fatalf("%v", lay.ProjectUnit)
	}
	ub, _ := json.Marshal(map[string]any{"homes": org.Undo, "delete_units": org.UndoDelete})
	if code, body = post(ada, "/api/v1/unit-organize/undo", string(ub)); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	lay, _ = e.api.Settings.Levels()
	for _, un := range lay.Units {
		if un.Name == "ledger" {
			t.Fatalf("the Squadron the organize made is still there")
		}
	}
	if lay.ProjectUnit["ledger-ui"] != "" {
		t.Fatalf("%v", lay.ProjectUnit)
	}
}
