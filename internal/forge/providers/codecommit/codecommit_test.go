package codecommit

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/forgetest"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/codecommit/codecommitfake"
)

type fixture struct {
	h    *codecommitfake.Host
	p    *Provider
	repo forge.Repository
}

func newFixture(t *testing.T, doc string, owners ...string) *fixture {
	t.Helper()
	h := codecommitfake.New()
	t.Cleanup(h.Close)
	t.Setenv("LW_TEST_CC_CRED", doc)
	g, _ := egress.New([]string{"127.0.0.1"})
	restore := UseTestAPI(h.API())
	ep, err := EndpointsOf("")
	restore()
	if err != nil {
		t.Fatal(err)
	}
	p := NewAt(forge.Connection{ID: "con_1", Name: "cc", Kind: forge.KindCodeCommit, Credential: forge.SecretRef{Env: "LW_TEST_CC_CRED"}, Owners: owners}, ep, g)
	repo, err := p.Repositories().Get(t.Context(), h.RepoPath())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h, p, repo}
}

func (f *fixture) noViolations(t *testing.T) {
	t.Helper()
	if len(f.h.Violations) > 0 {
		t.Errorf("the fake host saw calls the documentation does not allow: %v", f.h.Violations)
	}
}

func TestConformance(t *testing.T) {
	for name, doc := range map[string]string{"static key": codecommitfake.CredentialDoc(), "assumed role": codecommitfake.RoleCredentialDoc()} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, doc, codecommitfake.Account)
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
	for _, feat := range []forge.Capability{forge.CapResults, forge.CapReleases, forge.CapRepoCreate, forge.CapLand, forge.CapAutoLand, forge.CapForks, forge.CapProposalRef, forge.CapDrafts, forge.CapResultsGate} {
		if c.Has(feat) {
			t.Errorf("%s must be declared none", feat)
		}
		if d := c[feat]; d.Reason == "" || d.Workaround == "" {
			t.Errorf("%s: %+v", feat, d)
		}
	}
	if d := c[forge.CapResults]; !strings.Contains(d.Workaround, "summary comment") {
		t.Errorf("results say where they go: %+v", d)
	}
	for _, feat := range []forge.Capability{forge.CapIdentity, forge.CapRepositories, forge.CapEvents, forge.CapTags, forge.CapCommitFile, forge.CapProposalNote, forge.CapProposals, forge.CapRules} {
		if !c.Has(feat) {
			t.Errorf("%s must be available", feat)
		}
	}
	if d := c[forge.CapEvents]; d.Support != forge.Emulated || !strings.Contains(d.Reason, "no webhook") || !strings.Contains(d.Workaround, "polls") {
		t.Errorf("events: %+v", d)
	}
}

