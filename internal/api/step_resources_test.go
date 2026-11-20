package api

import (
	"net/http"
	"strings"
	"testing"
)

// Anyone signed in reads the step resources; only an administrator changes
// them, and every change, and every refusal, is in the audit trail.
func TestStepResourcesAreReadByAllChangedByAdminsAndAudited(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	base := e.srv.URL + "/api/v1/step-resources"

	// Read, as a non-admin: the whole configuration, the limits, a default.
	r := otto.get(base)
	if r.status != 200 || !strings.Contains(string(r.body), `"defaults"`) || !strings.Contains(string(r.body), `"kinds":["step"]`) {
		t.Fatalf("get: %d %s", r.status, r.body)
	}
	if r := otto.get(base + "/defaults/step"); r.status != 200 || !strings.Contains(string(r.body), `"kind":"step"`) {
		t.Fatalf("get default: %d %s", r.status, r.body)
	}
	if r := otto.get(base + "/defaults/deploy"); r.status != 404 {
		t.Fatalf("a kind with no default: %d %s", r.status, r.body)
	}

	// A non-admin is refused, and nothing changes.
	if r := otto.do(http.MethodPut, base+"/limits", `{"max_memory":"1Gi"}`); r.status != 403 {
		t.Fatalf("otto set limits: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodPut, base+"/defaults/step", `{"cpu":"3"}`); r.status != 403 {
		t.Fatalf("otto set default: %d %s", r.status, r.body)
	}

	// An admin changes them: round trip.
	if r := ada.do(http.MethodPut, base+"/limits", `{"max_cpu":"8","max_memory":"16Gi","memory_per_thread":"512Mi"}`); r.status != 200 {
		t.Fatalf("ada set limits: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPut, base+"/defaults/step", `{"cpu":"500m","cpu_limit":"2","memory":"1Gi","memory_limit":"3Gi"}`); r.status != 200 {
		t.Fatalf("ada set default: %d %s", r.status, r.body)
	}
	r = otto.get(base)
	for _, want := range []string{`"max_cpu":"8"`, `"max_memory":"16Gi"`, `"memory_per_thread":"512Mi"`, `"cpu":"500m"`, `"cpu_limit":"2"`, `"memory":"1Gi"`, `"memory_limit":"3Gi"`, `"updated_by":"ada"`} {
		if !strings.Contains(string(r.body), want) {
			t.Errorf("after the changes, %s is missing from %s", want, r.body)
		}
	}

	// Quantities that do not read, a limit below the promise and a value
	// above the installation's limits are refused, and change nothing.
	for _, c := range []struct{ path, body string }{
		{"/limits", `{"max_memory":"lots"}`},
		{"/limits", `{"max_cpu":"-1"}`},
		{"/limits", `{"memory_per_thread":"1Mi"}`},
		{"/limits", `{"max_memory":"1Gi"}`}, // below the default's 3Gi limit
		{"/defaults/step", `{"memory":"six"}`},
		{"/defaults/step", `{"cpu":"2","cpu_limit":"1"}`},
		{"/defaults/step", `{"memory":"32Gi"}`}, // above max_memory
		{"/defaults/deploy", `{"cpu":"1"}`},
	} {
		if r := ada.do(http.MethodPut, base+c.path, c.body); r.status != 400 {
			t.Errorf("PUT %s %s: %d %s, want 400", c.path, c.body, r.status, r.body)
		}
	}
	if r := otto.get(base + "/limits"); !strings.Contains(string(r.body), `"max_memory":"16Gi"`) {
		t.Errorf("a refused change altered the limits: %s", r.body)
	}

	// The audit trail has the changes, with before and after, and the refusals.
	var limits, defaults, denied, ok int
	for _, a := range e.auditEntries(t) {
		switch a.Action {
		case "step_resources.set_limits":
			limits++
		case "step_resources.set_default":
			defaults++
		default:
			continue
		}
		if a.Outcome == "denied" {
			denied++
			if a.User != "otto" {
				t.Errorf("denied entry for %s", a.User)
			}
		}
		if a.Outcome == "ok" {
			ok++
			if a.User != "ada" || !strings.Contains(a.Detail, "->") {
				t.Errorf("audit entry: %+v", a)
			}
		}
	}
	if ok != 2 || denied != 2 || limits < 3 || defaults < 3 {
		t.Errorf("audit: %d ok, %d denied, %d limits, %d defaults entries", ok, denied, limits, defaults)
	}
}

// The MCP tools do what the API does for the same caller, and the change is
// audited with the MCP client's name.
func TestStepResourcesOverMCP(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	ada := mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "admin"), nil, nil)
	mustCall(t, ada, "set_step_resource_limits", map[string]any{"max_memory": "8Gi", "memory_per_thread": "1Gi"})
	mustCall(t, ada, "set_step_resource_default", map[string]any{"kind": "step", "cpu": "1", "memory": "2Gi", "memory_limit": "4Gi"})
	out := mustCall(t, ada, "get_step_resources", nil)
	if got := asJSON(out); !strings.Contains(got, `"max_memory":"8Gi"`) || !strings.Contains(got, `"memory_limit":"4Gi"`) {
		t.Errorf("get_step_resources: %s", got)
	}
	if _, errText := call(t, ada, "set_step_resource_limits", map[string]any{"max_memory": "lots"}); errText == "" {
		t.Error("a quantity that does not read was accepted")
	}
	found := false
	for _, a := range e.auditEntries(t) {
		if a.Action == "step_resources.set_default" && a.Outcome == "ok" && strings.Contains(a.Detail, "via MCP") {
			found = true
		}
	}
	if !found {
		t.Error("no audit entry for the MCP change")
	}
}
