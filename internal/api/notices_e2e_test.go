package api

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/mail/smtptest"
	"github.com/jk-magic-creations/launchway/internal/notices"
)

type inboxResp struct {
	Items []struct {
		ID, Source, Kind, Title string
		Read, Done              bool
	} `json:"items"`
	Unread  int `json:"unread"`
	Waiting int `json:"waiting"`
}

// A person's inbox and choices over the API, and the rules of the mute link.
func TestInboxPreferencesAndTheMuteLink(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	e.api.EnableNotices(config.MailConfig{})
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	api := e.srv.URL + "/api/v1"
	ctx := context.Background()

	var in inboxResp
	ada.get(api+"/inbox").json(t, &in)
	if len(in.Items) != 0 || in.Unread != 0 {
		t.Fatalf("a new inbox is not empty: %+v", in)
	}
	if _, err := e.api.Notices.Notify(ctx, notices.Event{Kind: notices.ReviewRequested, People: []string{"ada"}, Actor: "otto", Name: "acme/shop", Title: "#1 Fix", Link: "/repositories/acme/shop/pulls/1"}); err != nil {
		t.Fatal(err)
	}
	ada.get(api+"/inbox?state=unread").json(t, &in)
	if len(in.Items) != 1 || in.Unread != 1 || in.Items[0].Kind != "review_requested" || in.Items[0].Source != "notice" {
		t.Fatalf("%+v", in)
	}
	var count struct{ Unread, Waiting int }
	ada.get(api+"/inbox/count").json(t, &count)
	if count.Unread != 1 {
		t.Fatalf("%+v", count)
	}
	id := in.Items[0].ID
	if r := otto.do("PUT", api+"/inbox/read", `{"ids":["`+id+`"]}`); r.status != 200 || !strings.Contains(string(r.body), `"changed":0`) {
		t.Fatalf("someone else's notice was changed: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", api+"/inbox/read", `{"ids":["`+id+`"]}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	ada.get(api+"/inbox?state=unread").json(t, &in)
	if len(in.Items) != 0 {
		t.Fatalf("read, still unread: %+v", in)
	}
	ada.do("PUT", api+"/inbox/done", `{"ids":["`+id+`"]}`)
	ada.get(api+"/inbox").json(t, &in)
	if len(in.Items) != 0 {
		t.Fatalf("done, still open: %+v", in)
	}
	ada.get(api+"/inbox?state=done").json(t, &in)
	if len(in.Items) != 1 || !in.Items[0].Done {
		t.Fatalf("%+v", in)
	}
	if r := ada.get(api + "/inbox?state=bogus"); r.status != 400 {
		t.Fatalf("a made-up state: %d", r.status)
	}
	if r := ada.do("PUT", api+"/inbox/read", `{"ids":["pull:acme/shop#1"]}`); r.status != 400 {
		t.Fatalf("marking a waiting pull request: %d", r.status)
	}

	// Choices.
	if r := ada.do("PUT", api+"/notices/preferences", `{"kinds":{"comment":"email","run_failed":"off"},"watches":[{"scope":"repo:acme/shop","level":"all"}],"settings":{"quiet_start":1320,"quiet_end":420,"zone":"Europe/Berlin","digest":true,"digest_hour":7}}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var prefs struct {
		Kinds    []struct{ Kind, Channel string }
		Watches  []notices.Watch
		Settings notices.Settings
		MailOn   bool `json:"mail_on"`
	}
	ada.get(api+"/notices/preferences").json(t, &prefs)
	got := map[string]string{}
	for _, k := range prefs.Kinds {
		got[k.Kind] = k.Channel
	}
	if got["comment"] != "email" || got["run_failed"] != "off" || got["review_requested"] != "email" || got["pull_merged"] != "in_app" ||
		len(prefs.Watches) != 1 || prefs.Settings.Zone != "Europe/Berlin" || !prefs.Settings.Digest || prefs.MailOn {
		t.Fatalf("%+v", prefs)
	}
	for _, bad := range []string{`{"kinds":{"nonsense":"email"}}`, `{"kinds":{"comment":"sms"}}`, `{"watches":[{"scope":"x","level":"all"}]}`, `{"settings":{"quiet_start":5,"quiet_end":-1,"zone":"UTC"}}`} {
		if r := ada.do("PUT", api+"/notices/preferences", bad); r.status != 400 {
			t.Errorf("%s: %d %s", bad, r.status, r.body)
		}
	}
	ada.get(api+"/notices/preferences").json(t, &prefs) // the bad ones changed nothing
	if len(prefs.Watches) != 1 {
		t.Fatalf("%+v", prefs)
	}

	// The mute link needs no sign-in, only a token made for that person and kind.
	key, _ := e.api.Notices.Key(ctx)
	tok := notices.SignMute(key, "ada", notices.Comment, e.api.Now())
	anon := e.srv.URL + "/notices/unsubscribe?token=" + url.QueryEscape(tok)
	page := newBrowser(t).get(anon)
	if page.status != 200 || !strings.Contains(string(page.body), "<form") || strings.Contains(string(page.body), "http://") || strings.Contains(string(page.body), "<script") {
		t.Fatalf("the page: %d %s", page.status, page.body)
	}
	if ch, _ := e.api.Notices.Channel(ctx, "ada", notices.Comment); ch != notices.Email {
		t.Fatal("opening the page muted: a mail scanner prefetching the link would mute for the person")
	}
	r := newBrowser(t).do("POST", anon, "")
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if ch, _ := e.api.Notices.Channel(ctx, "ada", notices.Comment); ch != notices.InApp {
		t.Fatalf("channel after mute: %s", ch)
	}
	if ch, _ := e.api.Notices.Channel(ctx, "otto", notices.Comment); ch != notices.InApp {
		t.Fatal("someone else's choice changed")
	}
	if r := newBrowser(t).do("POST", e.srv.URL+"/notices/unsubscribe?token="+url.QueryEscape(tok+"x"), ""); r.status != 400 {
		t.Fatalf("a forged token: %d", r.status)
	}
}

// Mail goes through the egress guard, over TLS, only an administrator can send
// the test message, and the audit says that it was sent, not what it said.
func TestMailTestMessageAndAuditWithoutBody(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	srv, err := smtptest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	e.api.EnableNotices(config.MailConfig{Host: srv.Host, Port: srv.Port, From: "Launchway <notify@example.com>", PublicURL: "https://launchway.example.com"})
	e.api.Mail.Sender.Config.TLSConfig = srv.ClientTLS()
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	api := e.srv.URL + "/api/v1"

	if r := otto.do("POST", api+"/mail/test", `{"to":"x@example.com"}`); r.status != 403 {
		t.Fatalf("an operator sent a test message: %d", r.status)
	}
	// A mail server on an internal address is refused until egress.allow lists it.
	old := egress.Default.Policy()
	t.Cleanup(func() { _ = egress.Default.Set(old) })
	_ = egress.Default.Set(egress.Policy{})
	r := ada.do("POST", api+"/mail/test", `{"to":"admin@example.com"}`)
	if r.status != 502 || !strings.Contains(string(r.body), "not allowed") {
		t.Fatalf("the guard did not stop it: %d %s", r.status, r.body)
	}
	if len(srv.Messages()) != 0 {
		t.Fatal("mail went to a blocked address")
	}
	_ = egress.Default.Set(egress.Policy{Allow: []string{srv.Host}})
	r = ada.do("POST", api+"/mail/test", `{"to":"admin@example.com"}`)
	if r.status != 200 || len(srv.Messages()) != 1 || !srv.Messages()[0].TLS {
		t.Fatalf("%d %s %+v", r.status, r.body, srv.Messages())
	}
	var st struct {
		Enabled  bool
		Host     string
		Security string
	}
	ada.get(api+"/mail").json(t, &st)
	if !st.Enabled || st.Security != "starttls" || strings.Contains(string(ada.get(api+"/mail").body), "password") {
		t.Fatalf("%+v", st)
	}
	for _, a := range e.auditEntries(t) {
		if a.Action == "mail.test" {
			if strings.Contains(a.Detail, "Launchway test message") || strings.Contains(a.Detail, "mail settings work") {
				t.Fatalf("the audit holds the message: %+v", a)
			}
			return
		}
	}
	t.Fatal("the test message was not audited")
}

// A pull request event becomes a notice for the person asked, naming the
// repository and the pull request.
func TestPullRequestEventsReachThePeopleInvolved(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	e.api.EnableNotices(config.MailConfig{})
	repo := hostedRepo(t, e, "acme/shop")
	otto := e.signIn(t, "otto")
	p := pulls.PullRequest{Number: 7, Title: "Rework checkout", Author: "ada", Reviewers: []string{"otto"}}
	e.api.noticePull(context.Background(), repo, pulls.Event{Action: "review_requested", RepoID: repo.ID, Pull: p, Actor: "ada", Detail: map[string]string{"reviewers": "otto"}})
	e.api.noticePull(context.Background(), repo, pulls.Event{Action: "commented", RepoID: repo.ID, Pull: p, Actor: "ada", Detail: map[string]string{"comment": "c1", "mentions": "otto"}})
	var in inboxResp
	otto.get(e.srv.URL+"/api/v1/inbox").json(t, &in)
	kinds := map[string]bool{}
	for _, it := range in.Items {
		kinds[it.Kind] = true
		if !strings.Contains(it.Title, "#7") && it.Kind != "comment" {
			t.Errorf("an entry does not say which pull request: %+v", it)
		}
	}
	if !kinds["review_requested"] || !kinds["mention"] || !kinds["comment"] {
		t.Fatalf("%+v", in)
	}
}