func TestEndpointsFromTheBaseAddress(t *testing.T) {
	for base, want := range map[string]struct{ api, sts, region, git string }{
		"https://git-codecommit.us-east-2.amazonaws.com":           {"codecommit.us-east-2.amazonaws.com", "sts.us-east-2.amazonaws.com", "us-east-2", "git-codecommit.us-east-2.amazonaws.com"},
		"https://git-codecommit-fips.us-gov-west-1.amazonaws.com/": {"codecommit-fips.us-gov-west-1.amazonaws.com", "sts.us-gov-west-1.amazonaws.com", "us-gov-west-1", "git-codecommit-fips.us-gov-west-1.amazonaws.com"},
		"https://git-codecommit.cn-north-1.amazonaws.com.cn":       {"codecommit.cn-north-1.amazonaws.com.cn", "sts.cn-north-1.amazonaws.com.cn", "cn-north-1", "git-codecommit.cn-north-1.amazonaws.com.cn"},
	} {
		ep, err := EndpointsOf(base)
		if err != nil || ep.API.Host != want.api || ep.STS.Host != want.sts || ep.Region != want.region || ep.GitHost != want.git || !ep.Hosts[want.api] || !ep.Hosts[want.sts] || ep.Hosts[want.git] {
			t.Errorf("%s: %+v %v", base, ep, err)
		}
	}
	for _, bad := range []string{"", "http://git-codecommit.us-east-1.amazonaws.com", "https://example.com", "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/x",
		"https://git-codecommit.us-east-1.amazonaws.com.evil.example", "https://codecommit.us-east-1.amazonaws.com"} {
		if _, err := EndpointsOf(bad); err == nil {
			t.Errorf("%q is accepted", bad)
		}
	}
	if _, err := New(forge.Connection{Name: "x", Kind: forge.KindCodeCommit, BaseURL: "https://example.com"}, nil); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestTheCredentialDocumentIsChecked(t *testing.T) {
	good := `{"access_key_id":"AKFAFAKEFAKEFAKE0001","secret_access_key":"s"}`
	if d, err := parseCreds(good); err != nil || d.RoleARN != "" {
		t.Fatalf("%+v %v", d, err)
	}
	for name, doc := range map[string]string{
		"not json":         `AKFAFAKEFAKEFAKE0001:secret`,
		"unknown field":    `{"access_key_id":"AKFAFAKEFAKEFAKE0001","secret_access_key":"s","token":"x"}`,
		"no secret":        `{"access_key_id":"AKFAFAKEFAKEFAKE0001"}`,
		"bad key id":       `{"access_key_id":"nope","secret_access_key":"s"}`,
		"bad role":         `{"access_key_id":"AKFAFAKEFAKEFAKE0001","secret_access_key":"s","role_arn":"arn:aws:s3:::bucket"}`,
		"role extras only": `{"access_key_id":"AKFAFAKEFAKEFAKE0001","secret_access_key":"s","external_id":"abc123"}`,
		"short duration":   `{"access_key_id":"AKFAFAKEFAKEFAKE0001","secret_access_key":"s","role_arn":"arn:aws:iam::123456789012:role/r","duration_seconds":60}`,
	} {
		_, err := parseCreds(doc)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "secret_access_key\":\"s") || strings.Contains(err.Error(), "AKFAFAKE") {
			t.Errorf("%s: the error repeats the credential: %v", name, err)
		}
	}
}

func TestEverySignatureIsVerifiedByTheFake(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc(), codecommitfake.Account)
	bad := strings.Replace(codecommitfake.CredentialDoc(), "fake-secret", "other-secret", 1)
	t.Setenv("LW_TEST_CC_CRED", bad)
	f.h.ClearViolations()
	_, err := f.p.Repositories().Get(t.Context(), f.h.RepoPath())
	if err == nil || !strings.Contains(err.Error(), "the host answered 403") || strings.Contains(err.Error(), "other-secret") {
		t.Fatalf("a wrong secret key must be refused without being repeated: %v", err)
	}
	if len(f.h.Violations) != 1 || !strings.Contains(f.h.Violations[0], "InvalidSignatureException") {
		t.Fatalf("violations %v", f.h.Violations)
	}
}

