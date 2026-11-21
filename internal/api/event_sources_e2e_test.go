package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// signedCall sends a call to an event source signed as the docs say.
func signedCall(b *browser, url, secret, body string, at time.Time) response {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return b.do(http.MethodPost, url, body, "X-Launchway-Timestamp", ts, "X-Launchway-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
}

func eventsOf(t *testing.T, e *authEnv, q string) []engine.EventRecord {
	t.Helper()
	evs, err := e.eng.EventLog(engine.EventFilter{Project: "pay-inputs", Limit: 100, Source: q})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// An event source: made once with a secret shown once; a signed call starts a
// run with inputs mapped from the body by expressions; every kind of refusal is
// an event with its reason; filters, dedupe and the schema work; a test
// delivery changes nothing.
func TestEventSourceEndToEnd(t *testing.T) {
	e := inputsEnv(t)
	e.api.Secrets = testSecrets(t, e.eng)
	otto, vic := e.signIn(t, "otto"), e.signIn(t, "vic")
	base := e.srv.URL + "/api/v1/projects/pay-inputs"
	anon := newBrowser(t)

	create := `{"name":"deploy-request","ref":"main",
	  "schema":"{\"type\":\"object\",\"required\":[\"release\"],\"properties\":{\"release\":{\"type\":\"object\",\"required\":[\"version\"]}}}",
	  "mapping":{"version":"body.release.version","environment":"default(body.env, \"dev\")","dry_run":"body.env != \"prod\""},
	  "filter":"body.action == \"released\"","dedupe_key":"body.release.version + \"@\" + default(body.env,\"dev\")"}`
	// A dedupe key is an expression; "+" is not in the language, so say so at once.
	if r := otto.do("POST", base+"/event-sources", create); r.status != 400 || !strings.Contains(string(r.body), "dedupe key") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	create = strings.Replace(create, `"dedupe_key":"body.release.version + \"@\" + default(body.env,\"dev\")"`, `"dedupe_key":"body.release.id"`, 1)
	if r := vic.do("POST", base+"/event-sources", create); r.status != 403 {
		t.Fatalf("a viewer made a source: %d %s", r.status, r.body)
	}
	r := otto.do("POST", base+"/event-sources", create)
	var made struct {
		Source sourceView `json:"source"`
		Secret string     `json:"secret"`
	}
	if r.status != 201 || json.Unmarshal(r.body, &made) != nil || !strings.HasPrefix(made.Secret, "lwes_") || !made.Source.HasSecret ||
		!strings.HasSuffix(made.Source.URL, "/api/v1/projects/pay-inputs/sources/deploy-request") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// The secret is never shown again, by the list or by the database.
	list := otto.get(base + "/event-sources")
	if list.status != 200 || strings.Contains(string(list.body), made.Secret) || strings.Contains(string(list.body), "secret_sealed") {
		t.Fatalf("%d %s", list.status, list.body)
	}
	if _, sealed, err := e.eng.EventSourceNamed("pay-inputs", "deploy-request"); err != nil || sealed == "" || strings.Contains(sealed, made.Secret) {
		t.Fatalf("the secret is stored in the clear or missing: %q %v", sealed, err)
	}
	url := e.srv.URL + "/api/v1/projects/pay-inputs/sources/deploy-request"
	now := e.api.Now()

	// A good call starts a run with the mapped inputs.
	body := `{"action":"released","env":"prod","release":{"id":"r1","version":"v7"},"token":"hunter2"}`
	r = signedCall(anon, url, made.Secret, body, now)
	var res source.SourceResult
	if r.status != 201 || json.Unmarshal(r.body, &res) != nil || res.Status != "started" || res.Run == nil || res.EventID == "" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var d engine.RunDetail
	otto.get(e.srv.URL+"/api/v1/runs/"+res.Run.ID).json(t, &d)
	if d.TriggerDetail == nil || d.TriggerDetail.Inputs["version"] != "v7" || d.TriggerDetail.Inputs["environment"] != "prod" || d.TriggerDetail.Inputs["dry_run"] != "false" || d.Event != "webhook" {
		t.Fatalf("%+v", d.TriggerDetail)
	}
	ev, err := e.eng.EventLogEntry(res.EventID)
	if err != nil || !ev.Verified || ev.Source != engine.SourceEventSource || len(ev.Decisions) != 1 || ev.Decisions[0].Decision != engine.DecStarted ||
		strings.Contains(string(ev.Payload), "hunter2") || !strings.Contains(string(ev.Payload), "[removed]") {
		t.Fatalf("%v %+v %s", err, ev, ev.Payload)
	}
	// The same signed request again is refused, and recorded.
	if r := signedCall(anon, url, made.Secret, body, now); r.status != 409 {
		t.Fatalf("a replayed signature: %d %s", r.status, r.body)
	}
	// The same release again, signed anew: a duplicate, with the first event named.
	r = signedCall(anon, url, made.Secret, body+" ", now.Add(time.Second))
	if r.status != 200 || json.Unmarshal(r.body, &res) != nil || res.Status != "duplicate" || !strings.Contains(res.Reason, ev.ID) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Filtered: the filter is false.
	r = signedCall(anon, url, made.Secret, `{"action":"deleted","release":{"id":"r2","version":"v8"}}`, now)
	if r.status != 200 || json.Unmarshal(r.body, &res) != nil || res.Status != "filtered" || !strings.Contains(res.Reason, "filter") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Schema: the version is missing.
	r = signedCall(anon, url, made.Secret, `{"action":"released","release":{"id":"r3"}}`, now)
	if r.status != 422 || json.Unmarshal(r.body, &res) != nil || res.Status != "refused" || len(res.Problems) == 0 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// An input that does not fit the pipeline is an error that says which.
	r = signedCall(anon, url, made.Secret, `{"action":"released","release":{"id":"r4","version":"seven"}}`, now)
	if r.status != 422 || json.Unmarshal(r.body, &res) != nil || res.Status != "error" || !strings.Contains(strings.Join(res.Problems, " "), "version") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Not verified: wrong signature, stale timestamp, no signature. All answer 404.
	for name, call := range map[string]func() response{
		"wrong secret": func() response { return signedCall(anon, url, "lwes_wrong", body, now) },
		"stale":        func() response { return signedCall(anon, url, made.Secret, body+"  ", now.Add(-time.Hour)) },
		"unsigned":     func() response { return anon.do("POST", url, body) },
	} {
		if r := call(); r.status != 404 {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
	if r := anon.do("POST", e.srv.URL+"/api/v1/projects/pay-inputs/sources/nothing", body); r.status != 404 {
		t.Fatalf("%d", r.status)
	}
	unverified, _ := e.eng.EventLog(engine.EventFilter{Project: "pay-inputs", Verified: "no", Limit: 50})
	if len(unverified) != 3 || unverified[0].Decisions[0].Decision != engine.DecRefused {
		t.Fatalf("%+v", unverified)
	}
	// Too large.
	small := `{"max_body":50}`
	if r := otto.do("POST", base+"/event-sources/deploy-request", small); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := signedCall(anon, url, made.Secret, body, now.Add(2*time.Second)); r.status != 413 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	otto.do("POST", base+"/event-sources/deploy-request", `{"max_body":65536}`)

	// A test delivery shows the inputs and changes nothing.
	before := len(eventsOf(t, e, ""))
	r = otto.do("POST", base+"/event-sources/deploy-request/test", `{"body":{"action":"released","env":"prod","release":{"id":"r9","version":"v9"}}}`)
	if r.status != 200 || json.Unmarshal(r.body, &res) != nil || res.Status != "ready" || res.Inputs["version"] != "v9" || !res.Dry || res.Key != "r9" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if after := len(eventsOf(t, e, "")); after != before {
		t.Fatalf("a test delivery recorded %d events", after-before)
	}
	r = otto.do("POST", base+"/event-sources/deploy-request/test", `{"body":{"action":"released","release":{"id":"r9","version":"nine"}}}`)
	if r.status != 200 || json.Unmarshal(r.body, &res) != nil || res.Status != "error" || len(res.Problems) == 0 {
		t.Fatalf("%d %s", r.status, r.body)
	}

	// A new secret replaces the old one.
	r = otto.do("POST", base+"/event-sources/deploy-request", `{"rotate_secret":true}`)
	var rot struct {
		Secret string `json:"secret"`
	}
	if r.status != 200 || json.Unmarshal(r.body, &rot) != nil || rot.Secret == "" || rot.Secret == made.Secret {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := signedCall(anon, url, made.Secret, `{"action":"released","release":{"id":"r10","version":"v10"}}`, now); r.status != 404 {
		t.Fatalf("the old secret still works: %d", r.status)
	}
	if r := signedCall(anon, url, rot.Secret, `{"action":"released","release":{"id":"r10","version":"v10"}}`, now); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Turned off, and deleted: not found.
	otto.do("POST", base+"/event-sources/deploy-request", `{"enabled":false}`)
	if r := signedCall(anon, url, rot.Secret, `{"action":"released","release":{"id":"r11","version":"v11"}}`, now); r.status != 404 {
		t.Fatalf("%d", r.status)
	}
	if r := otto.do("DELETE", base+"/event-sources/deploy-request", ""); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := otto.get(base + "/event-sources"); strings.TrimSpace(string(r.body)) != "[]" {
		t.Fatalf("%s", r.body)
	}
	// The audit trail names what happened, never a secret.
	for _, a := range e.auditEntries(t) {
		if strings.Contains(a.Detail, made.Secret) || strings.Contains(a.Detail, rot.Secret) {
			t.Fatalf("a secret is in the audit trail: %+v", a)
		}
	}
}

// Without a root key a source cannot be made: its secret would be kept in the clear.
func TestEventSourceNeedsARootKey(t *testing.T) {
	e := inputsEnv(t)
	otto := e.signIn(t, "otto")
	r := otto.do("POST", e.srv.URL+"/api/v1/projects/pay-inputs/event-sources", `{"name":"x"}`)
	if r.status != 409 || !strings.Contains(string(r.body), "no_root_key") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// A debounce window turns a burst of calls into one run, started once the
// calls stop; every call is an event, and the earlier ones say they were folded in.
func TestEventSourceDebounceStartsOneRun(t *testing.T) {
	e := inputsEnv(t)
	e.api.Secrets = testSecrets(t, e.eng)
	otto := e.signIn(t, "otto")
	base := e.srv.URL + "/api/v1/projects/pay-inputs"
	r := otto.do("POST", base+"/event-sources", `{"name":"burst","mapping":{"version":"body.v"},"debounce_seconds":1}`)
	var made struct{ Secret string }
	if r.status != 201 || json.Unmarshal(r.body, &made) != nil {
		t.Fatalf("%d %s", r.status, r.body)
	}
	anon := newBrowser(t)
	url := e.srv.URL + "/api/v1/projects/pay-inputs/sources/burst"
	for i, v := range []string{"v1", "v2", "v3"} {
		r := signedCall(anon, url, made.Secret, `{"v":"`+v+`","n":`+strconv.Itoa(i)+`}`, e.api.Now())
		if r.status != 202 {
			t.Fatalf("%d %s", r.status, r.body)
		}
	}
	runs := func() int {
		list, _ := e.eng.ListRuns(engine.RunFilter{Project: "pay-inputs", Limit: 50})
		return len(list)
	}
	if runs() != 0 {
		t.Fatalf("a run started inside the window")
	}
	deadline := time.Now().Add(15 * time.Second)
	for runs() == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		e.api.Projects.CloseWindows(t.Context())
	}
	if runs() != 1 {
		t.Fatalf("%d runs after the window closed, want 1", runs())
	}
	evs := eventsOf(t, e, engine.SourceEventSource)
	decisions := map[string]int{}
	for _, ev := range evs {
		for _, d := range ev.Decisions {
			decisions[d.Decision]++
		}
	}
	if len(evs) != 3 || decisions[engine.DecStarted] != 1 || decisions[engine.DecSuperseded] != 2 || decisions[engine.DecWaiting] != 3 {
		t.Fatalf("%v %+v", decisions, evs)
	}
}
