package gitea

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/forgetest"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/gitea/giteafake"
)

type fixture struct {
	h    *giteafake.Host
	p    *Provider
	repo forge.Repository
}

func newFixture(t *testing.T, kind forge.Kind, owners ...string) *fixture {
	t.Helper()
	h := giteafake.New(kind == forge.KindForgejo)
	t.Cleanup(h.Close)
	t.Setenv("LW_TEST_GT_TOKEN", giteafake.Token)
	g, err := egress.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(forge.Connection{ID: "con_1", Name: "gt", Kind: kind, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_TEST_GT_TOKEN"}, Owners: owners}, g)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h, p, forge.Repository{ID: "rep_1", Connection: "con_1", Path: "acme/app", DefaultBranch: "main", Visibility: forge.Private}}
}

func (f *fixture) noViolations(t *testing.T) {
	t.Helper()
	if len(f.h.Violations) > 0 {
		t.Errorf("the fake host saw calls the documentation does not allow: %v", f.h.Violations)
	}
}

// each runs a test for both kinds that share this implementation.
func each(t *testing.T, fn func(t *testing.T, f *fixture)) {
	for _, k := range []forge.Kind{forge.KindGitea, forge.KindForgejo} {
		t.Run(string(k), func(t *testing.T) { fn(t, newFixture(t, k)) })
	}
}

func TestConformance(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		forgetest.Run(t, forgetest.Harness{Provider: f.p, Repo: f.repo})
		f.noViolations(t)
	})
}

func TestCapabilitiesAreHonest(t *testing.T) {
	c := Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, feat := range []forge.Capability{forge.CapRepoCreate, forge.CapLand, forge.CapReviews, forge.CapDrafts} {
		if c.Has(feat) {
			t.Errorf("%s must be declared none", feat)
		}
	}
	each(t, func(t *testing.T, f *fixture) {
		if f.p.Kind() != f.p.conn.Kind {
			t.Errorf("kind %s", f.p.Kind())
		}
		if _, err := f.p.Proposals().Create(t.Context(), f.repo, forge.NewProposal{Title: "x", Draft: true}); err == nil {
			t.Error("a draft pull request is not made")
		}
		if _, err := New(forge.Connection{Kind: forge.KindGitHub, BaseURL: "https://x.example"}, nil); err == nil {
			t.Error("a kind that is not ours")
		}
	})
}

func TestWhoamiAndProbe(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		w, err := f.p.Identity().Whoami(t.Context())
		if err != nil || w.Principal.Login != "svc-launchway" {
			t.Fatalf("%+v %v", w, err)
		}
		r, err := f.p.Probe(t.Context())
		if err != nil || r.Account != "svc-launchway" || r.Repositories != 1 || r.TokenExpires != nil {
			t.Fatalf("%+v %v", r, err)
		}
		for _, c := range r.Checks {
			if !c.OK {
				t.Errorf("unexpected lack: %+v", c)
			}
		}
		f.h.Push = false
		r, _ = f.p.Probe(t.Context())
		lacking := 0
		for _, c := range r.Checks {
			if !c.OK {
				lacking++
				if c.Needs == "" || c.Detail == "" {
					t.Errorf("a lack must say what to grant: %+v", c)
				}
			}
		}
		if lacking != 4 {
			t.Errorf("a read-only account lacks %d write features, want 4", lacking)
		}
		f.h.Push, f.h.NoProtectionAccess = true, true
		r, _ = f.p.Probe(t.Context())
		for _, c := range r.Checks {
			if c.Feature == "Refuse a direct write to a protected branch" && (c.OK || c.Detail == "") {
				t.Errorf("a token that cannot read protection: %+v", c)
			}
		}
		f.noViolations(t)
	})
}

