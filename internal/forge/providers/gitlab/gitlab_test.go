package gitlab

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
	"github.com/jk-magic-creations/launchway/internal/forge/providers/gitlab/gitlabfake"
)

type fixture struct {
	h    *gitlabfake.Host
	p    *Provider
	repo forge.Repository
}

func newFixture(t *testing.T, owners ...string) *fixture {
	t.Helper()
	h := gitlabfake.New()
	t.Cleanup(h.Close)
	t.Setenv("LW_TEST_GL_TOKEN", gitlabfake.Token)
	g, err := egress.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(forge.Connection{ID: "con_1", Name: "gl", Kind: forge.KindGitLab, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_TEST_GL_TOKEN"}, Owners: owners}, g)
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
	for _, feat := range []forge.Capability{forge.CapRepoCreate, forge.CapLand, forge.CapReviews, forge.CapDrafts} {
		if c.Has(feat) {
			t.Errorf("%s must be declared none", feat)
		}
	}
	f := newFixture(t)
	if _, err := f.p.Proposals().Create(t.Context(), f.repo, forge.NewProposal{Title: "x", Draft: true}); err == nil {
		t.Error("a draft merge request is not made")
	} else if u, ok := forge.IsUnsupported(err); !ok || u.Workaround == "" {
		t.Errorf("draft: %v", err)
	}
	if len(Additions()) == 0 {
		t.Error("a host that adds things says so")
	}
	f.noViolations(t)
}

func TestWhoamiAndProbe(t *testing.T) {
	f := newFixture(t)
	w, err := f.p.Identity().Whoami(t.Context())
	if err != nil || w.Principal.Login != "svc-launchway" || len(w.Scopes) != 1 || w.Scopes[0] != "api" {
		t.Fatalf("%+v %v", w, err)
	}
	r, err := f.p.Probe(t.Context())
	if err != nil || r.Account != "svc-launchway" || r.TokenExpires == nil || r.TokenExpires.Year() != 2030 || r.TokenExpires.Day() != 3 || r.Repositories != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range r.Checks {
		if !c.OK {
			t.Errorf("unexpected lack: %+v", c)
		}
	}
	// A Reporter role cannot write.
	f.h.Level = 20
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
		t.Errorf("a Reporter lacks %d write features, want 4", lacking)
	}
	// A Developer can write statuses but not manage releases.
	f.h.Level = 30
	r, _ = f.p.Probe(t.Context())
	for _, c := range r.Checks {
		if c.Feature == "Tags, releases and release files" && (c.OK || !strings.Contains(c.Detail, "Developer")) {
			t.Errorf("a Developer is told what releases need: %+v", c)
		}
		if c.Feature == "Commit statuses on each run" && !c.OK {
			t.Errorf("a Developer posts statuses: %+v", c)
		}
	}
	// A read-only scope lacks the writes, and says why.
	f.h.Level, f.h.Scopes = 40, []string{"read_api", "read_repository"}
	r, _ = f.p.Probe(t.Context())
	for _, c := range r.Checks {
		if c.Feature == "Commit statuses on each run" && (c.OK || !strings.Contains(c.Detail, "api scope") || !strings.Contains(c.Detail, "read_api")) {
			t.Errorf("scope lack: %+v", c)
		}
	}
	// A project token has no record of its own: scopes are unknown, not wrong.
	f.h.Scopes, f.h.NoSelf = nil, true
	r, err = f.p.Probe(t.Context())
	if err != nil || r.TokenExpires != nil {
		t.Fatalf("%+v %v", r, err)
	}
	f.noViolations(t)
}

func TestOwnerAllowList(t *testing.T) {
	f := newFixture(t, "other")
	if _, err := f.p.Repositories().Get(t.Context(), "acme/app"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a project of an owner off the list: %v", err)
	}
	page, err := f.p.Repositories().List(t.Context(), "")
	if err != nil || len(page.Items) != 0 {
		t.Errorf("listing shows %v %v", page.Items, err)
	}
	if len(f.h.Requests) != 1 {
		t.Errorf("requests: %v", f.h.Requests)
	}
	g := newFixture(t, "acme")
	if r, err := g.p.Repositories().Get(t.Context(), "acme/app"); err != nil || r.Home != "acme" || !strings.HasSuffix(r.Clone.HTTPS, "acme/app.git") {
		t.Errorf("%+v %v", r, err)
	}
	if _, err := g.p.Repositories().Get(t.Context(), "acme"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a path with no project: %v", err)
	}
}

func TestResultsAreStatuses(t *testing.T) {
	f := newFixture(t)
	sha := strings.Repeat("d", 40)
	long := strings.Repeat("word ", 100)
	if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Pending, Summary: long, URL: "https://lw.example.com/runs/1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Results().Publish(t.Context(), f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: sha, State: forge.Failing}); err != nil {
		t.Fatal(err)
	}
	got, err := f.p.Results().List(t.Context(), f.repo, sha)
	if err != nil || len(got) != 1 || got[0].State != forge.Failing {
		t.Fatalf("the newest status of a name wins: %+v %v", got, err)
	}
	if st := f.h.Statuses; len(st) != 2 || st[0].State != "pending" || st[1].State != "failed" || len([]rune(st[0].Description)) > 255 {
		t.Errorf("statuses %+v", st)
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
	if err != nil || !strings.HasSuffix(u1, "/acme/app/-/releases/v1.0.0") {
		t.Fatalf("%q %v", u1, err)
	}
	rel.Notes = "edited"
	if _, err := f.p.Releases().PublishRelease(ctx, f.repo, rel); err != nil {
		t.Fatal(err)
	}
	uploads := 0
	for _, r := range f.h.Requests {
		if strings.HasPrefix(r, "PUT /api/v4/projects/acme%2Fapp/packages/generic/") {
			uploads++
		}
	}
	if uploads != 2 {
		t.Errorf("uploads %d, want 2 (a second publish uploads nothing again)", uploads)
	}
	if len(f.h.Packages) != 2 {
		t.Errorf("packages %v", f.h.Packages)
	}
	bad := file("a b.tgz", "x")
	rel.Files = []forge.ReleaseFile{bad}
	if _, err := f.p.Releases().PublishRelease(ctx, f.repo, rel); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a file name the registry refuses: %v", err)
	}
	f.noViolations(t)
}