func TestARoleIsAssumedAndKeptUntilShortlyBeforeItExpires(t *testing.T) {
	f := newFixture(t, codecommitfake.RoleCredentialDoc(), codecommitfake.Account)
	clock := time.Now().UTC()
	f.h.Now = func() time.Time { return clock }
	f.p.SetClock(func() time.Time { return clock })
	f.h.SessionSeconds(1800)
	ctx := t.Context()
	list := func() {
		t.Helper()
		if _, err := f.p.Repositories().List(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	base := f.h.Assumed // the fixture itself read one repository: that session lasts the default hour
	list()
	list()
	if got := f.h.Assumed - base; got != 0 {
		t.Fatalf("the temporary credentials are kept: %d more assumptions", got)
	}
	clock = clock.Add(56 * time.Minute) // within the last 5 minutes of the first hour: renewed, now for 30 minutes
	list()
	if f.h.Assumed != base+1 {
		t.Fatalf("not renewed shortly before expiry: %d", f.h.Assumed-base)
	}
	list()
	n := f.h.Assumed
	clock = clock.Add(20 * time.Minute) // 10 minutes left: still used
	list()
	if f.h.Assumed != n {
		t.Errorf("renewed too early: %d", f.h.Assumed-n)
	}
	clock = clock.Add(6 * time.Minute) // inside the last 5 minutes: renewed
	list()
	if f.h.Assumed != n+1 {
		t.Errorf("not renewed shortly before expiry: %d", f.h.Assumed-n)
	}
	list()
	if f.h.Assumed != n+1 {
		t.Errorf("renewed again at once: %d", f.h.Assumed-n)
	}
	f.noViolations(t)
	// A refusal to assume says why and never repeats the key.
	t.Setenv("LW_TEST_CC_CRED", strings.Replace(codecommitfake.RoleCredentialDoc(), "role/launchway-ci", "role/other", 1))
	_, err := f.p.Repositories().List(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "sts:AssumeRole") || strings.Contains(err.Error(), codecommitfake.SigningKey) {
		t.Fatalf("%v", err)
	}
	f.h.ClearViolations()
}

func TestGitCredentialIsSignedPerRequestAndVerifiedByTheFake(t *testing.T) {
	for name, doc := range map[string]string{"static": codecommitfake.CredentialDoc(), "role": codecommitfake.RoleCredentialDoc()} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, doc, codecommitfake.Account)
			ctx := t.Context()
			c1, err := f.p.Access().Credential(ctx, f.repo, false)
			if err != nil {
				t.Fatal(err)
			}
			if !f.h.CheckGit(c1.Username, c1.Secret) {
				t.Fatalf("the fake refuses the credential: %v", f.h.Violations)
			}
			if name == "role" != strings.Contains(c1.Username, "%") {
				t.Errorf("the session token goes after a %% in the user name only for temporary credentials: %q", c1.Username)
			}
			if len(c1.Secret) != 15+1+64 || c1.Secret[8] != 'T' || c1.Secret[15] != 'Z' {
				t.Errorf("the password is timestamp, Z, signature: %q", c1.Secret)
			}
			if f.h.CheckGit(c1.Username, c1.Secret[:16]+strings.Repeat("0", 64)) || f.h.CheckGit("AKFAOTHEROTHEROTHER1", c1.Secret) {
				t.Error("a wrong signature or key is accepted")
			}
			b, _ := f.p.Access().CloneAddresses(ctx, f.repo)
			if u, _ := url.Parse(b.HTTPS); u.User != nil || strings.Contains(b.HTTPS, "AKFA") {
				t.Errorf("the clone address holds a credential: %s", b.HTTPS)
			}
			// The same time gives the same password; another time another one.
			now := time.Now()
			f.p.SetClock(func() time.Time { return now })
			a, _ := f.p.Access().Credential(ctx, f.repo, true)
			b2, _ := f.p.Access().Credential(ctx, f.repo, true)
			f.p.SetClock(func() time.Time { return now.Add(time.Minute) })
			c, _ := f.p.Access().Credential(ctx, f.repo, true)
			if a.Secret != b2.Secret || a.Secret == c.Secret {
				t.Error("the password depends on the time and on nothing random")
			}
			// An address on another host never gets a signature.
			other := f.repo
			other.Clone.HTTPS = "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/app"
			if _, err := f.p.Access().Credential(ctx, other, false); err == nil {
				t.Error("a clone address on another host is signed")
			}
		})
	}
}

