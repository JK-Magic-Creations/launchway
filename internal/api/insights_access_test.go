package api

import "testing"

// A person sees the Insights of the Flights they may read and no others: a Flight they cannot
// read is "not found", and the installation's numbers count only the Flights they can read.
func TestInsightsObeyPermissions(t *testing.T) {
	e := setupAuth(t, envOpts{})
	e.startRun(t, "pay-api")
	e.startRun(t, "web")
	type want struct {
		flight int // status asking for pay-api
		runs   int // runs counted in the installation scope
	}
	for user, w := range map[string]want{
		"nobody": {404, 0}, // no binding: sees nothing
		"vic":    {200, 1}, // viewer of pay-*
		"otto":   {200, 1}, // operator of pay-*
		"dana":   {404, 1}, // operator of web only
		"ada":    {200, 2}, // administrator
	} {
		b := e.signIn(t, user)
		if got := b.statusOf("GET", e.srv.URL+"/api/v1/insights?flight=pay-api", ""); got != w.flight {
			t.Errorf("%s: insights of pay-api gave %d, want %d", user, got, w.flight)
		}
		var out struct {
			Insights struct {
				Runs int `json:"runs"`
			} `json:"insights"`
		}
		r := b.get(e.srv.URL + "/api/v1/insights?from=2026-01-01&to=2028-01-01")
		if r.status != 200 {
			t.Fatalf("%s: installation insights gave %d", user, r.status)
		}
		r.json(t, &out)
		if out.Insights.Runs != w.runs {
			t.Errorf("%s: the installation scope counted %d runs, want %d", user, out.Insights.Runs, w.runs)
		}
	}
	if got := e.signIn(t, "ada").statusOf("GET", e.srv.URL+"/api/v1/insights?flight=pay-api&squadron=x", ""); got != 400 {
		t.Errorf("a Flight and a Squadron together gave %d, want 400", got)
	}
}
