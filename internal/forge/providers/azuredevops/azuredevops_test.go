package azuredevops

import (
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
	"github.com/jk-magic-creations/launchway/internal/forge/providers/azuredevops/azuredevopsfake"
)

type fixture struct {
	h    *azuredevopsfake.Host
	p    *Provider
	repo forge.Repository
}

func endpointsOf(t *testing.T, api, collection string) Endpoints {
	t.Helper()
	u, err := url.Parse(api)
	if err != nil {
		t.Fatal(err)
	}
	var e Endpoints
	e.API, e.Collection = u, collection
	e.Hosts = map[string]bool{u.Host: true}
	return e
}

func fixtureOn(t *testing.T, h *azuredevopsfake.Host, owners ...string) *fixture {
	t.Helper()
	t.Cleanup(h.Close)
	t.Setenv("LW_TEST_ADO_TOKEN", azuredevopsfake.Token)
	g, _ := egress.New([]string{"127.0.0.1"})
	p := NewAt(forge.Connection{ID: "con_1", Name: "ado", Kind: forge.KindAzureDevOps, Credential: forge.SecretRef{Env: "LW_TEST_ADO_TOKEN"}, Owners: owners},
		endpointsOf(t, h.API(), h.Collection()), g)
	return &fixture{h, p, forge.Repository{ID: "rep_1", Connection: "con_1", Path: h.RepoPath(), DefaultBranch: "main", Visibility: forge.Private}}
}

// newFixture is the cloud shape with the organization acme on the owners list.
func newFixture(t *testing.T) *fixture { return fixtureOn(t, azuredevopsfake.New(), "acme") }

func (f *fixture) noViolations(t *testing.T) {
	t.Helper()
	if len(f.h.Violations) > 0 {
		t.Errorf("the fake host saw calls the documentation does not allow: %v", f.h.Violations)
	}
}

func TestConformance(t *testing.T) {
	for name, f := range map[string]*fixture{"cloud": newFixture(t), "server": fixtureOn(t, azuredevopsfake.NewServer())} {
		t.Run(name, func(t *testing.T) {
			forgetest.Run(t, forgetest.Harness{Provider: f.p, Repo: f.repo})
			f.noViolations(t)
		})
	}
}

// Azure DevOps has no release object: the capability is none, with the reason and the workaround, and a call says so.
func TestReleasesAreDeclaredNoneHonestly(t *testing.T) {
	c := Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	d := c[forge.CapReleases]
	if d.Support != forge.None || !strings.Contains(d.Reason, "no release object") || !strings.Contains(d.Workaround, "release page") {
		t.Fatalf("%+v", d)
	}
	for _, feat := range []forge.Capability{forge.CapRepoCreate, forge.CapLand, forge.CapReviews, forge.CapResultsGate, forge.CapProposalRef} {
		if c.Has(feat) {
			t.Errorf("%s must be declared none", feat)
		}
	}
	for _, feat := range []forge.Capability{forge.CapIdentity, forge.CapEvents, forge.CapRules} {
		if d := c[feat]; d.Support != forge.Emulated || d.Reason == "" || d.Workaround == "" {
			t.Errorf("%s is emulated, and says why: %+v", feat, d)
		}
	}
	if !strings.Contains(c[forge.CapEvents].Reason, "no signature") || !strings.Contains(c[forge.CapEvents].Reason, "weaker") {
		t.Errorf("the webhook is declared weaker: %q", c[forge.CapEvents].Reason)
	}
	f := newFixture(t)
	_, err := f.p.Releases().PublishRelease(t.Context(), f.repo, forge.ReleaseSpec{Tag: "v1"})
	if u, ok := forge.IsUnsupported(err); !ok || u.Capability != forge.CapReleases || u.Workaround == "" {
		t.Fatalf("%v", err)
	}
	if err := f.p.Rules().Set(t.Context(), f.repo, nil); err == nil {
		t.Error("policies are not set by Launchway")
	} else if u, ok := forge.IsUnsupported(err); !ok || u.Workaround == "" {
		t.Errorf("%v", err)
	}
	if len(f.h.Requests) != 0 {
		t.Error("an unsupported call reached the host")
	}
	var auto bool
	for _, a := range f.p.Additions() {
		auto = auto || a.Key == forge.AdditionAutoMerge && a.Drivable
	}
	if !auto {
		t.Error("auto-complete is an addition Launchway drives")
	}
}

