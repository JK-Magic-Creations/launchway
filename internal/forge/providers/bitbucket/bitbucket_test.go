package bitbucket

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/forgetest"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/bitbucket/bitbucketfake"
)

type fixture struct {
	h    *bitbucketfake.Host
	p    *Provider
	repo forge.Repository
}

func endpointsOf(t *testing.T, api string) Endpoints {
	u, err := url.Parse(api)
	if err != nil {
		t.Fatal(err)
	}
	return Endpoints{API: u, Hosts: map[string]bool{u.Host: true}}
}

func newFixture(t *testing.T, owners ...string) *fixture {
	t.Helper()
	h := bitbucketfake.New()
	t.Cleanup(h.Close)
	t.Setenv("LW_TEST_BB_TOKEN", bitbucketfake.Access)
	g, _ := egress.New([]string{"127.0.0.1"})
	p := NewAt(forge.Connection{ID: "con_1", Name: "bb", Kind: forge.KindBitbucket, Credential: forge.SecretRef{Env: "LW_TEST_BB_TOKEN"}, Owners: owners}, endpointsOf(t, h.API()), g)
	return &fixture{h, p, forge.Repository{ID: "rep_1", Connection: "con_1", Path: "acme/app", DefaultBranch: "main", Visibility: forge.Private}}
}

func (f *fixture) noViolations(t *testing.T) {
	t.Helper()
	if len(f.h.Violations) > 0 {
		t.Errorf("the fake host saw calls the documentation does not allow: %v", f.h.Violations)
	}
}

func TestConformance(t *testing.T) {
	f := newFixture(t)
	forgetest.Run(t, forgetest.Harness{Provider: f.p, Repo: f.repo})
	f.noViolations(t)
}

// Bitbucket has no release object: the capability is none, with the reason and the workaround, and a call says so.
func TestReleasesAreDeclaredNoneHonestly(t *testing.T) {
	c := Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	d := c[forge.CapReleases]
	if d.Support != forge.None || !strings.Contains(d.Reason, "no release object") || !strings.Contains(d.Workaround, "release page") {
		t.Fatalf("%+v", d)
	}
	for _, feat := range []forge.Capability{forge.CapRepoCreate, forge.CapLand, forge.CapReviews, forge.CapAutoLand, forge.CapProposalRef} {
		if c.Has(feat) {
			t.Errorf("%s must be declared none", feat)
		}
	}
	f := newFixture(t)
	_, err := f.p.Releases().PublishRelease(t.Context(), f.repo, forge.ReleaseSpec{Tag: "v1"})
	if u, ok := forge.IsUnsupported(err); !ok || u.Capability != forge.CapReleases || u.Workaround == "" {
		t.Fatalf("%v", err)
	}
	if !f.p.ProposalRefs().IsZero() {
		t.Error("a host with no head refs gives no layout")
	}
	if len(f.h.Requests) != 0 {
		t.Error("an unsupported call reached the host")
	}
}

func TestWhoamiAndProbe(t *testing.T) {
	f := newFixture(t)
	w, err := f.p.Identity().Whoami(t.Context())
	if err != nil || w.Principal.Login != "svc-launchway" {
		t.Fatalf("%+v %v", w, err)
	}
	r, err := f.p.Probe(t.Context())
	if err != nil || r.Account != "svc-launchway" || r.Repositories != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range r.Checks {
		if !c.OK {
			t.Errorf("unexpected lack: %+v", c)
		}
	}
	f.h.Write = false
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
	// A repository access token has no account: it is named plainly and its writes cannot be checked.
	f.h.NoAccount = true
	w, err = f.p.Identity().Whoami(t.Context())
	if err != nil || w.Principal.Login != "access-token" {
		t.Fatalf("%+v %v", w, err)
	}
	r, err = f.p.Probe(t.Context())
	if err != nil || r.Account != "access-token" || r.Repositories != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range r.Checks {
		if c.Feature == "Commit statuses on each run" && (c.OK || !strings.Contains(c.Detail, "no token scopes")) {
			t.Errorf("%+v", c)
		}
	}
	f.noViolations(t)
}

func TestOwnerAllowListAndRepositories(t *testing.T) {
	f := newFixture(t, "other")
	if _, err := f.p.Repositories().Get(t.Context(), "acme/app"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a workspace off the list: %v", err)
	}
	g := newFixture(t) // no allow-list: the account's workspaces are asked
	page, err := g.p.Repositories().List(t.Context(), "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Path != "acme/app" || page.Items[0].DefaultBranch != "main" || page.Items[0].Clone.HTTPS == "" || page.Items[0].Home != "acme" {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err := g.p.Repositories().List(t.Context(), "x"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a bad cursor: %v", err)
	}
	h := newFixture(t, "acme") // an allow-list is the list: no call for the account's workspaces
	if page, err := h.p.Repositories().List(t.Context(), ""); err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	for _, r := range h.h.Requests {
		if strings.Contains(r, "/user") {
			t.Errorf("the account was asked for its workspaces: %v", h.h.Requests)
		}
	}
	f.noViolations(t)
	g.noViolations(t)
}

