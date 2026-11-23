package hosts_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/github/githubfake"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func setup(t *testing.T) (*hosts.Service, *githubfake.Host) {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	h := githubfake.New()
	t.Cleanup(h.Close)
	t.Setenv("LW_HOSTS_TOKEN", githubfake.Token)
	t.Setenv("LW_HOSTS_HOOK", "hooksecret")
	g, _ := egress.New([]string{"127.0.0.1"})
	return hosts.New(eng.Database(), eng, g), h
}

func add(t *testing.T, s *hosts.Service, h *githubfake.Host, owners ...string) {
	t.Helper()
	v, err := s.Add(t.Context(), hosts.AddInput{Name: "gh", Kind: forge.KindGitHub, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_HOSTS_TOKEN"},
		WebhookSecret: forge.SecretRef{Env: "LW_HOSTS_HOOK"}, Owners: owners}, "dana")
	if err != nil {
		t.Fatal(err)
	}
	if v.Credential != "env:LW_HOSTS_TOKEN" || v.Capabilities == nil || v.AuthKind != "token" {
		t.Fatalf("%+v", v)
	}
}

func TestConnectionRules(t *testing.T) {
	s, h := setup(t)
	ctx := t.Context()
	if _, err := s.Add(ctx, hosts.AddInput{Name: "x", Kind: "nope", Credential: forge.SecretRef{Env: "LW_HOSTS_TOKEN"}}, "d"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a kind with no provider: %v", err)
	}
	if _, err := s.Add(ctx, hosts.AddInput{Name: "x", Kind: forge.KindGitHub, BaseURL: h.BaseURL()}, "d"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("no credential: %v", err)
	}
	if _, err := s.Add(ctx, hosts.AddInput{Name: "x", Kind: forge.KindGitHub, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_NOT_SET"}}, "d"); !errors.Is(err, forge.ErrInvalid) || strings.Contains(err.Error(), githubfake.Token) {
		t.Errorf("a reference that does not resolve: %v", err)
	}
	strict, _ := egress.New(nil)
	if _, err := hosts.New(s.Store().DB(), nil, strict).Add(ctx, hosts.AddInput{Name: "y", Kind: forge.KindGitHub, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_HOSTS_TOKEN"}}, "d"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("an internal host is refused unless allowed: %v", err)
	}
	add(t, s, h)
	if _, err := s.Add(ctx, hosts.AddInput{Name: "gh", Kind: forge.KindGitHub, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_HOSTS_TOKEN"}}, "d"); !errors.Is(err, forge.ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	vs, err := s.List(ctx)
	if err != nil || len(vs) != 1 {
		t.Fatalf("%v %v", vs, err)
	}
}

func TestFlightLifecycle(t *testing.T) {
	s, h := setup(t)
	ctx := t.Context()
	add(t, s, h)
	tr, err := s.Test(ctx, "gh")
	if err != nil || tr.Report.Account != "svc-launchway" {
		t.Fatalf("%+v %v", tr, err)
	}
	pg, err := s.Repositories(ctx, "gh", "", "app")
	if err != nil || len(pg.Items) != 1 {
		t.Fatalf("%+v %v", pg, err)
	}
	if pg, _ := s.Repositories(ctx, "gh", "", "zzz"); len(pg.Items) != 0 {
		t.Error("search")
	}
	if _, err := s.Attach(ctx, "shop", "gh", "other/thing"); err == nil {
		t.Error("a repository the fake does not have")
	}
	h.Violations = nil // that probe was the test's own
	f, err := s.Attach(ctx, "shop", "gh", "acme/app")
	if err != nil || !f.ReportStatuses || !f.ReportComment {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := s.Remove(ctx, "gh"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a connection in use is not removed: %v", err)
	}
	sha := strings.Repeat("d", 40)
	ok, err := s.ReportRun(ctx, "shop", forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Pending, URL: "https://lw/runs/1"})
	if err != nil || !ok || len(h.Statuses) != 1 {
		t.Fatalf("%v %v %v", ok, err, h.Statuses)
	}
	off := false
	if _, err := s.SetReports(ctx, "shop", &off, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ReportRun(ctx, "shop", forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Passing}); err != nil || ok || len(h.Statuses) != 1 {
		t.Errorf("statuses switched off still wrote: %v %v", ok, err)
	}
	if ok, err := s.ReportRun(ctx, "unattached", forge.Result{Producer: "p", Name: "n", SHA: sha, State: forge.Pending}); err != nil || ok {
		t.Errorf("an unattached project reports nothing: %v %v", ok, err)
	}
	for _, body := range []string{"one", "two"} {
		if ok, err := s.ReportSummary(ctx, "shop", 3, body); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if len(h.Comments) != 1 {
		t.Errorf("the summary is one comment, edited: %v", h.Comments)
	}
	ca, err := s.Clone(ctx, "shop")
	if err != nil || ca.Username != "x-access-token" || ca.Secret != githubfake.Token || !strings.HasSuffix(ca.URL, "/acme/app.git") {
		t.Fatalf("%+v %v", ca, err)
	}
	if _, err := s.CutRelease(ctx, "shop", strings.Repeat("e", 40), forge.ReleaseSpec{Tag: "v2.0.0", Title: "Two", Notes: "n"}, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Ref("tags/v2.0.0"); !ok {
		t.Error("the tag is made on the host")
	}
	// A direct write to a protected branch is refused with a message naming the pull request mode.
	h.Protect = true
	_, err = s.WriteFile(ctx, "shop", hosts.Writes{Branch: "main", Path: "v.yaml", Content: []byte("a"), Message: "bump"})
	if !errors.Is(err, hosts.ErrProtected) || !strings.Contains(err.Error(), "pull_request mode") {
		t.Fatalf("%v", err)
	}
	wr, err := s.WriteFile(ctx, "shop", hosts.Writes{Branch: "main", PullRequest: true, NewBranch: "bump/1", Path: "v.yaml", Content: []byte("a"), Message: "bump", Title: "Bump"})
	if err != nil || wr.PullRequest != 1 || wr.Branch != "bump/1" {
		t.Fatalf("%+v %v", wr, err)
	}
	if err := s.Detach(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, "gh"); err != nil {
		t.Fatal(err)
	}
	if len(h.Violations) > 0 {
		t.Errorf("violations: %v", h.Violations)
	}
}

func TestWebhookReplayAndFlights(t *testing.T) {
	s, h := setup(t)
	ctx := t.Context()
	add(t, s, h)
	if _, err := s.Attach(ctx, "shop", "gh", "acme/app"); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"ref":"refs/heads/main","before":"` + strings.Repeat("0", 40) + `","after":"` + strings.Repeat("a", 40) + `","repository":{"full_name":"Acme/App"},"sender":{"login":"bob"}}`)
	req := func(sig string) *http.Request {
		r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x/hook", bytes.NewReader(body))
		r.Header.Set("X-GitHub-Event", "push")
		r.Header.Set("X-GitHub-Delivery", "del-1")
		r.Header.Set("X-Hub-Signature-256", sig)
		return r
	}
	if _, err := s.Webhook(ctx, "gh", req("sha256=00")); err == nil {
		t.Fatal("a wrong signature is accepted")
	}
	d, err := s.Webhook(ctx, "gh", req(githubfake.Sign([]byte("hooksecret"), body)))
	if err != nil || !d.Fresh || len(d.Flights) != 1 || d.Flights[0].Project != "shop" {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = s.Webhook(ctx, "gh", req(githubfake.Sign([]byte("hooksecret"), body)))
	if err != nil || d.Fresh {
		t.Fatalf("a replay by delivery id is ignored: %+v %v", d, err)
	}
}

// The Flight's view carries the address of the webhook to set up and when a delivery last arrived, so the page can
// show both and say plainly what the host cannot do.
func TestFlightViewHasTheWebhookAddressAndLastDelivery(t *testing.T) {
	s, h := setup(t)
	ctx := t.Context()
	add(t, s, h)
	s.PublicURL = "https://lw.example.com/"
	if _, err := s.Attach(ctx, "shop", "gh", "acme/app"); err != nil {
		t.Fatal(err)
	}
	v, err := s.View(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if v.WebhookURL != "https://lw.example.com/api/v1/outside-hosts/gh/webhook" || v.LastDelivery != 0 || v.Kind != "github" || len(v.Unsupported) == 0 || len(v.Additions) == 0 {
		t.Fatalf("view %+v", v)
	}
	if _, err := s.Store().SeeDelivery(ctx, connectionID(t, s), "d-1"); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.View(ctx, "shop"); v.LastDelivery == 0 {
		t.Fatal("a delivery was received and the view does not say when")
	}
	if at, ok, err := s.LastDelivery(ctx, "gh"); err != nil || !ok || at.IsZero() {
		t.Fatalf("last delivery %v %v %v", at, ok, err)
	}
}

func connectionID(t *testing.T, s *hosts.Service) string {
	t.Helper()
	cs, err := s.Store().Connections(t.Context())
	if err != nil || len(cs) != 1 {
		t.Fatalf("%v %v", cs, err)
	}
	return cs[0].ID
}

// What is remembered of a delivery is what the signature covers: a different body under the same delivery id is a
// new delivery, the same body under another id is a replay, and after the replay window the same body is new again.
func TestReplayIsKeyedOnTheSignedBodyNotTheDeliveryID(t *testing.T) {
	s, h := setup(t)
	ctx := t.Context()
	add(t, s, h)
	if _, err := s.Attach(ctx, "shop", "gh", "acme/app"); err != nil {
		t.Fatal(err)
	}
	body := func(after string) []byte {
		return []byte(`{"ref":"refs/heads/main","before":"` + strings.Repeat("0", 40) + `","after":"` + strings.Repeat(after, 40) + `","repository":{"full_name":"Acme/App"},"sender":{"login":"bob"}}`)
	}
	send := func(b []byte, id string) hosts.Delivery {
		t.Helper()
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://x/hook", bytes.NewReader(b))
		r.Header.Set("X-GitHub-Event", "push")
		r.Header.Set("X-GitHub-Delivery", id)
		r.Header.Set("X-Hub-Signature-256", githubfake.Sign([]byte("hooksecret"), b))
		d, err := s.Webhook(ctx, "gh", r)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	now := time.Now()
	s.Store().SetClock(func() time.Time { return now })
	if d := send(body("a"), "id-1"); !d.Fresh || d.HostID != "id-1" {
		t.Fatalf("first: %+v", d)
	}
	if d := send(body("a"), "id-2"); d.Fresh {
		t.Fatal("the same signed body under a fresh id was processed twice")
	}
	if d := send(body("b"), "id-1"); !d.Fresh {
		t.Fatal("a different body under a used id was dropped")
	}
	now = now.Add(store.DefaultReplayWindow + time.Minute)
	if d := send(body("a"), "id-3"); !d.Fresh {
		t.Fatal("after the replay window the same body is a new delivery")
	}
}