func TestOwnerAllowList(t *testing.T) {
	f := newFixture(t, forge.KindGitea, "other")
	if _, err := f.p.Repositories().Get(t.Context(), "acme/app"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a repository of an owner off the list: %v", err)
	}
	page, err := f.p.Repositories().List(t.Context(), "")
	if err != nil || len(page.Items) != 0 {
		t.Errorf("listing shows %v %v", page.Items, err)
	}
	g := newFixture(t, forge.KindGitea, "acme")
	if r, err := g.p.Repositories().Get(t.Context(), "acme/app"); err != nil || r.Home != "acme" || r.Visibility != forge.Private {
		t.Errorf("%+v %v", r, err)
	}
}

func TestResultsAreStatuses(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		sha := strings.Repeat("d", 40)
		if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Pending, Summary: strings.Repeat("word ", 60), URL: "https://lw.example.com/runs/1"}); err != nil {
			t.Fatal(err)
		}
		if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Errored}); err != nil {
			t.Fatal(err)
		}
		got, err := f.p.Results().List(t.Context(), f.repo, sha)
		if err != nil || len(got) != 1 || got[0].State != forge.Errored {
			t.Fatalf("the newest status of a context wins: %+v %v", got, err)
		}
		if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "x", Name: "n", SHA: "short", State: forge.Pending}); err == nil {
			t.Error("a short sha is refused before it leaves")
		}
		f.noViolations(t)
	})
}

func TestTagReleaseAndFiles(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		ctx := t.Context()
		commit := strings.Repeat("e", 40)
		file := func(name, body string) forge.ReleaseFile {
			return forge.ReleaseFile{Name: name, Size: int64(len(body)), Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }}
		}
		if _, err := f.p.Releases().PublishRelease(ctx, f.repo, forge.ReleaseSpec{Tag: "v1.0.0"}); !errors.Is(err, ErrTagMissing) {
			t.Fatalf("a release of a missing tag is not made: %v", err)
		}
		if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: commit, Message: "v1.0.0"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: commit, Message: "again"}); err != nil {
			t.Errorf("the same tag at the same commit is accepted: %v", err)
		}
		if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: strings.Repeat("f", 40), Message: "x"}); !errors.Is(err, forge.ErrExists) {
			t.Errorf("the same tag at another commit: %v", err)
		}
		rel := forge.ReleaseSpec{Tag: "v1.0.0", Title: "One", Notes: "first", Prerelease: true, Files: []forge.ReleaseFile{file("a.tgz", "AAAA"), file("b.tgz", "BB")}}
		u1, err := f.p.Releases().PublishRelease(ctx, f.repo, rel)
		if err != nil || !strings.HasSuffix(u1, "/acme/app/releases/tag/v1.0.0") {
			t.Fatalf("%q %v", u1, err)
		}
		rel.Notes = "edited"
		rel.Files[0] = file("a.tgz", "AAAAAA") // another size: replaced
		if _, err := f.p.Releases().PublishRelease(ctx, f.repo, rel); err != nil {
			t.Fatal(err)
		}
		uploads := 0
		for _, r := range f.h.Requests {
			if strings.HasSuffix(r, "/assets") && strings.HasPrefix(r, "POST") {
				uploads++
			}
		}
		if uploads != 3 {
			t.Errorf("uploads %d, want 3 (two, then the one that changed)", uploads)
		}
		f.noViolations(t)
	})
}

