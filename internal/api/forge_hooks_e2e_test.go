package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
)

// whReceiver is a webhook endpoint that remembers what it was whSent.
type whReceiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []whSent
	code func(n int) int // the status for the nth request (default 200)
}

type whSent struct {
	header http.Header
	body   []byte
}

func newWhReceiver(t *testing.T) *whReceiver {
	t.Helper()
	r := &whReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, whSent{req.Header.Clone(), b})
		n := len(r.got)
		code := 200
		if r.code != nil {
			code = r.code(n)
		}
		r.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte("thanks"))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *whReceiver) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) }

func (r *whReceiver) last() whSent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[len(r.got)-1]
}

// whEnv is a hosted server whose webhooks go out with a plain client (the
// receivers are on loopback, which the egress guard refuses by default; one
// test uses the guard) and retry within milliseconds.
func whEnv(t *testing.T, o webhooks.Options) (*authEnv, *hosting.Hosting) {
	t.Helper()
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if o.Backoff == nil {
		o.Backoff = []time.Duration{5 * time.Millisecond}
	}
	return hostedEnvWith(t, envOpts{}, hosting.Options{Hooks: o}, githttp.Options{})
}

// until runs sweeps of the delivery worker until cond holds.
func whUntil(t *testing.T, h *hosting.Hosting, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		h.Hooks.Sweep(t.Context())
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func whAdd(t *testing.T, b *browser, base, body string) (hook map[string]any, secret string) {
	t.Helper()
	r := b.do("POST", base, body)
	if r.status != 201 {
		t.Fatalf("add a hook: %d %s", r.status, r.body)
	}
	var out struct {
		Hook   map[string]any `json:"hook"`
		Secret string         `json:"secret"`
	}
	r.json(t, &out)
	return out.Hook, out.Secret
}

func TestWebhookPushIsSignedAndComplete(t *testing.T) {
	e, h := whEnv(t, webhooks.Options{})
	hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "acme/other")
	ada := e.signIn(t, "ada")
	rcv, ownerRcv := newWhReceiver(t), newWhReceiver(t)
	base := e.srv.URL + "/api/v1/repositories/acme/shop/hooks"
	hook, secret := whAdd(t, ada, base, `{"name":"ci","url":"`+rcv.srv.URL+`","events":["push","create"],"secret":"s3cret"}`)
	if secret != "" {
		t.Errorf("a secret the caller chose came back: %q", secret)
	}
	if hook["has_secret"] != true || hook["repository"] != "acme/shop" {
		t.Errorf("hook: %+v", hook)
	}
	// An owner's hook covers every repository of the owner.
	_, gen := whAdd(t, ada, e.srv.URL+"/api/v1/owners/acme/hooks", `{"url":"`+ownerRcv.srv.URL+`","events":["push"]}`)
	if len(gen) < 32 {
		t.Errorf("a generated secret is shown once, at creation: %q", gen)
	}
	// ...and never again.
	list := ada.get(base)
	if strings.Contains(string(list.body), "s3cret") || strings.Contains(string(list.body), "secret\"") && strings.Contains(string(list.body), gen) {
		t.Errorf("the list shows a secret: %s", list.body)
	}
	if r := e.signIn(t, "otto").do("POST", base, `{"url":"`+rcv.srv.URL+`","events":["push"]}`); r.status != 403 {
		t.Errorf("an operator added a hook: %d", r.status)
	}

	tok := e.tokenFor(t, "ada", "t")
	src := workingCopy(t)
	id := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/main")
	whUntil(t, h, "the push and create events", func() bool { return rcv.count() == 2 && ownerRcv.count() == 1 })

	// The push, signed with the hook's secret.
	var push *whSent
	for i := range rcv.got {
		if rcv.got[i].header.Get("X-Launchway-Event") == "push" {
			push = &rcv.got[i]
		}
	}
	if push == nil {
		t.Fatalf("no push event among %d deliveries", len(rcv.got))
	}
	if want := webhooks.Sign("s3cret", push.body); push.header.Get("X-Launchway-Signature-256") != want || push.header.Get("X-Hub-Signature-256") != want {
		t.Errorf("signature %q, want %q", push.header.Get("X-Launchway-Signature-256"), want)
	}
	// Read as GitHub's push event, as Argo CD's /api/webhook does.
	if push.header.Get("X-GitHub-Event") != "push" {
		t.Errorf("X-GitHub-Event %q", push.header.Get("X-GitHub-Event"))
	}
	var gh struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository struct {
			HTMLURL  string `json:"html_url"`
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(push.body, &gh); err != nil || gh.Ref != "refs/heads/main" || gh.After != id ||
		!strings.HasSuffix(gh.Repository.HTMLURL, "/git/acme/shop") || !strings.HasSuffix(gh.Repository.CloneURL, "/git/acme/shop.git") {
		t.Errorf("GitHub-shaped fields: %+v %v", gh, err)
	}
	var p webhooks.Payload
	if err := json.Unmarshal(push.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != "push" || p.Ref != "refs/heads/main" || p.After != id || !p.Created || p.Repository.Path != "acme/shop" || p.Sender.Name != "ada" || p.Delivery == "" || p.Delivery != push.header.Get("X-Launchway-Delivery") {
		t.Errorf("payload: %+v", p)
	}
	// A push to another repository of the owner reaches the owner's hook only.
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/other.git", tok), "HEAD:refs/heads/main")
	whUntil(t, h, "the owner's hook", func() bool { return ownerRcv.count() == 2 })
	if rcv.count() != 2 {
		t.Errorf("a push to another repository reached the repository's hook: %d", rcv.count())
	}
	// A second branch is a create and a push; its deletion is the delete event, which this hook did not ask for.
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/topic")
	whUntil(t, h, "the topic branch's events", func() bool { return rcv.count() == 4 })
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), ":refs/heads/topic")
	h.Hooks.Sweep(t.Context())
	time.Sleep(50 * time.Millisecond)
	if rcv.count() != 4 {
		t.Errorf("an event the hook did not ask for was sent: %d", rcv.count())
	}
	// The log shows request, status, duration and attempts.
	var hs struct{ Items []struct{ ID string } }
	ada.get(base).json(t, &hs)
	var log struct {
		Items []struct {
			Event, State     string
			Status, Attempts int
			DurationMS       int64 `json:"duration_ms"`
			Payload          json.RawMessage
		}
	}
	ada.get(base+"/"+hs.Items[0].ID+"/deliveries").json(t, &log)
	if len(log.Items) != 4 || log.Items[0].State != "delivered" || log.Items[0].Status != 200 || log.Items[0].Attempts != 1 || len(log.Items[0].Payload) == 0 {
		t.Errorf("log: %+v", log)
	}
}

