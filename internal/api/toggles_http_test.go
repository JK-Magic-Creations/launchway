package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// sdkCall is a request an application makes with an SDK key.
func sdkCall(t *testing.T, method, url, key, body string, header ...string) (int, http.Header, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

type streamEvent struct{ id, event, data string }

// openStream reads server-sent events from a stream, handing them on.
func openStream(t *testing.T, url, key, lastID string) <-chan streamEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		cancel()
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	out := make(chan streamEvent, 16)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev streamEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				ev.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				ev.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			case line == "" && ev.event != "":
				select {
				case out <- ev:
				default:
				}
				ev = streamEvent{}
			}
		}
	}()
	return out
}

func next(t *testing.T, ch <-chan streamEvent, within time.Duration) (streamEvent, bool) {
	t.Helper()
	select {
	case ev := <-ch:
		return ev, true
	case <-time.After(within):
		return streamEvent{}, false
	}
}

func TestTogglesOverHTTPWithSDKKeys(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto, dana, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "dana"), e.signIn(t, "vic")
	u := e.srv.URL
	api := u + "/api/v1/projects/pay-api"

	// otto (operator of pay-*) makes a toggle; vic (viewer) may read, not change; dana cannot see it.
	if r := otto.do("POST", api+"/toggles", `{"key":"checkout-v2","type":"boolean","description":"new checkout","reason":"new"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := vic.do("PUT", api+"/toggles/checkout-v2/environments/dev", `{"state":{"on":true,"off_value":false,"fallthrough":{"value":true}},"reason":"x"}`); r.status != 403 {
		t.Fatalf("a viewer changed a toggle: %d", r.status)
	}
	if r := dana.get(api + "/toggles/checkout-v2"); r.status != 404 {
		t.Fatalf("another team's toggle: %d", r.status)
	}
	if r := vic.get(api + "/toggles/checkout-v2"); r.status != 200 {
		t.Fatalf("%d", r.status)
	}
	// A reason is required.
	if r := otto.do("PUT", api+"/toggles/checkout-v2/environments/dev", `{"state":{"on":true,"off_value":false,"fallthrough":{"value":true}}}`); r.status != 400 || !strings.Contains(string(r.body), "reason") {
		t.Fatalf("no reason: %d %s", r.status, r.body)
	}
	rollout := `{"state":{"on":true,"off_value":false,"fallthrough":{"rollout":{"buckets":[{"value":true,"weight":2500},{"value":false,"weight":7500}]}}},"reason":"start at 25%"}`
	if r := otto.do("PUT", api+"/toggles/checkout-v2/environments/dev", rollout); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// production is protected: the change waits, and the one who asked cannot approve it.
	var out struct {
		Waiting bool
		Change  struct{ ID int64 }
	}
	r := otto.do("PUT", api+"/toggles/checkout-v2/environments/production", `{"state":{"on":true,"off_value":false,"fallthrough":{"value":true}},"reason":"launch"}`)
	r.json(t, &out)
	if r.status != 202 || !out.Waiting || out.Change.ID == 0 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	approve := func(b *browser) response {
		return b.do("POST", api+"/toggle-changes/"+strconv.FormatInt(out.Change.ID, 10)+"/approve", `{"comment":"ok"}`)
	}
	if r := approve(otto); r.status != 403 {
		t.Fatalf("self approval: %d %s", r.status, r.body)
	}
	if r := approve(vic); r.status != 403 {
		t.Fatalf("a viewer approved: %d", r.status)
	}
	if r := approve(ada); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}

	// Keys: only an admin makes them; the secret is shown once.
	mk := func(b *browser, project, body string) (int, string) {
		r := b.do("POST", u+"/api/v1/projects/"+project+"/toggle-keys", body)
		var k struct{ Key string }
		_ = json.Unmarshal(r.body, &k)
		return r.status, k.Key
	}
	if code, _ := mk(otto, "pay-api", `{"environment":"dev","kind":"server"}`); code != 403 {
		t.Fatalf("an operator made a key: %d", code)
	}
	code, devKey := mk(ada, "pay-api", `{"environment":"dev","kind":"server","name":"api"}`)
	if code != 201 || !strings.HasPrefix(devKey, "lwt_srv_") {
		t.Fatalf("%d %q", code, devKey)
	}
	_, prodKey := mk(ada, "pay-api", `{"environment":"production","kind":"server"}`)
	_, webKey := mk(ada, "web", `{"environment":"dev","kind":"server"}`)
	_, cliKey := mk(ada, "pay-api", `{"environment":"dev","kind":"client","origins":["https://shop.example.com"]}`)
	if r := otto.get(api + "/toggle-keys"); r.status != 200 || strings.Contains(string(r.body), devKey) || strings.Contains(string(r.body), "secret_hash") {
		t.Fatalf("the key list shows a secret: %d %s", r.status, r.body)
	}

	// A server key downloads its environment's ruleset, with an ETag.
	st, hdr, body := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", devKey, "")
	etag := hdr.Get("ETag")
	if st != 200 || etag == "" || !strings.Contains(body, `"checkout-v2"`) || !strings.Contains(body, `"rollout"`) {
		t.Fatalf("%d %s %s", st, etag, body)
	}
	if st, _, _ := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", devKey, "", "If-None-Match", etag); st != 304 {
		t.Fatalf("If-None-Match: %d", st)
	}
	// Production has the change that was approved; dev has the rollout; the other project's key sees neither toggle.
	if _, _, body := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", prodKey, ""); !strings.Contains(body, `"fallthrough":{"value":true}`) {
		t.Fatalf("production: %s", body)
	}
	if _, _, body := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", webKey, ""); strings.Contains(body, "checkout-v2") {
		t.Fatalf("another project's key read this project's toggles: %s", body)
	}
	// Not a key, a revoked-looking key, a client key where a server key is needed: all refused, none say why.
	for _, k := range []string{"", "lwt_srv_nope", devKey + "x", "token"} {
		if st, _, _ := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", k, ""); st != 401 {
			t.Errorf("key %q: %d", k, st)
		}
	}
	if st, _, _ := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", cliKey, ""); st != 403 {
		t.Fatalf("a client key downloaded rules: %d", st)
	}
	// An API token or session is not an SDK key.
	if st, _, _ := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", "", ""); st != 401 {
		t.Fatalf("no key: %d", st)
	}

	// A client key gets values for a context, never rules, and only of visible toggles.
	eval := func(key, ctx string, header ...string) (int, string) {
		st, _, b := sdkCall(t, "POST", u+"/api/sdk/v1/evaluate", key, ctx, append([]string{"Content-Type", "application/json", "Origin", "https://shop.example.com"}, header...)...)
		return st, b
	}
	st2, b2 := eval(cliKey, `{"context":{"key":"u1"}}`)
	if st2 != 200 || strings.Contains(b2, "checkout-v2\":{\"value") {
		t.Fatalf("a toggle that is not client-visible was evaluated for a client: %d %s", st2, b2)
	}
	if r := otto.do("PATCH", api+"/toggles/checkout-v2", `{"client_visible":true,"reason":"the web app reads it"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	time.Sleep(20 * time.Millisecond)
	st2, b2 = eval(cliKey, `{"context":{"key":"u1","email":"x@example.com"},"toggles":["checkout-v2"]}`)
	if st2 != 200 || !strings.Contains(b2, `"checkout-v2"`) || strings.Contains(b2, "rollout") || strings.Contains(b2, "rules") || strings.Contains(b2, "buckets") || strings.Contains(b2, "x@example.com") {
		t.Fatalf("%d %s", st2, b2)
	}
	if st, _ := eval(cliKey, `{"context":{"key":"u1"}}`, "Origin", "https://evil.example.com"); st != 403 {
		t.Fatalf("a client key from another website: %d", st)
	}
	// Server keys evaluate remotely too, and a server key's answer includes the rule that matched.
	if st, b := eval(devKey, `{"context":{"key":"u1"}}`); st != 200 || !strings.Contains(b, "checkout-v2") {
		t.Fatalf("%d %s", st, b)
	}
	// The rate limit: a burst over the allowance is refused.
	limited := false
	for i := 0; i < 400 && !limited; i++ {
		if st, _ := eval(cliKey, `{"context":{"key":"u1"}}`); st == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("400 evaluations in a burst were not limited")
	}

	// Revoking ends a key at once.
	var keys struct{ Keys []struct{ ID, Kind string } }
	ada.get(api+"/toggle-keys").json(t, &keys)
	for _, k := range keys.Keys {
		if strings.HasPrefix(devKey, "lwt_srv_"+k.ID) {
			if r := ada.do("DELETE", api+"/toggle-keys/"+k.ID, ""); r.status != 200 {
				t.Fatalf("%d", r.status)
			}
		}
	}
	time.Sleep(20 * time.Millisecond)
	if st, _, _ := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", devKey, ""); st != 401 {
		t.Fatalf("a revoked key: %d", st)
	}
}