func TestCommitFileAndPullRequestAndSummary(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		ctx := t.Context()
		c1, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "deploy/1", Path: "env/prod/values.yaml", Content: []byte("image: v1\n"), Message: "deploy v1"})
		if err != nil || c1 == "" {
			t.Fatal(c1, err)
		}
		c2, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "deploy/1", Path: "env/prod/values.yaml", Content: []byte("image: v2\n"), Message: "deploy v2"})
		if err != nil || c2 == c1 {
			t.Fatal(c2, err)
		}
		if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "deploy/1", Path: "env/prod/other.yaml", Content: []byte("a: b\n"), Message: "more"}); err != nil {
			t.Fatal(err)
		}
		if b, _ := f.h.File("deploy/1", "env/prod/values.yaml"); string(b) != "image: v2\n" {
			t.Errorf("file %q", b)
		}
		if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", Path: "../x", Content: []byte("x"), Message: "m"}); err == nil {
			t.Error("a path outside the repository is refused")
		}
		pr, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "Deploy v2", Source: forge.Ref{Name: "refs/heads/deploy/1"}, Target: forge.Ref{Name: "refs/heads/main"}})
		if err != nil || pr.Number != 1 || pr.State != forge.ProposalOpen {
			t.Fatalf("%+v %v", pr, err)
		}
		k, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, "", "first summary")
		if err != nil {
			t.Fatal(err)
		}
		k2, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, k, "second summary")
		if err != nil || k2 != k || len(f.h.Comments) != 1 {
			t.Fatalf("the summary is updated in place: %q %q %v", k, k2, f.h.Comments)
		}
		f.h.Lock()
		f.h.Comments = map[int]string{}
		f.h.Unlock()
		if k3, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, k, "third"); err != nil || k3 == k {
			t.Errorf("a deleted comment is posted again: %q %v", k3, err)
		}
		if err := f.p.Proposals().Close(ctx, f.repo, 1); err != nil {
			t.Fatal(err)
		}
		if got, _ := f.p.Proposals().Get(ctx, f.repo, 1); got.State != forge.ProposalClosed {
			t.Errorf("closed: %+v", got)
		}
		f.noViolations(t)
	})
}

func TestAutoMergeIsScheduledAndCancelled(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		ctx := t.Context()
		if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "d", Path: "a", Content: []byte("x"), Message: "m"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "t", Source: forge.Ref{Name: "refs/heads/d"}, Target: forge.Ref{Name: "refs/heads/main"}}); err != nil {
			t.Fatal(err)
		}
		if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategySquash, true); err != nil || f.h.AutoMerge[1] != "squash" {
			t.Fatalf("%v %v", err, f.h.AutoMerge)
		}
		if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategyMerge, false); err != nil || len(f.h.AutoMerge) != 0 {
			t.Fatalf("%v %v", err, f.h.AutoMerge)
		}
		if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategyMerge, false); err != nil {
			t.Errorf("cancelling what is not scheduled is not an error: %v", err)
		}
		if err := f.p.Land().AutoLand(ctx, f.repo, 1, "bogus", true); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("a method the host lacks: %v", err)
		}
		f.noViolations(t)
	})
}

func TestRulesAndProtected(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		rs, err := f.p.Rules().List(t.Context(), f.repo)
		if err != nil || len(rs) != 0 {
			t.Fatalf("%v %v", rs, err)
		}
		f.h.Protect = true
		rs, err = f.p.Rules().List(t.Context(), f.repo)
		if err != nil || len(rs) != 1 || !rs[0].ProposalsOnly || rs[0].RequiredReviews != 1 || rs[0].RequiredResults[0] != "launchway/ci" {
			t.Fatalf("%+v %v", rs, err)
		}
		if f.p.conn.Kind == forge.KindForgejo && rs[0].Immutable {
			t.Error("a Forgejo rule has no force-push setting: unknown is not immutable")
		}
		if f.p.conn.Kind == forge.KindGitea && !rs[0].Immutable {
			t.Error("force push is off on the Gitea rule")
		}
		f.noViolations(t)
	})
}

