package anygit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/forgetest"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/anygit/anygitfake"
)

type fixture struct {
	h    *anygitfake.Host
	p    *Provider
	repo forge.Repository
}

func newFixture(t *testing.T, doc string, owners ...string) *fixture {
	t.Helper()
	h := anygitfake.New()
	t.Cleanup(h.Close)
	if doc != "" {
		h.Accept = [2]string{anygitfake.User, anygitfake.Password}
	} else {
		doc = anygitfake.Token
	}
	t.Setenv("LW_TEST_ANYGIT_CRED", doc)
	g, _ := egress.New([]string{"127.0.0.1"})
	p, err := New(forge.Connection{ID: "con_1", Name: "any", Kind: forge.KindPlain, BaseURL: h.Base(), Credential: forge.SecretRef{Env: "LW_TEST_ANYGIT_CRED"}, Owners: owners}, g)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := p.Repositories().Get(t.Context(), anygitfake.Path)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h, p, repo}
}

func (f *fixture) noViolations(t *testing.T) {
	t.Helper()
	if len(f.h.Violations) > 0 {
		t.Errorf("the fake server saw requests a Git client must not make: %v", f.h.Violations)
	}
}

const passwordDoc = `{"username":"mary","password":"fake-password-for-tests-only-0001"}`

func TestConformance(t *testing.T) {
	for name, doc := range map[string]string{"token": "", "user and password": passwordDoc} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, doc)
			forgetest.Run(t, forgetest.Harness{Provider: f.p, Repo: f.repo})
			f.noViolations(t)
		})
	}
}

func TestCapabilitiesAreHonest(t *testing.T) {
	c := Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, feat := range []forge.Capability{forge.CapResults, forge.CapProposalNote, forge.CapReleases, forge.CapProposals, forge.CapReviews, forge.CapLand, forge.CapAutoLand, forge.CapRules,
		forge.CapRepoCreate, forge.CapProposalRef, forge.CapForks, forge.CapDrafts, forge.CapResultsGate} {
		if c.Has(feat) {
			t.Errorf("%s must be declared none", feat)
		}
		if d := c[feat]; d.Reason == "" || d.Workaround == "" {
			t.Errorf("%s: %+v", feat, d)
		}
	}
	for _, feat := range []forge.Capability{forge.CapAccess, forge.CapTags, forge.CapCommitFile, forge.CapIdentity, forge.CapRepositories, forge.CapContent, forge.CapEvents} {
		if !c.Has(feat) {
			t.Errorf("%s must be available", feat)
		}
	}
	if d := c[forge.CapRepositories]; d.Support != forge.Emulated || !strings.Contains(d.Workaround, "Enter the repository's path") {
		t.Errorf("repositories: %+v", d)
	}
	if d := c[forge.CapEvents]; d.Support != forge.Emulated || !strings.Contains(d.Workaround, "post-receive") || !strings.Contains(d.Reason, "no webhook") {
		t.Errorf("events: %+v", d)
	}
	if d := c[forge.CapProposals]; !strings.Contains(d.Workaround, "ask a person to merge") {
		t.Errorf("proposals: %+v", d)
	}
	if d := c[forge.CapResults]; !strings.Contains(d.Workaround, "Launchway only") {
		t.Errorf("results: %+v", d)
	}
}