func TestCommitFileAndMergeRequestAndSummary(t *testing.T) {
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
	// Retrying the first call after the branch exists writes to that branch.
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
	if err != nil || k2 != k || len(f.h.Notes) != 1 {
		t.Fatalf("the summary is updated in place: %q %q %v", k, k2, f.h.Notes)
	}
	for _, v := range f.h.Notes {
		if v != "second summary" {
			t.Errorf("note %q", v)
		}
	}
	// A note deleted on the host is posted again.
	f.h.Lock()
	f.h.Notes = map[int]string{}
	f.h.Unlock()
	if k3, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, k, "third"); err != nil || k3 == k {
		t.Errorf("%q %v", k3, err)
	}
	f.noViolations(t)
}

func TestAutoMergeIsDrivenAndCancelled(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "d", Path: "a", Content: []byte("x"), Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "t", Source: forge.Ref{Name: "refs/heads/d"}, Target: forge.Ref{Name: "refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategyMerge, true); err != nil || !f.h.AutoMerge[1] {
		t.Fatalf("%v %v", err, f.h.AutoMerge)
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategyMerge, false); err != nil || f.h.AutoMerge[1] {
		t.Fatalf("%v %v", err, f.h.AutoMerge)
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategyMerge, false); err != nil {
		t.Errorf("cancelling what is not set is not an error: %v", err)
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategyReplay, true); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("rebase: %v", err)
	}
	var drivable bool
	for _, a := range f.p.Additions() {
		drivable = drivable || a.Key == forge.AdditionAutoMerge && a.Drivable
	}
	if !drivable {
		t.Error("auto-merge is a drivable addition")
	}
	f.noViolations(t)
}

func TestRulesAndProtected(t *testing.T) {
	f := newFixture(t)
	rs, err := f.p.Rules().List(t.Context(), f.repo)
	if err != nil || len(rs) != 0 {
		t.Fatalf("%v %v", rs, err)
	}
	f.h.Protect = true
	rs, err = f.p.Rules().List(t.Context(), f.repo)
	if err != nil || len(rs) != 1 || !rs[0].ProposalsOnly || rs[0].Pattern != "refs/heads/main" {
		t.Fatalf("%+v %v", rs, err)
	}
	f.noViolations(t)
}

