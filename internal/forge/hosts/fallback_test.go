package hosts_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// plainHost is a provider that has comments only, or statuses the token may not
// write, or neither: the three hosts of the roadmap's "hosts without webhooks
// or statuses". The connection's base address says which.
type plainHost struct {
	forge.Base
	mu       sync.Mutex
	statuses int
	comments map[string]string
	nextID   int
	answer   string // what a status gets
}

var hostsByName sync.Map // base address -> *plainHost of the current test

func newPlain(c forge.Connection) *plainHost {
	caps := forge.NoCapabilities("the host has none", "use the host")
	for _, f := range []forge.Capability{forge.CapIdentity, forge.CapRepositories} {
		caps[f] = forge.Declaration{Support: forge.Native}
	}
	h := &plainHost{comments: map[string]string{}}
	switch {
	case strings.Contains(c.BaseURL, "comments"):
		caps[forge.CapProposalNote] = forge.Declaration{Support: forge.Native}
	case strings.Contains(c.BaseURL, "denied"):
		caps[forge.CapProposalNote] = forge.Declaration{Support: forge.Native}
		caps[forge.CapResults] = forge.Declaration{Support: forge.Native}
		h.answer = "the host answered 403. Either the token lacks the permission this needs on the repository, or the repository is not visible to it"
	case strings.Contains(c.BaseURL, "failing"):
		caps[forge.CapProposalNote] = forge.Declaration{Support: forge.Native}
		caps[forge.CapResults] = forge.Declaration{Support: forge.Native}
		h.answer = "the host answered 500"
	}
	h.Base = forge.Base{K: c.Kind, Caps: caps}
	return h
}

func (h *plainHost) Repositories() forge.Repositories { return plainRepos{h} }
func (h *plainHost) Results() forge.Results           { return plainResults{h} }
func (h *plainHost) Releases() forge.Releases         { return plainReleases{h} }

type plainRepos struct{ h *plainHost }

func (plainRepos) List(context.Context, string) (forge.Page[forge.Repository], error) {
	return forge.Page[forge.Repository]{}, nil
}
func (plainRepos) Get(_ context.Context, path string) (forge.Repository, error) {
	return forge.Repository{ID: "r1", Path: path, Name: "app", DefaultBranch: "main"}, nil
}
func (plainRepos) Create(context.Context, forge.NewRepository) (forge.Repository, error) {
	return forge.Repository{}, errors.New("no")
}
func (plainRepos) Delete(context.Context, string) error                   { return errors.New("no") }
func (plainRepos) SetDefaultBranch(context.Context, string, string) error { return errors.New("no") }

type plainResults struct{ h *plainHost }

func (r plainResults) Publish(context.Context, forge.Repository, forge.Result) error {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	r.h.statuses++
	return errors.New(r.h.answer)
}
func (plainResults) List(context.Context, forge.Repository, string) ([]forge.Result, error) {
	return nil, nil
}

type plainReleases struct{ h *plainHost }

func (r plainReleases) UpsertSummary(_ context.Context, _ forge.Repository, number int, key, body string) (string, error) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	if key == "" {
		r.h.nextID++
		key = "c" + string(rune('0'+r.h.nextID))
	}
	r.h.comments[key] = body
	return key, nil
}
func (plainReleases) CreateTag(context.Context, forge.Repository, forge.TagSpec) (forge.Ref, error) {
	return forge.Ref{}, errors.New("no")
}
func (plainReleases) PublishRelease(context.Context, forge.Repository, forge.ReleaseSpec) (string, error) {
	return "", errors.New("no")
}
func (plainReleases) CommitFile(context.Context, forge.Repository, forge.FileChange) (string, error) {
	return "", errors.New("no")
}

func init() {
	// The double stands in for a host of a kind the product does not offer (launchway is the built-in forge,
	// which has no entry of its own here), so that it never collides with a real kind. The real codecommit kind
	// has its own test of this path in codecommit_test.go.
	hosts.Register(hosts.Entry{Kind: forge.KindLaunchway, Label: "Test host",
		New: func(c forge.Connection, _ *egress.Guard) (forge.Provider, error) {
			if v, ok := hostsByName.Load(c.BaseURL); ok {
				return v.(*plainHost), nil
			}
			h := newPlain(c)
			hostsByName.Store(c.BaseURL, h)
			return h, nil
		},
		Endpoints: func(c forge.Connection) ([]string, error) { return []string{c.BaseURL}, nil }})
}

func plainService(t *testing.T, base string) (*hosts.Service, *plainHost) {
	t.Helper()
	hostsByName.Delete(base)
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	t.Setenv("LW_PLAIN_TOKEN", "tok-123456")
	g, _ := egress.New([]string{"comments.example", "denied.example", "bare.example", "failing.example"})
	s := hosts.New(eng.Database(), eng, g)
	if _, err := s.Add(t.Context(), hosts.AddInput{Name: "p", Kind: forge.KindLaunchway, BaseURL: base, Credential: forge.SecretRef{Env: "LW_PLAIN_TOKEN"}}, "dana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(t.Context(), "shop", "p", "acme/app"); err != nil {
		t.Fatal(err)
	}
	v, _ := hostsByName.Load(base)
	return s, v.(*plainHost)
}