func TestTheBaseAddressIsChecked(t *testing.T) {
	for base, want := range map[string]string{
		"https://git.example.com":        "https://git.example.com",
		"https://git.example.com/":       "https://git.example.com",
		"https://git.example.com/git/":   "https://git.example.com/git",
		"http://127.0.0.1:3000/git":      "http://127.0.0.1:3000/git",
		"http://localhost:3000":          "http://localhost:3000",
		"https://git.example.com:8443/x": "https://git.example.com:8443/x",
	} {
		u, err := EndpointsOf(base)
		if err != nil || u.String() != want {
			t.Errorf("%s: %v %v", base, u, err)
		}
	}
	for _, bad := range []string{"", "http://git.example.com", "ssh://git@git.example.com:22", "git@git.example.com:acme/app.git", "ftp://x",
		"https://u:p@git.example.com", "https://git.example.com?x=1", "https://git.example.com/#f", "git.example.com"} {
		if _, err := EndpointsOf(bad); err == nil {
			t.Errorf("%q is accepted", bad)
		}
	}
	if _, err := EndpointsOf("ssh://git@git.example.com:22"); !errors.Is(err, ErrSSH) {
		t.Errorf("an ssh address says what is supported: %v", err)
	}
	if _, err := New(forge.Connection{Name: "x", Kind: forge.KindPlain, BaseURL: "http://git.example.com"}, nil); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestTheCredentialIsChecked(t *testing.T) {
	for doc, want := range map[string]cred{
		"  tok-123 \n":                         {"git", "tok-123"},
		`{"token":"abc"}`:                      {"git", "abc"},
		`{"username":"ann","token":"abc"}`:     {"ann", "abc"},
		`{"username":"ann","password":"pw w"}`: {"ann", "pw w"},
	} {
		if c, err := parseCred(doc); err != nil || c != want {
			t.Errorf("%q: %+v %v", doc, c, err)
		}
	}
	for name, doc := range map[string]string{
		"empty":         "  ",
		"two lines":     "abc\ndef",
		"unknown":       `{"password":"sekret-value","key":"x"}`,
		"both":          `{"password":"sekret-value","token":"t"}`,
		"neither":       `{"username":"ann"}`,
		"colon user":    `{"username":"a:b","password":"sekret-value"}`,
		"trailing":      `{"password":"sekret-value"} {}`,
		"not an object": `{"password":"sekret-value"`,
	} {
		_, err := parseCred(doc)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "sekret-value") {
			t.Errorf("%s: the error repeats the credential: %v", name, err)
		}
	}
}

func TestChoosingARepositoryProvesItWithLsRemote(t *testing.T) {
	f := newFixture(t, "")
	if f.repo.DefaultBranch != "main" || f.repo.Name != "app" || f.repo.Home != "acme" || f.repo.Path != anygitfake.Path || f.repo.Clone.HTTPS != f.h.Base()+"/"+anygitfake.Path {
		t.Fatalf("%+v", f.repo)
	}
	if page, err := f.p.Repositories().List(t.Context(), ""); err != nil || len(page.Items) != 0 {
		t.Errorf("a Git server lists nothing: %v %v", page, err)
	}
	if _, err := f.p.Repositories().Get(t.Context(), "acme/nothing.git"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("an unknown repository is not found: %v", err)
	}
	f.h.ClearViolations()
	for _, bad := range []string{"app.git", "a/b/c", "acme/../x", "acme/a b", "/acme/", "acme/x.lock"} {
		if _, err := f.p.Repositories().Get(t.Context(), bad); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	ca, err := f.p.Access().CloneAddresses(t.Context(), f.repo)
	if err != nil || ca.HTTPS != f.repo.Clone.HTTPS || ca.SSH != "" {
		t.Errorf("%+v %v", ca, err)
	}
	cr, err := f.p.Access().Credential(t.Context(), f.repo, true)
	if err != nil || cr.Username != "git" || cr.Secret != anygitfake.Token {
		t.Errorf("%+v %v", cr, err)
	}
	f.noViolations(t)
}

func TestAWrongCredentialIsRefusedWithoutBeingRepeated(t *testing.T) {
	f := newFixture(t, "")
	t.Setenv("LW_TEST_ANYGIT_CRED", "not-the-token-9876")
	f.h.ClearViolations()
	_, err := f.p.Repositories().Get(t.Context(), anygitfake.Path)
	if err == nil || strings.Contains(err.Error(), "not-the-token-9876") || !strings.Contains(err.Error(), "401") && !strings.Contains(strings.ToLower(err.Error()), "authentication") {
		t.Fatalf("%v", err)
	}
	if len(f.h.Violations) == 0 || !strings.Contains(f.h.Violations[0], "not accepted") {
		t.Errorf("the fake saw %v", f.h.Violations)
	}
	f.h.ClearViolations()
	f.h.Denied["pull"] = true
	t.Setenv("LW_TEST_ANYGIT_CRED", anygitfake.Token)
	if _, err := f.p.Repositories().Get(t.Context(), anygitfake.Path); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a read the server denies: %v", err)
	}
}

func TestOwnersAreEnforced(t *testing.T) {
	f := newFixture(t, "", "acme")
	if _, err := f.p.Repositories().Get(t.Context(), "other/app.git"); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "allow-list") {
		t.Errorf("%v", err)
	}
	if _, err := f.p.gitURL("other/app.git"); err == nil {
		t.Error("a credential must never be sent for a path off the list")
	}
}