func TestWebhook(t *testing.T) {
	f := newFixture(t)
	secret := []byte(gitlabfake.WebhookSecret)
	send := func(event, delivery, body, token string) ([]forge.Event, error) {
		req, _ := http.NewRequest(http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
		req.Header.Set("X-Gitlab-Event", event)
		if delivery != "" {
			req.Header.Set("webhook-id", delivery)
		}
		if token != "" {
			req.Header.Set("X-Gitlab-Token", token)
		}
		return f.p.Events().Verify(req, secret)
	}
	good := gitlabfake.WebhookSecret
	push := `{"object_kind":"tag_push","ref":"refs/tags/v1","before":"` + strings.Repeat("0", 40) + `","after":"` + strings.Repeat("a", 40) + `","user_username":"bob","project":{"path_with_namespace":"acme/app"}}`
	if _, err := send("Tag Push Hook", "d1", push, ""); !errors.Is(err, ErrBadSignature) {
		t.Errorf("no token: %v", err)
	}
	if _, err := send("Tag Push Hook", "d1", push, "wrong"); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong token: %v", err)
	}
	if _, err := send("Tag Push Hook", "d1", push, good+"x"); !errors.Is(err, ErrBadSignature) {
		t.Errorf("longer token: %v", err)
	}
	evs, err := send("Tag Push Hook", "d1", push, good)
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventRefUpdated || evs[0].Ref != "refs/tags/v1" || evs[0].DeliveryID != "d1" || evs[0].Repository != "acme/app" || evs[0].Actor.Login != "bob" {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs, err = send("Push Hook", "", strings.Replace(push, "refs/tags/v1", "refs/heads/main", 1), good); err != nil || len(evs) != 1 || !strings.HasPrefix(evs[0].DeliveryID, "body-") {
		t.Errorf("a delivery with no id is named by its body: %+v %v", evs, err)
	}
	mr := func(action string, source int, last string) string {
		return `{"object_kind":"merge_request","user":{"username":"eve"},"project":{"path_with_namespace":"acme/app"},"object_attributes":{"iid":7,"title":"T","action":"` + action + `","draft":true,"source_branch":"f","target_branch":"main","source_project_id":` + string(rune('0'+source)) + `,"target_project_id":2,"url":"https://x/mr/7","last_commit":{"id":"` + last + `"}}}`
	}
	head := strings.Repeat("b", 40)
	for _, c := range []struct {
		action string
		source int
		kind   forge.EventKind
		fork   bool
	}{
		{"open", 2, forge.EventProposalOpened, false},
		{"reopen", 2, forge.EventProposalOpened, false},
		{"update", 3, forge.EventProposalRevised, true},
		{"close", 2, forge.EventProposalClosed, false},
		{"merge", 2, forge.EventProposalLanded, false},
	} {
		evs, err := send("Merge Request Hook", "d2", mr(c.action, c.source, head), good)
		if err != nil || len(evs) != 1 || evs[0].Kind != c.kind || evs[0].Fork != c.fork || evs[0].Number != 7 || !evs[0].Draft || evs[0].Head != head || evs[0].Ref != "refs/pull/7/head" || evs[0].Base != "main" {
			t.Errorf("%s: %+v %v", c.action, evs, err)
		}
	}
	if evs, err := send("Merge Request Hook", "d3", mr("approved", 2, head), good); err != nil || len(evs) != 0 {
		t.Errorf("approved starts nothing: %v %v", evs, err)
	}
	if evs, err := send("Issue Hook", "d4", `{}`, good); err != nil || len(evs) != 0 {
		t.Errorf("other events: %v %v", evs, err)
	}
	if l := f.p.ProposalRefs(); l.Ref(7) != "refs/merge-requests/7/head" {
		t.Errorf("layout %v", l)
	}
}

func TestCredentialNeverLeavesTheConnectionsHost(t *testing.T) {
	f := newFixture(t)
	_, err := f.p.Client().Do(t.Context(), http.MethodGet, "https://evil.example.org/api/v4/user", nil, 0, "")
	if err == nil || !strings.Contains(err.Error(), "not this connection's host") || strings.Contains(err.Error(), gitlabfake.Token) {
		t.Fatalf("%v", err)
	}
	cred, err := f.p.Access().Credential(t.Context(), f.repo, false)
	if err != nil || cred.Secret != gitlabfake.Token || cred.Username == "" {
		t.Fatalf("%+v %v", cred, err)
	}
}

func TestEgressGuardRefusesAPrivateHostUnlessAllowed(t *testing.T) {
	h := gitlabfake.New()
	defer h.Close()
	t.Setenv("LW_TEST_GL_TOKEN", gitlabfake.Token)
	p, _ := New(forge.Connection{ID: "c", Name: "gl", Kind: forge.KindGitLab, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_TEST_GL_TOKEN"}}, nil)
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
	red := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, f.h.URL+r.URL.Path, http.StatusFound)
	}))
	defer red.Close()
	before := len(f.h.Requests)
	g, _ := egress.New([]string{"127.0.0.1"})
	p, _ := New(forge.Connection{ID: "c", Name: "gl", Kind: forge.KindGitLab, BaseURL: red.URL, Credential: forge.SecretRef{Env: "LW_TEST_GL_TOKEN"}}, g)
	_, err := p.Identity().Whoami(t.Context())
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("%v", err)
	}
	if len(f.h.Requests) != before {
		t.Error("the redirect was followed")
	}
}