func TestWhoamiAndProbe(t *testing.T) {
	f := newFixture(t)
	w, err := f.p.Identity().Whoami(t.Context())
	if err != nil || w.Principal.Login != PrincipalName {
		t.Fatalf("%+v %v", w, err)
	}
	r, err := f.p.Probe(t.Context())
	if err != nil || r.Account != PrincipalName || r.Repositories != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	unproven := 0
	for _, c := range r.Checks {
		if !c.OK || c.Needs == "" {
			t.Errorf("unexpected lack: %+v", c)
		}
		if strings.HasPrefix(c.Detail, "Not checked") {
			unproven++
		}
	}
	if unproven != 4 {
		t.Errorf("the four write features are said not to be checked, got %d", unproven)
	}
	f.h.Lock()
	f.h.Protect = true
	f.h.Unlock()
	if r, _ = f.p.Probe(t.Context()); len(r.Checks) == 0 {
		t.Fatal("no checks")
	}
	// A token the host refuses: the message says so, and nothing of the token shows.
	bad := forge.Connection{ID: "c", Name: "b", Kind: forge.KindAzureDevOps, Credential: forge.SecretRef{Env: "LW_TEST_ADO_WRONG"}, Owners: []string{"acme"}}
	t.Setenv("LW_TEST_ADO_WRONG", "not-the-token-0123456789")
	g, _ := egress.New([]string{"127.0.0.1"})
	_, err = NewAt(bad, endpointsOf(t, f.h.API(), ""), g).Identity().Whoami(t.Context())
	if err == nil || !strings.Contains(err.Error(), "refused the credential") || strings.Contains(err.Error(), "not-the-token") {
		t.Fatalf("%v", err)
	}
	f.h.Lock()
	f.h.Violations = nil // the refusal was meant
	f.h.Unlock()
	// The cloud service needs the owners list: the host's calls name no organization of a token.
	noOwners := fixtureOn(t, azuredevopsfake.New())
	if _, err := noOwners.p.Identity().Whoami(t.Context()); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "owners") {
		t.Errorf("%v", err)
	}
	f.noViolations(t)
}

