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
	"github.com/jk-magic-creations/launchway/internal/forge/providers/azuredevops"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/azuredevops/azuredevopsfake"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/bitbucket"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/bitbucket/bitbucketfake"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/gitea/giteafake"
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
	// repo is the repository's path on the host ("acme/app" when empty), cloneEnd what its clone address ends with
	// ("acme/app.git" when empty) and owners the connection's allow-list (none when empty).
	repo, cloneEnd string
	owners         []string
}

func (c hostCase) repoPath() string {
	if c.repo != "" {
		return c.repo
	}
	return "acme/app"
}

func (c hostCase) cloneSuffix() string {
	if c.cloneEnd != "" {
		return c.cloneEnd
	}
	return "acme/app.git"
}

func giteaCase(kind forge.Kind) hostCase {
	return hostCase{
		kind: kind,
		start: func(t *testing.T) (string, string, string, func() []string) {
			h := giteafake.New(kind == forge.KindForgejo)
			t.Cleanup(h.Close)
			hostFake = h
			return h.BaseURL(), giteafake.Token, giteafake.WebhookSecret, func() []string { return h.Violations }
		},
		delivery: func(t *testing.T, secret string) *http.Request {
			body := `{"ref":"refs/heads/main","before":"` + strings.Repeat("0", 40) + `","after":"` + strings.Repeat("a", 40) + `","repository":{"full_name":"acme/app"},"sender":{"login":"bob"}}`
			r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
			for k, v := range hostFake.Delivery("push", "del-1", []byte(body)) {
				r.Header[k] = v
			}
			if secret != giteafake.WebhookSecret { // a delivery signed with another secret
				name := map[bool]string{true: "X-Forgejo-Signature", false: "X-Gitea-Signature"}[kind == forge.KindForgejo]
				r.Header.Set(name, giteafake.Sign([]byte(secret), []byte(body)))
			}
			return r
		},
		lacks: forge.CapDrafts,
	}
}

var hostFake *giteafake.Host

var bbRestore func()

func bitbucketCase() hostCase {
	return hostCase{
		kind: forge.KindBitbucket,
		start: func(t *testing.T) (string, string, string, func() []string) {
			h := bitbucketfake.New()
			t.Cleanup(h.Close)
			bbRestore = bitbucket.UseTestAPI(h.API())
			t.Cleanup(bbRestore)
			return "", bitbucketfake.Access, bitbucketfake.WebhookSecret, func() []string { return h.Violations }
		},
		delivery: func(t *testing.T, secret string) *http.Request {
			body := `{"actor":{"nickname":"bob"},"repository":{"full_name":"acme/app"},"push":{"changes":[{"new":{"type":"branch","name":"main","target":{"hash":"` + strings.Repeat("a", 40) + `"}},"old":null}]}}`
			r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
			r.Header = bitbucketfake.Delivery("repo:push", "del-1", []byte(body))
			if secret != bitbucketfake.WebhookSecret {
				r.Header.Set("X-Hub-Signature", bitbucketfake.Sign([]byte(secret), []byte(body)))
			}
			return r
		},
		lacks: forge.CapReleases,
	}
}

func azureDevOpsCase() hostCase {
	return hostCase{
		kind: forge.KindAzureDevOps,
		start: func(t *testing.T) (string, string, string, func() []string) {
			h := azuredevopsfake.New()
			t.Cleanup(h.Close)
			adoFake = h
			t.Cleanup(azuredevops.UseTestAPI(h.API(), h.Collection()))
			return "", azuredevopsfake.Token, azuredevopsfake.WebhookSecret, func() []string { return h.Violations }
		},
		delivery: func(t *testing.T, secret string) *http.Request {
			body := `{"id":"notif-1","eventType":"git.push","createdDate":"2026-01-02T03:04:05Z","resource":{"refUpdates":[{"name":"refs/heads/main","oldObjectId":"` +
				strings.Repeat("0", 40) + `","newObjectId":"` + strings.Repeat("a", 40) + `"}],"repository":{"name":"app","project":{"name":"proj"}},"pushedBy":{"displayName":"Bob"}},` +
				`"resourceContainers":{"collection":{"baseUrl":"https://dev.azure.com/acme/"}}}`
			r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
			r.Header.Set("Authorization", azuredevopsfake.Basic("launchway", secret)) // a wrong secret is a wrong password
			return r
		},
		lacks: forge.CapReleases, repo: "acme/proj/app", cloneEnd: "proj/_git/app", owners: []string{"acme"},
	}
}

var adoFake *azuredevopsfake.Host

var kindCases = []hostCase{bitbucketCase(), azureDevOpsCase(), giteaCase(forge.KindGitea), giteaCase(forge.KindForgejo), {
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
				WebhookSecret: forge.SecretRef{Env: "LW_KIND_HOOK"}, Owners: c.owners}, "dana")
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
			if _, err := s.Attach(ctx, "shop", "h", c.repoPath()); err != nil {
				t.Fatal(err)
			}
			fv, err := s.View(ctx, "shop")
			if err != nil || fv.Kind != string(c.kind) || len(fv.Additions) == 0 || fv.Unsupported[c.lacks].Support == "" || fv.Unsupported[c.lacks].Reason == "" {
				t.Fatalf("view: %+v %v", fv, err)
			}
			if ca, err := s.Clone(ctx, "shop"); err != nil || ca.Secret != token || !strings.HasSuffix(ca.URL, c.cloneSuffix()) {
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
			_, noRefs := fv.Unsupported[forge.CapProposalRef]
			if l, ok := s.ProposalRefs(ctx, "shop"); !ok || l.IsZero() != noRefs {
				t.Errorf("layout %v %v (a host that declares no head refs gives none, and only then)", l, ok)
			}
			if vs := violations(); len(vs) > 0 {
				t.Errorf("the fake saw: %v", vs)
			}
		})
	}
}