func TestATagIsMadeAndPushedWithGit(t *testing.T) {
	f := newFixture(t, codecommitfake.RoleCredentialDoc(), codecommitfake.Account)
	ctx := t.Context()
	main := f.h.Branch("main")
	r := f.p.Releases()
	ref, err := r.CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: main, Message: "Release 1.0.0"})
	if err != nil || ref.Name != "refs/tags/v1.0.0" || ref.Commit != main {
		t.Fatalf("%+v %v", ref, err)
	}
	if kind, commit := f.h.Tag("v1.0.0"); kind != "tag" || commit != main {
		t.Fatalf("the host has %q at %q: an annotated tag at the commit", kind, commit)
	}
	if m := f.h.TagMessage("v1.0.0"); m != "Release 1.0.0" {
		t.Errorf("message %q", m)
	}
	pushes := f.h.Seen("GIT push")
	if _, err := r.CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: main, Message: "again"}); err != nil || f.h.Seen("GIT push") != pushes {
		t.Errorf("a tag already at the commit is accepted without a push: %v", err)
	}
	if _, err := r.CreateTag(ctx, f.repo, forge.TagSpec{Name: "v1.0.0", Commit: f.h.Branch("feature"), Message: "x"}); !errors.Is(err, forge.ErrExists) {
		t.Errorf("another commit: %v", err)
	}
	if _, err := r.CreateTag(ctx, f.repo, forge.TagSpec{Name: "v2", Commit: strings.Repeat("e", 40), Message: "x"}); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("a commit the host has not: %v", err)
	}
	for _, bad := range []string{"", "-x", "a..b", "a b", "x.lock", "v1/"} {
		if _, err := r.CreateTag(ctx, f.repo, forge.TagSpec{Name: bad, Commit: main, Message: "x"}); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("tag %q: %v", bad, err)
		}
	}
	f.h.Denied["GitPush"] = true
	if _, err := r.CreateTag(ctx, f.repo, forge.TagSpec{Name: "v3", Commit: main, Message: "x"}); err == nil || strings.Contains(err.Error(), codecommitfake.SigningKey) {
		t.Errorf("a push the host refuses is an error that keeps the credentials out: %v", err)
	}
	f.h.ClearViolations()
	if _, err := r.PublishRelease(ctx, f.repo, forge.ReleaseSpec{Tag: "v1.0.0"}); !isUnsup(err) {
		t.Errorf("no release object: %v", err)
	}
}

func TestTheSummaryIsOneCommentUpdatedInPlace(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc(), codecommitfake.Account)
	ctx, r := t.Context(), f.p.Releases()
	k1, err := r.UpsertSummary(ctx, f.repo, 1, "", "first")
	if err != nil || k1 == "" {
		t.Fatal(k1, err)
	}
	k2, err := r.UpsertSummary(ctx, f.repo, 1, k1, "second")
	if err != nil || k2 != k1 || f.h.Comments() != 1 {
		t.Fatalf("%q %v comments %d", k2, err, f.h.Comments())
	}
	if c, _ := f.h.Comment(k1); c != "second" {
		t.Errorf("comment %q", c)
	}
	k3, err := r.UpsertSummary(ctx, f.repo, 1, "cmt99999999", "third") // one that is not there is posted again
	if err != nil || k3 == "cmt99999999" || f.h.Comments() != 2 {
		t.Fatalf("%q %v", k3, err)
	}
	// Written by another identity (the credentials changed): posted again, not edited.
	t.Setenv("LW_TEST_CC_CRED", codecommitfake.RoleCredentialDoc())
	k4, err := r.UpsertSummary(ctx, f.repo, 1, k1, "fourth")
	if err != nil || k4 == k1 || f.h.Comments() != 3 {
		t.Fatalf("%q %v", k4, err)
	}
	long := strings.Repeat("é", 20000)
	if k, err := r.UpsertSummary(ctx, f.repo, 1, k4, long); err != nil || k != k4 {
		t.Errorf("a long summary is cut to the host's limit: %v", err)
	}
	if _, err := r.UpsertSummary(ctx, f.repo, 99, "", "x"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("%v", err)
	}
	f.noViolations(t)
}

