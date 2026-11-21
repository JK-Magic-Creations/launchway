package plain_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/forgetest"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/plain"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

type fixture struct {
	prov  *plain.Provider
	repo  forge.Repository
	conn  forge.Connection
	st    *store.Store
	git   *testlib.Repo
	reg   *forge.Registry
	mgr   *source.Manager
	first string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	g := testlib.New(t, t.TempDir(), map[string]string{"README.md": "hello\n", "src/a.txt": "a\n"})
	first := testlib.Git(t, g.Work, "rev-parse", "HEAD")
	mgr, err := source.NewManager(eng, t.TempDir(), []source.Project{{Name: "app", URL: g.Bare, DefaultBranch: "main", Poll: time.Hour}}, log)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(eng.Database())
	conn, err := st.SyncPlain(context.Background(), []store.Project{{Name: "app", URL: g.Bare, DefaultBranch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepositoryOfProject(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	reg := forge.NewRegistry()
	if err := plain.Register(reg, mgr, st); err != nil {
		t.Fatal(err)
	}
	p, err := reg.Open(conn)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{prov: p.(*plain.Provider), repo: repo, conn: conn, st: st, git: g, reg: reg, mgr: mgr, first: first}
}

func TestPlainPassesTheConformanceSuite(t *testing.T) {
	f := setup(t)
	forgetest.Run(t, forgetest.Harness{Provider: f.prov, Repo: f.repo})
	forgetest.RunRegistry(t, f.reg, map[forge.Kind]forgetest.Harness{forge.KindPlain: {Provider: f.prov, Repo: f.repo}})
}

func TestEveryNoneCapabilityHasAPlainWorkaround(t *testing.T) {
	f := setup(t)
	caps := f.prov.Capabilities()
	if err := caps.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, feat := range []forge.Capability{forge.CapProposals, forge.CapResults, forge.CapRules, forge.CapReviews, forge.CapLand, forge.CapIdentity} {
		d := caps[feat]
		if d.Support != forge.None || !strings.Contains(d.Reason, "plain Git has no proposals, results or rules") {
			t.Errorf("%s: %+v", feat, d)
		}
	}
	for _, feat := range []forge.Capability{forge.CapContent, forge.CapEvents, forge.CapAccess, forge.CapRepositories} {
		if caps[feat].Support != forge.Native {
			t.Errorf("%s should be native", feat)
		}
	}
	if f.prov.Maturity() != forge.Supported || f.prov.ProposalRefs() != forge.PullRefLayout {
		t.Error("maturity or layout")
	}
}

func TestRepositoriesComeFromTheStore(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	page, err := f.prov.Repositories().List(ctx, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Path != "app" {
		t.Fatalf("%v %v", page, err)
	}
	got, err := f.prov.Repositories().Get(ctx, "app")
	if err != nil || got.ID != f.repo.ID {
		t.Fatalf("%v", err)
	}
	if _, err := f.prov.Repositories().Create(ctx, forge.NewRepository{Name: "x"}); !isUnsupported(err) {
		t.Errorf("create: %v", err)
	}
	if err := f.prov.Repositories().Delete(ctx, "app"); !isUnsupported(err) {
		t.Errorf("delete: %v", err)
	}
	if err := f.prov.Repositories().SetDefaultBranch(ctx, "app", "x"); !isUnsupported(err) {
		t.Errorf("default branch: %v", err)
	}
	if _, err := f.prov.Repositories().Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%v", err)
	}
}

func isUnsupported(err error) bool { _, ok := forge.IsUnsupported(err); return ok }

func TestContentFromTheMirror(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.prov.Content()
	refs, err := c.Refs(ctx, f.repo)
	if err != nil || len(refs) != 1 || refs[0].Name != "refs/heads/main" || refs[0].Commit != f.first {
		t.Fatalf("%v %v", refs, err)
	}
	cm, err := c.Commit(ctx, f.repo, f.first)
	if err != nil || cm.ID != f.first || cm.Message == "" || cm.CreatedAt.IsZero() || cm.Author.Login == "" {
		t.Fatalf("%+v %v", cm, err)
	}
	if _, err := c.Commit(ctx, f.repo, "main"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("a short name is not a commit ID: %v", err)
	}
	tree, err := c.Tree(ctx, f.repo, "main", "")
	if err != nil || len(tree) != 2 {
		t.Fatalf("%v %v", tree, err)
	}
	kinds := map[string]string{}
	for _, e := range tree {
		kinds[e.Name] = e.Kind
	}
	if kinds["README.md"] != "file" || kinds["src"] != "dir" {
		t.Errorf("%v", kinds)
	}
	sub, err := c.Tree(ctx, f.repo, "main", "src")
	if err != nil || len(sub) != 1 || sub[0].Name != "a.txt" || sub[0].Size != 2 {
		t.Fatalf("%v %v", sub, err)
	}
	if _, err := c.Tree(ctx, f.repo, "--output=x", ""); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("option as rev: %v", err)
	}
	blob, err := c.Blob(ctx, f.repo, "main", "README.md", 1000)
	if err != nil || string(blob) != "hello\n" {
		t.Fatalf("%q %v", blob, err)
	}
	if _, err := c.Blob(ctx, f.repo, "main", "README.md", 2); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("over the limit: %v", err)
	}
	if _, err := c.Blob(ctx, f.repo, "main", "missing.txt", 10); err == nil {
		t.Error("missing file")
	}
	if _, err := c.Blob(ctx, f.repo, "-x", "a", 10); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := c.Blob(ctx, f.repo, "main", "", 10); err == nil {
		t.Error("empty path")
	}

	// A second commit: compare.
	second := f.git.Commit(map[string]string{"b.txt": "b\n"}, "second")
	time.Sleep(3100 * time.Millisecond) // the mirror is reused for 3 seconds
	cmp, err := c.Compare(ctx, f.repo, f.first, second)
	if err != nil || cmp.Ahead != 1 || cmp.Behind != 0 || len(cmp.Commits) != 1 || cmp.Commits[0].ID != second {
		t.Fatalf("%+v %v", cmp, err)
	}
	if _, err := c.Compare(ctx, f.repo, "--x", second); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := c.Refs(ctx, forge.Repository{}); err == nil {
		t.Error("a repository without a path")
	}
}

func TestEventsAreWhatThePollerDecided(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ev := f.prov.Events()
	if ev.Mode() != forge.EventsPolling {
		t.Error("mode")
	}
	for i, o := range []string{"ignored", "started", "error"} {
		f.st.AppendEvent(ctx, store.Event{Connection: f.conn.ID, Repository: f.repo.ID, Kind: forge.EventRefUpdated, Ref: "refs/heads/main", Outcome: o, New: strings.Repeat("a", 40)})
		_ = i
	}
	got, cursor, err := ev.Poll(ctx, f.repo, "")
	if err != nil || len(got) != 3 || cursor == "" || got[0].Kind != forge.EventRefUpdated || got[0].Ref != "refs/heads/main" {
		t.Fatalf("%v %q %v", got, cursor, err)
	}
	again, cursor2, err := ev.Poll(ctx, f.repo, cursor)
	if err != nil || len(again) != 0 || cursor2 != cursor {
		t.Fatalf("%v %q %v", again, cursor2, err)
	}
	f.st.AppendEvent(ctx, store.Event{Connection: f.conn.ID, Repository: f.repo.ID, Kind: forge.EventRefUpdated, Ref: "refs/heads/x"})
	more, _, _ := ev.Poll(ctx, f.repo, cursor)
	if len(more) != 1 || more[0].Ref != "refs/heads/x" {
		t.Fatalf("%v", more)
	}
	if _, _, err := ev.Poll(ctx, f.repo, "abc"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := ev.Verify(nil, nil); !isUnsupported(err) {
		t.Errorf("%v", err)
	}
}

func TestAccessAddresses(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a, err := f.prov.Access().CloneAddresses(ctx, forge.Repository{Clone: forge.CloneAddresses{HTTPS: "https://h/x.git"}})
	if err != nil || a.HTTPS != "https://h/x.git" {
		t.Fatal(a, err)
	}
	cred, err := f.prov.Access().Credential(ctx, f.repo, true)
	if err != nil || cred.Secret != "" {
		t.Fatal(cred, err)
	}
	_ = filepath.Join
}
