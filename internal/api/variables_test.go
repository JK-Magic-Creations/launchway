package api

import (
	"time"

	"github.com/jk-magic-creations/launchway/internal/togglestore"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// testVariables is the variable service of a test server: pay-api and web are
// projects, pay-api in the unit pay.
func testVariables(eng *engine.Engine) *settings.VariableService {
	lay := &settings.Layout{Levels: settings.DefaultLevels, Units: []settings.UnitView{{Name: "pay", Projects: []string{"pay-api"}}},
		ProjectUnit: map[string]string{"pay-api": "pay", "web": "", "ledger": "pay"}}
	return settings.NewVariableService(settings.NewVariableStore(eng), func() (*settings.Layout, error) { return lay, nil })
}

// The whole story over HTTP: set at the base, override at a project, lock at the
// base and see the override refused, and the effective view tell it all.
func TestVariablesOverHTTP(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto, dana := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "dana")
	u := e.srv.URL
	put := func(b *browser, path, body string) (int, string) {
		t.Helper()
		r := b.do("PUT", u+path, body)
		return r.status, string(r.body)
	}
	if code, body := put(ada, "/api/v1/variables/replicas", `{"level":"installation","type":"number","value":1}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := put(otto, "/api/v1/variables/replicas", `{"level":"project","scope":"pay-api","type":"number","value":2}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	// An operator may not set at the base, nor lock.
	if code, _ := put(otto, "/api/v1/variables/replicas", `{"level":"installation","type":"number","value":9}`); code != 403 {
		t.Fatalf("an operator set at the base: %d", code)
	}
	var v settings.VariableView
	ada.get(u+"/api/v1/projects/pay-api/variables").json(t, &v)
	if len(v.Variables) != 1 || v.Variables[0].Display != "2" || len(v.Variables[0].Chain) != 2 || !strings.Contains(v.Rule, "The nearest setting wins") {
		t.Fatalf("%+v", v)
	}
	if code, body := put(ada, "/api/v1/variables/replicas/lock", `{"level":"installation","reason":"fleet rule"}`); code != 200 || !strings.Contains(body, `"scope":"pay-api"`) {
		t.Fatalf("%d %s", code, body)
	}
	code, body := put(otto, "/api/v1/variables/replicas", `{"level":"project","scope":"pay-api","environment":"production","type":"number","value":5}`)
	if code != 409 || !strings.Contains(body, "locked by the base (fleet rule). Only an administrator of every project can change it.") {
		t.Fatalf("the override below the lock: %d %s", code, body)
	}
	ada.get(u+"/api/v1/projects/pay-api/variables").json(t, &v)
	if e := v.Variables[0]; e.Display != "1" || e.Locked == nil || e.CanOverride {
		t.Fatalf("%+v", e)
	}
	// Someone outside pay-api sees nothing of it; secrets are references.
	if code, _ := put(dana, "/api/v1/variables/tok", `{"level":"project","scope":"web","type":"secret","value":"hunter2"}`); code != 400 {
		t.Fatalf("a secret written as text: %d", code)
	}
	if code, body := put(dana, "/api/v1/variables/tok", `{"level":"project","scope":"web","type":"secret","value":{"store":"vault","key":"a/b"}}`); code != 200 || strings.Contains(body, "hunter2") {
		t.Fatalf("%d %s", code, body)
	}
}

// testToggles is the toggle service of a test server: the same units as the variables, production protected.
func testToggles(eng *engine.Engine) *togglestore.Service {
	return togglestore.New(togglestore.Deps{DB: eng.Database(), CheckEvery: time.Millisecond,
		UnitOf: func(p string) string {
			if p == "pay-api" || p == "ledger" {
				return "pay"
			}
			return ""
		},
		Environment: func(n string) togglestore.Env { return togglestore.Env{Known: true, Protected: n == "production"} }})
}