func TestWebhook(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		secret := []byte(giteafake.WebhookSecret)
		send := func(event, id, body string, hdr http.Header) ([]forge.Event, error) {
			req, _ := http.NewRequest(http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
			for k, v := range hdr {
				req.Header[k] = v
			}
			return f.p.Events().Verify(req, secret)
		}
		sign := func(event, id, body string) http.Header { return f.h.Delivery(event, id, []byte(body)) }
		push := `{"ref":"refs/heads/main","before":"` + strings.Repeat("0", 40) + `","after":"` + strings.Repeat("a", 40) + `","repository":{"full_name":"acme/app"},"sender":{"login":"bob"}}`
		bad := sign("push", "d1", push)
		bad.Set(headerNames(f.p.conn.Kind, "Signature")[0], strings.Repeat("0", 64))
		if _, err := send("push", "d1", push, bad); !errors.Is(err, ErrBadSignature) {
			t.Errorf("wrong signature: %v", err)
		}
		none := sign("push", "d1", push)
		none.Del(headerNames(f.p.conn.Kind, "Signature")[0])
		if _, err := send("push", "d1", push, none); !errors.Is(err, ErrBadSignature) {
			t.Errorf("no signature: %v", err)
		}
		wrong := http.Header{}
		for k, v := range sign("push", "d1", push) {
			wrong[k] = v
		}
		wrong.Set(headerNames(f.p.conn.Kind, "Signature")[0], giteafake.Sign([]byte("other"), []byte(push)))
		if _, err := send("push", "d1", push, wrong); !errors.Is(err, ErrBadSignature) {
			t.Errorf("another secret: %v", err)
		}
		evs, err := send("push", "d1", push, sign("push", "d1", push))
		if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventRefUpdated || evs[0].Ref != "refs/heads/main" || evs[0].DeliveryID != "d1" || evs[0].Repository != "acme/app" {
			t.Fatalf("%+v %v", evs, err)
		}
		tag := `{"ref":"v1","ref_type":"tag","sha":"` + strings.Repeat("c", 40) + `","repository":{"full_name":"acme/app"},"sender":{"login":"bob"}}`
		evs, err = send("create", "d2", tag, sign("create", "d2", tag))
		if err != nil || len(evs) != 1 || evs[0].Ref != "refs/tags/v1" || evs[0].Commit != strings.Repeat("c", 40) {
			t.Fatalf("a tag: %+v %v", evs, err)
		}
		br := strings.Replace(tag, `"tag"`, `"branch"`, 1)
		if evs, err = send("create", "d3", br, sign("create", "d3", br)); err != nil || len(evs) != 0 {
			t.Errorf("a branch arrives as a push: %v %v", evs, err)
		}
		head := strings.Repeat("b", 40)
		pull := func(action, headRepo string, merged bool) string {
			return `{"action":"` + action + `","number":7,"repository":{"full_name":"acme/app"},"sender":{"login":"eve"},"pull_request":{"number":7,"title":"T","draft":true,"merged":` +
				map[bool]string{true: "true", false: "false"}[merged] + `,"head":{"ref":"f","sha":"` + head + `","repo":{"full_name":"` + headRepo + `"}},"base":{"ref":"main","sha":"x","repo":{"full_name":"acme/app"}}}}`
		}
		for _, c := range []struct {
			event, action, repo string
			merged              bool
			kind                forge.EventKind
			fork                bool
		}{
			{"pull_request", "opened", "acme/app", false, forge.EventProposalOpened, false},
			{"pull_request", "reopened", "acme/app", false, forge.EventProposalOpened, false},
			{"pull_request_sync", "synchronized", "eve/app", false, forge.EventProposalRevised, true},
			{"pull_request", "closed", "acme/app", false, forge.EventProposalClosed, false},
			{"pull_request", "closed", "acme/app", true, forge.EventProposalLanded, false},
		} {
			b := pull(c.action, c.repo, c.merged)
			evs, err := send(c.event, "d4", b, sign(c.event, "d4", b))
			if err != nil || len(evs) != 1 || evs[0].Kind != c.kind || evs[0].Fork != c.fork || evs[0].Number != 7 || !evs[0].Draft || evs[0].Head != head || evs[0].Base != "main" {
				t.Errorf("%s: %+v %v", c.action, evs, err)
			}
		}
		// A payload that does not carry the pull request's head is still an event; the caller asks the API.
		bare := `{"action":"opened","number":7,"repository":{"full_name":"acme/app"},"sender":{"login":"eve"}}`
		if evs, err := send("pull_request", "d5", bare, sign("pull_request", "d5", bare)); err != nil || len(evs) != 1 || evs[0].Head != "" || evs[0].Number != 7 {
			t.Errorf("no head: %+v %v", evs, err)
		}
		if evs, err := send("issues", "d6", `{}`, sign("issues", "d6", `{}`)); err != nil || len(evs) != 0 {
			t.Errorf("other events: %v %v", evs, err)
		}
		if DeliveryHeader(f.p.conn.Kind) != headerNames(f.p.conn.Kind, "Delivery")[0] {
			t.Error("delivery header")
		}
	})
}