func TestPullRequests(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc(), codecommitfake.Account)
	ctx, pr := t.Context(), f.p.Proposals()
	page, err := pr.List(ctx, f.repo, forge.ProposalOpen, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	p := page.Items[0]
	if p.Number != 1 || p.Title != "A feature" || p.State != forge.ProposalOpen || p.Source.Name != "refs/heads/feature" || p.Target.Name != "refs/heads/main" ||
		p.Head != f.h.Branch("feature") || p.Author.Login != "Jane_Doe" || p.Ext["fork"] != false || p.Base != f.h.Branch("main") {
		t.Errorf("%+v", p)
	}
	if drafts, _ := pr.List(ctx, f.repo, forge.ProposalDraft, ""); len(drafts.Items) != 0 {
		t.Errorf("drafts %+v", drafts)
	}
	// A pull request through the API: a file on a new branch, then the pull request.
	commit, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "bump", Path: "deploy/version.txt", Content: []byte("1.2.3\n"), Message: "Bump"})
	if err != nil || commit != f.h.Branch("bump") {
		t.Fatalf("%q %v", commit, err)
	}
	if got, ok := f.h.File("bump", "deploy/version.txt"); !ok || got != "1.2.3" {
		t.Errorf("the file on the host: %q %v", got, ok)
	}
	if again, err := f.p.Releases().CommitFile(ctx, f.repo, forge.FileChange{Branch: "main", NewBranch: "bump", Path: "deploy/version.txt", Content: []byte("1.2.3\n"), Message: "Bump"}); err != nil || again != commit {
		t.Errorf("the same content again is not an error: %q %v", again, err)
	}
	n, err := pr.Create(ctx, f.repo, forge.NewProposal{Title: "Bump the version", Body: "by Launchway", Source: forge.Ref{Name: "bump"}, Target: forge.Ref{Name: "main"}})
	if err != nil || n.Number != 2 || n.Source.Commit != commit {
		t.Fatalf("%+v %v", n, err)
	}
	if _, err := pr.Create(ctx, f.repo, forge.NewProposal{Title: "d", Source: forge.Ref{Name: "bump"}, Target: forge.Ref{Name: "main"}, Draft: true}); !isUnsup(err) {
		t.Errorf("a draft: %v", err)
	}
	title := "A better title"
	if u, err := pr.Update(ctx, f.repo, 2, forge.ProposalPatch{Title: &title}); err != nil || u.Title != title {
		t.Errorf("%+v %v", u, err)
	}
	if err := pr.Close(ctx, f.repo, 2); err != nil {
		t.Fatal(err)
	}
	if err := pr.Reopen(ctx, f.repo, 2); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "cannot reopen") {
		t.Errorf("reopen: %v", err)
	}
	f.h.Merge(1)
	landed, _ := pr.List(ctx, f.repo, forge.ProposalLanded, "")
	closed, _ := pr.List(ctx, f.repo, forge.ProposalClosed, "")
	if len(landed.Items) != 1 || landed.Items[0].Number != 1 || len(closed.Items) != 1 || closed.Items[0].Number != 2 {
		t.Errorf("a merged pull request is landed, a closed one is closed: %+v %+v", landed, closed)
	}
	f.noViolations(t)
}

func TestApprovalRulesAreRead(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc(), codecommitfake.Account)
	ctx := t.Context()
	rs, err := f.p.Rules().List(ctx, f.repo)
	if err != nil || len(rs) != 1 || rs[0].Pattern != "refs/heads/main" || rs[0].RequiredReviews != 2 || rs[0].ProposalsOnly || len(rs[0].RequiredResults) != 0 {
		t.Fatalf("%+v %v", rs, err)
	}
	if err := f.p.Rules().Set(ctx, f.repo, rs); !isUnsup(err) {
		t.Errorf("set: %v", err)
	}
	rv, err := f.p.Reviews().Reviews(ctx, f.repo, 1)
	if err != nil || len(rv) != 1 || rv[0].Verdict != forge.Approve || rv[0].Author.Login != "Mary_Major" {
		t.Fatalf("%+v %v", rv, err)
	}
	if _, err := f.p.Reviews().Submit(ctx, f.repo, 1, forge.Approve, ""); !isUnsup(err) {
		t.Errorf("submit: %v", err)
	}
	f.noViolations(t)
}

func TestPollingFindsOpenedAndRevisedPullRequests(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc(), codecommitfake.Account)
	ctx, ev := t.Context(), f.p.Events()
	if ev.Mode() != forge.EventsPolling {
		t.Fatal(ev.Mode())
	}
	evs, cur, err := ev.Poll(ctx, f.repo, "")
	if err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalOpened || evs[0].Number != 1 || evs[0].Fork || evs[0].Head != f.h.Branch("feature") || evs[0].Base != "main" {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs, cur, err = ev.Poll(ctx, f.repo, cur); err != nil || len(evs) != 0 {
		t.Fatalf("nothing changed: %+v %v", evs, err)
	}
	f.h.Push(1, "more.txt")
	if evs, cur, err = ev.Poll(ctx, f.repo, cur); err != nil || len(evs) != 1 || evs[0].Kind != forge.EventProposalRevised {
		t.Fatalf("%+v %v", evs, err)
	}
	n := f.h.OpenPull("feature", "again")
	if evs, _, err = ev.Poll(ctx, f.repo, cur); err != nil || len(evs) != 1 || evs[0].Number != n || evs[0].Kind != forge.EventProposalOpened {
		t.Fatalf("%+v %v", evs, err)
	}
	if _, _, err := ev.Poll(ctx, f.repo, "!"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("bad cursor: %v", err)
	}
	if _, err := ev.Verify(nil, []byte("s")); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a delivery is refused: %v", err)
	}
	f.noViolations(t)
}

