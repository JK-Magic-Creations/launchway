package hosts_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/gitlab/gitlabfake"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// hostCase is one kind of outside host, run through the same service journey:
// the service knows no kind by name, so every kind that is registered must
// pass it with its own strict fake.
type hostCase struct {
	kind     forge.Kind
	start    func(t *testing.T) (base, token, hookSecret string, violations func() []string)
	delivery func(t *testing.T, secret string) *http.Request // a valid push delivery for acme/app
	lacks    forge.Capability                                // one capability the kind declares not native
}

var kindCases = []hostCase{{
	kind: forge.KindGitLab,
	start: func(t *testing.T) (string, string, string, func() []string) {
		h := gitlabfake.New()
		t.Cleanup(h.Close)
		return h.BaseURL(), gitlabfake.Token, gitlabfake.WebhookSecret, func() []string { return h.Violations }
	},
	delivery: func(t *testing.T, secret string) *http.Request {
		body := `{"ref":"refs/heads/main","before":"` + strings.Repeat("0", 40) + `","after":"` + strings.Repeat("a", 40) + `","user_username":"bob","project":{"path_with_namespace":"acme/app"}}`
		r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
		r.Header.Set("X-Gitlab-Event", "Push Hook")
		r.Header.Set("X-Gitlab-Token", secret)
		r.Header.Set("webhook-id", "del-1")
		return r
	},
	lacks: forge.CapDrafts,
}}

func TestEveryKindPassesTheServiceJourney(t *testing.T) {
	for _, c := range kindCases {
		t.Run(string(c.kind), func(t *testing.T) {
			eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { eng.Close() })
			base, token, hook, violations := c.start(t)
			t.Setenv("LW_KIND_TOKEN", token)
			t.Setenv("LW_KIND_HOOK", hook)
			g, _ := egress.New([]string{"127.0.0.1"})
			s := hosts.New(eng.Database(), eng, g)
			s.PublicURL = "https://lw.example.com"
			ctx := t.Context()
			v, err := s.Add(ctx, hosts.AddInput{Name: "h", Kind: c.kind, BaseURL: base, Credential: forge.SecretRef{Env: "LW_KIND_TOKEN"},
				WebhookSecret: forge.SecretRef{Env: "LW_KIND_HOOK"}}, "dana")
			if err != nil || v.Kind != string(c.kind) || len(v.Capabilities) == 0 {
				t.Fatalf("%+v %v", v, err)
			}
			if strings.Contains(v.Credential, token) {
				t.Fatal("the view shows the token")
			}
			res, err := s.Test(ctx, "h")
			if err != nil || res.Report.Account == "" || len(res.Report.Checks) == 0 {
				t.Fatalf("test: %+v %v", res, err)
			}
			for _, ch := range res.Report.Checks {
				if !ch.OK {
					t.Errorf("a full token lacks %+v", ch)
				}
			}
			page, err := s.Repositories(ctx, "h", "", "app")
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("repositories: %+v %v", page, err)
			}
			if _, err := s.Attach(ctx, "shop", "h", "acme/app"); err != nil {
				t.Fatal(err)
			}
			fv, err := s.View(ctx, "shop")
			if err != nil || fv.Kind != string(c.kind) || len(fv.Additions) == 0 || fv.Unsupported[c.lacks].Support == "" || fv.Unsupported[c.lacks].Reason == "" {
				t.Fatalf("view: %+v %v", fv, err)
			}
			if ca, err := s.Clone(ctx, "shop"); err != nil || ca.Secret != token || !strings.HasSuffix(ca.URL, "acme/app.git") {
				t.Fatalf("clone: %+v %v", ca, err)
			}
			if ok, err := s.ReportRun(ctx, "shop", forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: strings.Repeat("d", 40), State: forge.Passing}); err != nil || !ok {
				t.Fatalf("status: %v %v", ok, err)
			}
			// A webhook: a wrong secret is refused, a right one is read once, its replay is dropped.
			if _, err := s.Webhook(ctx, "h", c.delivery(t, "wrong")); err == nil {
				t.Fatal("a wrong secret is accepted")
			}
			d, err := s.Webhook(ctx, "h", c.delivery(t, hook))
			if err != nil || !d.Fresh || len(d.Flights) != 1 || d.Flights[0].Project != "shop" {
				t.Fatalf("webhook: %+v %v", d, err)
			}
			if d, err = s.Webhook(ctx, "h", c.delivery(t, hook)); err != nil || d.Fresh {
				t.Fatalf("a replay is dropped: %+v %v", d, err)
			}
			if l, ok := s.ProposalRefs(ctx, "shop"); !ok || l.IsZero() {
				t.Errorf("layout %v %v", l, ok)
			}
			if vs := violations(); len(vs) > 0 {
				t.Errorf("the fake saw: %v", vs)
			}
		})
	}
}