func TestCredentialAndRedirectAndGuard(t *testing.T) {
	f := newFixture(t, forge.KindGitea)
	_, err := f.p.Client().Do(t.Context(), http.MethodGet, "https://evil.example.org/api/v1/user", nil, 0, "")
	if err == nil || !strings.Contains(err.Error(), "not this connection's host") || strings.Contains(err.Error(), giteafake.Token) {
		t.Fatalf("%v", err)
	}
	cred, err := f.p.Access().Credential(t.Context(), f.repo, false)
	if err != nil || cred.Secret != giteafake.Token || cred.Username != "svc-launchway" {
		t.Fatalf("%+v %v", cred, err)
	}
	red := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, f.h.URL+r.URL.Path, http.StatusFound)
	}))
	defer red.Close()
	before := len(f.h.Requests)
	g, _ := egress.New([]string{"127.0.0.1"})
	p, _ := New(forge.Connection{ID: "c", Name: "g", Kind: forge.KindGitea, BaseURL: red.URL, Credential: forge.SecretRef{Env: "LW_TEST_GT_TOKEN"}}, g)
	if _, err := p.Identity().Whoami(t.Context()); err == nil || !strings.Contains(err.Error(), "redirect") || len(f.h.Requests) != before {
		t.Fatalf("redirect: %v", err)
	}
	p2, _ := New(forge.Connection{ID: "c", Name: "g", Kind: forge.KindForgejo, BaseURL: f.h.BaseURL(), Credential: forge.SecretRef{Env: "LW_TEST_GT_TOKEN"}}, nil)
	if _, err := p2.Identity().Whoami(t.Context()); err == nil || !strings.Contains(err.Error(), "internal address") {
		t.Fatalf("guard: %v", err)
	}
}

func TestEndpoints(t *testing.T) {
	for base, want := range map[string]string{"https://git.example.com": "https://git.example.com/api/v1", "https://git.example.com/": "https://git.example.com/api/v1",
		"https://git.example.com/api/v1": "https://git.example.com/api/v1", "https://example.com/forge": "https://example.com/forge/api/v1"} {
		ep, err := EndpointsOf(base)
		if err != nil || ep.API.String() != want || len(ep.Hosts) != 1 {
			t.Errorf("%q: %v %v", base, ep.API, err)
		}
	}
	if _, err := EndpointsOf(""); err == nil || !strings.Contains(err.Error(), "self-managed") {
		t.Errorf("an install has to be named: %v", err)
	}
	if _, err := EndpointsOf("https://user:pw@x.example"); err == nil {
		t.Error("credentials in the address")
	}
}