func TestToggleStreamSendsChangesAndResumes(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	u := e.srv.URL
	api := u + "/api/v1/projects/pay-api"
	if r := otto.do("POST", api+"/toggles", `{"key":"a","type":"boolean","reason":"x"}`); r.status != 201 {
		t.Fatal(string(r.body))
	}
	r := ada.do("POST", api+"/toggle-keys", `{"environment":"dev","kind":"server"}`)
	var k struct{ Key string }
	r.json(t, &k)

	ch := openStream(t, u+"/api/sdk/v1/stream", k.Key, "")
	first, ok := next(t, ch, 3*time.Second)
	if !ok || first.event != "ruleset" || first.id == "" || !strings.Contains(first.data, `"a"`) {
		t.Fatalf("the first event: %+v %v", first, ok)
	}
	// Nothing changes: nothing is sent.
	if _, ok := next(t, ch, 1500*time.Millisecond); ok {
		t.Fatal("an event with no change")
	}
	if r := otto.do("PUT", api+"/toggles/a/environments/dev", `{"state":{"on":true,"off_value":false,"fallthrough":{"value":true}},"reason":"go"}`); r.status != 200 {
		t.Fatal(string(r.body))
	}
	second, ok := next(t, ch, 3*time.Second)
	if !ok || second.id == first.id || !strings.Contains(second.data, `"on":true`) {
		t.Fatalf("after the change: %+v %v", second, ok)
	}
	// Resuming with the id of the newest event sends nothing; with an older one, the current ruleset.
	if _, ok := next(t, openStream(t, u+"/api/sdk/v1/stream", k.Key, second.id), 1500*time.Millisecond); ok {
		t.Fatal("a client that is current was sent the ruleset again")
	}
	resumed, ok := next(t, openStream(t, u+"/api/sdk/v1/stream", k.Key, first.id), 3*time.Second)
	if !ok || resumed.id != second.id {
		t.Fatalf("resuming from an older id: %+v %v", resumed, ok)
	}
	// A client key cannot open a stream.
	r = ada.do("POST", api+"/toggle-keys", `{"environment":"dev","kind":"client"}`)
	var c struct{ Key string }
	r.json(t, &c)
	if st, _, _ := sdkCall(t, "GET", u+"/api/sdk/v1/stream", c.Key, ""); st != 403 {
		t.Fatalf("a client key opened a stream: %d", st)
	}
}

