package api

import (
	"net/http"
	"strings"
	"testing"
)

// Anyone signed in reads the installation's limits on pipelines; only an
// administrator changes them, and every change and refusal is audited.
func TestRunLimitsAreReadByAllChangedByAdminsAndAudited(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	url := e.srv.URL + "/api/v1/run-limits"
	if r := otto.get(url); r.status != 200 || !strings.Contains(string(r.body), `"max_run_timeout":""`) {
		t.Fatalf("get: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodPut, url, `{"max_run_timeout":"1h"}`); r.status != 403 {
		t.Fatalf("an operator changed the limits: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPut, url, `{"max_run_timeout":"48h"}`); r.status != 200 || !strings.Contains(string(r.body), `"max_run_timeout":"48h"`) {
		t.Fatalf("set: %d %s", r.status, r.body)
	}
	for _, bad := range []string{`{"max_run_timeout":"soon"}`, `{"max_run_timeout":"10s"}`, `{"max_run_timeout":"900h"}`} {
		if r := ada.do(http.MethodPut, url, bad); r.status != 400 {
			t.Errorf("%s: %d %s, want 400", bad, r.status, r.body)
		}
	}
	if r := otto.get(url); !strings.Contains(string(r.body), `"max_run_timeout":"48h"`) || !strings.Contains(string(r.body), `"updated_by":"ada"`) {
		t.Fatalf("a refused change altered the limits: %s", r.body)
	}
	var ok, denied int
	for _, a := range e.auditEntries(t) {
		if a.Action != "run_limits.set" {
			continue
		}
		switch a.Outcome {
		case "ok":
			ok++
			if a.User != "ada" || !strings.Contains(a.Detail, `max_run_timeout="" -> max_run_timeout="48h"`) {
				t.Errorf("audit: %+v", a)
			}
		case "denied":
			denied++
		}
	}
	if ok != 1 || denied != 1 {
		t.Fatalf("audit: %d ok, %d denied", ok, denied)
	}
}

func TestRunLimitsOverMCP(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	ada := mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "admin"), nil, nil)
	mustCall(t, ada, "set_run_limits", map[string]any{"max_run_timeout": "6h"})
	if got := asJSON(mustCall(t, ada, "get_run_limits", nil)); !strings.Contains(got, `"max_run_timeout":"6h"`) {
		t.Fatalf("%s", got)
	}
	if _, errText := call(t, ada, "set_run_limits", map[string]any{"max_run_timeout": "soon"}); errText == "" {
		t.Fatal("a duration that does not read was accepted")
	}
	otto := mcpSession(t, e.mcpURL(), e.tokenFor(t, "otto", "op"), nil, nil)
	if _, errText := call(t, otto, "set_run_limits", map[string]any{"max_run_timeout": "1h"}); !strings.Contains(errText, "unknown tool") {
		t.Fatalf("an operator was offered set_run_limits: %q", errText)
	}
	found := false
	for _, a := range e.auditEntries(t) {
		found = found || a.Action == "run_limits.set" && a.Outcome == "ok" && strings.Contains(a.Detail, "via MCP")
	}
	if !found {
		t.Error("no audit entry for the MCP change")
	}
}

// A pipeline whose timeout is longer than the installation allows is
// refused by validate, with the limit in the message and the line of the key.
func TestValidateRefusesATimeoutOverTheInstallationsLimit(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	text := "pipeline: p\ntimeout: 10h\nsteps:\n  a: {image: x, run: a}\n"
	if r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/validate", text); r.status != 200 || !strings.Contains(string(r.body), `"ok":true`) {
		t.Fatalf("no limit: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPut, e.srv.URL+"/api/v1/run-limits", `{"max_run_timeout":"4h"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/validate", text)
	if r.status != 200 || !strings.Contains(string(r.body), `"ok":false`) || !strings.Contains(string(r.body), "max_run_timeout 4h") ||
		!strings.Contains(string(r.body), `"line":2`) || !strings.Contains(string(r.body), `"path":"timeout"`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// A pipeline that filters pull requests by target branch is valid, but
// validate says it will not work in a project with no forge connection.
func TestValidateWarnsWhenTheProjectHasNoForge(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	text := "pipeline: p\non: { pull_request: { into: [main] } }\nsteps:\n  a: {image: x, run: a}\n"
	r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/validate?project=pay-api", text)
	if r.status != 200 || !strings.Contains(string(r.body), `"ok":true`) || !strings.Contains(string(r.body), "has no forge connection") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/validate", text); strings.Contains(string(r.body), "warnings") {
		t.Fatalf("without a project there is nothing to warn about: %s", r.body)
	}
	plain := "pipeline: p\non: { pull_request: {} }\nsteps:\n  a: {image: x, run: a}\n"
	if r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/validate?project=pay-api", plain); strings.Contains(string(r.body), "warnings") {
		t.Fatalf("pull_request: {} needs no forge: %s", r.body)
	}
}