func TestEndpoints(t *testing.T) {
	for base, want := range map[string]string{"": "https://gitlab.com/api/v4", "https://gitlab.com/": "https://gitlab.com/api/v4",
		"https://git.example.com": "https://git.example.com/api/v4", "https://git.example.com/api/v4": "https://git.example.com/api/v4",
		"https://example.com/gitlab": "https://example.com/gitlab/api/v4"} {
		ep, err := EndpointsOf(base)
		if err != nil || ep.API.String() != want || !ep.Hosts[ep.API.Host] || len(ep.Hosts) != 1 {
			t.Errorf("%q: %v %v", base, ep.API, err)
		}
	}
	if _, err := EndpointsOf("https://user:pw@x.example"); err == nil {
		t.Error("credentials in the address")
	}
}

// Without a webhook, Poll finds merge requests from the host's API.
func TestPollFindsMergeRequests(t *testing.T) {
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
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalOpened || evs[0].Number != pr.Number || evs[0].Base != "main" || evs[0].Title != "Add thing" || evs[0].Ref != "refs/pull/1/head" || evs[0].Fork {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs, cur, err = f.p.Events().Poll(ctx, f.repo, cur); err != nil || len(evs) != 0 {
		t.Fatalf("nothing changed: %+v %v", evs, err)
	}
	f.h.Lock()
	f.h.MRs[0].SHA = strings.Repeat("e", 40)
	f.h.MRs[0].SourceProject = 99 // now from a fork
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
	if err != nil || c.Author.Name != "Dev" || c.CreatedAt.Year() != 2026 || len(c.Parents) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	f.noViolations(t)
}

// Live test against a real installation: opt in with LAUNCHWAY_LIVE_GITLAB_TOKEN
// and LAUNCHWAY_LIVE_GITLAB_PROJECT (group/name); LAUNCHWAY_LIVE_GITLAB_BASE
// names a self-managed install. It only reads and posts one commit status
// name to the default branch head.
func TestLive(t *testing.T) {
	tok, path := os.Getenv("LAUNCHWAY_LIVE_GITLAB_TOKEN"), os.Getenv("LAUNCHWAY_LIVE_GITLAB_PROJECT")
	if tok == "" || path == "" {
		t.Skip("set LAUNCHWAY_LIVE_GITLAB_TOKEN and LAUNCHWAY_LIVE_GITLAB_PROJECT to run against a real installation")
	}
	p, err := New(forge.Connection{ID: "con_live", Name: "live", Kind: forge.KindGitLab, BaseURL: os.Getenv("LAUNCHWAY_LIVE_GITLAB_BASE"),
		Credential: forge.SecretRef{Env: "LAUNCHWAY_LIVE_GITLAB_TOKEN"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rep, err := p.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("account %s, expires %v, checks %+v", rep.Account, rep.TokenExpires, rep.Checks)
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
		t.Logf("protected branch read: %v", err)
	}
}
