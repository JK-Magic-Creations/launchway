package github

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/forgetest"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/github/githubfake"
)

type fixture struct {
	h    *githubfake.Host
	p    *Provider
	repo forge.Repository
}

func newFixture(t *testing.T, owners ...string) *fixture {
	t.Helper()
	h := githubfake.New()
	t.Cleanup(h.Close)
	t.Setenv("LW_TEST_GH_TOKEN", githubfake.Token)
	g, err := egress.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(forge.Connection{ID: "con_1", Name: "gh", Kind: forge.KindGitHub, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_TEST_GH_TOKEN"}, Owners: owners}, g)
	if err != nil {
		t.Fatal(err)
	}
	repo := forge.Repository{ID: "rep_1", Connection: "con_1", Path: "acme/app", DefaultBranch: "main", Visibility: forge.Private}
	return &fixture{h, p, repo}
}

func (f *fixture) noViolations(t *testing.T) {
	t.Helper()
	if len(f.h.Violations) > 0 {
		t.Errorf("the fake host saw calls the documentation does not allow: %v", f.h.Violations)
	}
}

// The same conformance suite as the built-in provider, against the fake host.
func TestConformance(t *testing.T) {
	f := newFixture(t)
	forgetest.Run(t, forgetest.Harness{Provider: f.p, Repo: f.repo})
	f.noViolations(t)
}

func TestCapabilitiesAreHonest(t *testing.T) {
	c := Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, feat := range []forge.Capability{forge.CapRepoCreate, forge.CapLand, forge.CapReviews} {
		if c.Has(feat) {
			t.Errorf("%s must be declared none", feat)
		}
	}
	f := newFixture(t)
	_, err := f.p.Repositories().Create(context.Background(), forge.NewRepository{Name: "x"})
	if u, ok := forge.IsUnsupported(err); !ok || u.Workaround == "" {
		t.Errorf("create: %v", err)
	}
}

func TestWhoamiAndProbe(t *testing.T) {
	f := newFixture(t)
	w, err := f.p.Identity().Whoami(t.Context())
	if err != nil || w.Principal.Login != "svc-launchway" || w.RateRemaining != 4999 {
		t.Fatalf("%+v %v", w, err)
	}
	r, err := f.p.Probe(t.Context())
	if err != nil || r.Account != "svc-launchway" || r.TokenExpires == nil || r.TokenExpires.Year() != 2030 || r.Repositories != 1 {
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
		t.Errorf("a read-only token lacks %d write features, want 4", lacking)
	}
	f.noViolations(t)
}

func TestOwnerAllowList(t *testing.T) {
	f := newFixture(t, "other")
	if _, err := f.p.Repositories().Get(t.Context(), "acme/app"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a repository of an owner off the list: %v", err)
	}
	page, err := f.p.Repositories().List(t.Context(), "")
	if err != nil || len(page.Items) != 0 {
		t.Errorf("listing shows %v %v", page.Items, err)
	}
	if len(f.h.Requests) != 1 { // only the listing; the Get never left the process
		t.Errorf("requests: %v", f.h.Requests)
	}
}

func TestResultsAreStatuses(t *testing.T) {
	f := newFixture(t)
	sha := strings.Repeat("d", 40)
	long := strings.Repeat("word ", 60)
	if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Pending, Summary: long, URL: "https://lw.example.com/runs/1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Passing}); err != nil {
		t.Fatal(err)
	}
	got, err := f.p.Results().List(t.Context(), f.repo, sha)
	if err != nil || len(got) != 1 || got[0].State != forge.Passing {
		t.Fatalf("the newest status of a context wins: %+v %v", got, err)
	}
	if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "x", Name: "n", SHA: "short", State: forge.Pending}); err == nil {
		t.Error("a short sha is refused before it leaves")
	}
	f.noViolations(t)
}

func TestTagReleaseAndFiles(t *testing.T) {
	f := newFixture(t)
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
	rel := forge.ReleaseSpec{Tag: "v1.0.0", Title: "One", Notes: "first", Files: []forge.ReleaseFile{file("a.tgz", "AAAA"), file("b.tgz", "BB")}}
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
		if strings.HasPrefix(r, "POST /api/uploads/") {
			uploads++
		}
	}
	if uploads != 3 {
		t.Errorf("uploads %d, want 3 (two, then the one that changed)", uploads)
	}
	f.noViolations(t)
}