func TestTheOwnersListLimitsAccounts(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc())
	f.p.conn.Owners = []string{"999999999999"}
	if _, err := f.p.Repositories().Get(t.Context(), f.h.RepoPath()); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("%v", err)
	}
	page, err := f.p.Repositories().List(t.Context(), "")
	if err != nil || len(page.Items) != 0 {
		t.Errorf("%+v %v", page, err)
	}
	for _, bad := range []string{"app", "123/app", "123456789012/a/b", "123456789012/a b"} {
		if _, err := f.p.Repositories().Get(t.Context(), bad); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestTheConnectionTestReadsOnly(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc(), codecommitfake.Account)
	rep, err := f.p.Probe(t.Context())
	if err != nil || rep.Account == "" || rep.Repositories != 1 || len(rep.Checks) < 6 || strings.Contains(rep.Account, "FAKEFAKE") {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, c := range rep.Checks {
		if !c.OK && !c.NotChecked {
			t.Errorf("%+v", c)
		}
	}
	if f.h.Seen("POST CodeCommit_20150413.PutFile")+f.h.Seen("POST CodeCommit_20150413.Create")+f.h.Seen("POST CodeCommit_20150413.Post")+f.h.Seen("GIT push") != 0 {
		t.Errorf("the test wrote: %v", f.h.Requests)
	}
	f.h.Denied["ListPullRequests"] = true
	rep, _ = f.p.Probe(t.Context())
	failed := 0
	for _, c := range rep.Checks {
		if !c.OK && !c.NotChecked {
			failed++
			if !strings.Contains(c.Detail, "403") {
				t.Errorf("%+v", c)
			}
		}
	}
	if failed != 1 {
		t.Errorf("one check names what the IAM policy lacks: %+v", rep.Checks)
	}
}

// TestLive runs the read-only part of the conformance against a real account:
// LAUNCHWAY_LIVE_CODECOMMIT_CREDENTIAL is the credential document (JSON), _BASE the
// Git endpoint of the region (https://git-codecommit.<region>.amazonaws.com) and
// _REPO account/name. It reads; it writes nothing.
func TestLive(t *testing.T) {
	doc, base, path := os.Getenv("LAUNCHWAY_LIVE_CODECOMMIT_CREDENTIAL"), os.Getenv("LAUNCHWAY_LIVE_CODECOMMIT_BASE"), os.Getenv("LAUNCHWAY_LIVE_CODECOMMIT_REPO")
	if doc == "" || base == "" || path == "" {
		t.Skip("set LAUNCHWAY_LIVE_CODECOMMIT_CREDENTIAL (the credential JSON), LAUNCHWAY_LIVE_CODECOMMIT_BASE and LAUNCHWAY_LIVE_CODECOMMIT_REPO (account/name) to run against AWS")
	}
	acct, _, _ := strings.Cut(path, "/")
	p, err := New(forge.Connection{ID: "con_live", Name: "live", Kind: forge.KindCodeCommit, BaseURL: base,
		Credential: forge.SecretRef{Env: "LAUNCHWAY_LIVE_CODECOMMIT_CREDENTIAL"}, Owners: []string{acct}}, nil)
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
	if _, err := p.Proposals().List(ctx, repo, forge.ProposalOpen, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rules().List(ctx, repo); err != nil {
		t.Logf("approval rules read: %v", err)
	}
	if c, err := p.Access().Credential(ctx, repo, false); err != nil || c.Username == "" || c.Secret == "" {
		t.Fatalf("%v", err)
	}
}

func isUnsup(err error) bool { _, ok := forge.IsUnsupported(err); return ok }