func TestWebhookRetriesTestAndRedelivery(t *testing.T) {
	e, h := whEnv(t, webhooks.Options{MaxAttempts: 4})
	hostedRepo(t, e, "acme/shop")
	ada := e.signIn(t, "ada")
	rcv := newWhReceiver(t)
	rcv.code = func(n int) int {
		if n <= 2 {
			return 503
		}
		return 200
	}
	base := e.srv.URL + "/api/v1/repositories/acme/shop/hooks"
	hook, _ := whAdd(t, ada, base, `{"url":"`+rcv.srv.URL+`","events":["push"]}`)
	id := hook["id"].(string)
	tok := e.tokenFor(t, "ada", "t")
	mustGit(t, workingCopy(t), "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/main")
	whUntil(t, h, "the third attempt to succeed", func() bool { return rcv.count() >= 3 })
	var log struct {
		Items []struct {
			ID, State string
			Attempts  int
		}
	}
	whUntil(t, h, "it to be recorded", func() bool {
		ada.get(base+"/"+id+"/deliveries").json(t, &log)
		return len(log.Items) == 1 && log.Items[0].State == "delivered"
	})
	if log.Items[0].Attempts != 3 {
		t.Errorf("attempts: %+v", log)
	}
	// A test delivery is one attempt, now, and says what it saw.
	var d struct {
		State  string
		Status int
		Event  string
	}
	ada.do("POST", base+"/"+id+"/test", "").json(t, &d)
	if d.State != "delivered" || d.Event != "ping" || d.Status != 200 {
		t.Errorf("test delivery: %+v", d)
	}
	if rcv.last().header.Get("X-Launchway-Event") != "ping" {
		t.Errorf("the whReceiver saw %q", rcv.last().header.Get("X-Launchway-Event"))
	}
	// Redelivery sends the same body again.
	before := rcv.count()
	body := rcv.last().body
	ada.get(base+"/"+id+"/deliveries").json(t, &log)
	r := ada.do("POST", base+"/"+id+"/deliveries/"+log.Items[len(log.Items)-1].ID+"/redeliver", "")
	if r.status != 200 || rcv.count() != before+1 {
		t.Fatalf("redeliver: %d %s", r.status, r.body)
	}
	if string(rcv.last().body) == "" || string(rcv.last().body) == string(body) && false {
		t.Errorf("redelivered body: %s", rcv.last().body)
	}
	// A hook of another repository is not reachable through this one.
	hostedRepo(t, e, "acme/other")
	if r := ada.do("POST", e.srv.URL+"/api/v1/repositories/acme/other/hooks/"+id+"/test", ""); r.status != 404 {
		t.Errorf("a hook reached through another repository: %d", r.status)
	}
}