var res = forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: strings.Repeat("a", 40), State: forge.Passing, Summary: "Sortie #1 passed"}

func TestAHostWithCommentsOnlyGetsTheResultInTheComment(t *testing.T) {
	s, h := plainService(t, "https://comments.example")
	ctx := t.Context()
	if via := s.StatusVia(ctx, "shop"); via != hosts.ViaComment {
		t.Fatalf("via %s", via)
	}
	got, err := s.ReportResult(ctx, "shop", res, 4, "body one")
	if err != nil || got != hosts.ViaComment {
		t.Fatalf("%v %v", got, err)
	}
	if got, err = s.ReportResult(ctx, "shop", res, 4, "body two"); err != nil || got != hosts.ViaComment || len(h.comments) != 1 {
		t.Fatalf("the one comment is updated in place: %v %v %v", got, err, h.comments)
	}
	for _, b := range h.comments {
		if b != "body two" {
			t.Errorf("comment %q", b)
		}
	}
	if h.statuses != 0 {
		t.Error("a status was asked of a host that has none")
	}
	if ok, err := s.ReportRun(ctx, "shop", res); ok || err != nil {
		t.Errorf("a step status is not sent: %v %v", ok, err)
	}
	// A commit with no pull request has nowhere to say it.
	if got, _ := s.ReportResult(ctx, "shop", res, 0, ""); got != hosts.ViaNone {
		t.Errorf("no pull request: %v", got)
	}
	v, err := s.View(ctx, "shop")
	n := v.Unsupported[forge.CapResults]
	if err != nil || n.Support != forge.Emulated || !strings.Contains(n.Workaround, "summary comment") || n.Reason == "" {
		t.Fatalf("the page says so: %+v %v", n, err)
	}
	// With the statuses switch off nothing is sent either way.
	off := false
	if _, err := s.SetReports(ctx, "shop", &off, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReportResult(ctx, "shop", res, 4, "x"); got != hosts.ViaNone {
		t.Errorf("switch off: %v", got)
	}
}

func TestARefusedStatusFallsBackToTheCommentAndIsNotAskedAgain(t *testing.T) {
	s, h := plainService(t, "https://denied.example")
	ctx := t.Context()
	if via := s.StatusVia(ctx, "shop"); via != hosts.ViaStatus {
		t.Fatalf("it starts as a status: %s", via)
	}
	got, err := s.ReportResult(ctx, "shop", res, 9, "body")
	if err != nil || got != hosts.ViaComment || h.statuses != 1 || len(h.comments) != 1 {
		t.Fatalf("%v %v statuses %d comments %v", got, err, h.statuses, h.comments)
	}
	if got, err = s.ReportResult(ctx, "shop", res, 9, "body2"); err != nil || got != hosts.ViaComment || h.statuses != 1 {
		t.Fatalf("not asked again: %v %v statuses %d", got, err, h.statuses)
	}
	v, _ := s.View(ctx, "shop")
	if v.Unsupported[forge.CapResults].Support != forge.Emulated || !strings.Contains(v.Unsupported[forge.CapResults].Reason, "token may not write") {
		t.Errorf("the page says why: %+v", v.Unsupported[forge.CapResults])
	}
}

func TestAHostWithNeitherSaysSo(t *testing.T) {
	s, _ := plainService(t, "https://bare.example")
	ctx := t.Context()
	if got, err := s.ReportResult(ctx, "shop", res, 3, "b"); err != nil || got != hosts.ViaNone {
		t.Fatalf("%v %v", got, err)
	}
	v, _ := s.View(ctx, "shop")
	if v.Unsupported[forge.CapResults].Support != forge.None || v.Unsupported[forge.CapResults].Workaround != "Read results in Launchway" {
		t.Errorf("%+v", v.Unsupported[forge.CapResults])
	}
}

// A host that fails in another way is an error to report, not a reason to change how results are sent.
func TestOtherFailuresAreNotAFallback(t *testing.T) {
	s, h := plainService(t, "https://failing.example")
	ctx := t.Context()
	for i := 0; i < 2; i++ {
		if got, err := s.ReportResult(ctx, "shop", res, 3, "b"); err == nil || got != hosts.ViaNone {
			t.Fatalf("a server error is returned: %v %v", got, err)
		}
	}
	if h.statuses != 2 || len(h.comments) != 0 || s.StatusVia(ctx, "shop") != hosts.ViaStatus {
		t.Errorf("statuses %d comments %v via %s", h.statuses, h.comments, s.StatusVia(ctx, "shop"))
	}
}