func TestResultsAreBuildStatuses(t *testing.T) {
	f := newFixture(t)
	sha := strings.Repeat("d", 40)
	for _, st := range []forge.State{forge.Pending, forge.Passing, forge.Errored} {
		if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: st, Summary: "Sortie #1", URL: "https://lw.example.com/runs/1"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.p.Results().List(t.Context(), f.repo, sha)
	if err != nil || len(got) != 1 || got[0].State != forge.Errored || got[0].Name != "launchway/ci" {
		t.Fatalf("a name has one status, updated in place: %+v %v", got, err)
	}
	if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "x", Name: "n", SHA: "short", State: forge.Pending}); err == nil {
		t.Error("a short sha is refused before it leaves")
	}
	f.noViolations(t)
}

func TestTagFileAndPullRequestAndComment(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	commit := strings.Repeat("e", 40)
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: commit, Message: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: commit, Message: "again"}); err != nil {
		t.Errorf("the same tag at the same commit is accepted: %v", err)
	}
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: strings.Repeat("f", 40), Message: "x"}); !errors.Is(err, forge.ErrExists) {
		t.Errorf("the same tag at another commit: %v", err)
	}
	c1, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "deploy/1", Path: "env/prod/values.yaml", Content: []byte("image: v1\n"), Message: "deploy v1"})
	if err != nil || c1 == "" {
		t.Fatal(c1, err)
	}
	c2, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "deploy/1", Path: "env/prod/values.yaml", Content: []byte("image: v2\n"), Message: "deploy v2"})
	if err != nil || c2 == c1 {
		t.Fatal(c2, err)
	}
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "deploy/1", Path: "other.yaml", Content: []byte("a: b\n"), Message: "more"}); err != nil {
		t.Fatalf("a retry writes to the branch that exists: %v", err)
	}
	if b, _ := f.h.File("deploy/1", "env/prod/values.yaml"); string(b) != "image: v2\n" {
		t.Errorf("file %q", b)
	}
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", Path: "../x", Content: []byte("x"), Message: "m"}); err == nil {
		t.Error("a path outside the repository is refused")
	}
	pr, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "Deploy v2", Source: forge.Ref{Name: "refs/heads/deploy/1"}, Target: forge.Ref{Name: "refs/heads/main"}, Draft: true})
	if err != nil || pr.Number != 1 || pr.State != forge.ProposalDraft {
		t.Fatalf("%+v %v", pr, err)
	}
	if got, err := f.p.Proposals().Update(ctx, f.repo, 1, forge.ProposalPatch{Draft: ptr(false)}); err != nil || got.State != forge.ProposalOpen {
		t.Fatalf("ready: %+v %v", got, err)
	}
	k, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, "", "first summary")
	if err != nil {
		t.Fatal(err)
	}
	if k2, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, k, "second summary"); err != nil || k2 != k || len(f.h.Comments) != 1 {
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
		t.Errorf("declined: %+v", got)
	}
	if err := f.p.Proposals().Reopen(ctx, f.repo, 1); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a declined pull request is not reopened: %v", err)
	}
	f.noViolations(t)
}

func ptr[T any](v T) *T { return &v }

func TestRulesAndProtected(t *testing.T) {
	f := newFixture(t)
	rs, err := f.p.Rules().List(t.Context(), f.repo)
	if err != nil || len(rs) != 0 {
		t.Fatalf("%v %v", rs, err)
	}
	f.h.Protect = true
	rs, err = f.p.Rules().List(t.Context(), f.repo)
	if err != nil || len(rs) != 1 || !rs[0].ProposalsOnly || rs[0].RequiredReviews != 2 || len(rs[0].RequiredResults) != 1 {
		t.Fatalf("%+v %v", rs, err)
	}
	f.noViolations(t)
}