func TestPollFindsPullRequests(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		ctx := t.Context()
		if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "feat", Path: "a", Content: []byte("x"), Message: "m"}); err != nil {
			t.Fatal(err)
		}
		pr, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "Add thing", Source: forge.Ref{Name: "refs/heads/feat"}, Target: forge.Ref{Name: "refs/heads/main"}})
		if err != nil {
			t.Fatal(err)
		}
		evs, cur, err := f.p.Events().Poll(ctx, f.repo, "")
		if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalOpened || evs[0].Number != pr.Number || evs[0].Base != "main" || evs[0].Title != "Add thing" || evs[0].Ref != "refs/pull/1/head" || evs[0].Fork {
			t.Fatalf("%+v %v", evs, err)
		}
		if evs, cur, err = f.p.Events().Poll(ctx, f.repo, cur); err != nil || len(evs) != 0 {
			t.Fatalf("nothing changed: %+v %v", evs, err)
		}
		f.h.Lock()
		f.h.PRs[0].HeadSHA, f.h.PRs[0].HeadRepo = strings.Repeat("e", 40), "eve/app"
		f.h.Unlock()
		evs, _, err = f.p.Events().Poll(ctx, f.repo, cur)
		if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalRevised || evs[0].Head != strings.Repeat("e", 40) || !evs[0].Fork {
			t.Fatalf("a moved head: %+v %v", evs, err)
		}
		if _, _, err := f.p.Events().Poll(ctx, f.repo, "!!"); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("a bad cursor: %v", err)
		}
		f.noViolations(t)
	})
}

func TestRefsAndCommit(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		if _, err := f.p.Releases().CreateTag(t.Context(), f.repo, forge.TagSpec{Name: "v1", Commit: strings.Repeat("c", 40), Message: "m"}); err != nil {
			t.Fatal(err)
		}
		refs, err := f.p.Content().Refs(t.Context(), f.repo)
		if err != nil || len(refs) != 2 {
			t.Fatalf("%v %v", refs, err)
		}
		for _, r := range refs {
			if len(r.Commit) != 40 {
				t.Errorf("a ref with no commit: %+v", r)
			}
		}
		c, err := f.p.Content().Commit(t.Context(), f.repo, strings.Repeat("a", 40))
		if err != nil || c.Author.Name != "Dev" || c.CreatedAt.Year() != 2026 || len(c.Parents) != 1 {
			t.Fatalf("%+v %v", c, err)
		}
		f.noViolations(t)
	})
}

// Live test against a real installation (Gitea or Forgejo): opt in with
// LAUNCHWAY_LIVE_GITEA_TOKEN, LAUNCHWAY_LIVE_GITEA_BASE and
// LAUNCHWAY_LIVE_GITEA_REPO (owner/name); LAUNCHWAY_LIVE_GITEA_KIND is gitea
// (default) or forgejo. It only reads and posts one commit status context.
func TestLive(t *testing.T) {
	tok, base, path := os.Getenv("LAUNCHWAY_LIVE_GITEA_TOKEN"), os.Getenv("LAUNCHWAY_LIVE_GITEA_BASE"), os.Getenv("LAUNCHWAY_LIVE_GITEA_REPO")
	if tok == "" || base == "" || path == "" {
		t.Skip("set LAUNCHWAY_LIVE_GITEA_TOKEN, LAUNCHWAY_LIVE_GITEA_BASE and LAUNCHWAY_LIVE_GITEA_REPO to run against a real installation")
	}
	kind := forge.Kind(os.Getenv("LAUNCHWAY_LIVE_GITEA_KIND"))
	if kind == "" {
		kind = forge.KindGitea
	}
	p, err := New(forge.Connection{ID: "con_live", Name: "live", Kind: kind, BaseURL: base, Credential: forge.SecretRef{Env: "LAUNCHWAY_LIVE_GITEA_TOKEN"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rep, err := p.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("account %s, checks %+v", rep.Account, rep.Checks)
	repo, err := p.Repositories().Get(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := p.Content().Refs(ctx, repo)
	if err != nil || len(refs) == 0 {
		t.Fatalf("%v %v", refs, err)
	}
	head := ""
	for _, r := range refs {
		if r.Name == "refs/heads/"+repo.DefaultBranch {
			head = r.Commit
		}
	}
	if head == "" {
		t.Fatal("the default branch has no head")
	}
	if err := p.Results().Publish(ctx, repo, forge.Result{Producer: "launchway", Name: "launchway/live-test", SHA: head, State: forge.Passing, Summary: "Launchway live conformance test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rules().List(ctx, repo); err != nil {
		t.Logf("branch protection read: %v", err)
	}
}