func TestRefsAreListedFromTheServer(t *testing.T) {
	f := newFixture(t, "")
	refs, err := f.p.Content().Refs(t.Context(), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range refs {
		got[r.Name] = r.Commit
	}
	if got["refs/heads/main"] != f.h.Branch("main") || got["refs/heads/feature"] != f.h.Branch("feature") || len(got) != 2 {
		t.Errorf("%v", got)
	}
	if _, err := f.p.Content().Commit(t.Context(), f.repo, f.h.Branch("main")); err == nil {
		t.Error("commits are read from the clone")
	} else if u, ok := forge.IsUnsupported(err); !ok || u.Workaround == "" {
		t.Errorf("%v", err)
	}
}

func TestPollingFindsNothingOfItsOwnAndWebhooksAreRefused(t *testing.T) {
	f := newFixture(t, "")
	evs, next, err := f.p.Events().Poll(t.Context(), f.repo, "7")
	if err != nil || len(evs) != 0 || next != "7" {
		t.Errorf("%v %q %v", evs, next, err)
	}
	if f.p.Events().Mode() != forge.EventsPolling {
		t.Error("mode")
	}
	if _, err := f.p.Events().Verify(nil, []byte("s")); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "git-hook") {
		t.Errorf("%v", err)
	}
}

func TestTagsArePushedAnnotated(t *testing.T) {
	f := newFixture(t, "")
	ctx := t.Context()
	main := f.h.Branch("main")
	ref, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: main, Message: "Release 1.0.0"})
	if err != nil || ref.Name != "refs/tags/v1.0.0" || ref.Commit != main {
		t.Fatalf("%+v %v", ref, err)
	}
	if kind, commit := f.h.Tag("v1.0.0"); kind != "tag" || commit != main || f.h.TagMessage("v1.0.0") != "Release 1.0.0" {
		t.Errorf("the tag on the server: %q %q %q", kind, commit, f.h.TagMessage("v1.0.0"))
	}
	pushes := f.h.Seen("GIT push")
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: main, Message: "again"}); err != nil || f.h.Seen("GIT push") != pushes {
		t.Errorf("a tag already there is accepted without a push: %v", err)
	}
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: f.h.Branch("feature"), Message: "x"}); !errors.Is(err, forge.ErrExists) {
		t.Errorf("a tag that stands for another commit is refused: %v", err)
	}
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v2", Commit: strings.Repeat("a", 40), Message: "x"}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("a commit the server does not have: %v", err)
	}
	for _, bad := range []forge.TagSpec{{Name: "-x", Commit: main}, {Name: "a..b", Commit: main}, {Name: "v1", Commit: "main"}, {Name: "x.lock", Commit: main}} {
		if _, err := f.p.Releases().CreateTag(ctx, f.repo, bad); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	f.h.Denied["push"] = true
	if _, err := f.p.Releases().CreateTag(ctx, f.repo, forge.TagSpec{Name: "v3", Commit: main, Message: "x"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a push the server denies comes back with its answer: %v", err)
	}
	f.noViolations(t)
}

func TestAFileIsCommittedAndPushed(t *testing.T) {
	f := newFixture(t, "")
	ctx := t.Context()
	main := f.h.Branch("main")
	id, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "bump/1", Path: "deploy/values/app.yaml", Content: []byte("image: v2\n"), Message: "Bump to v2"})
	if err != nil || len(id) != 40 {
		t.Fatalf("%q %v", id, err)
	}
	if f.h.Branch("bump/1") != id || f.h.Branch("main") != main {
		t.Errorf("the new branch is at the commit and main did not move: %s %s", f.h.Branch("bump/1"), f.h.Branch("main"))
	}
	if c, ok := f.h.File("bump/1", "deploy/values/app.yaml"); !ok || c != "image: v2" {
		t.Errorf("file: %q %v", c, ok)
	}
	if c, ok := f.h.File("bump/1", "README.md"); !ok || c != "hello" {
		t.Errorf("the rest of the tree is kept: %q %v", c, ok)
	}
	if a := f.h.Author("bump/1"); a != "Launchway <launchway@users.noreply.invalid>" || f.h.Parents("bump/1") != 1 {
		t.Errorf("the Launchway identity, one parent: %q %d", a, f.h.Parents("bump/1"))
	}
	pushes := f.h.Seen("GIT push")
	if same, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "bump/1", Path: "deploy/values/app.yaml", Content: []byte("image: v2\n"), Message: "again"}); err != nil || same != id || f.h.Seen("GIT push") != pushes {
		t.Errorf("the same content again is not a change: %q %v", same, err)
	}
	next, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "bump/1", Path: "deploy/values/app.yaml", Content: []byte("image: v3\n"), Message: "Bump to v3"})
	if err != nil || next == id || f.h.Branch("bump/1") != next || f.h.Parents("bump/1") != 1 {
		t.Errorf("an existing new branch is added to, not replaced: %q %v", next, err)
	}
	direct, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "feature", Path: "notes.txt", Content: []byte("n\n"), Message: "notes"})
	if err != nil || f.h.Branch("feature") != direct {
		t.Errorf("a direct commit to the branch: %q %v", direct, err)
	}
	if _, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "nowhere", NewBranch: "x1", Path: "a.txt", Content: []byte("a"), Message: "m"}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("a base branch that is not there: %v", err)
	}
	f.noViolations(t)
}

