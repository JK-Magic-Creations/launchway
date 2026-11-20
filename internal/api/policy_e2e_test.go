package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

const apiPolicies = `policies:
  - name: shape
    mode: report
    scope: { projects: ["pay-*"] }
    rules:
      max_timeout: 30m
  - name: prod-gate
    mode: enforce
    scope: { projects: ["pay-*"], environments: [prod, staging] }
    rules:
      tests: { min: 1 }
`

func policyEnv(t *testing.T, role auth.Role, bindings []auth.Binding) *authEnv {
	t.Helper()
	ps, err := policy.ParseFile([]byte(apiPolicies), "test.yaml", "config")
	if err != nil {
		t.Fatal(err)
	}
	o := envOpts{policies: engine.PolicyOptions{Inline: ps}, waiveRole: &role,
		environments: []engine.Environment{{Name: "prod", Target: "t", Projects: []string{"*"}}, {Name: "staging", Target: "t", Projects: []string{"*"}}}}
	if bindings != nil {
		p, err := auth.NewPolicy(bindings)
		if err != nil {
			t.Fatal(err)
		}
		o.policy = p
	}
	return setupAuth(t, o)
}

func waiverBody(rule, extra string) string {
	return `{"policy":"` + map[string]string{"max_timeout": "shape", "tests": "prod-gate"}[rule] + `","rule":"` + rule +
		`","reason":"hotfix","expires_in_hours":24` + extra + `}`
}