func TestCommitFileAndPullRequestAndSummary(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	c1, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "deploy/1", Path: "env/prod/values.yaml", Content: []byte("image: v1\n"), Message: "deploy v1"})
	if err != nil || c1 == "" {
		t.Fatal(c1, err)
	}
	c2, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "deploy/1", Path: "env/prod/values.yaml", Content: []byte("image: v2\n"), Message: "deploy v2"})
	if err != nil || c2 == c1 {
		t.Fatal(c2, err)
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
	for _, v := range f.h.Comments {
		if v != "second summary" {
			t.Errorf("comment %q", v)
		}
	}
	f.noViolations(t)
}

func TestRulesAndProtected(t *testing.T) {
	f := newFixture(t)
	rs, err := f.p.Rules().List(t.Context(), f.repo)
	if err != nil || len(rs) != 0 || Protected(rs, "main") {
		t.Fatalf("%v %v", rs, err)
	}
	f.h.Protect = true
	rs, err = f.p.Rules().List(t.Context(), f.repo)
	if err != nil || len(rs) != 1 || !Protected(rs, "main") || rs[0].RequiredReviews != 1 || rs[0].RequiredResults[0] != "launchway/ci" {
		t.Fatalf("%+v %v", rs, err)
	}
	f.noViolations(t)
}

func TestWebhook(t *testing.T) {
	f := newFixture(t)
	secret := []byte("s3cret")
	send := func(event, delivery, body, sig string) ([]forge.Event, error) {
		req, _ := http.NewRequest(http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", delivery)
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		return f.p.Events().Verify(req, secret)
	}
	push := `{"ref":"refs/tags/v1","before":"` + strings.Repeat("0", 40) + `","after":"` + strings.Repeat("a", 40) + `","repository":{"full_name":"acme/app"},"sender":{"login":"bob"}}`
	if _, err := send("push", "d1", push, ""); !errors.Is(err, ErrBadSignature) {
		t.Errorf("unsigned: %v", err)
	}
	if _, err := send("push", "d1", push, githubfake.Sign([]byte("wrong"), []byte(push))); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong secret: %v", err)
	}
	evs, err := send("push", "d1", push, githubfake.Sign(secret, []byte(push)))
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventRefUpdated || evs[0].Ref != "refs/tags/v1" || evs[0].DeliveryID != "d1" || evs[0].Repository != "acme/app" {
		t.Fatalf("%+v %v", evs, err)
	}
	pull := func(action, headRepo string, merged bool) string {
		m := "null"
		if merged {
			m = `"2026-01-01T00:00:00Z"`
		}
		return `{"action":"` + action + `","number":7,"pull_request":{"number":7,"title":"T","draft":true,"state":"open","merged_at":` + m + `,"head":{"ref":"f","sha":"` + strings.Repeat("b", 40) + `","repo":{"full_name":"` + headRepo + `"}},"base":{"ref":"main","sha":"x","repo":{"full_name":"acme/app"}}},"repository":{"full_name":"acme/app"},"sender":{"login":"eve"}}`
	}
	for _, c := range []struct {
		action, repo string
		merged       bool
		kind         forge.EventKind
		fork         bool
	}{
		{"opened", "acme/app", false, forge.EventProposalOpened, false},
		{"synchronize", "eve/app", false, forge.EventProposalRevised, true},
		{"closed", "acme/app", false, forge.EventProposalClosed, false},
		{"closed", "acme/app", true, forge.EventProposalLanded, false},
	} {
		b := pull(c.action, c.repo, c.merged)
		evs, err := send("pull_request", "d2", b, githubfake.Sign(secret, []byte(b)))
		if err != nil || len(evs) != 1 || evs[0].Kind != c.kind || evs[0].Fork != c.fork || evs[0].Number != 7 || !evs[0].Draft || evs[0].Head != strings.Repeat("b", 40) {
			t.Errorf("%s: %+v %v", c.action, evs, err)
		}
	}
	if b := pull("labeled", "acme/app", false); true {
		if evs, err := send("pull_request", "d3", b, githubfake.Sign(secret, []byte(b))); err != nil || len(evs) != 0 {
			t.Errorf("labeled starts nothing: %v %v", evs, err)
		}
	}
	if evs, err := send("ping", "d4", `{}`, githubfake.Sign(secret, []byte(`{}`))); err != nil || len(evs) != 0 {
		t.Errorf("ping: %v %v", evs, err)
	}
}