func TestAFileChangeCannotLeaveTheRepository(t *testing.T) {
	f := newFixture(t, "")
	for _, p := range []string{"", "/etc/passwd", "../x", "a/../../x", ".git/config", "a/.GIT/x", "a\\b", "."} {
		_, err := f.p.Releases().CommitFile(t.Context(), f.repo, forge.FileChange{Branch: "main", NewBranch: "w1", Path: p, Content: []byte("x"), Message: "m"})
		if !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, b := range []forge.FileChange{{Branch: "-x", Path: "a"}, {Branch: "main", NewBranch: "a..b", Path: "a"}} {
		b.Content, b.Message = []byte("x"), "m"
		if _, err := f.p.Releases().CommitFile(t.Context(), f.repo, b); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%+v: %v", b, err)
		}
	}
	dir := t.TempDir()
	if err := os.Symlink("/etc", dir+"/link"); err != nil {
		t.Fatal(err)
	}
	if err := writeInside(dir, "link/x", []byte("x")); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a link in the tree is not written through: %v", err)
	}
	if err := os.WriteFile(dir+"/file", []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeInside(dir, "file/x", []byte("x")); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a file is not a directory: %v", err)
	}
	if f.h.Seen("GIT push") != 0 {
		t.Error("nothing was pushed")
	}
}

func TestAProtectedBranchRefusalComesBackInTheServersWords(t *testing.T) {
	f := newFixture(t, "")
	f.h.Protect("main")
	main := f.h.Branch("main")
	_, err := f.p.Releases().CommitFile(t.Context(), f.repo, forge.FileChange{Branch: "main", Path: "a.txt", Content: []byte("a"), Message: "m"})
	if err == nil || !strings.Contains(err.Error(), "protected branch: main takes changes through a merge request only") {
		t.Fatalf("%v", err)
	}
	if f.h.Branch("main") != main {
		t.Error("main moved")
	}
	// A branch that is not protected still takes the change.
	if _, err := f.p.Releases().CommitFile(t.Context(), f.repo, forge.FileChange{Branch: "main", NewBranch: "bump/2", Path: "a.txt", Content: []byte("a"), Message: "m"}); err != nil {
		t.Errorf("%v", err)
	}
}

func TestTheProbeSaysWhatItCannotTell(t *testing.T) {
	f := newFixture(t, passwordDoc)
	r, err := f.p.Probe(t.Context())
	if err != nil || r.Account != "mary" || len(r.Checks) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range r.Checks {
		if !c.NotChecked || c.OK || c.Needs == "" || c.Detail == "" {
			t.Errorf("%+v", c)
		}
	}
	t.Setenv("LW_TEST_ANYGIT_CRED", `{"password":"p","extra":1}`)
	if _, err := f.p.Probe(t.Context()); err == nil || strings.Contains(err.Error(), `"p"`) {
		t.Errorf("a malformed credential is refused: %v", err)
	}
}