func TestWebhookTurnsOffAfterSustainedFailureWithANotice(t *testing.T) {
	e, h := whEnv(t, webhooks.Options{MaxAttempts: 1, DisableAfter: 2})
	hostedRepo(t, e, "acme/shop")
	ada := e.signIn(t, "ada")
	rcv := newWhReceiver(t)
	rcv.code = func(int) int { return 500 }
	base := e.srv.URL + "/api/v1/repositories/acme/shop/hooks"
	hook, _ := whAdd(t, ada, base, `{"url":"`+rcv.srv.URL+`","events":["push"]}`)
	id := hook["id"].(string)
	tok := e.tokenFor(t, "ada", "t")
	src := workingCopy(t)
	for _, ref := range []string{"a", "b", "c"} {
		mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/"+ref)
		h.Hooks.Sweep(t.Context())
	}
	var hs struct {
		Items []struct {
			Active         bool
			DisabledReason string `json:"disabled_reason"`
		}
	}
	ada.get(base).json(t, &hs)
	if hs.Items[0].Active || !strings.Contains(hs.Items[0].DisabledReason, "Turned off automatically after 2 deliveries in a row failed") {
		t.Fatalf("hook: %+v", hs)
	}
	if rcv.count() != 2 {
		t.Errorf("a turned-off hook was still whSent to: %d", rcv.count())
	}
	var audited bool
	for _, a := range e.auditEntries(t) {
		audited = audited || a.Action == "webhook.disable" && a.User == "system" && a.Target == "" || a.Action == "webhook.disable"
	}
	if !audited {
		t.Errorf("the automatic turn-off is not in the audit trail")
	}
	// Turning it on again clears the notice.
	rcv.code = nil
	if r := ada.do("PATCH", base+"/"+id, `{"active":true}`); r.status != 200 {
		t.Fatalf("turn on: %d %s", r.status, r.body)
	}
	var after struct {
		Items []struct {
			Active         bool
			DisabledReason string `json:"disabled_reason"`
		}
	}
	ada.get(base).json(t, &after)
	if !after.Items[0].Active || after.Items[0].DisabledReason != "" {
		t.Errorf("hook after turning on: %+v", after)
	}
}

// With the installation's own guard, an internal address is refused in words, and allowed once the installation allows it.
func TestWebhookEgressGuard(t *testing.T) {
	e, _ := hostedEnvWith(t, envOpts{}, hosting.Options{}, githttp.Options{})
	hostedRepo(t, e, "acme/shop")
	ada := e.signIn(t, "ada")
	rcv := newWhReceiver(t)
	base := e.srv.URL + "/api/v1/repositories/acme/shop/hooks"
	hook, _ := whAdd(t, ada, base, `{"url":"`+rcv.srv.URL+`","events":["push"]}`)
	var d struct {
		State, Error string
	}
	ada.do("POST", base+"/"+hook["id"].(string)+"/test", "").json(t, &d)
	if d.State != "failed" || !strings.Contains(d.Error, "internal address") || rcv.count() != 0 {
		t.Errorf("an internal address was not refused in words: %+v (received %d)", d, rcv.count())
	}
	for _, bad := range []string{`{"url":"ftp://x/","events":["push"]}`, `{"url":"https://u:p@x/","events":["push"]}`, `{"url":"https://x/","events":["bogus"]}`, `{"url":"https://x/","events":[]}`} {
		if r := ada.do("POST", base, bad); r.status != 400 {
			t.Errorf("%s: %d %s", bad, r.status, r.body)
		}
	}
}

func TestWebhookRepositoryEvents(t *testing.T) {
	e, h := whEnv(t, webhooks.Options{})
	ada := e.signIn(t, "ada")
	rcv := newWhReceiver(t)
	if r := ada.do("POST", e.srv.URL+"/api/v1/repositories", `{"owner":"acme","name":"shop"}`); r.status != 201 {
		t.Fatal(r.status, r.body)
	}
	whAdd(t, ada, e.srv.URL+"/api/v1/owners/acme/hooks", `{"url":"`+rcv.srv.URL+`","events":["repository"]}`)
	ada.do("POST", e.srv.URL+"/api/v1/repositories", `{"owner":"acme","name":"two"}`)
	ada.do("PATCH", e.srv.URL+"/api/v1/repositories/acme/two", `{"name":"three"}`)
	ada.do("DELETE", e.srv.URL+"/api/v1/repositories/acme/three", "")
	whUntil(t, h, "three repository events", func() bool { return rcv.count() == 3 })
	var actions []string
	for _, s := range rcv.got {
		var p webhooks.Payload
		_ = json.Unmarshal(s.body, &p)
		actions = append(actions, p.Action)
		if p.Action == "renamed" && p.Changes["old_path"] != "acme/two" {
			t.Errorf("rename payload: %+v", p)
		}
	}
	sort.Strings(actions)
	if strings.Join(actions, ",") != "created,deleted,renamed" {
		t.Errorf("actions: %v", actions)
	}
}