func TestWebhook(t *testing.T) {
	f := newFixture(t)
	secret := []byte(bitbucketfake.WebhookSecret)
	send := func(event, id, body string, hdr http.Header) ([]forge.Event, error) {
		req, _ := http.NewRequest(http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
		for k, v := range hdr {
			req.Header[k] = v
		}
		return f.p.Events().Verify(req, secret)
	}
	signed := func(event, id, body string) http.Header { return bitbucketfake.Delivery(event, id, []byte(body)) }
	h40 := func(c string) string { return strings.Repeat(c, 40) }
	push := `{"actor":{"nickname":"bob"},"repository":{"full_name":"acme/app"},"push":{"changes":[` +
		`{"new":{"type":"branch","name":"main","target":{"hash":"` + h40("a") + `"}},"old":{"type":"branch","name":"main","target":{"hash":"` + h40("b") + `"}}},` +
		`{"new":{"type":"tag","name":"v1","target":{"hash":"` + h40("c") + `"}},"old":null},` +
		`{"new":null,"old":{"type":"branch","name":"gone","target":{"hash":"` + h40("d") + `"}}}]}}`
	bad := signed("repo:push", "d1", push)
	bad.Set("X-Hub-Signature", "sha256="+strings.Repeat("0", 64))
	if _, err := send("repo:push", "d1", push, bad); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong signature: %v", err)
	}
	none := signed("repo:push", "d1", push)
	none.Del("X-Hub-Signature")
	if _, err := send("repo:push", "d1", push, none); !errors.Is(err, ErrBadSignature) {
		t.Errorf("no signature: %v", err)
	}
	sha1 := signed("repo:push", "d1", push)
	sha1.Set("X-Hub-Signature", strings.Replace(sha1.Get("X-Hub-Signature"), "sha256=", "sha1=", 1))
	if _, err := send("repo:push", "d1", push, sha1); !errors.Is(err, ErrBadSignature) {
		t.Errorf("another method: %v", err)
	}
	evs, err := send("repo:push", "d1", push, signed("repo:push", "d1", push))
	if err != nil || len(evs) != 3 || evs[0].Ref != "refs/heads/main" || evs[0].Commit != h40("a") || evs[0].Old != h40("b") || evs[1].Ref != "refs/tags/v1" ||
		evs[2].Ref != "refs/heads/gone" || evs[2].Commit != strings.Repeat("0", 40) || evs[0].Repository != "acme/app" || evs[0].Actor.Login != "bob" {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs[0].DeliveryID == evs[1].DeliveryID {
		t.Error("each change of a push has its own delivery id")
	}
	pull := func(srcRepo string) string {
		return `{"actor":{"nickname":"eve"},"repository":{"full_name":"acme/app"},"pullrequest":{"id":7,"title":"T","draft":true,"state":"OPEN","author":{"nickname":"eve"},` +
			`"source":{"branch":{"name":"f"},"commit":{"hash":"` + h40("b") + `"},"repository":{"full_name":"` + srcRepo + `"}},` +
			`"destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/app"}},"links":{"html":{"href":"https://x/pr/7"}}}}`
	}
	for _, c := range []struct {
		key  string
		repo string
		kind forge.EventKind
		fork bool
	}{
		{"pullrequest:created", "acme/app", forge.EventProposalOpened, false},
		{"pullrequest:updated", "eve/app", forge.EventProposalRevised, true},
		{"pullrequest:fulfilled", "acme/app", forge.EventProposalLanded, false},
		{"pullrequest:rejected", "acme/app", forge.EventProposalClosed, false},
	} {
		b := pull(c.repo)
		evs, err := send(c.key, "d2", b, signed(c.key, "d2", b))
		if err != nil || len(evs) != 1 || evs[0].Kind != c.kind || evs[0].Fork != c.fork || evs[0].Number != 7 || !evs[0].Draft || evs[0].Head != h40("b") || evs[0].Base != "main" || evs[0].Author != "eve" {
			t.Errorf("%s: %+v %v", c.key, evs, err)
		}
	}
	for _, other := range []string{"pullrequest:approved", "repo:fork", "pullrequest:comment_created"} {
		if evs, err := send(other, "d3", `{}`, signed(other, "d3", `{}`)); err != nil || len(evs) != 0 {
			t.Errorf("%s starts nothing: %v %v", other, evs, err)
		}
	}
	// No request id: a delivery is named by its body.
	h := signed("repo:push", "", push)
	h.Del("X-Request-UUID")
	if evs, err := send("repo:push", "", push, h); err != nil || !strings.HasPrefix(evs[0].DeliveryID, "body-") {
		t.Errorf("%v %v", evs, err)
	}
}

func TestCredentialRedirectGuardAndEndpoints(t *testing.T) {
	f := newFixture(t)
	if _, err := f.p.Client().Do(t.Context(), http.MethodGet, "https://evil.example.org/2.0/user", nil, 0, ""); err == nil ||
		!strings.Contains(err.Error(), "not this connection's host") || strings.Contains(err.Error(), bitbucketfake.Access) {
		t.Fatalf("%v", err)
	}
	cred, err := f.p.Access().Credential(t.Context(), f.repo, false)
	if err != nil || cred.Secret != bitbucketfake.Access || cred.Username != "x-token-auth" {
		t.Fatalf("%+v %v", cred, err)
	}
	red := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, f.h.URL+r.URL.Path, http.StatusFound)
	}))
	defer red.Close()
	before := len(f.h.Requests)
	g, _ := egress.New([]string{"127.0.0.1"})
	p := NewAt(forge.Connection{ID: "c", Name: "b", Kind: forge.KindBitbucket, Credential: forge.SecretRef{Env: "LW_TEST_BB_TOKEN"}}, endpointsOf(t, red.URL+"/2.0"), g)
	if _, err := p.Identity().Whoami(t.Context()); err == nil || !strings.Contains(err.Error(), "redirect") || len(f.h.Requests) != before {
		t.Fatalf("redirect: %v", err)
	}
	p2 := NewAt(forge.Connection{ID: "c", Name: "b", Kind: forge.KindBitbucket, Credential: forge.SecretRef{Env: "LW_TEST_BB_TOKEN"}}, endpointsOf(t, f.h.API()), nil)
	if _, err := p2.Identity().Whoami(t.Context()); err == nil || !strings.Contains(err.Error(), "internal address") {
		t.Fatalf("guard: %v", err)
	}
	for _, base := range []string{"", "https://bitbucket.org", "https://bitbucket.org/"} {
		if ep, err := EndpointsOf(base); err != nil || ep.API.String() != "https://api.bitbucket.org/2.0" || len(ep.Hosts) != 1 {
			t.Errorf("%q: %v %v", base, ep.API, err)
		}
	}
	if _, err := EndpointsOf("https://bitbucket.example.com"); err == nil || !strings.Contains(err.Error(), "Data Center") || !strings.Contains(err.Error(), "kind bitbucket-datacenter") {
		t.Errorf("the self-managed product is pointed to its own kind: %v", err)
	}
}