func TestOwnerAllowListAndRepositories(t *testing.T) {
	f := fixtureOn(t, azuredevopsfake.New(), "other")
	if _, err := f.p.Repositories().Get(t.Context(), "acme/proj/app"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("an organization off the list: %v", err)
	}
	for _, bad := range []string{"acme/app", "acme/proj/app/x", ""} {
		if _, err := newFixture(t).p.Repositories().Get(t.Context(), bad); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	g := newFixture(t)
	page, err := g.p.Repositories().List(t.Context(), "")
	if err != nil || len(page.Items) != 1 || page.Next != "" {
		t.Fatalf("%+v %v", page, err)
	}
	r := page.Items[0]
	if r.Path != "acme/proj/app" || r.Home != "acme/proj" || r.DefaultBranch != "main" || r.Clone.SSH != "ssh://git@ssh.dev.azure.com/v3/acme/proj/app" || r.Ext["id"] == "" ||
		r.Clone.HTTPS != "https://dev.azure.com/acme/proj/_git/app" {
		t.Fatalf("%+v", r)
	}
	r.Connection, r.ID = "con_1", "rep_1"
	if err := r.Validate(); err != nil {
		t.Errorf("a clone address with a user name was kept: %v", err)
	}
	if _, err := g.p.Repositories().List(t.Context(), "x"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a bad cursor: %v", err)
	}
	if got, err := g.p.Repositories().Get(t.Context(), "acme/proj/app"); err != nil || got.Path != "acme/proj/app" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := g.p.Repositories().Get(t.Context(), "acme/proj/nope"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("%v", err)
	}
	// The server product: the base address carries the collection, which is the first element of a path.
	s := fixtureOn(t, azuredevopsfake.NewServer())
	if page, err := s.p.Repositories().List(t.Context(), ""); err != nil || len(page.Items) != 1 || page.Items[0].Path != "DefaultCollection/proj/app" ||
		page.Items[0].Clone.HTTPS != "https://devops.example.com/tfs/DefaultCollection/proj/_git/app" {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err := s.p.Repositories().Get(t.Context(), "acme/proj/app"); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "collection") {
		t.Errorf("another collection: %v", err)
	}
	f.noViolations(t)
	g.noViolations(t)
	s.noViolations(t)
}

func TestResultsAreStatusesOnTheCommitAndThePullRequest(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "feat", Path: "a.txt", Content: []byte("x"), Message: "m"}); err != nil {
		t.Fatal(err)
	}
	pr, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "T", Source: forge.Ref{Name: "refs/heads/feat"}, Target: forge.Ref{Name: "refs/heads/main"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []forge.State{forge.Pending, forge.Passing, forge.Errored} {
		if err := f.p.Results().Publish(ctx, f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: pr.Head, State: st, Summary: "Sortie #1\n  done", URL: "https://lw.example.com/runs/1"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.p.Results().List(ctx, f.repo, pr.Head)
	if err != nil || len(got) != 1 || got[0].State != forge.Errored || got[0].Name != "launchway/ci" || got[0].Summary != "Sortie #1 done" {
		t.Fatalf("a context has one status, updated in place: %+v %v", got, err)
	}
	if n := len(f.h.PRStatuses[pr.Number]); n != 1 {
		t.Errorf("the pull request whose head is the commit has the status: %v", f.h.PRStatuses)
	}
	other := strings.Repeat("d", 40)
	if err := f.p.Results().Publish(ctx, f.repo, forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: other, State: forge.Passing}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.h.PRStatuses[pr.Number]); n != 1 {
		t.Errorf("a commit that is no pull request's head leaves them alone: %v", f.h.PRStatuses)
	}
	if err := f.p.Results().Publish(ctx, f.repo, forge.Result{Producer: "x", Name: "n", SHA: "short", State: forge.Pending}); err == nil {
		t.Error("a short sha is refused before it leaves")
	}
	f.noViolations(t)
}

func TestTagFileAndPullRequestAndComment(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	commit := f.h.Branch
	main, _ := commit("main")
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: main, Message: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: main, Message: "again"}); err != nil {
		t.Errorf("the same tag at the same commit is accepted: %v", err)
	}
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: strings.Repeat("f", 40), Message: "x"}); !errors.Is(err, forge.ErrExists) {
		t.Errorf("the same tag at another commit: %v", err)
	}
	refs, err := f.p.Content().Refs(ctx, f.repo)
	if err != nil || len(refs) != 2 {
		t.Fatalf("%v %v", refs, err)
	}
	for _, r := range refs {
		if r.Name == "refs/tags/v1.0.0" && r.Commit != main {
			t.Errorf("an annotated tag stands for the commit it tags, not its own object: %+v", r)
		}
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
	if b, _ := f.h.File("main", "env/prod/values.yaml"); b != nil {
		t.Error("main was changed")
	}
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", Path: "../x", Content: []byte("x"), Message: "m"}); err == nil {
		t.Error("a path outside the repository is refused")
	}
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "nope", Path: "x", Content: []byte("x"), Message: "m"}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("a branch that is not there: %v", err)
	}
	pr, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "Deploy v2", Source: forge.Ref{Name: "refs/heads/deploy/1"}, Target: forge.Ref{Name: "refs/heads/main"}, Draft: true})
	if tip, _ := f.h.Branch("deploy/1"); err != nil || pr.Number != 1 || pr.State != forge.ProposalDraft || pr.Head != tip {
		t.Fatalf("%+v %v", pr, err)
	}
	if _, err := f.p.Proposals().Update(ctx, f.repo, 1, forge.ProposalPatch{Draft: ptr(false)}); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("the draft flag is not among what the reference lets an update change: %v", err)
	}
	if got, err := f.p.Proposals().Update(ctx, f.repo, 1, forge.ProposalPatch{Title: ptr("Deploy v2 now"), Body: ptr("notes")}); err != nil || got.Title != "Deploy v2 now" || got.Body != "notes" {
		t.Fatalf("%+v %v", got, err)
	}
	k, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, "", "first summary")
	if err != nil {
		t.Fatal(err)
	}
	if k2, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, k, "second summary"); err != nil || k2 != k || len(f.h.Comments) != 1 || f.h.Comments[k] != "second summary" {
		t.Fatalf("the summary is updated in place: %q %q %v", k, k2, f.h.Comments)
	}
	f.h.Lock()
	f.h.Comments = map[string]string{}
	f.h.Unlock()
	if k3, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, k, "third"); err != nil || k3 == k {
		t.Errorf("a deleted comment is posted again: %q %v", k3, err)
	}
	if _, err := f.p.Releases().UpsertSummary(ctx, f.repo, 1, "x:y", "z"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a bad key: %v", err)
	}
	if err := f.p.Proposals().Close(ctx, f.repo, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.p.Proposals().Get(ctx, f.repo, 1); got.State != forge.ProposalClosed {
		t.Errorf("abandoned: %+v", got)
	}
	if err := f.p.Proposals().Reopen(ctx, f.repo, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.p.Proposals().Get(ctx, f.repo, 1); got.State != forge.ProposalDraft {
		t.Errorf("active again: %+v", got)
	}
	f.noViolations(t)
}

// The reference does not say the annotated tag call makes the ref: when it does not, the provider makes it.
func TestATagWhoseCallMakesNoRefGetsOne(t *testing.T) {
	f := newFixture(t)
	f.h.TagWithoutRef = true
	main, _ := f.h.Branch("main")
	if _, err := f.p.Releases().CreateTag(t.Context(), f.repo, forge.TagSpec{Name: "v2", Commit: main, Message: "m"}); err != nil {
		t.Fatal(err)
	}
	refs, _ := f.p.Content().Refs(t.Context(), f.repo)
	found := false
	for _, r := range refs {
		found = found || r.Name == "refs/tags/v2" && r.Commit == main
	}
	if !found {
		t.Errorf("%+v", refs)
	}
	f.noViolations(t)
}

func ptr[T any](v T) *T { return &v }

func TestAutoCompleteIsDriven(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "feat", Path: "a", Content: []byte("x"), Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Proposals().Create(ctx, f.repo, forge.NewProposal{Title: "T", Source: forge.Ref{Name: "feat"}, Target: forge.Ref{Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategySquash, true); err != nil {
		t.Fatal(err)
	}
	got, _ := f.p.Proposals().Get(ctx, f.repo, 1)
	if got.Ext["auto_complete"] != true || f.h.PRs[0].Strategy != "squash" {
		t.Errorf("%+v %+v", got, f.h.PRs[0])
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategyFastForward, true); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a strategy the host has no name for: %v", err)
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 1, forge.StrategySquash, false); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "cancels") {
		t.Errorf("the reference names no way to cancel it: %v", err)
	}
	if err := f.p.Land().AutoLand(ctx, f.repo, 9, forge.StrategySquash, true); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("%v", err)
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
	rs, err = f.p.Rules().List(t.Context(), f.repo) // the repository's id is asked of the host when the copy lacks it
	if err != nil || len(rs) != 1 || !rs[0].ProposalsOnly || rs[0].RequiredReviews != 2 || len(rs[0].RequiredResults) != 1 || rs[0].Pattern != "refs/heads/main" {
		t.Fatalf("%+v %v", rs, err)
	}
	f.noViolations(t)
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
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalOpened || evs[0].Number != pr.Number || evs[0].Base != "main" || evs[0].Title != "Add thing" || evs[0].Fork ||
		evs[0].URL != "https://dev.azure.com/acme/proj/_git/app/pullrequest/1" {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs, cur, err = f.p.Events().Poll(ctx, f.repo, cur); err != nil || len(evs) != 0 {
		t.Fatalf("nothing changed: %+v %v", evs, err)
	}
	f.h.Lock()
	f.h.PRs[0].SHA, f.h.PRs[0].Fork = strings.Repeat("e", 40), true
	f.h.Unlock()
	evs, _, err = f.p.Events().Poll(ctx, f.repo, cur)
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalRevised || evs[0].Head != strings.Repeat("e", 40) || !evs[0].Fork {
		t.Fatalf("a moved head, from a fork: %+v %v", evs, err)
	}
	got, _ := f.p.Proposals().Get(ctx, f.repo, 1)
	if got.Source.Repository != "eve/app" {
		t.Errorf("the fork is named: %+v", got.Source)
	}
	if _, _, err := f.p.Events().Poll(ctx, f.repo, "!!"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a bad cursor: %v", err)
	}
	f.noViolations(t)
}

func TestCommit(t *testing.T) {
	f := newFixture(t)
	c, err := f.p.Content().Commit(t.Context(), f.repo, strings.Repeat("a", 40))
	if err != nil || c.Author.Name != "Dev" || c.CreatedAt.Year() != 2026 || len(c.Parents) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	f.noViolations(t)
}

func TestCredentialRedirectGuardAndEndpoints(t *testing.T) {
	f := newFixture(t)
	if _, err := f.p.Client().Do(t.Context(), http.MethodGet, "https://evil.example.org/acme/_apis/projects", nil, 0, ""); err == nil ||
		!strings.Contains(err.Error(), "not this connection's host") || strings.Contains(err.Error(), azuredevopsfake.Token) {
		t.Fatalf("%v", err)
	}
	cred, err := f.p.Access().Credential(t.Context(), f.repo, true)
	if err != nil || cred.Secret != azuredevopsfake.Token || cred.Username != GitUser {
		t.Fatalf("%+v %v", cred, err)
	}
	if _, err := f.p.Access().Credential(t.Context(), forge.Repository{Path: "elsewhere/proj/app"}, true); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a credential for an organization off the list: %v", err)
	}
	red := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, f.h.URL+r.URL.Path, http.StatusFound)
	}))
	defer red.Close()
	before := len(f.h.Requests)
	g, _ := egress.New([]string{"127.0.0.1"})
	conn := forge.Connection{ID: "c", Name: "b", Kind: forge.KindAzureDevOps, Credential: forge.SecretRef{Env: "LW_TEST_ADO_TOKEN"}, Owners: []string{"acme"}}
	if _, err := NewAt(conn, endpointsOf(t, red.URL, ""), g).Identity().Whoami(t.Context()); err == nil || !strings.Contains(err.Error(), "redirect") || len(f.h.Requests) != before {
		t.Fatalf("redirect: %v", err)
	}
	if _, err := NewAt(conn, endpointsOf(t, f.h.API(), ""), nil).Identity().Whoami(t.Context()); err == nil || !strings.Contains(err.Error(), "internal address") {
		t.Fatalf("guard: %v", err)
	}
	for _, base := range []string{"", "https://dev.azure.com", "https://dev.azure.com/"} {
		if ep, err := EndpointsOf(base); err != nil || ep.API.String() != "https://dev.azure.com" || ep.Collection != "" || len(ep.Hosts) != 1 {
			t.Errorf("%q: %v %v", base, ep.API, err)
		}
	}
	ep, err := EndpointsOf("https://devops.example.com/tfs/DefaultCollection/")
	if err != nil || ep.API.String() != "https://devops.example.com/tfs/DefaultCollection" || ep.Collection != "DefaultCollection" || !ep.Hosts["devops.example.com"] {
		t.Errorf("the server product: %v %v", ep, err)
	}
	for _, bad := range []string{"https://devops.example.com", "https://dev.azure.com/acme", "https://acme.visualstudio.com", "ftp://x/y", "https://u:p@x/y"} {
		if _, err := EndpointsOf(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// Live test against Azure DevOps: opt in with LAUNCHWAY_LIVE_AZUREDEVOPS_TOKEN and
// LAUNCHWAY_LIVE_AZUREDEVOPS_REPO (organization/project/repository), and
// LAUNCHWAY_LIVE_AZUREDEVOPS_BASE for the server product (its address with the collection).
// It only reads and posts one status to the default branch head.
func TestLive(t *testing.T) {
	tok, path := os.Getenv("LAUNCHWAY_LIVE_AZUREDEVOPS_TOKEN"), os.Getenv("LAUNCHWAY_LIVE_AZUREDEVOPS_REPO")
	if tok == "" || path == "" {
		t.Skip("set LAUNCHWAY_LIVE_AZUREDEVOPS_TOKEN and LAUNCHWAY_LIVE_AZUREDEVOPS_REPO (organization/project/repository) to run against Azure DevOps")
	}
	org, _, _ := strings.Cut(path, "/")
	p, err := New(forge.Connection{ID: "con_live", Name: "live", Kind: forge.KindAzureDevOps, BaseURL: os.Getenv("LAUNCHWAY_LIVE_AZUREDEVOPS_BASE"),
		Credential: forge.SecretRef{Env: "LAUNCHWAY_LIVE_AZUREDEVOPS_TOKEN"}, Owners: []string{org}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
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
	if err := p.Results().Publish(ctx, repo, forge.Result{Producer: "launchway", Name: "live-test", SHA: head, State: forge.Passing, Summary: "Launchway live conformance test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rules().List(ctx, repo); err != nil {
		t.Logf("branch policies read: %v", err)
	}
}