// Waiving needs the role policy.waivers.allow names (admin by default) on
// the project, and for a deploy rule on the environment; every grant and
// refusal is in the audit trail; a limited token never grants more.
func TestWaiverAccess(t *testing.T) {
	e := policyEnv(t, auth.Admin, nil)
	pay := e.startRun(t, "pay-api")
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	base := e.srv.URL + "/api/v1/projects/pay-api/waivers"

	if r := otto.do(http.MethodPost, base, waiverBody("max_timeout", "")); r.status != http.StatusForbidden ||
		!strings.Contains(string(r.body), "you need the admin role on pay-api to waive its policies") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r := ada.do(http.MethodPost, base, waiverBody("tests", `,"environment":"prod","run":"`+pay+`"`))
	if r.status != http.StatusCreated {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var w engine.WaiverView
	r.json(t, &w)
	if w.GrantedBy != "ada" || w.Environment != "prod" || w.Run != pay || w.State != "active" {
		t.Fatalf("%+v", w)
	}
	// Mistakes are refused as such.
	for body, want := range map[string]string{
		`{"policy":"nope","rule":"tests","reason":"x","expires_in_hours":1}`:          "no policy called",
		`{"policy":"prod-gate","rule":"sbom","reason":"x","expires_in_hours":1}`:      "has no rule",
		`{"policy":"prod-gate","rule":"tests","expires_in_hours":1}`:                  "say why",
		`{"policy":"prod-gate","rule":"tests","reason":"x","expires_in_hours":10000}`: "at most 30 days",
	} {
		if r := ada.do(http.MethodPost, base, body); r.status != http.StatusBadRequest || !strings.Contains(string(r.body), want) {
			t.Errorf("%s: %d %s", body, r.status, r.body)
		}
	}
	// A token of ada's limited to the viewer role cannot waive.
	_, tok, tr := ada.makeToken(e.srv.URL, `{"name":"ro","role":"viewer"}`)
	if tr.status != http.StatusCreated {
		t.Fatalf("%d %s", tr.status, tr.body)
	}
	if got := newBrowser(t).do(http.MethodPost, base, waiverBody("max_timeout", ""), bearer(tok)...).status; got != http.StatusForbidden {
		t.Fatalf("viewer token: %d", got)
	}

	// The run shows its decisions and the waivers that apply to it.
	var rp struct {
		Decisions []engine.PolicyDecision `json:"decisions"`
		Waivers   []engine.WaiverView     `json:"waivers"`
		CanWaive  bool                    `json:"can_waive"`
	}
	e.signIn(t, "vic").get(e.srv.URL+"/api/v1/runs/"+pay+"/policy").json(t, &rp)
	if len(rp.Decisions) != 1 || rp.Decisions[0].Point != policy.PointRun || len(rp.Waivers) != 1 || rp.CanWaive {
		t.Fatalf("%+v", rp)
	}
	// Lists show only waivers of projects the caller may see.
	var lw struct {
		Waivers       []engine.WaiverView `json:"waivers"`
		WaiveProjects []string            `json:"waive_projects"`
	}
	e.signIn(t, "dana").get(e.srv.URL+"/api/v1/waivers").json(t, &lw)
	if len(lw.Waivers) != 0 || len(lw.WaiveProjects) != 0 {
		t.Fatalf("dana sees %+v", lw)
	}
	ada.get(e.srv.URL+"/api/v1/waivers?active=1").json(t, &lw)
	if len(lw.Waivers) != 1 || len(lw.WaiveProjects) == 0 {
		t.Fatalf("ada sees %+v", lw)
	}
	// Revoking: refused to otto, done by ada.
	if r := otto.do(http.MethodPost, base+"/"+w.ID+"/revoke", `{"reason":"x"}`); r.status != http.StatusForbidden {
		t.Fatalf("%d", r.status)
	}
	if r := ada.do(http.MethodPost, base+"/"+w.ID+"/revoke", `{"reason":"fixed"}`); r.status != http.StatusOK {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, base+"/"+w.ID+"/revoke", `{}`); r.status != http.StatusConflict {
		t.Fatalf("%d %s", r.status, r.body)
	}
	got := map[string]int{}
	for _, a := range e.auditEntries(t) {
		if strings.HasPrefix(a.Action, "policy.") {
			got[a.Action+" "+a.User+" "+a.Outcome]++
			if a.Project != "pay-api" {
				t.Errorf("audit without the project: %+v", a)
			}
		}
	}
	for _, want := range []string{"policy.waive otto denied", "policy.waive ada ok", "policy.waive ada failed", "policy.revoke_waiver otto denied",
		"policy.revoke_waiver ada ok", "policy.revoke_waiver ada failed"} {
		if got[want] == 0 {
			t.Errorf("no audit entry %q in %v", want, got)
		}
	}
}

// With waivers.allow: operator, a binding limited to environments may
// waive deploy rules only there, and never for every environment.
func TestWaiverEnvironments(t *testing.T) {
	e := policyEnv(t, auth.Operator, []auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{Group: "pay-ops", Role: auth.Operator, Projects: []string{"pay-*"}, Environments: []string{"staging"}},
	})
	otto := e.signIn(t, "otto")
	base := e.srv.URL + "/api/v1/projects/pay-api/waivers"
	cases := []struct {
		body string
		want int
		msg  string
	}{
		{waiverBody("max_timeout", ""), http.StatusCreated, ""},
		{waiverBody("tests", `,"environment":"staging"`), http.StatusCreated, ""},
		{waiverBody("tests", `,"environment":"prod"`), http.StatusForbidden, "in environment prod"},
		{waiverBody("tests", ""), http.StatusForbidden, "name the environment"},
	}
	for _, c := range cases {
		r := otto.do(http.MethodPost, base, c.body)
		if r.status != c.want || !strings.Contains(string(r.body), c.msg) {
			t.Errorf("%s: %d %s", c.body, r.status, r.body)
		}
	}
	// waivers.allow: none turns waivers off for everyone.
	off := policyEnv(t, auth.NoRole, nil)
	if r := off.signIn(t, "ada").do(http.MethodPost, off.srv.URL+"/api/v1/projects/pay-api/waivers", waiverBody("max_timeout", "")); r.status != http.StatusForbidden ||
		!strings.Contains(string(r.body), "waivers are turned off") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// The Policy page's data: the policies in force and recent verdicts of
// runs the caller may see only.
func TestPolicyOverviewIsFiltered(t *testing.T) {
	e := policyEnv(t, auth.Admin, nil)
	e.startRun(t, "pay-api")
	var o engine.PolicyOverview
	e.signIn(t, "vic").get(e.srv.URL+"/api/v1/policies").json(t, &o)
	if len(o.Sources) != 1 || len(o.Sources[0].Policies) != 2 || len(o.Recent) != 1 || len(o.Catalogue) != len(policy.Catalogue) {
		t.Fatalf("%+v", o)
	}
	e.signIn(t, "dana").get(e.srv.URL+"/api/v1/policies").json(t, &o)
	if len(o.Recent) != 0 {
		t.Fatalf("dana sees %+v", o.Recent)
	}
}