func TestPollFindsPullRequests(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "feat", Path: "a", Content: []byte("x"), Message: "m"}); err != nil {
		t.Fatal(err)
	}
	pr, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "Add thing", Source: forge.Ref{Name: "refs/heads/feat"}, Target: forge.Ref{Name: "refs/heads/main"}})
	if err != nil {
		t.Fatal(err)
	}
	evs, cur, err := f.p.Events().Poll(ctx, f.repo, "")
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalOpened || evs[0].Number != pr.Number || evs[0].Base != "main" || evs[0].Title != "Add thing" || evs[0].Fork {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs, cur, err = f.p.Events().Poll(ctx, f.repo, cur); err != nil || len(evs) != 0 {
		t.Fatalf("nothing changed: %+v %v", evs, err)
	}
	f.h.Lock()
	f.h.PRs[0].SHA, f.h.PRs[0].SrcRepo = strings.Repeat("e", 40), "eve/app"
	f.h.Unlock()
	evs, _, err = f.p.Events().Poll(ctx, f.repo, cur)
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalRevised || evs[0].Head != strings.Repeat("e", 40) || !evs[0].Fork {
		t.Fatalf("a moved head: %+v %v", evs, err)
	}
	if _, _, err := f.p.Events().Poll(ctx, f.repo, "!!"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a bad cursor: %v", err)
	}
	f.noViolations(t)
}

func TestRefsAndCommit(t *testing.T) {
	f := newFixture(t)
	if _, err := f.p.Releases().CreateTag(t.Context(), f.repo, forge.TagSpec{Name: "v1", Commit: strings.Repeat("c", 40), Message: "m"}); err != nil {
		t.Fatal(err)
	}
	refs, err := f.p.Content().Refs(t.Context(), f.repo)
	if err != nil || len(refs) != 2 {
		t.Fatalf("%v %v", refs, err)
	}
	c, err := f.p.Content().Commit(t.Context(), f.repo, strings.Repeat("a", 40))
	if err != nil || !strings.Contains(c.Author.Name, "Dev") || c.CreatedAt.Year() != 2026 || len(c.Parents) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	f.noViolations(t)
}

// Live test against Bitbucket Cloud: opt in with LAUNCHWAY_LIVE_BITBUCKET_TOKEN
// and LAUNCHWAY_LIVE_BITBUCKET_REPO (workspace/name). It only reads and posts
// one build status to the main branch head.
func TestLive(t *testing.T) {
	tok, path := os.Getenv("LAUNCHWAY_LIVE_BITBUCKET_TOKEN"), os.Getenv("LAUNCHWAY_LIVE_BITBUCKET_REPO")
	if tok == "" || path == "" {
		t.Skip("set LAUNCHWAY_LIVE_BITBUCKET_TOKEN and LAUNCHWAY_LIVE_BITBUCKET_REPO to run against Bitbucket Cloud")
	}
	ws, _, _ := strings.Cut(path, "/")
	p, err := New(forge.Connection{ID: "con_live", Name: "live", Kind: forge.KindBitbucket, Credential: forge.SecretRef{Env: "LAUNCHWAY_LIVE_BITBUCKET_TOKEN"}, Owners: []string{ws}}, nil)
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
		t.Logf("branch restrictions read: %v", err)
	}
}