// The credential goes only to the connection's hosts; a host the API names
// elsewhere (an upload address on another host) is refused before any request.
func TestCredentialNeverLeavesTheConnectionsHosts(t *testing.T) {
	f := newFixture(t)
	_, err := f.p.Client().Upload(t.Context(), "https://evil.example.org/upload{?name}", "a", forge.ReleaseFile{Name: "a", Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }})
	if err == nil || !strings.Contains(err.Error(), "not this connection's host") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), githubfake.Token) {
		t.Error("token in error")
	}
}

func TestEgressGuardRefusesAPrivateHostUnlessAllowed(t *testing.T) {
	h := githubfake.New()
	defer h.Close()
	t.Setenv("LW_TEST_GH_TOKEN", githubfake.Token)
	p, _ := New(forge.Connection{ID: "c", Name: "gh", Kind: forge.KindGitHub, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_TEST_GH_TOKEN"}}, nil)
	_, err := p.Identity().Whoami(t.Context())
	if err == nil || !errors.Is(err, egress.ErrRefused) && !strings.Contains(err.Error(), "internal address") {
		t.Fatalf("%v", err)
	}
	if len(h.Requests) != 0 {
		t.Error("a request reached the refused host")
	}
}

func TestNoRedirectIsFollowed(t *testing.T) {
	f := newFixture(t)
	srv := newRedirector(t, f.h.URL)
	before := len(f.h.Requests)
	t.Setenv("LW_TEST_GH_TOKEN", githubfake.Token)
	g, _ := egress.New([]string{"127.0.0.1"})
	p, _ := New(forge.Connection{ID: "c", Name: "gh", Kind: forge.KindGitHub, BaseURL: srv, Credential: forge.SecretRef{Env: "LW_TEST_GH_TOKEN"}}, g)
	_, err := p.Identity().Whoami(t.Context())
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("%v", err)
	}
	if len(f.h.Requests) != before {
		t.Error("the redirect was followed")
	}
}

func TestEndpoints(t *testing.T) {
	for base, want := range map[string]string{"": "https://api.github.com", "https://github.com/": "https://api.github.com",
		"https://git.example.com": "https://git.example.com/api/v3", "https://git.example.com/api/v3": "https://git.example.com/api/v3"} {
		ep, err := EndpointsOf(base, "")
		if err != nil || ep.API.String() != want {
			t.Errorf("%q: %v %v", base, ep.API, err)
		}
	}
	ep, _ := EndpointsOf("", "")
	if ep.Upload.Host != "uploads.github.com" || !ep.Hosts["api.github.com"] || len(ep.Hosts) != 2 {
		t.Errorf("%+v", ep)
	}
	if _, err := EndpointsOf("https://user:pw@x.example", ""); err == nil {
		t.Error("credentials in the address")
	}
}

// Live test against the real service: opt in with LAUNCHWAY_LIVE_GITHUB_TOKEN
// and LAUNCHWAY_LIVE_GITHUB_REPO (owner/name). It only reads and posts one
// commit status context to the repository's default branch head.
func TestLive(t *testing.T) {
	tok, repoPath := os.Getenv("LAUNCHWAY_LIVE_GITHUB_TOKEN"), os.Getenv("LAUNCHWAY_LIVE_GITHUB_REPO")
	if tok == "" || repoPath == "" {
		t.Skip("set LAUNCHWAY_LIVE_GITHUB_TOKEN and LAUNCHWAY_LIVE_GITHUB_REPO to run against the real service")
	}
	t.Setenv("LAUNCHWAY_LIVE_GITHUB_TOKEN", tok)
	p, err := New(forge.Connection{ID: "con_live", Name: "live", Kind: forge.KindGitHub, BaseURL: os.Getenv("LAUNCHWAY_LIVE_GITHUB_BASE"),
		Credential: forge.SecretRef{Env: "LAUNCHWAY_LIVE_GITHUB_TOKEN"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rep, err := p.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("account %s, expires %v, checks %+v", rep.Account, rep.TokenExpires, rep.Checks)
	repo, err := p.Repositories().Get(ctx, repoPath)
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