// Live test against a real Git server over https: opt in with LAUNCHWAY_LIVE_ANYGIT_CREDENTIAL (a token, or the
// JSON document), LAUNCHWAY_LIVE_ANYGIT_BASE (the server's https address) and LAUNCHWAY_LIVE_ANYGIT_REPO
// (owner/name as in its clone address). It only reads: the refs and the default branch.
func TestLive(t *testing.T) {
	cr, base, path := os.Getenv("LAUNCHWAY_LIVE_ANYGIT_CREDENTIAL"), os.Getenv("LAUNCHWAY_LIVE_ANYGIT_BASE"), os.Getenv("LAUNCHWAY_LIVE_ANYGIT_REPO")
	if cr == "" || base == "" || path == "" {
		t.Skip("set LAUNCHWAY_LIVE_ANYGIT_CREDENTIAL, LAUNCHWAY_LIVE_ANYGIT_BASE and LAUNCHWAY_LIVE_ANYGIT_REPO to run against a Git server")
	}
	p, err := New(forge.Connection{ID: "con_live", Name: "live", Kind: forge.KindPlain, BaseURL: base, Credential: forge.SecretRef{Env: "LAUNCHWAY_LIVE_ANYGIT_CREDENTIAL"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo, err := p.Repositories().Get(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := p.Content().Refs(ctx, repo)
	if err != nil || len(refs) == 0 {
		t.Fatalf("%v %v", refs, err)
	}
	t.Logf("default branch %q, %d refs", repo.DefaultBranch, len(refs))
}

// A server that answers with a redirect to another host is refused, and the other host sees nothing.
func TestARedirectToAnotherHostIsRefusedAndCarriesNoCredential(t *testing.T) {
	target := anygitfake.New()
	defer target.Close()
	target.Accept = [2]string{"git", anygitfake.Token}
	var hit atomic.Int32
	red := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		http.Redirect(w, r, target.URL+r.URL.Path+"?"+r.URL.RawQuery, http.StatusFound)
	}))
	defer red.Close()
	t.Setenv("LW_TEST_ANYGIT_CRED", anygitfake.Token)
	g, _ := egress.New([]string{"127.0.0.1"})
	p, err := New(forge.Connection{ID: "c", Name: "r", Kind: forge.KindPlain, BaseURL: red.URL + anygitfake.Prefix, Credential: forge.SecretRef{Env: "LW_TEST_ANYGIT_CRED"}}, g)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Repositories().Get(t.Context(), anygitfake.Path)
	if err == nil || hit.Load() == 0 {
		t.Fatalf("hit %d, err %v", hit.Load(), err)
	}
	if n := len(target.Requests) + len(target.Violations); n != 0 {
		t.Errorf("the redirect target was reached %d times: %v %v", n, target.Requests, target.Violations)
	}
	if strings.Contains(err.Error(), anygitfake.Token) {
		t.Errorf("the error holds the credential: %v", err)
	}
}

// With no guard given the default guard applies: a loopback server is refused, not reached unchecked.
func TestNoGuardMeansTheDefaultGuardNotNoGuard(t *testing.T) {
	h := anygitfake.New()
	defer h.Close()
	t.Setenv("LW_TEST_ANYGIT_CRED", anygitfake.Token)
	p, err := New(forge.Connection{ID: "c", Name: "n", Kind: forge.KindPlain, BaseURL: h.Base(), Credential: forge.SecretRef{Env: "LW_TEST_ANYGIT_CRED"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Repositories().Get(t.Context(), anygitfake.Path); err == nil || !strings.Contains(err.Error(), "internal address") {
		t.Errorf("%v", err)
	}
	if len(h.Requests) != 0 {
		t.Errorf("the server was reached: %v", h.Requests)
	}
}

// The user name and the password never appear in the arguments of the git process, only in its environment.
func TestTheCredentialIsNotInGitsArguments(t *testing.T) {
	f := newFixture(t, passwordDoc)
	bin := t.TempDir()
	log := bin + "/args"
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	wrap := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + log + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(bin+"/git", []byte(wrap), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if _, err := f.p.Content().Refs(t.Context(), f.repo); err != nil {
		t.Skipf("the wrapper did not run (%v)", err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Skipf("git was not run through the wrapper: %v", err)
	}
	for _, leak := range []string{"fake-password-for-tests-only-0001", "mary", "Authorization", "Basic "} {
		if strings.Contains(string(b), leak) {
			t.Errorf("git's arguments hold %q:\n%s", leak, b)
		}
	}
}

// An address with a user name or a password in it is refused at connection creation, with a message that does not repeat it.
func TestAnAddressWithEmbeddedCredentialsIsRefusedPlainly(t *testing.T) {
	_, err := New(forge.Connection{Name: "x", Kind: forge.KindPlain, BaseURL: "https://deploy:hunter2-not-real@git.example.com/x"}, nil)
	if !errors.Is(err, forge.ErrInvalid) || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "deploy") || !strings.Contains(err.Error(), "without a user name") {
		t.Errorf("%v", err)
	}
}