func TestToggleSafeguardsOverHTTP(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	api := e.srv.URL + "/api/v1/projects/pay-api"
	otto.do("POST", api+"/toggles", `{"key":"a","type":"boolean","reason":"x"}`)
	// An operator may not relax the safeguards of a project; an admin may, with a reason that is recorded.
	if r := otto.do("PUT", api+"/toggle-safeguards", `{"environment":"production","require_approval":false,"reason":"x"}`); r.status != 403 {
		t.Fatalf("%d", r.status)
	}
	if r := ada.do("PUT", api+"/toggle-safeguards", `{"environment":"production","require_approval":false}`); r.status != 400 {
		t.Fatalf("relaxing with no reason: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", api+"/toggle-safeguards", `{"environment":"production","require_approval":false,"reason":"two-person team"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := otto.do("PUT", api+"/toggles/a/environments/production", `{"state":{"on":true,"off_value":false,"fallthrough":{"value":true}},"reason":"go"}`); r.status != 200 {
		t.Fatalf("the change should be made now: %d %s", r.status, r.body)
	}
	var hist struct {
		Changes []struct{ Action, Reason string }
	}
	ada.get(api+"/toggles/a/history").json(t, &hist)
	if len(hist.Changes) < 2 {
		t.Fatalf("%+v", hist)
	}
	// The relaxing is on the environment's timeline too.
	var tl struct {
		Changes []struct{ Action, Reason string }
	}
	ada.get(api+"/toggle-timeline?environment=production").json(t, &tl)
	found := false
	for _, c := range tl.Changes {
		if c.Action == "safeguards" && strings.Contains(c.Reason, "relaxed require_approval") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the relaxing of a safeguard is not on the timeline: %+v", tl)
	}
}

// A rollback restores a version; the toggle changes made since it went live are
// listed on the rollback page and reverted with it, on request.
func TestRollbackListsAndRevertsToggleChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	de := setupDeploy(t)
	de.deployRunner(t, "deployer-1")
	base := de.srv.URL
	api := base + "/api/v1/projects/shop"
	mk := func(key string) {
		if code := postJSON(t, api+"/toggles", map[string]any{"key": key, "type": "boolean", "reason": "new"}, nil); code != 201 {
			t.Fatalf("%s: %d", key, code)
		}
	}
	put := func(key string, on bool) {
		t.Helper()
		body := map[string]any{"state": map[string]any{"on": on, "off_value": false, "fallthrough": map[string]any{"value": true}}, "reason": "change " + key}
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPut, api+"/toggles/"+key+"/environments/prod", strings.NewReader(string(b)))
		req.Header.Set(csrfHeader, "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", key, err, resp)
		}
		resp.Body.Close()
	}
	for _, k := range []string{"before", "during", "after"} {
		mk(k)
	}
	put("before", true) // changed before the good version went live: not part of what a rollback leaves behind
	time.Sleep(1100 * time.Millisecond)
	if r := waitDeployRun(t, de.eng, de.startShop(t, "1.1.0", "true"), time.Minute); r.Status != engine.RunSucceeded {
		t.Fatalf("%s", r.Status)
	}
	time.Sleep(1100 * time.Millisecond)
	put("during", true)
	if r := waitDeployRun(t, de.eng, de.startShop(t, "1.2.0", "true"), time.Minute); r.Status != engine.RunSucceeded {
		t.Fatalf("%s", r.Status)
	}
	put("after", true)

	var plan struct {
		Toggles struct {
			Since   int64
			Changes []struct {
				ID     int64
				Toggle string
			}
			Note string
		}
	}
	getJSON(t, base+"/api/v1/projects/shop/rollback?environment=prod", &plan)
	got := []string{}
	for _, c := range plan.Toggles.Changes {
		got = append(got, c.Toggle)
	}
	if strings.Join(got, ",") != "after,during" || plan.Toggles.Since == 0 || !strings.Contains(plan.Toggles.Note, "2 toggle change") {
		t.Fatalf("what the rollback page says: %+v", plan.Toggles)
	}
	var sum struct {
		ID            string
		ToggleReverts []struct {
			Toggle, Error string
			RevertedBy    int64 `json:"reverted_by"`
		} `json:"toggle_reverts"`
	}
	if code := postJSON(t, base+"/api/v1/projects/shop/rollbacks", map[string]any{"environment": "prod", "reason": "checkout is down", "revert_toggles": true}, &sum); code != 201 {
		t.Fatalf("rollback: %d", code)
	}
	if len(sum.ToggleReverts) != 2 || sum.ToggleReverts[0].Error != "" || sum.ToggleReverts[1].Error != "" {
		t.Fatalf("%+v", sum.ToggleReverts)
	}
	if r := waitDeployRun(t, de.eng, sum.ID, time.Minute); r.Status != engine.RunSucceeded {
		t.Fatalf("rollback run: %s", r.Status)
	}
	state := func(key string) bool {
		var sm struct {
			Environments []struct {
				Environment string
				State       struct{ On bool }
			}
		}
		getJSON(t, api+"/toggles/"+key, &sm)
		for _, e := range sm.Environments {
			if e.Environment == "prod" {
				return e.State.On
			}
		}
		return false
	}
	if !state("before") || state("during") || state("after") {
		t.Fatalf("after the rollback: before=%v during=%v after=%v", state("before"), state("during"), state("after"))
	}
	// The page for the rollback that happened shows them gone.
	var after struct{ Toggles *struct{ Changes []any } }
	getJSON(t, base+"/api/v1/projects/shop/rollback?environment=prod", &after)
	if after.Toggles != nil && len(after.Toggles.Changes) != 0 {
		t.Fatalf("%+v", after.Toggles)
	}
	// The timeline of the environment has the toggle changes and their reverts.
	var tl struct {
		Changes []struct{ Toggle, Via string }
	}
	getJSON(t, api+"/toggle-timeline?environment=prod", &tl)
	if len(tl.Changes) != 5 { // three changes, two of them reverted
		t.Fatalf("timeline: %+v", tl.Changes)
	}
}

// An SDK key is not an API credential, and an API token is not an SDK key: each opens its own routes and nothing
// of the other's; and a key belongs to one project's one environment, whoever makes it.
func TestSDKKeysAndAPITokensDoNotCrossOver(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, dana := e.signIn(t, "ada"), e.signIn(t, "dana")
	u := e.srv.URL
	_, token := ada.newToken(u, "for toggles")
	r := ada.do("POST", u+"/api/v1/projects/pay-api/toggle-keys", `{"environment":"dev","kind":"server"}`)
	var k struct{ Key string }
	r.json(t, &k)
	if st, _, _ := sdkCall(t, "GET", u+"/api/v1/projects/pay-api/toggles", k.Key, ""); st != 401 {
		t.Fatalf("a server key used as an API credential: %d", st)
	}
	if st, _, _ := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", token, ""); st != 401 {
		t.Fatalf("an API token used as an SDK key: %d", st)
	}
	if st, _, _ := sdkCall(t, "POST", u+"/api/v1/projects/pay-api/toggles", k.Key, `{"key":"x","type":"boolean","reason":"x"}`, "X-Launchway-CSRF", "1"); st != 401 {
		t.Fatalf("a key made a toggle: %d", st)
	}
	// dana operates web only: she cannot make a key for pay-api, nor list its keys.
	if r := dana.do("POST", u+"/api/v1/projects/pay-api/toggle-keys", `{"environment":"dev","kind":"server"}`); r.status != 404 {
		t.Fatalf("another team's key: %d", r.status)
	}
	if r := dana.get(u + "/api/v1/projects/pay-api/toggle-keys"); r.status != 404 {
		t.Fatalf("another team's key list: %d", r.status)
	}
	// A client key cannot report counts or open a stream.
	r = ada.do("POST", u+"/api/v1/projects/pay-api/toggle-keys", `{"environment":"dev","kind":"client"}`)
	var c struct{ Key string }
	r.json(t, &c)
	if st, _, _ := sdkCall(t, "POST", u+"/api/sdk/v1/telemetry", c.Key, `{"counts":[]}`); st != 403 {
		t.Fatalf("a client key reported counts: %d", st)
	}
}

// Keys that are not valid are slowed down: after enough from one address even a good key waits a minute.
func TestGuessingSDKKeysIsSlowed(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	u := e.srv.URL
	r := ada.do("POST", u+"/api/v1/projects/pay-api/toggle-keys", `{"environment":"dev","kind":"server"}`)
	var k struct{ Key string }
	r.json(t, &k)
	for i := 0; i < 40; i++ {
		sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", "lwt_srv_000000000000_nope", "")
	}
	if st, hdr, _ := sdkCall(t, "GET", u+"/api/sdk/v1/ruleset", k.Key, ""); st != 429 || hdr.Get("Retry-After") == "" {
		t.Fatalf("after many wrong keys: %d", st)
	}
}

// A toggle shared at the organization level reaches projects, only an administrator may change it, and a project
// of another unit sees the same one.
func TestSharedToggleOverHTTP(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	u := e.srv.URL
	if r := otto.do("POST", u+"/api/v1/projects/pay-api/toggles", `{"key":"dark-mode","type":"boolean","level":"organization","reason":"x"}`); r.status != 403 {
		t.Fatalf("an operator shared a toggle: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", u+"/api/v1/projects/pay-api/toggles", `{"key":"dark-mode","type":"boolean","level":"organization","reason":"for everyone"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	for _, p := range []string{"pay-api", "web"} {
		var got struct{ Inherited bool }
		ada.get(u+"/api/v1/projects/"+p+"/toggles/dark-mode").json(t, &got)
		if !got.Inherited {
			t.Fatalf("%s does not see the shared toggle", p)
		}
	}
	// otto may not change the shared toggle's state; an admin may.
	body := `{"state":{"on":true,"off_value":false,"fallthrough":{"value":true}},"reason":"go"}`
	if r := otto.do("PUT", u+"/api/v1/projects/pay-api/toggles/dark-mode/environments/dev", body); r.status != 403 {
		t.Fatalf("an operator changed a shared toggle: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", u+"/api/v1/projects/pay-api/toggles/dark-mode/environments/dev", body); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A lock keeps a project from defining its own.
	if r := ada.do("PUT", u+"/api/v1/projects/pay-api/toggles/dark-mode/lock", `{"reason":"one fleet-wide switch"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := otto.do("POST", u+"/api/v1/projects/pay-api/toggles", `{"key":"dark-mode","type":"boolean","reason":"mine"}`); r.status != 409 {
		t.Fatalf("a project defined a locked key: %d %s", r.status, r.body)
	}
}
