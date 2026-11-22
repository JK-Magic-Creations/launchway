package hosts_test

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/anygit/anygitfake"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func anyGitService(t *testing.T) (*hosts.Service, *anygitfake.Host) {
	t.Helper()
	h := anygitfake.New()
	t.Cleanup(h.Close)
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	t.Setenv("LW_ANY_TOKEN", anygitfake.Token)
	g, _ := egress.New([]string{"127.0.0.1"})
	s := hosts.New(eng.Database(), eng, g)
	ctx := t.Context()
	if _, err := s.Add(ctx, hosts.AddInput{Name: "any", Kind: forge.KindPlain, BaseURL: h.Base(), Credential: forge.SecretRef{Env: "LW_ANY_TOKEN"}}, "dana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "shop", "any", anygitfake.Path); err != nil {
		t.Fatal(err)
	}
	return s, h
}

// A Git server has no pull requests: write-back by pull request is refused before anything is pushed, with
// the host's own reason and the way around it; a direct write to a branch the credential may push to works;
// a file change that asks for a pull request pushes its branch and says a person must merge it.
func TestAGitServerWithNoPullRequestsIsHonestAboutWriteBack(t *testing.T) {
	s, h := anyGitService(t)
	ctx := t.Context()
	_, err := s.WriteBackAuth(ctx, "any", anygitfake.Path, "main", "pull_request", "person")
	if !errors.Is(err, hosts.ErrNoPullRequests) || !strings.Contains(err.Error(), "no pull requests") || !strings.Contains(err.Error(), "ask a person to merge") {
		t.Fatalf("pull_request mode: %v", err)
	}
	pa, err := s.WriteBackAuth(ctx, "any", anygitfake.Path, "main", "direct", "")
	if err != nil || pa.Secret != anygitfake.Token || pa.Username != "git" || !strings.HasSuffix(pa.URL, "/git/"+anygitfake.Path) {
		t.Fatalf("direct mode: %+v %v", pa, err)
	}
	wr, err := s.WriteFile(ctx, "shop", hosts.Writes{Branch: "main", PullRequest: true, NewBranch: "bump/9", Path: "v.yaml", Content: []byte("a\n"), Message: "bump", Title: "Bump"})
	if !errors.Is(err, hosts.ErrNoPullRequests) || wr.Branch != "bump/9" || len(wr.Commit) != 40 || wr.PullRequest != 0 || !strings.Contains(err.Error(), "ask a person to merge") {
		t.Fatalf("%+v %v", wr, err)
	}
	if h.Branch("bump/9") != wr.Commit {
		t.Error("the branch was pushed")
	}
	wr, err = s.WriteFile(ctx, "shop", hosts.Writes{Branch: "feature", Path: "n.txt", Content: []byte("n\n"), Message: "notes"})
	if err != nil || h.Branch("feature") != wr.Commit {
		t.Fatalf("a direct write: %+v %v", wr, err)
	}
	h.Protect("main")
	if _, err := s.WriteFile(ctx, "shop", hosts.Writes{Branch: "main", Path: "n.txt", Content: []byte("n\n"), Message: "notes"}); err == nil || !strings.Contains(err.Error(), "protected branch: main") {
		t.Errorf("the server's own refusal comes back: %v", err)
	}
	if len(h.Violations) > 0 {
		t.Errorf("violations %v", h.Violations)
	}
}

// Results have nowhere to go on a Git server: the page says they are read in Launchway; the tag is pushed and the
// release stays in Launchway; a repository is entered, not searched.
func TestAGitServerResultsReleasesAndRepositories(t *testing.T) {
	s, h := anyGitService(t)
	ctx := t.Context()
	if via := s.StatusVia(ctx, "shop"); via != hosts.ViaNone {
		t.Fatalf("via %s", via)
	}
	v, err := s.View(ctx, "shop")
	if n := v.Unsupported[forge.CapResults]; err != nil || n.Support != forge.None || !strings.Contains(n.Workaround, "Read results in Launchway") {
		t.Fatalf("%+v %v", n, err)
	}
	link, err := s.CutRelease(ctx, "shop", h.Branch("main"), forge.ReleaseSpec{Tag: "v1.2.0", Title: "One two", Notes: "n"}, "Release 1.2.0")
	if err != nil || link != "" {
		t.Fatalf("the tag is pushed and nothing else is asked: %q %v", link, err)
	}
	if kind, commit := h.Tag("v1.2.0"); kind != "tag" || commit != h.Branch("main") {
		t.Errorf("tag %q %q", kind, commit)
	}
	if _, err := s.PublishRelease(ctx, "shop", forge.ReleaseSpec{Tag: "v1.2.0"}); !errors.Is(err, hosts.ErrNoRelease) {
		t.Errorf("%v", err)
	}
	if n := s.ReleaseNote(ctx, "shop"); !strings.Contains(n, "release page and its files stay in Launchway") {
		t.Errorf("note %q", n)
	}
	page, err := s.Repositories(ctx, "any", "", "")
	if err != nil || len(page.Items) != 0 {
		t.Errorf("a Git server lists nothing: %+v %v", page, err)
	}
	if _, err := s.Attach(ctx, "ghost", "any", "acme/ghost.git"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("an entered path that is not there is refused at attach: %v", err)
	}
	if _, err := s.Attach(ctx, "ssh", "any", "git@host:acme/app.git"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("an ssh address is not a path: %v", err)
	}
	if ok, err := s.ReportRun(ctx, "shop", forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: h.Branch("main"), State: forge.Passing}); ok || err != nil {
		t.Errorf("no status is sent: %v %v", ok, err)
	}
}

// An ssh address is refused for a connection, in words; the watcher's own connection stays out of the list.
func TestAGitServerConnectionRefusesSSHAndTheWatcherIsNotListed(t *testing.T) {
	s, _ := anyGitService(t)
	ctx := t.Context()
	_, err := s.Add(ctx, hosts.AddInput{Name: "viassh", Kind: forge.KindPlain, BaseURL: "ssh://git@git.example.com:22", Credential: forge.SecretRef{Env: "LW_ANY_TOKEN"}}, "dana")
	if !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := s.Add(ctx, hosts.AddInput{Name: "nohttp", Kind: forge.KindPlain, BaseURL: "http://git.example.com", Credential: forge.SecretRef{Env: "LW_ANY_TOKEN"}}, "dana"); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "https") {
		t.Errorf("a credential is not sent over plain http: %v", err)
	}
	if _, err := s.Add(ctx, hosts.AddInput{Name: "nobase", Kind: forge.KindPlain, Credential: forge.SecretRef{Env: "LW_ANY_TOKEN"}}, "dana"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a base address is required: %v", err)
	}
	// The watcher's connection (a Flight that watches a bare address) is not an outside host.
	if _, err := s.Store().CreateConnection(ctx, forge.Connection{Name: store.PlainConnectionName, Kind: forge.KindPlain, Events: forge.EventsPolling, Enabled: true}, "startup"); err != nil {
		t.Fatal(err)
	}
	vs, err := s.List(ctx)
	if err != nil || len(vs) != 1 || vs[0].Name != "any" || vs[0].Kind != "plain" {
		t.Fatalf("%+v %v", vs, err)
	}
	if _, err := s.Get(ctx, store.PlainConnectionName); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("%v", err)
	}
	res, err := s.Test(ctx, "any")
	if err != nil || res.Report.Account != "git" || len(res.Report.Checks) != 3 {
		t.Errorf("%+v %v", res, err)
	}
}
